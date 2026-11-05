//go:build linux

package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

const conformanceImageTag = "debian:bookworm-slim"

var managedContainerIDPattern = regexp.MustCompile(`^[a-f0-9]{12,64}$`)

// This test deliberately skips in ordinary local test runs. The dedicated CI
// job sets PRIZM_SANDBOX_OCI_CONFORMANCE=1; in that mode engine/image failures
// are fatal and cannot be reported as skipped conformance.
func TestSandboxOCIConformance(t *testing.T) {
	if os.Getenv("PRIZM_SANDBOX_OCI_CONFORMANCE") != "1" {
		if os.Getenv("GITHUB_JOB") == "test-sandbox-oci" {
			t.Fatal("the dedicated OCI job must set PRIZM_SANDBOX_OCI_CONFORMANCE=1; conformance cannot be skipped")
		}
		t.Skip("set PRIZM_SANDBOX_OCI_CONFORMANCE=1 to require a local Docker engine")
	}
	dockerPath := os.Getenv("PRIZM_SUBAGENT_SANDBOX_DOCKER")
	if dockerPath == "" {
		dockerPath = "docker"
	}
	baseConfig := withDefaults(Config{DockerPath: dockerPath})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if _, err := conformanceDockerOutput(ctx, baseConfig, "info", "--format", "{{.ServerVersion}}"); err != nil {
		t.Fatalf("required local Docker engine is unavailable: %v", err)
	}
	if _, err := conformanceDockerOutput(ctx, baseConfig, "pull", conformanceImageTag); err != nil {
		t.Fatalf("required official conformance image could not be pulled: %v", err)
	}
	digestOutput, err := conformanceDockerOutput(ctx, baseConfig, "image", "inspect", "--format", "{{index .RepoDigests 0}}", conformanceImageTag)
	if err != nil {
		t.Fatalf("pulled conformance image digest could not be resolved: %v", err)
	}
	image := strings.TrimSpace(string(digestOutput))
	if !imageDigestPattern.MatchString(image) {
		t.Fatalf("Docker returned no immutable official image digest: %q", image)
	}

	before := conformanceManagedContainerIDs(t, ctx, baseConfig)
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	tracked := filepath.Join(source, "tracked.txt")
	if err := os.WriteFile(tracked, []byte("source-is-read-only\n"), 0644); err != nil {
		t.Fatal(err)
	}
	git(t, source, "init", "--quiet")
	git(t, source, "-c", "user.name=Sandbox OCI Test", "-c", "user.email=sandbox-oci@example.invalid", "add", "tracked.txt")
	git(t, source, "-c", "user.name=Sandbox OCI Test", "-c", "user.email=sandbox-oci@example.invalid", "commit", "--quiet", "-m", "conformance fixture")
	varTmpInfo, err := os.Lstat("/var/tmp")
	if err != nil || !varTmpInfo.IsDir() || isReparsePoint(varTmpInfo) || varTmpInfo.Mode().Perm()&0005 != 0005 {
		t.Fatalf("host /var/tmp must be a real, traversable directory for the outside-mount sentinel: %v", err)
	}
	sentinelDir, err := os.MkdirTemp("/var/tmp", "prizm-oci-conformance-")
	if err != nil {
		t.Fatalf("outside-mount sentinel directory could not be created: %v", err)
	}
	defer os.RemoveAll(sentinelDir)
	if err := os.Chmod(sentinelDir, 0755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(sentinelDir, "host-sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("host-sentinel-unchanged\n"), 0666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sentinel, 0666); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("host loopback listener could not be prepared: %v", err)
	}
	defer listener.Close()
	listenerPort := listener.Addr().(*net.TCPAddr).Port

	config := withDefaults(Config{
		DockerPath:   dockerPath,
		Image:        image,
		WorkRoot:     filepath.Join(root, "work"),
		ArtifactRoot: filepath.Join(root, "artifacts"),
		MaxFiles:     100,
		MaxBytes:     8 << 20,
		MaxOutput:    4096,
		Timeout:      30 * time.Second,
	})
	reapToken, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	staleName := "prizm-sandbox-stale-" + reapToken
	unrelatedName := "prizm-sandbox-unrelated-" + reapToken
	for _, name := range []string{staleName, unrelatedName} {
		name := name
		defer func() { _ = runDocker(context.Background(), config, "rm", "--force", name) }()
	}
	if err := runDocker(ctx, config, "run", "--rm", "--detach", "--pull=never", "--network=none", "--name", staleName,
		"--label="+sandboxManagedLabel+"=true", "--label="+sandboxOwnerLabel+"="+staleName, image, "/bin/sh", "-c", "sleep 60"); err != nil {
		t.Fatalf("could not create disposable owner-labeled stale-container fixture: %v", err)
	}
	if err := runDocker(ctx, config, "run", "--rm", "--detach", "--pull=never", "--network=none", "--name", unrelatedName,
		"--label="+sandboxManagedLabel+"=true", "--label="+sandboxOwnerLabel+"=not-the-container-owner", image, "/bin/sh", "-c", "sleep 60"); err != nil {
		t.Fatalf("could not create disposable mismatched-owner fixture: %v", err)
	}
	if err := reapStaleSandboxContainersBefore(ctx, config, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("stale labeled container reaper failed: %v", err)
	}
	if conformanceContainerExists(t, ctx, config, staleName) {
		t.Fatal("owner-matched stale sandbox container was not reaped")
	}
	if !conformanceContainerExists(t, ctx, config, unrelatedName) {
		t.Fatal("stale reaper removed a container with a mismatched owner label")
	}
	if err := runDocker(ctx, config, "rm", "--force", unrelatedName); err != nil {
		t.Fatalf("unrelated fixture could not be removed after its preservation check: %v", err)
	}

	session, err := NewProvider(config).Open(ctx, source)
	if err != nil {
		t.Fatalf("Provider.Open failed with the resolved immutable image %q: %v", image, err)
	}
	defer session.Close()
	args := session.runArgs("prizm-sandbox-mount-check", "true")
	mounts := make([]string, 0, 1)
	for index, arg := range args {
		if arg == "--mount" && index+1 < len(args) {
			mounts = append(mounts, args[index+1])
		}
	}
	if len(mounts) != 1 || !strings.Contains(mounts[0], "source="+session.workspace+",") || strings.Contains(mounts[0], sentinelDir) {
		t.Fatalf("sandbox mounts must expose only the read-only source snapshot, got %#v", mounts)
	}

	command := fmt.Sprintf(`set -eu
printf 'created-inside-container\n' > /workspace/created.txt
if printf 'source-write\n' > /source/tracked.txt 2>/dev/null; then echo source mount was writable >&2; exit 61; fi
if cat %s >/dev/null 2>&1; then echo host sentinel was readable >&2; exit 62; fi
if printf 'host-write\n' > %s 2>/dev/null; then echo host sentinel was writable >&2; exit 63; fi
command -v /bin/bash >/dev/null
if timeout 3s /bin/bash -c 'printf x > /dev/tcp/127.0.0.1/%d' >/dev/null 2>&1; then echo host loopback listener was reachable >&2; exit 64; fi
if timeout 3s /bin/bash -c 'printf x > /dev/tcp/1.1.1.1/80' >/dev/null 2>&1; then echo internet was reachable >&2; exit 65; fi
printf 'isolated\n'`, shellQuote(sentinel), shellQuote(sentinel), listenerPort)
	output, code, err := session.Execute(ctx, command)
	if err != nil || code != 0 {
		diagnostic := conformanceLaunchDiagnostic(ctx, config, session)
		t.Fatalf("isolated write/read/network probe failed (code=%d err=%v output=%q; launch diagnostic=%q)", code, err, output, diagnostic)
	}
	if got, err := os.ReadFile(filepath.Join(session.workspace, "created.txt")); err != nil || string(got) != "created-inside-container\n" {
		t.Fatalf("writable tmpfs result was not returned: %q, %v", got, err)
	}
	if got, err := os.ReadFile(tracked); err != nil || string(got) != "source-is-read-only\n" {
		t.Fatalf("host source changed: %q, %v", got, err)
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "host-sentinel-unchanged\n" {
		t.Fatalf("host sentinel changed: %q, %v", got, err)
	}

	output, code, err = session.Execute(ctx, "yes bounded-output | head -c 1000000")
	if err != nil || code != 0 || len(output) > config.MaxOutput {
		t.Fatalf("stdout flood was not bounded (code=%d bytes=%d err=%v)", code, len(output), err)
	}

	if _, _, err := session.Execute(ctx, "printf hostile > 'bad<name'"); err == nil {
		t.Fatal("unsafe container output path was accepted")
	}
	if _, err := os.Stat(filepath.Join(session.workspace, "bad<name")); !os.IsNotExist(err) {
		t.Fatalf("hostile tar path entered the session workspace: %v", err)
	}

	beforeCancellation := conformanceManagedContainerIDs(t, ctx, baseConfig)
	cancelCtx, cancelCommand := context.WithCancel(ctx)
	cancelResult := make(chan error, 1)
	go func() {
		_, _, executeErr := session.Execute(cancelCtx, "sleep 60")
		cancelResult <- executeErr
	}()
	containerID, waitErr := waitForNewManagedContainer(ctx, baseConfig, beforeCancellation)
	if waitErr != nil {
		cancelCommand()
		waitForConformanceCommand(cancelResult)
		t.Fatalf("sandbox container did not launch for cancellation test: %v", waitErr)
	}
	inspect, err := conformanceDockerOutput(ctx, baseConfig, "inspect", "--format", `{{.Name}}|{{index .Config.Labels "`+sandboxOwnerLabel+`"}}|{{index .Config.Labels "`+sandboxManagedLabel+`"}}`, containerID)
	if err != nil {
		cancelCommand()
		waitForConformanceCommand(cancelResult)
		t.Fatalf("could not inspect labeled cancellation container: %v", err)
	}
	parts := strings.Split(strings.TrimSpace(string(inspect)), "|")
	if len(parts) != 3 || strings.TrimPrefix(parts[0], "/") != parts[1] || parts[2] != "true" || !strings.HasPrefix(parts[1], "prizm-sandbox-") {
		cancelCommand()
		waitForConformanceCommand(cancelResult)
		t.Fatalf("sandbox cancellation container labels were not owner-bound: %q", inspect)
	}
	if err := inspectSandboxMounts(ctx, baseConfig, containerID, session); err != nil {
		cancelCommand()
		waitForConformanceCommand(cancelResult)
		t.Fatalf("live container mount configuration violated the boundary: %v", err)
	}
	cancelCommand()
	select {
	case err := <-cancelResult:
		if err == nil {
			t.Fatal("canceled sandbox command unexpectedly succeeded")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("sandbox execution did not return after caller cancellation")
	}
	assertNoNewManagedContainers(t, ctx, baseConfig, before)
}

// conformanceLaunchDiagnostic reruns a harmless command synchronously when the
// production path cannot start its detached container. The bounded Docker
// output identifies daemon/CLI option failures without changing production
// error handling or logging model-supplied command text.
func conformanceLaunchDiagnostic(ctx context.Context, config Config, session *Session) string {
	name, err := randomContainerName()
	if err != nil {
		return "could not allocate diagnostic container name"
	}
	args := session.runArgs(name, "true")
	filtered := args[:0]
	for _, arg := range args {
		if arg != "--detach" {
			filtered = append(filtered, arg)
		}
	}
	args = filtered
	if len(args) < 3 {
		return "sandbox launch arguments were malformed"
	}
	scriptIndex := len(args) - 3
	args[scriptIndex] = strings.Replace(args[scriptIndex], "sleep 120", "sleep 1", 1)
	var output cappedBuffer
	output.limit = 2048
	cmd := dockerCommand(ctx, config, args...)
	cmd.Stdout = &output
	cmd.Stderr = &output
	err = cmd.Run()
	return fmt.Sprintf("exit=%v output=%q", err, output.String())
}

func inspectSandboxMounts(ctx context.Context, config Config, id string, session *Session) error {
	mountOutput, err := conformanceDockerOutput(ctx, config, "inspect", "--format", "{{json .Mounts}}", id)
	if err != nil {
		return fmt.Errorf("live mounts could not be inspected: %w", err)
	}
	var mounts []struct {
		Type        string `json:"Type"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		RW          bool   `json:"RW"`
	}
	if err := json.Unmarshal(mountOutput, &mounts); err != nil {
		return fmt.Errorf("live mount record was malformed: %w", err)
	}
	binds := 0
	for _, mount := range mounts {
		switch mount.Type {
		case "bind":
			binds++
			if filepath.Clean(mount.Source) != filepath.Clean(session.workspace) || mount.Destination != "/source" || mount.RW {
				return fmt.Errorf("live host bind mount is not the private read-only /source snapshot")
			}
		case "tmpfs":
			if mount.Destination != "/workspace" && mount.Destination != "/tmp" {
				return fmt.Errorf("unexpected live tmpfs mount target %q", mount.Destination)
			}
		default:
			return fmt.Errorf("unexpected live mount type %q at %q", mount.Type, mount.Destination)
		}
	}
	if binds != 1 {
		return fmt.Errorf("expected exactly one read-only host bind mount, got %d", binds)
	}
	tmpfsOutput, err := conformanceDockerOutput(ctx, config, "inspect", "--format", "{{json .HostConfig.Tmpfs}}", id)
	if err != nil {
		return fmt.Errorf("live tmpfs limits could not be inspected: %w", err)
	}
	var tmpfs map[string]string
	if err := json.Unmarshal(tmpfsOutput, &tmpfs); err != nil {
		return fmt.Errorf("live tmpfs configuration was malformed: %w", err)
	}
	if len(tmpfs) != 2 {
		return fmt.Errorf("expected only bounded /workspace and /tmp tmpfs mounts, got %v", tmpfs)
	}
	workspaceLimit := fmt.Sprintf("size=%d,nr_inodes=%d", session.config.MaxBytes, session.config.MaxFiles*8+100)
	archiveLimitValue := fmt.Sprintf("size=%d,nr_inodes=1024", archiveLimit(session.config))
	if !strings.Contains(tmpfs["/workspace"], workspaceLimit) || !strings.Contains(tmpfs["/tmp"], archiveLimitValue) {
		return fmt.Errorf("live tmpfs bounds are incorrect: %v", tmpfs)
	}
	return nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func conformanceDockerOutput(ctx context.Context, config Config, args ...string) ([]byte, error) {
	var out cappedBuffer
	out.limit = 1 << 20
	if err := dockerOutputTo(ctx, config, &out, args...); err != nil {
		return out.Bytes(), err
	}
	return out.Bytes(), nil
}

func conformanceManagedContainerIDs(t *testing.T, ctx context.Context, config Config) map[string]struct{} {
	t.Helper()
	ids, err := conformanceDockerOutput(ctx, config, "ps", "-aq", "--filter", "label="+sandboxManagedLabel+"=true")
	if err != nil {
		t.Fatalf("managed sandbox containers could not be listed: %v", err)
	}
	result := map[string]struct{}{}
	for _, id := range strings.Fields(string(ids)) {
		if !managedContainerIDPattern.MatchString(id) {
			t.Fatalf("Docker returned an invalid container ID: %q", id)
		}
		result[id] = struct{}{}
	}
	return result
}

func conformanceContainerExists(t *testing.T, ctx context.Context, config Config, name string) bool {
	t.Helper()
	ids, err := conformanceDockerOutput(ctx, config, "ps", "-aq", "--filter", "name=^/"+name+"$")
	if err != nil {
		t.Fatalf("container %q could not be checked: %v", name, err)
	}
	return strings.TrimSpace(string(ids)) != ""
}

func waitForNewManagedContainer(ctx context.Context, config Config, before map[string]struct{}) (string, error) {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		ids, err := conformanceDockerOutput(ctx, config, "ps", "-aq", "--filter", "label="+sandboxManagedLabel+"=true")
		if err != nil {
			return "", fmt.Errorf("managed sandbox containers could not be listed during cancellation: %w", err)
		}
		for _, id := range strings.Fields(string(ids)) {
			if _, existed := before[id]; !existed {
				return id, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("conformance context ended before sandbox container launch: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	return "", fmt.Errorf("sandbox container was not observable before cancellation")
}

func waitForConformanceCommand(result <-chan error) {
	select {
	case <-result:
	case <-time.After(15 * time.Second):
	}
}

func assertNoNewManagedContainers(t *testing.T, ctx context.Context, config Config, before map[string]struct{}) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		ids := conformanceManagedContainerIDs(t, ctx, config)
		foundNew := false
		for id := range ids {
			if _, existed := before[id]; !existed {
				foundNew = true
				break
			}
		}
		if !foundNew {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("sandbox left a new managed container behind")
}

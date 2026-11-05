package sandbox

import (
	"archive/tar"
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSnapshotTrackedCopiesOnlyAllowlistedRegularSource(t *testing.T) {
	source := t.TempDir()
	cleanTempGitObjectsOnWindows(t, source)
	for _, path := range []string{"main.go", ".env", "prizm.yaml", "config.json", "credentials/token.txt", ".prizm/state.json", "id_rsa", "certs/client.pem", ".npmrc", ".docker/config.json"} {
		full := filepath.Join(source, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("safe synthetic fixture\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git(t, source, "init", "--quiet")
	git(t, source, "-c", "user.name=Sandbox Test", "-c", "user.email=sandbox@example.invalid", "add", "--all")
	git(t, source, "-c", "user.name=Sandbox Test", "-c", "user.email=sandbox@example.invalid", "commit", "--quiet", "-m", "fixture")

	destination := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	got, err := snapshotTracked(context.Background(), source, destination, withDefaults(Config{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["main.go"] == "" {
		t.Fatalf("snapshot manifest contains unexpected paths: %#v", got)
	}
	for _, forbidden := range []string{".env", "prizm.yaml", "config.json", "credentials/token.txt", ".prizm/state.json", "id_rsa", "certs/client.pem", ".npmrc", ".docker/config.json"} {
		if _, err := os.Stat(filepath.Join(destination, filepath.FromSlash(forbidden))); !os.IsNotExist(err) {
			t.Fatalf("sensitive path copied into sandbox: %s", forbidden)
		}
	}
}

func TestSandboxPathsArePortableAcrossArchiveConsumers(t *testing.T) {
	for _, path := range []string{"main.go", "internal/sandbox.go", "../escape", `..\escape`, `C:\secret`, `/absolute`, "dir//file", "CON", "nul.txt", "COM1.log", "LPT9"} {
		want := !strings.ContainsAny(path, `\\:`) && !strings.HasPrefix(path, "/") && path != "../escape" && path != "dir//file"
		if path == "CON" || path == "nul.txt" || path == "COM1.log" || path == "LPT9" {
			want = false
		}
		if got := validPortableRelativePath(path); got != want {
			t.Errorf("validPortableRelativePath(%q) = %t, want %t", path, got, want)
		}
	}
}

func TestSandboxRejectsWindowsInvalidPathCharacters(t *testing.T) {
	for _, name := range []string{"bad<name", "bad>name", `bad"name`, "bad|name", "bad?name", "bad*name", "bad\x01name", "bad\x7fname"} {
		if validPortableRelativePath(name) {
			t.Errorf("validPortableRelativePath(%q) accepted a Windows-invalid character", name)
		}
	}
}

func TestSnapshotRejectsWindowsInvalidCharacters(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot create filenames with these characters")
	}
	for _, name := range []string{"bad<name", "bad>name", `bad"name`, "bad|name", "bad?name", "bad*name", "bad\x01name", "bad\x7fname"} {
		t.Run(name, func(t *testing.T) {
			source := t.TempDir()
			cleanTempGitObjectsOnWindows(t, source)
			if err := os.WriteFile(filepath.Join(source, name), []byte("unsafe"), 0600); err != nil {
				t.Fatal(err)
			}
			git(t, source, "init", "--quiet")
			git(t, source, "-c", "user.name=Sandbox Test", "-c", "user.email=sandbox@example.invalid", "add", "--all")
			git(t, source, "-c", "user.name=Sandbox Test", "-c", "user.email=sandbox@example.invalid", "commit", "--quiet", "-m", "fixture")
			if _, err := snapshotTracked(context.Background(), source, t.TempDir(), withDefaults(Config{})); err == nil {
				t.Fatalf("Windows-invalid tracked path %q must be rejected", name)
			}
		})
	}
}

func TestSnapshotRejectsBackslashTraversalNameOnUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows cannot create a filename containing backslashes")
	}
	source := t.TempDir()
	cleanTempGitObjectsOnWindows(t, source)
	if err := os.WriteFile(filepath.Join(source, `..\escape`), []byte("unsafe"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, source, "init", "--quiet")
	git(t, source, "-c", "user.name=Sandbox Test", "-c", "user.email=sandbox@example.invalid", "add", "--all")
	git(t, source, "-c", "user.name=Sandbox Test", "-c", "user.email=sandbox@example.invalid", "commit", "--quiet", "-m", "fixture")
	if _, err := snapshotTracked(context.Background(), source, t.TempDir(), withDefaults(Config{})); err == nil {
		t.Fatal("backslash path must be rejected before it enters a portable review archive")
	}
}

func TestSnapshotRejectsTrailingDotAndSpacePaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows normalizes trailing dots and spaces in filenames")
	}
	for _, name := range []string{".. ", "...", "name."} {
		t.Run(name, func(t *testing.T) {
			source := t.TempDir()
			cleanTempGitObjectsOnWindows(t, source)
			if err := os.WriteFile(filepath.Join(source, name), []byte("unsafe"), 0600); err != nil {
				t.Fatal(err)
			}
			git(t, source, "init", "--quiet")
			git(t, source, "-c", "user.name=Sandbox Test", "-c", "user.email=sandbox@example.invalid", "add", "--all")
			git(t, source, "-c", "user.name=Sandbox Test", "-c", "user.email=sandbox@example.invalid", "commit", "--quiet", "-m", "fixture")
			if _, err := snapshotTracked(context.Background(), source, t.TempDir(), withDefaults(Config{})); err == nil {
				t.Fatalf("non-portable tracked path %q must be rejected", name)
			}
		})
	}
}

func TestSnapshotRejectsGitIndexVisibilityFlags(t *testing.T) {
	for _, flag := range []string{"--assume-unchanged", "--skip-worktree"} {
		t.Run(flag, func(t *testing.T) {
			source := t.TempDir()
			cleanTempGitObjectsOnWindows(t, source)
			if err := os.WriteFile(filepath.Join(source, "tracked.txt"), []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			git(t, source, "init", "--quiet")
			git(t, source, "-c", "user.name=Sandbox Test", "-c", "user.email=sandbox@example.invalid", "add", "tracked.txt")
			git(t, source, "-c", "user.name=Sandbox Test", "-c", "user.email=sandbox@example.invalid", "commit", "--quiet", "-m", "fixture")
			git(t, source, "update-index", flag, "tracked.txt")
			if err := os.WriteFile(filepath.Join(source, "tracked.txt"), []byte("changed behind index flag"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := snapshotTracked(context.Background(), source, t.TempDir(), withDefaults(Config{})); err == nil {
				t.Fatal("hidden tracked modification must be rejected")
			}
		})
	}
}

func TestSnapshotFailsClosedOnUntrackedOrLinkedSource(t *testing.T) {
	source := t.TempDir()
	cleanTempGitObjectsOnWindows(t, source)
	if err := os.WriteFile(filepath.Join(source, "tracked.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, source, "init", "--quiet")
	git(t, source, "-c", "user.name=Sandbox Test", "-c", "user.email=sandbox@example.invalid", "add", "tracked.txt")
	git(t, source, "-c", "user.name=Sandbox Test", "-c", "user.email=sandbox@example.invalid", "commit", "--quiet", "-m", "fixture")
	if err := os.WriteFile(filepath.Join(source, "untracked.txt"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := snapshotTracked(context.Background(), source, t.TempDir(), withDefaults(Config{})); err == nil {
		t.Fatal("untracked source state must fail closed")
	}
}

func TestSnapshotRejectsTrackedSymlink(t *testing.T) {
	source := t.TempDir()
	cleanTempGitObjectsOnWindows(t, source)
	if err := os.WriteFile(filepath.Join(source, "target.txt"), []byte("target"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.txt", filepath.Join(source, "link.txt")); err != nil {
		t.Skipf("symlink creation unavailable on this host: %v", err)
	}
	git(t, source, "init", "--quiet")
	git(t, source, "-c", "user.name=Sandbox Test", "-c", "user.email=sandbox@example.invalid", "add", "--all")
	git(t, source, "-c", "user.name=Sandbox Test", "-c", "user.email=sandbox@example.invalid", "commit", "--quiet", "-m", "fixture")
	destination := t.TempDir()
	if _, err := snapshotTracked(context.Background(), source, destination, withDefaults(Config{})); err == nil {
		t.Fatal("tracked symlinks must not enter the sandbox snapshot")
	}
}

func TestRunArgsEnforceContainerBoundary(t *testing.T) {
	s := &Session{
		config:    withDefaults(Config{Image: "registry.example.invalid/prizm@sha256:" + strings.Repeat("a", 64)}),
		workspace: filepath.Join(t.TempDir(), "workspace"),
	}
	args := strings.Join(s.runArgs("prizm-sandbox-test", "make test"), "\n")
	for _, required := range []string{"--tmpfs=/workspace:rw,nosuid,nodev,mode=1777,size=536870912,nr_inodes=80100", "--tmpfs=/tmp:rw,noexec,nosuid,nodev,mode=1777,size=702169088,nr_inodes=1024"} {
		if !strings.Contains(args, required) {
			t.Errorf("bounded tmpfs configuration missing %q", required)
		}
	}
	for _, required := range []string{"run", "--rm", "--detach", "--log-driver=local", "--log-opt=max-size=64k", "--log-opt=max-file=1", "--log-opt=compress=false", "--label=" + sandboxManagedLabel + "=true", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit=128", "--memory=2g", "--cpus=2", "--user=1000:1000", "nr_inodes=80100", "nr_inodes=1024", "target=/source,readonly", "--workdir=/workspace", s.config.Image, "/bin/sh", "-c", "prizm-wrapper", "readonly PATH", `/bin/sh -c "$1"`, "timeout --signal=TERM --kill-after=5s", "tar -cf /tmp/prizm-workspace.tar", "sleep 120", "make test"} {
		if !strings.Contains(args, required) {
			t.Errorf("container argv missing %q", required)
		}
	}
	if strings.Index(args, `/bin/sh -c "$1"`) > strings.Index(args, "tar -cf /tmp/prizm-workspace.tar") {
		t.Fatal("trusted archive must be created only after the isolated command returns")
	}
	if strings.Contains(args, "docker.sock") || strings.Contains(args, "DOCKER_HOST") || strings.Contains(args, "--privileged") || strings.Contains(args, "target=/workspace") || strings.Contains(args, "--mount type=bind,source="+s.workspace+",target=/workspace") {
		t.Fatalf("container argv exposes host execution surface: %s", args)
	}
}

func TestBoundedWriterRejectsOverflowWithoutPartialWrite(t *testing.T) {
	var output strings.Builder
	writer := boundedWriter{destination: &output, remaining: 4}
	if n, err := writer.Write([]byte("four")); err != nil || n != 4 {
		t.Fatalf("exact-limit write failed: n=%d err=%v", n, err)
	}
	if n, err := writer.Write([]byte("x")); err == nil || n != 0 {
		t.Fatalf("overflow write was not rejected: n=%d err=%v", n, err)
	}
	if got := output.String(); got != "four" || !writer.exceeded {
		t.Fatalf("overflow changed bounded destination: output=%q exceeded=%v", got, writer.exceeded)
	}
}

func TestBoundedWriterPropagatesCopyFailure(t *testing.T) {
	writer := boundedWriter{destination: failingWriter{}, remaining: 4}
	if n, err := writer.Write([]byte("copy")); err == nil || n != 0 {
		t.Fatalf("destination failure was not propagated: n=%d err=%v", n, err)
	}
}

func TestCappedBufferReaderFromEnforcesLimit(t *testing.T) {
	var output cappedBuffer
	output.limit = 4096
	source := io.LimitReader(strings.NewReader(strings.Repeat("x", 1<<20)), 1<<20)
	if _, err := io.Copy(&output, source); err != nil {
		t.Fatal(err)
	}
	if got := output.Len(); got != output.limit {
		t.Fatalf("ReaderFrom bypassed the output cap: got %d bytes, want %d", got, output.limit)
	}
}

func TestCappedBufferBoundsExecPipeOutput(t *testing.T) {
	const helperEnv = "PRIZM_TEST_CAPPED_BUFFER_HELPER"
	if os.Getenv(helperEnv) == "1" {
		_, _ = os.Stdout.Write([]byte(strings.Repeat("o", 1<<20)))
		_, _ = os.Stderr.Write([]byte(strings.Repeat("e", 1<<20)))
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCappedBufferBoundsExecPipeOutput$")
	cmd.Env = append(os.Environ(), helperEnv+"=1")
	var output cappedBuffer
	output.limit = 4096
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if got := output.Len(); got != output.limit {
		t.Fatalf("exec pipe output exceeded its cap: got %d bytes, want %d", got, output.limit)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("synthetic copy failure") }

func TestExtractBoundedTarRejectsLinksTraversalAndOversize(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header tar.Header
		data   string
	}{
		{name: "traversal", header: tar.Header{Name: "../escape", Typeflag: tar.TypeReg, Mode: 0600, Size: 1}, data: "x"},
		{name: "windows-dot-space", header: tar.Header{Name: ".. ", Typeflag: tar.TypeReg, Mode: 0600, Size: 1}, data: "x"},
		{name: "windows-device", header: tar.Header{Name: "NUL.txt", Typeflag: tar.TypeReg, Mode: 0600, Size: 1}, data: "x"},
		{name: "windows-invalid-char", header: tar.Header{Name: "bad<name", Typeflag: tar.TypeReg, Mode: 0600, Size: 1}, data: "x"},
		{name: "ascii-control", header: tar.Header{Name: "bad\x01name", Typeflag: tar.TypeReg, Mode: 0600, Size: 1}, data: "x"},
		{name: "symlink", header: tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "target"}},
		{name: "oversize", header: tar.Header{Name: "large", Typeflag: tar.TypeReg, Mode: 0600, Size: 9}, data: "123456789"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archivePath := writeTarFixture(t, tc.header, tc.data)
			dest := t.TempDir()
			cfg := withDefaults(Config{MaxBytes: 8})
			if err := extractBoundedTar(archivePath, dest, cfg); err == nil {
				t.Fatal("unsafe tar entry should be rejected")
			}
		})
	}
}

func TestCanonicalSandboxRootsRejectOverlapAndLinkedParents(t *testing.T) {
	source := t.TempDir()
	if pathsOverlap(filepath.Join(source, "sandbox"), source) == false || !pathsOverlap(filepath.Dir(source), source) {
		t.Fatal("ancestor/descendant paths must overlap")
	}
	if _, err := canonicalPathNoLinks(source, true); err != nil {
		t.Fatalf("real source path rejected: %v", err)
	}
	link := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(source, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	if _, err := canonicalPathNoLinks(filepath.Join(link, "work"), false); err == nil {
		t.Fatal("sandbox roots through linked parents must be rejected")
	}
}

func TestStaleTaskRootReaperPreservesFreshAndLockedRoots(t *testing.T) {
	workRoot := t.TempDir()
	stale := filepath.Join(workRoot, "task-stale")
	fresh := filepath.Join(workRoot, "task-fresh")
	active := filepath.Join(workRoot, "task-active")
	unrelated := filepath.Join(workRoot, "other-data")
	for _, path := range []string{stale, fresh, active, unrelated} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	lease, err := lockSandboxLease(filepath.Join(active, ".lease"), false)
	if err != nil {
		t.Fatal(err)
	}
	expired := time.Now().Add(-artifactRetention - time.Hour)
	for _, path := range []string{stale, active} {
		if err := os.Chtimes(path, expired, expired); err != nil {
			t.Fatal(err)
		}
	}
	if err := reapStaleTaskRoots(workRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("expired unlocked task root remains: %v", err)
	}
	for _, path := range []string{fresh, active, unrelated} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("fresh or locked data was removed (%s): %v", path, err)
		}
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := reapStaleTaskRoots(workRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(active); !os.IsNotExist(err) {
		t.Fatalf("expired unlocked task root remains after lease release: %v", err)
	}
}

func TestPeriodicReaperIncludesStaleTaskRoots(t *testing.T) {
	workRoot := t.TempDir()
	stale := filepath.Join(workRoot, "task-stale-periodic")
	if err := os.Mkdir(stale, 0700); err != nil {
		t.Fatal(err)
	}
	expired := time.Now().Add(-artifactRetention - time.Hour)
	if err := os.Chtimes(stale, expired, expired); err != nil {
		t.Fatal(err)
	}
	// cleanupRoots is the shared path used by the hourly reaper after it
	// snapshots registered artifact and task roots.
	cleanupRoots(nil, []string{workRoot})
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("periodic cleanup retained an expired task root: %v", err)
	}
}

func TestSandboxLeaseExcludesConcurrentReaper(t *testing.T) {
	path := filepath.Join(t.TempDir(), "task", ".lease")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	active, err := lockSandboxLease(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockSandboxLease(path, true); !errors.Is(err, errSandboxLeaseBusy) {
		t.Fatalf("active task lease was not exclusive: %v", err)
	}
	_ = active.Close()
	if lease, err := lockSandboxLease(path, true); err != nil {
		t.Fatalf("lease should be reclaimable after owner closes: %v", err)
	} else {
		_ = lease.Close()
	}
}

func TestArtifactQuotaExpiresOnlyOldReviewArtifacts(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, "review-old")
	fresh := filepath.Join(root, "review-fresh")
	for _, path := range []string{old, fresh} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "changes.zip"), []byte("artifact"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	expired := time.Now().Add(-artifactRetention - time.Hour)
	if err := os.Chtimes(old, expired, expired); err != nil {
		t.Fatal(err)
	}
	reservation, err := reserveArtifactQuota(root, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer reservation.release()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("expired artifact was retained: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh review artifact was removed: %v", err)
	}
	if _, err := reserveArtifactQuota(root, maxArtifactBytes+1); err == nil {
		t.Fatal("artifact quota must fail closed when requested reservation exceeds the store limit")
	}
}

func TestArtifactQuotaReclaimsOnlyStaleInvalidReservations(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "reserve-abandoned")
	if err := os.WriteFile(marker, []byte(`{"partial":`), 0600); err != nil {
		t.Fatal(err)
	}
	lease, err := lockSandboxLease(marker, false)
	if err != nil {
		t.Fatal(err)
	}
	if reservation, err := reserveArtifactQuota(root, 1); err == nil {
		reservation.release()
		t.Fatal("active invalid reservation must fail closed")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("active invalid reservation was removed: %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	fresh := time.Now().Add(-time.Hour)
	if err := os.Chtimes(marker, fresh, fresh); err != nil {
		t.Fatal(err)
	}
	if reservation, err := reserveArtifactQuota(root, 1); err == nil {
		reservation.release()
		t.Fatal("young invalid reservation must fail closed")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("young invalid reservation was removed: %v", err)
	}
	stale := time.Now().Add(-reservationRetention - time.Hour)
	if err := os.Chtimes(marker, stale, stale); err != nil {
		t.Fatal(err)
	}
	reservation, err := reserveArtifactQuota(root, 1)
	if err != nil {
		t.Fatalf("stale invalid reservation should be reclaimed: %v", err)
	}
	defer reservation.release()
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("stale invalid reservation remains: %v", err)
	}
}

func TestArtifactReservationsRemainWithinQuotaAcrossConcurrentCallers(t *testing.T) {
	root := t.TempDir()
	const workers = 8
	start := make(chan struct{})
	results := make(chan *artifactReservation, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			reservation, err := reserveArtifactQuota(root, 1<<30)
			if err == nil {
				results <- reservation
			}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var reservations []*artifactReservation
	for path := range results {
		reservations = append(reservations, path)
	}
	if len(reservations) > 2 {
		t.Fatalf("concurrent reservations exceeded the 2 GiB quota: %d", len(reservations))
	}
	for _, reservation := range reservations {
		reservation.release()
	}
	if reservation, err := reserveArtifactQuota(root, 1<<30); err != nil {
		t.Fatalf("quota should be available after reservations are released: %v", err)
	} else {
		reservation.release()
	}
}

func TestOpenRejectsOverlappingSandboxRootsBeforeRuntime(t *testing.T) {
	source := t.TempDir()
	for _, roots := range []struct{ work, artifact string }{
		{work: filepath.Join(source, "sandbox"), artifact: filepath.Join(t.TempDir(), "artifacts")},
		{work: filepath.Dir(source), artifact: filepath.Join(t.TempDir(), "artifacts")},
		{work: filepath.Join(t.TempDir(), "sandbox"), artifact: filepath.Join(source, "artifacts")},
	} {
		provider := NewProvider(Config{Image: "registry.example.invalid/prizm@sha256:" + strings.Repeat("a", 64), WorkRoot: roots.work, ArtifactRoot: roots.artifact})
		if _, err := provider.Open(context.Background(), source); err == nil {
			t.Fatal("overlapping sandbox roots must fail before runtime access")
		}
	}
	if _, err := os.Stat(filepath.Join(source, "sandbox")); !os.IsNotExist(err) {
		t.Fatalf("overlap validation created a sandbox path inside the source: %v", err)
	}
}

func TestExportArtifactIsReviewOnlyAndSurvivesSessionClose(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	artifactRoot := filepath.Join(t.TempDir(), "artifacts")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "change.txt"), []byte("new result\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &Session{root: root, workspace: workspace, artifactRoot: artifactRoot, config: withDefaults(Config{}), baseline: map[string]string{}}
	artifact, err := s.ExportArtifact()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(artifact); err != nil {
		t.Fatalf("review artifact did not survive workspace cleanup: %v", err)
	}
	zr, err := zip.OpenReader(artifact)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	entries := map[string][]byte{}
	for _, f := range zr.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			t.Fatal(err)
		}
		entries[f.Name] = data
	}
	var manifest struct {
		Files []manifestEntry `json:"files"`
	}
	if err := json.Unmarshal(entries["manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Files) != 1 || manifest.Files[0].Path != "change.txt" || manifest.Files[0].Status != "added" {
		t.Fatalf("unexpected review manifest: %#v", manifest.Files)
	}
	if string(entries["files/change.txt"]) != "new result\n" {
		t.Fatalf("unexpected artifact content: %q", entries["files/change.txt"])
	}
}

func TestExportArtifactRejectsTrailingDotSpacePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows normalizes trailing dots and spaces in filenames")
	}
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".. "), []byte("unsafe"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &Session{root: root, workspace: workspace, artifactRoot: filepath.Join(t.TempDir(), "artifacts"), config: withDefaults(Config{})}
	if _, err := s.ExportArtifact(); err == nil {
		t.Fatal("unportable path must be rejected before export")
	}
}

func TestPinnedImageRequiredBeforeRuntime(t *testing.T) {
	p := NewProvider(Config{Image: "latest"})
	if _, err := p.Open(context.Background(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "sha256 digest") {
		t.Fatalf("unpinned image should be rejected before runtime access, got %v", err)
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func writeTarFixture(t *testing.T, header tar.Header, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.tar")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := tar.NewWriter(f)
	if err := w.WriteHeader(&header); err != nil {
		t.Fatal(err)
	}
	if data != "" {
		if _, err := w.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func cleanTempGitObjectsOnWindows(t *testing.T, repo string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		return
	}
	root, err := filepath.Abs(repo)
	if err != nil {
		t.Fatal(err)
	}
	objects := filepath.Join(root, ".git", "objects")
	t.Cleanup(func() {
		info, err := os.Lstat(objects)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return
		}
		_ = filepath.WalkDir(objects, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			abs, err := filepath.Abs(path)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, abs)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return os.ErrPermission
			}
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			fileInfo, err := os.Lstat(path)
			if err != nil || !fileInfo.Mode().IsRegular() {
				return err
			}
			return os.Chmod(path, 0600)
		})
	})
}

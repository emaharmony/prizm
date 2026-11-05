// Package sandbox owns the OS-enforced execution boundary used by opt-in
// code-capable workers. It snapshots tracked source files into a disposable
// workspace and runs commands only in a local, network-disabled OCI container.
package sandbox

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultMaxFiles       = 10000
	defaultMaxSnapshot    = int64(512 << 20)
	defaultMaxOutput      = 64 << 10
	defaultCommandTimeout = 2 * time.Minute
	commandLimit          = 16 << 10
	sandboxArchivePath    = "/tmp/prizm-workspace.tar"
	sandboxStatusPath     = "/tmp/prizm-exit-code"
	sandboxReadyPath      = "/tmp/prizm-ready"
	sandboxOwnerLabel     = "com.prizm.sandbox.owner"
	sandboxManagedLabel   = "com.prizm.sandbox"
	sandboxKeepalive      = 2 * time.Minute
	sandboxStaleAfter     = 10 * time.Minute
	maxArtifactFiles      = 100
	maxArtifactBytes      = int64(2 << 30)
	artifactRetention     = 7 * 24 * time.Hour
	reservationRetention  = 24 * time.Hour
	artifactQuotaLockName = ".quota.lock"
)

var imageDigestPattern = regexp.MustCompile(`^.+@sha256:[0-9a-f]{64}$`)
var artifactStoreMu sync.Mutex
var executionSlots = make(chan struct{}, 2)
var errSandboxLeaseBusy = errors.New("sandbox task lease is active")
var artifactReaperMu sync.Mutex
var artifactRoots = map[string]struct{}{}
var taskRoots = map[string]struct{}{}
var artifactReaperStarted bool

// Config describes a trusted, preloaded OCI image and a private host scratch
// root. Image pulls are never attempted. DockerPath may be overridden in tests.
type Config struct {
	DockerPath   string
	Image        string
	WorkRoot     string
	ArtifactRoot string
	MaxFiles     int
	MaxBytes     int64
	MaxOutput    int
	Timeout      time.Duration
}

// Provider opens isolated task workspaces.
type Provider struct{ config Config }

func NewProvider(config Config) *Provider {
	c := withDefaults(config)
	return &Provider{config: c}
}

func startArtifactReaper(artifactRoot, workRoot string) {
	abs, err := filepath.Abs(artifactRoot)
	if err != nil {
		return
	}
	workAbs, err := filepath.Abs(workRoot)
	if err != nil {
		return
	}
	artifactReaperMu.Lock()
	artifactRoots[filepath.Clean(abs)] = struct{}{}
	taskRoots[filepath.Clean(workAbs)] = struct{}{}
	if !artifactReaperStarted {
		artifactReaperStarted = true
		go func() {
			ticker := time.NewTicker(time.Hour)
			defer ticker.Stop()
			for range ticker.C {
				cleanupRegisteredRoots()
			}
		}()
	}
	artifactReaperMu.Unlock()
	_ = cleanupExpiredArtifacts(abs)
	_ = cleanupExpiredTaskRoots(workAbs)
}

func cleanupRegisteredRoots() {
	artifactReaperMu.Lock()
	artifacts := make([]string, 0, len(artifactRoots))
	for path := range artifactRoots {
		artifacts = append(artifacts, path)
	}
	tasks := make([]string, 0, len(taskRoots))
	for path := range taskRoots {
		tasks = append(tasks, path)
	}
	artifactReaperMu.Unlock()
	cleanupRoots(artifacts, tasks)
}

func cleanupRoots(artifacts, tasks []string) {
	for _, path := range artifacts {
		_ = cleanupExpiredArtifacts(path)
	}
	for _, path := range tasks {
		_ = cleanupExpiredTaskRoots(path)
	}
}

func cleanupExpiredArtifacts(root string) error {
	canonical, err := canonicalPathNoLinks(root, false)
	if err != nil || canonical != filepath.Clean(root) {
		return errors.New("artifact reaper root is not canonical")
	}
	if _, err := os.Stat(canonical); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	artifactStoreMu.Lock()
	defer artifactStoreMu.Unlock()
	quotaLease, err := lockArtifactQuota(canonical)
	if err != nil {
		return errors.New("sandbox artifact quota lock could not be acquired")
	}
	defer quotaLease.Close()
	return enforceArtifactQuota(canonical, "", 0)
}

func lockArtifactQuota(root string) (*os.File, error) {
	path := filepath.Join(root, artifactQuotaLockName)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || isReparsePoint(info) {
			return nil, errors.New("sandbox artifact quota lock is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	lease, err := lockSandboxLease(path, false)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || isReparsePoint(info) {
		_ = lease.Close()
		return nil, errors.New("sandbox artifact quota lock path changed")
	}
	return lease, nil
}

func withDefaults(c Config) Config {
	if c.DockerPath == "" {
		c.DockerPath = "docker"
	}
	if c.WorkRoot == "" {
		c.WorkRoot = filepath.Join(os.TempDir(), "prizm-sandbox")
	}
	if c.ArtifactRoot == "" {
		c.ArtifactRoot = filepath.Join(c.WorkRoot, "artifacts")
	}
	if c.MaxFiles <= 0 {
		c.MaxFiles = defaultMaxFiles
	}
	if c.MaxBytes <= 0 {
		c.MaxBytes = defaultMaxSnapshot
	}
	if c.MaxOutput <= 0 {
		c.MaxOutput = defaultMaxOutput
	}
	if c.Timeout <= 0 {
		c.Timeout = defaultCommandTimeout
	}
	return c
}

// Session is one task's isolated workspace. Commands receive a read-only
// source mount and a size-limited tmpfs workspace; validated tar snapshots are
// copied back to the private host workspace only after the container exits.
type Session struct {
	config       Config
	root         string
	workspace    string
	artifactRoot string
	baseline     map[string]string
	lease        *os.File
	closed       bool
}

// Open validates the local engine and pinned image before copying any source.
// Only clean, tracked regular files are considered; untracked and ignored data
// are never copied into the sandbox.
func (p *Provider) Open(ctx context.Context, sourceRoot string) (*Session, error) {
	c := withDefaults(p.config)
	if c.MaxBytes > defaultMaxSnapshot || c.MaxFiles > defaultMaxFiles || c.MaxOutput > defaultMaxOutput || c.Timeout > defaultCommandTimeout {
		return nil, errors.New("sandbox resource limits exceed the supported maximum")
	}
	if !imageDigestPattern.MatchString(c.Image) {
		return nil, errors.New("sandbox image must be pinned by sha256 digest")
	}
	var err error
	sourceRoot, err = canonicalPathNoLinks(sourceRoot, true)
	if err != nil {
		return nil, errors.New("sandbox source root must have a real, unlinked path")
	}
	c.WorkRoot, err = canonicalPathNoLinks(c.WorkRoot, false)
	if err != nil {
		return nil, errors.New("sandbox work root must have real, unlinked parents")
	}
	c.ArtifactRoot, err = canonicalPathNoLinks(c.ArtifactRoot, false)
	if err != nil {
		return nil, errors.New("sandbox artifact root must have real, unlinked parents")
	}
	if pathsOverlap(c.WorkRoot, sourceRoot) || pathsOverlap(c.ArtifactRoot, sourceRoot) {
		return nil, errors.New("sandbox storage roots must be disjoint from the source worktree")
	}
	if err := reapStaleTaskRoots(c.WorkRoot); err != nil {
		return nil, errors.New("stale sandbox task roots could not be reaped")
	}
	startArtifactReaper(c.ArtifactRoot, c.WorkRoot)
	if err := verifyLocalDocker(ctx, c); err != nil {
		return nil, err
	}
	if err := reapStaleSandboxContainers(ctx, c); err != nil {
		return nil, errors.New("stale sandbox containers could not be reaped")
	}
	if err := runDocker(ctx, c, "image", "inspect", c.Image); err != nil {
		return nil, errors.New("pinned sandbox image is not available locally")
	}
	if err := verifySandboxImage(ctx, c); err != nil {
		return nil, errors.New("pinned sandbox image lacks required shell archive utilities")
	}
	rootInfo, err := os.Lstat(sourceRoot)
	if err != nil || !rootInfo.IsDir() || isReparsePoint(rootInfo) {
		return nil, errors.New("sandbox source root must be a real directory")
	}
	if err := os.MkdirAll(c.WorkRoot, 0700); err != nil {
		return nil, fmt.Errorf("prepare private sandbox root: %w", err)
	}
	if err := os.Chmod(c.WorkRoot, 0700); err != nil {
		return nil, fmt.Errorf("restrict sandbox root: %w", err)
	}
	if err := os.MkdirAll(c.ArtifactRoot, 0700); err != nil {
		return nil, fmt.Errorf("prepare sandbox artifact root: %w", err)
	}
	if err := os.Chmod(c.ArtifactRoot, 0700); err != nil {
		return nil, fmt.Errorf("restrict sandbox artifact root: %w", err)
	}
	root, err := os.MkdirTemp(c.WorkRoot, "task-")
	if err != nil {
		return nil, fmt.Errorf("create private sandbox root: %w", err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("restrict sandbox root: %w", err)
	}
	lease, err := lockSandboxLease(filepath.Join(root, ".lease"), false)
	if err != nil {
		_ = os.RemoveAll(root)
		return nil, errors.New("sandbox task lease could not be acquired")
	}
	workspace := filepath.Join(root, "workspace")
	if strings.Contains(workspace, ",") {
		_ = lease.Close()
		_ = os.RemoveAll(root)
		return nil, errors.New("sandbox workspace path contains an unsupported mount separator")
	}
	if err := os.Mkdir(workspace, 0700); err != nil {
		_ = lease.Close()
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("create sandbox workspace: %w", err)
	}
	// The workspace is beneath a private task directory and is mounted read-only.
	// Read/traverse bits let the numeric container user copy the snapshot.
	if err := os.Chmod(workspace, 0755); err != nil {
		_ = lease.Close()
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("prepare readable sandbox source: %w", err)
	}
	baseline, err := snapshotTracked(ctx, sourceRoot, workspace, c)
	if err != nil {
		_ = lease.Close()
		_ = os.RemoveAll(root)
		return nil, err
	}
	return &Session{config: c, root: root, workspace: workspace, artifactRoot: c.ArtifactRoot, baseline: baseline, lease: lease}, nil
}

// Execute runs one bounded command inside the pinned image. Only a validated
// tar file crosses back from the size-limited container workspace.
func (s *Session) Execute(ctx context.Context, command string) (string, int, error) {
	if s.closed {
		return "", -1, errors.New("sandbox session is closed")
	}
	if strings.TrimSpace(command) == "" || len(command) > commandLimit {
		return "", -1, errors.New("sandbox command is empty or exceeds its bound")
	}
	ctx, cancel := context.WithTimeout(ctx, s.config.Timeout+sandboxKeepalive+time.Minute)
	defer cancel()
	select {
	case executionSlots <- struct{}{}:
		defer func() { <-executionSlots }()
	case <-ctx.Done():
		return "", -1, fmt.Errorf("sandbox execution slot wait stopped: %w", ctx.Err())
	}
	if err := reapStaleSandboxContainers(ctx, s.config); err != nil {
		return "", -1, errors.New("stale sandbox containers could not be reaped")
	}
	name, err := randomContainerName()
	if err != nil {
		return "", -1, err
	}
	launchAttempted := true
	defer func() {
		if launchAttempted {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = cleanupSandboxContainer(cleanupCtx, s.config, name)
			cleanupCancel()
		}
	}()
	if err := runDocker(ctx, s.config, s.runArgs(name, command)...); err != nil {
		if ctx.Err() != nil {
			return "", -1, fmt.Errorf("sandbox command stopped: %w", ctx.Err())
		}
		return "", -1, errors.New("sandbox runtime could not launch isolated container")
	}
	transferRoot, err := os.MkdirTemp(s.root, "transfer-")
	if err != nil {
		return "", -1, errors.New("sandbox transfer directory could not be created")
	}
	defer os.RemoveAll(transferRoot)
	readyPath := filepath.Join(transferRoot, "ready")
	if err := copyContainerFile(ctx, s.config, name, sandboxReadyPath, readyPath); err != nil {
		return "", -1, err
	}
	tarPath := filepath.Join(transferRoot, "workspace.tar")
	statusPath := filepath.Join(transferRoot, "status")
	if err := runDocker(ctx, s.config, "cp", name+":"+sandboxArchivePath, tarPath); err != nil {
		return "", -1, errors.New("sandbox archive could not be copied from container")
	}
	if err := runDocker(ctx, s.config, "cp", name+":"+sandboxStatusPath, statusPath); err != nil {
		return "", -1, errors.New("sandbox exit status could not be copied from container")
	}
	var out cappedBuffer
	out.limit = s.config.MaxOutput
	if err := dockerOutputTo(ctx, s.config, &out, "logs", name); err != nil {
		return "", -1, errors.New("sandbox command output could not be collected")
	}
	tarInfo, err := os.Lstat(tarPath)
	if err != nil || !tarInfo.Mode().IsRegular() || tarInfo.Size() > archiveLimit(s.config) {
		return out.String(), -1, errors.New("sandbox archive is missing or exceeds its bound")
	}
	statusInfo, err := os.Lstat(statusPath)
	if err != nil || !statusInfo.Mode().IsRegular() || statusInfo.Size() > 4 {
		return out.String(), -1, errors.New("sandbox exit status is missing or invalid")
	}
	statusBytes, err := os.ReadFile(statusPath)
	if err != nil {
		return out.String(), -1, errors.New("sandbox exit status could not be read")
	}
	exitCode, err := strconv.Atoi(strings.TrimSpace(string(statusBytes)))
	if err != nil || exitCode < 0 || exitCode > 255 {
		return out.String(), -1, errors.New("sandbox returned an invalid exit status")
	}
	nextWorkspace, err := os.MkdirTemp(s.root, "workspace-next-")
	if err != nil {
		return out.String(), -1, errors.New("sandbox workspace staging could not be created")
	}
	defer func() {
		if nextWorkspace != "" {
			_ = os.RemoveAll(nextWorkspace)
		}
	}()
	if err := extractBoundedTar(tarPath, nextWorkspace, s.config); err != nil {
		return out.String(), -1, err
	}
	if err := cleanupSandboxContainer(ctx, s.config, name); err != nil {
		return out.String(), -1, errors.New("sandbox container could not be removed after completion")
	}
	launchAttempted = false
	if err := replaceWorkspace(s.workspace, nextWorkspace); err != nil {
		return out.String(), -1, err
	}
	nextWorkspace = ""
	return out.String(), exitCode, nil
}

func (s *Session) runArgs(name, command string) []string {
	mount := "type=bind,source=" + s.workspace + ",target=/source,readonly"
	commandTimeout := int(s.config.Timeout.Seconds())
	if commandTimeout < 1 {
		commandTimeout = 1
	}
	workspaceScript := `PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin
export PATH
readonly PATH
/bin/cp -R /source/. /workspace/ || exit 124
timeout --signal=TERM --kill-after=5s ` + strconv.Itoa(commandTimeout) + `s /bin/sh -c "$1"
command_status=$?
/bin/rm -rf /tmp/prizm-workspace.tar
tar -cf /tmp/prizm-workspace.tar -C /workspace . || exit 125
printf '%s\n' "$command_status" > /tmp/prizm-exit-code
: > /tmp/prizm-ready
sleep 120
exit "$command_status"`
	return []string{
		"run", "--rm", "--detach", "--pull=never", "--init", "--name", name,
		"--label=" + sandboxManagedLabel + "=true", "--label=" + sandboxOwnerLabel + "=" + name,
		"--log-driver=local", "--log-opt=max-size=64k", "--log-opt=max-file=1",
		"--network=none", "--read-only", "--cap-drop=ALL",
		"--security-opt=no-new-privileges", "--pids-limit=128",
		"--memory=2g", "--cpus=2", "--user=1000:1000",
		"--tmpfs=/workspace:rw,nosuid,nodev,mode=1777,size=" + strconv.FormatInt(s.config.MaxBytes, 10) + ",nr_inodes=" + strconv.Itoa(s.config.MaxFiles*8+100),
		"--tmpfs=/tmp:rw,noexec,nosuid,nodev,mode=1777,size=" + strconv.FormatInt(archiveLimit(s.config), 10) + ",nr_inodes=1024", "--mount", mount,
		"--workdir=/workspace", "--env=HOME=/tmp",
		"--env=PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin",
		"--env=LANG=C.UTF-8", s.config.Image, "/bin/sh", "-c", workspaceScript, "prizm-wrapper", command,
	}
}

// ExportArtifact writes a zip containing only added or modified regular files
// plus a manifest that lists changed and deleted paths. It never applies the
// sandbox result to the source worktree.
func (s *Session) ExportArtifact() (string, error) {
	if s.closed {
		return "", errors.New("sandbox session is closed")
	}
	artifactRoot, err := canonicalPathNoLinks(s.artifactRoot, false)
	if err != nil || artifactRoot != s.artifactRoot {
		return "", errors.New("sandbox artifact root path is no longer canonical")
	}
	if err := os.MkdirAll(s.artifactRoot, 0700); err != nil {
		return "", fmt.Errorf("prepare sandbox artifact root: %w", err)
	}
	artifactRootInfo, err := os.Lstat(s.artifactRoot)
	if err != nil || !artifactRootInfo.IsDir() || isReparsePoint(artifactRootInfo) {
		return "", errors.New("sandbox artifact root must be a real directory")
	}
	if err := os.Chmod(s.artifactRoot, 0700); err != nil {
		return "", fmt.Errorf("restrict sandbox artifact root: %w", err)
	}
	current, err := hashWorkspace(s.workspace, s.config)
	if err != nil {
		return "", err
	}
	artifactStoreMu.Lock()
	defer artifactStoreMu.Unlock()
	reservation, err := reserveArtifactQuota(s.artifactRoot, archiveLimit(s.config))
	if err != nil {
		return "", err
	}
	defer reservation.release()
	changed := make([]manifestEntry, 0)
	for path, digest := range current {
		if old, ok := s.baseline[path]; !ok || old != digest {
			changed = append(changed, manifestEntry{Path: path, Status: statusFor(s.baseline, path), SHA256: digest})
		}
	}
	for path := range s.baseline {
		if _, ok := current[path]; !ok {
			changed = append(changed, manifestEntry{Path: path, Status: "deleted"})
		}
	}
	sort.Slice(changed, func(i, j int) bool { return changed[i].Path < changed[j].Path })
	artifactDir, err := os.MkdirTemp(s.artifactRoot, "review-")
	if err != nil {
		return "", fmt.Errorf("create sandbox artifact directory: %w", err)
	}
	if err := os.Chmod(artifactDir, 0700); err != nil {
		_ = os.RemoveAll(artifactDir)
		return "", fmt.Errorf("restrict sandbox artifact directory: %w", err)
	}
	artifact := filepath.Join(artifactDir, "changes.zip")
	f, err := os.OpenFile(artifact, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		_ = os.RemoveAll(artifactDir)
		return "", fmt.Errorf("create sandbox artifact: %w", err)
	}
	zw := zip.NewWriter(f)
	manifest, _ := json.MarshalIndent(struct {
		Files []manifestEntry `json:"files"`
	}{Files: changed}, "", "  ")
	manifestWriter, err := zw.Create("manifest.json")
	if err == nil {
		_, err = manifestWriter.Write(manifest)
	}
	for _, entry := range changed {
		if err != nil || entry.Status == "deleted" {
			continue
		}
		path := filepath.Join(s.workspace, filepath.FromSlash(entry.Path))
		in, openErr := os.Open(path)
		if openErr != nil {
			err = openErr
			break
		}
		out, createErr := zw.Create(filepath.ToSlash(filepath.Join("files", entry.Path)))
		if createErr == nil {
			_, createErr = io.Copy(out, in)
		}
		closeErr := in.Close()
		if createErr != nil {
			err = createErr
			break
		}
		if closeErr != nil {
			err = closeErr
			break
		}
	}
	zipErr := zw.Close()
	fileErr := f.Close()
	if err == nil {
		err = zipErr
	}
	if err == nil {
		err = fileErr
	}
	if err != nil {
		_ = os.RemoveAll(artifactDir)
		return "", fmt.Errorf("write sandbox artifact: %w", err)
	}
	artifactInfo, err := os.Lstat(artifact)
	if err != nil || !artifactInfo.Mode().IsRegular() || artifactInfo.Size() > archiveLimit(s.config) {
		_ = os.RemoveAll(artifactDir)
		return "", errors.New("sandbox review artifact exceeds its reserved storage bound")
	}
	reservation.release()
	return artifact, nil
}

func statusFor(baseline map[string]string, path string) string {
	if _, ok := baseline[path]; ok {
		return "modified"
	}
	return "added"
}

type manifestEntry struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	SHA256 string `json:"sha256,omitempty"`
}

// Close removes only this task's disposable workspace. The review artifact
// remains in the private artifact root after the session ends.
func (s *Session) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	if s.lease != nil {
		_ = s.lease.Close()
		s.lease = nil
	}
	return os.RemoveAll(s.root)
}

func snapshotTracked(ctx context.Context, source, destination string, c Config) (map[string]string, error) {
	status := exec.CommandContext(ctx, "git", "-C", source, "status", "--porcelain", "--untracked-files=all")
	status.Env = trustedGitEnv()
	if out, err := status.Output(); err != nil || len(bytes.TrimSpace(out)) != 0 {
		return nil, errors.New("sandbox source worktree is not clean")
	}
	flags := exec.CommandContext(ctx, "git", "-C", source, "ls-files", "-v", "-z")
	flags.Env = trustedGitEnv()
	flagOutput, err := flags.Output()
	if err != nil {
		return nil, errors.New("could not verify tracked source index flags")
	}
	for _, record := range bytes.Split(flagOutput, []byte{0}) {
		if len(record) < 3 || record[1] != ' ' {
			continue
		}
		tag := record[0]
		if tag == 'S' || tag == 's' || (tag >= 'a' && tag <= 'z') {
			return nil, errors.New("sandbox source has assume-unchanged or skip-worktree index flags")
		}
	}
	cmd := exec.CommandContext(ctx, "git", "-C", source, "ls-files", "-z")
	cmd.Env = trustedGitEnv()
	raw, err := cmd.Output()
	if err != nil {
		return nil, errors.New("could not enumerate tracked sandbox source files")
	}
	paths := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	if len(paths) == 1 && paths[0] == "" {
		return nil, errors.New("sandbox source has no tracked files")
	}
	if len(paths) > c.MaxFiles {
		return nil, errors.New("sandbox source exceeds tracked-file limit")
	}
	manifest := make(map[string]string, len(paths))
	var total int64
	for _, rel := range paths {
		if excludedPath(rel) {
			continue
		}
		if !validPortableRelativePath(rel) {
			return nil, errors.New("sandbox source contains a non-portable tracked path")
		}
		clean := filepath.Clean(filepath.FromSlash(rel))
		if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, errors.New("sandbox source contains an unsafe tracked path")
		}
		sourcePath := filepath.Join(source, clean)
		info, err := lstatNoLinkedParents(source, clean)
		if err != nil || !info.Mode().IsRegular() || isReparsePoint(info) {
			return nil, errors.New("sandbox source contains a non-regular or linked tracked path")
		}
		total += info.Size()
		if total > c.MaxBytes {
			return nil, errors.New("sandbox source exceeds snapshot size limit")
		}
		data, err := os.ReadFile(sourcePath)
		if err != nil {
			return nil, errors.New("could not read tracked sandbox source file")
		}
		target := filepath.Join(destination, clean)
		if err := os.MkdirAll(filepath.Dir(target), 0777); err != nil {
			return nil, err
		}
		mode := os.FileMode(0666)
		if info.Mode()&0111 != 0 {
			mode = 0777
		}
		if err := os.WriteFile(target, data, mode); err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		manifest[filepath.ToSlash(clean)] = hex.EncodeToString(sum[:])
	}
	return manifest, nil
}

func lstatNoLinkedParents(root, rel string) (os.FileInfo, error) {
	current := root
	parts := strings.Split(filepath.Clean(rel), string(filepath.Separator))
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return nil, err
		}
		if isReparsePoint(info) || (i < len(parts)-1 && !info.IsDir()) {
			return nil, errors.New("tracked path traverses a linked or non-directory parent")
		}
		if i == len(parts)-1 {
			return info, nil
		}
	}
	return nil, errors.New("tracked path is empty")
}

func excludedPath(path string) bool {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for _, part := range parts {
		lower := strings.ToLower(part)
		if lower == ".git" || lower == ".prizm" || lower == ".aws" || lower == ".ssh" || lower == ".codex" || lower == ".docker" || lower == ".kube" || lower == "credentials" || lower == "secrets" || lower == "private" || lower == ".env" || strings.HasPrefix(lower, ".env.") || strings.Contains(lower, "credential") || strings.Contains(lower, "secret") {
			return true
		}
	}
	base := strings.ToLower(parts[len(parts)-1])
	if base == "prizm.yaml" || base == "prizm.yml" || base == "prizm.local.yaml" || base == "prizm.local.yml" || base == "config.yaml" || base == "config.yml" || base == "config.json" || base == "config.toml" || base == ".npmrc" || base == ".netrc" || base == ".pypirc" || base == "id_rsa" || base == "id_ed25519" || base == "id_ecdsa" || base == "id_dsa" || base == "kubeconfig" {
		return true
	}
	for _, suffix := range []string{".pem", ".key", ".p12", ".pfx", ".jks", ".keystore"} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return false
}

// validPortableRelativePath rejects names whose meaning changes when a review
// archive moves between Unix and Windows, including backslash path traversal.
func validPortableRelativePath(path string) bool {
	if path == "" || strings.Contains(path, `\`) || strings.Contains(path, ":") || strings.HasPrefix(path, "/") {
		return false
	}
	for _, r := range path {
		if r < 0x20 || r == 0x7f || strings.ContainsRune("<>\"|?*", r) {
			return false
		}
	}
	for _, part := range strings.Split(path, "/") {
		trimmed := strings.TrimRight(part, " .")
		if part == "" || part == "." || part == ".." || trimmed == "" || trimmed == "." || trimmed == ".." || trimmed != part || len(part) > 255 || windowsDeviceName(trimmed) {
			return false
		}
	}
	return true
}

func windowsDeviceName(component string) bool {
	base := strings.ToUpper(strings.SplitN(component, ".", 2)[0])
	switch base {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return true
	}
	if len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9' {
		return true
	}
	return false
}

func hashWorkspace(root string, c Config) (map[string]string, error) {
	result := map[string]string{}
	var total int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		portable := filepath.ToSlash(rel)
		if !validPortableRelativePath(portable) {
			return errors.New("sandbox output contains a non-portable path")
		}
		if excludedPath(portable) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil || isReparsePoint(info) || (!info.IsDir() && !info.Mode().IsRegular()) {
			return errors.New("sandbox output contains a linked or non-regular path")
		}
		if info.IsDir() {
			return nil
		}
		if len(result) >= c.MaxFiles {
			return errors.New("sandbox output exceeds file-count limit")
		}
		total += info.Size()
		if total > c.MaxBytes {
			return errors.New("sandbox output exceeds size limit")
		}
		h, err := hashFile(path)
		if err != nil {
			return err
		}
		result[filepath.ToSlash(rel)] = h
		return nil
	})
	return result, err
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func trustedGitEnv() []string {
	allowed := map[string]bool{"PATH": true, "SYSTEMROOT": true, "WINDIR": true, "TEMP": true, "TMP": true}
	var env []string
	for _, value := range os.Environ() {
		key, _, ok := strings.Cut(value, "=")
		if ok && allowed[strings.ToUpper(key)] {
			env = append(env, value)
		}
	}
	env = append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
	return env
}

func verifyLocalDocker(ctx context.Context, c Config) error {
	cmd := dockerCommand(ctx, c, "context", "inspect", "default", "--format", `{{(index .Endpoints "docker").Host}}`)
	out, err := cmd.Output()
	if err != nil {
		return errors.New("local container runtime is unavailable")
	}
	host := strings.TrimSpace(string(out))
	if !strings.HasPrefix(host, "unix://") && !strings.HasPrefix(host, "npipe://") {
		return errors.New("sandbox requires a local Docker context")
	}
	return nil
}

func dockerCommand(ctx context.Context, c Config, args ...string) *exec.Cmd {
	cmdArgs := append([]string{"--context", "default"}, args...)
	cmd := exec.CommandContext(ctx, c.DockerPath, cmdArgs...)
	cmd.Env = withoutRemoteDockerEnv(os.Environ())
	return cmd
}

func withoutRemoteDockerEnv(env []string) []string {
	blocked := map[string]bool{"DOCKER_HOST": true, "DOCKER_CONTEXT": true, "DOCKER_TLS_VERIFY": true, "DOCKER_CERT_PATH": true, "DOCKER_API_VERSION": true}
	result := make([]string, 0, len(env))
	for _, value := range env {
		key, _, ok := strings.Cut(value, "=")
		if ok && !blocked[strings.ToUpper(key)] {
			result = append(result, value)
		}
	}
	return result
}

func runDocker(ctx context.Context, c Config, args ...string) error {
	cmd := dockerCommand(ctx, c, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

func dockerOutput(ctx context.Context, c Config, args ...string) ([]byte, error) {
	var out bytes.Buffer
	if err := dockerOutputTo(ctx, c, &out, args...); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func dockerOutputTo(ctx context.Context, c Config, out io.Writer, args ...string) error {
	cmd := dockerCommand(ctx, c, args...)
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}

func verifySandboxImage(ctx context.Context, c Config) error {
	return runDocker(ctx, c,
		"run", "--rm", "--pull=never", "--network=none", "--read-only",
		"--cap-drop=ALL", "--security-opt=no-new-privileges", "--memory=64m",
		"--user=1000:1000", "--tmpfs=/tmp:rw,noexec,nosuid,nodev,size=1m",
		"--env=PATH=/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin",
		c.Image, "/bin/sh", "-c", `command -v cp >/dev/null && command -v rm >/dev/null && command -v tar >/dev/null && command -v timeout >/dev/null && command -v sleep >/dev/null && command -v grep >/dev/null && timeout --help 2>&1 | grep -q -- --kill-after`)
}

func copyContainerFile(ctx context.Context, c Config, name, source, destination string) error {
	for {
		_ = os.Remove(destination)
		if err := runDocker(ctx, c, "cp", name+":"+source, destination); err == nil {
			info, statErr := os.Lstat(destination)
			if statErr == nil && info.Mode().IsRegular() && info.Size() <= 16 {
				return nil
			}
			return errors.New("sandbox protocol marker is not a regular file")
		}
		state, err := dockerOutput(ctx, c, "inspect", "--format", `{{.State.Running}}`, name)
		if err != nil || strings.TrimSpace(string(state)) != "true" {
			return errors.New("sandbox container exited before producing its bounded result")
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("sandbox result collection stopped: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func cleanupSandboxContainer(ctx context.Context, c Config, name string) error {
	owner, err := dockerOutput(ctx, c, "inspect", "--format", `{{ index .Config.Labels "`+sandboxOwnerLabel+`" }}`, name)
	if err != nil {
		ids, listErr := dockerOutput(ctx, c, "ps", "-aq", "--filter", "name=^/"+name+"$")
		if listErr == nil && strings.TrimSpace(string(ids)) == "" {
			return nil // --rm already removed the completed container.
		}
		return errors.New("sandbox container ownership could not be confirmed")
	}
	if strings.TrimSpace(string(owner)) != name {
		return errors.New("sandbox container ownership label did not match")
	}
	return runDocker(ctx, c, "rm", "--force", name)
}

func reapStaleSandboxContainers(ctx context.Context, c Config) error {
	return reapStaleSandboxContainersBefore(ctx, c, time.Now().Add(-sandboxStaleAfter))
}

func reapStaleSandboxContainersBefore(ctx context.Context, c Config, cutoff time.Time) error {
	ids, err := dockerOutput(ctx, c, "ps", "-aq", "--filter", "label="+sandboxManagedLabel+"=true")
	if err != nil {
		return err
	}
	for _, id := range strings.Fields(string(ids)) {
		state, err := dockerOutput(ctx, c, "inspect", "--format", `{{.Created}}|{{.State.Status}}|{{.Name}}|{{ index .Config.Labels "`+sandboxOwnerLabel+`" }}`, id)
		if err != nil {
			still, listErr := dockerOutput(ctx, c, "ps", "-aq", "--filter", "id="+id)
			if listErr == nil && strings.TrimSpace(string(still)) == "" {
				continue
			}
			return errors.New("sandbox container could not be inspected by the stale reaper")
		}
		parts := strings.SplitN(strings.TrimSpace(string(state)), "|", 4)
		if len(parts) != 4 {
			return errors.New("sandbox container state was malformed")
		}
		name := strings.TrimPrefix(parts[2], "/")
		if !strings.HasPrefix(name, "prizm-sandbox-") || parts[3] != name {
			continue
		}
		created, err := time.Parse(time.RFC3339Nano, parts[0])
		if err != nil {
			return errors.New("sandbox container creation time was malformed")
		}
		if created.Before(cutoff) && parts[1] != "removing" {
			if err := runDocker(ctx, c, "rm", "--force", id); err != nil {
				return err
			}
		}
	}
	return nil
}

func archiveLimit(c Config) int64 {
	return c.MaxBytes + int64(c.MaxFiles*4+100)*4096 + (1 << 20)
}

type artifactReservation struct {
	path  string
	lease *os.File
}

func (r *artifactReservation) release() {
	if r == nil {
		return
	}
	if r.lease != nil {
		_ = r.lease.Close()
		r.lease = nil
	}
	_ = os.Remove(r.path)
}

func reserveArtifactQuota(root string, reserve int64) (*artifactReservation, error) {
	if reserve <= 0 || reserve > maxArtifactBytes {
		return nil, errors.New("sandbox artifact reservation exceeds its storage quota")
	}
	token, err := randomToken()
	if err != nil {
		return nil, err
	}
	quotaLease, err := lockArtifactQuota(root)
	if err != nil {
		return nil, errors.New("sandbox artifact quota lock could not be acquired")
	}
	defer quotaLease.Close()
	reservation := filepath.Join(root, "reserve-"+token)
	f, err := os.OpenFile(reservation, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, errors.New("sandbox artifact reservation could not be created")
	}
	lease, err := lockSandboxFile(f, false)
	if err != nil {
		_ = f.Close()
		_ = os.Remove(reservation)
		return nil, errors.New("sandbox artifact reservation lease could not be acquired")
	}
	metadata, _ := json.Marshal(artifactReservationMetadata{PID: os.Getpid(), CreatedUnix: time.Now().Unix(), Bytes: reserve})
	if _, err := lease.Write(metadata); err != nil {
		_ = lease.Close()
		_ = os.Remove(reservation)
		return nil, errors.New("sandbox artifact reservation could not be written")
	}
	if err := lease.Sync(); err != nil {
		_ = lease.Close()
		_ = os.Remove(reservation)
		return nil, errors.New("sandbox artifact reservation could not be persisted")
	}
	if err := enforceArtifactQuota(root, reservation, reserve); err != nil {
		_ = lease.Close()
		_ = os.Remove(reservation)
		return nil, err
	}
	return &artifactReservation{path: reservation, lease: lease}, nil
}

func enforceArtifactQuota(root string, ownReservation string, ownBytes int64) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return errors.New("sandbox artifact store could not be listed")
	}
	cutoff := time.Now().Add(-artifactRetention)
	count := 0
	var total int64
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		info, err := os.Lstat(path)
		if err != nil || isReparsePoint(info) {
			return errors.New("sandbox artifact store contains an unreadable or linked entry")
		}
		switch {
		case entry.Name() == artifactQuotaLockName:
			if !info.Mode().IsRegular() {
				return errors.New("sandbox artifact quota lock is not a regular file")
			}
		case strings.HasPrefix(entry.Name(), "review-"):
			if !info.IsDir() {
				return errors.New("sandbox artifact store contains a non-directory review entry")
			}
			if info.ModTime().Before(cutoff) {
				if err := os.RemoveAll(path); err != nil {
					return errors.New("expired sandbox artifact could not be removed")
				}
				continue
			}
			count++
			walkErr := filepath.WalkDir(path, func(file string, de os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if file == path {
					return nil
				}
				finfo, err := os.Lstat(file)
				if err != nil || isReparsePoint(finfo) || (!finfo.IsDir() && !finfo.Mode().IsRegular()) {
					return errors.New("artifact store contains a link or special file")
				}
				if !finfo.IsDir() {
					if finfo.Size() < 0 || finfo.Size() > maxArtifactBytes-total {
						return errors.New("artifact store exceeds its byte quota")
					}
					total += finfo.Size()
				}
				return nil
			})
			if walkErr != nil {
				return errors.New("sandbox artifact store could not be validated")
			}
		case strings.HasPrefix(entry.Name(), "reserve-"):
			if !info.Mode().IsRegular() {
				return errors.New("sandbox artifact reservation is not a regular file")
			}
			if path == ownReservation {
				count++
				if ownBytes <= 0 || ownBytes > maxArtifactBytes-total {
					return errors.New("sandbox artifact quota reached by own reservation")
				}
				total += ownBytes
				continue
			}
			lease, lockErr := lockExistingSandboxLease(path, true)
			if errors.Is(lockErr, os.ErrNotExist) {
				continue
			}
			active := errors.Is(lockErr, errSandboxLeaseBusy)
			if lockErr != nil && !active {
				return errors.New("sandbox reservation owner could not be checked")
			}
			if active {
				count++
				if total > 0 {
					return errors.New("sandbox artifact quota reached during active reservation")
				}
				total += maxArtifactBytes
				continue
			}
			// Read through the locked handle: Windows byte-range locks can prevent
			// opening a second handle to the same file while the probe is held.
			_, seekErr := lease.Seek(0, io.SeekStart)
			data, err := io.ReadAll(lease)
			var metadata artifactReservationMetadata
			parseErr := json.Unmarshal(data, &metadata)
			if seekErr != nil || err != nil || parseErr != nil || metadata.PID <= 0 || metadata.CreatedUnix <= 0 || metadata.Bytes <= 0 || metadata.Bytes > maxArtifactBytes {
				if info.ModTime().Before(time.Now().Add(-reservationRetention)) {
					_ = lease.Close()
					if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
						return errors.New("stale invalid sandbox reservation could not be removed")
					}
					continue
				}
				_ = lease.Close()
				count++
				if total > 0 {
					return errors.New("sandbox artifact quota reached by invalid reservation")
				}
				total = maxArtifactBytes
				continue
			}
			if info.ModTime().Before(time.Now().Add(-reservationRetention)) && time.Since(time.Unix(metadata.CreatedUnix, 0)) > reservationRetention && !active {
				_ = lease.Close()
				_ = os.Remove(path)
				continue
			}
			_ = lease.Close()
			if metadata.Bytes > maxArtifactBytes-total {
				return errors.New("sandbox artifact quota reached by active reservations")
			}
			count++
			total += metadata.Bytes
		default:
			return errors.New("sandbox artifact store contains an unmanaged entry")
		}
	}
	if count > maxArtifactFiles || total > maxArtifactBytes {
		return errors.New("sandbox artifact retention quota reached; remove expired review artifacts")
	}
	return nil
}

type artifactReservationMetadata struct {
	PID         int   `json:"pid"`
	CreatedUnix int64 `json:"created_unix"`
	Bytes       int64 `json:"bytes"`
}

func canonicalPathNoLinks(path string, mustExist bool) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("path must be absolute")
	}
	path = filepath.Clean(path)
	volume := filepath.VolumeName(path)
	current := volume + string(filepath.Separator)
	rest := strings.TrimPrefix(path, current)
	if volume == "" {
		current = string(filepath.Separator)
		rest = strings.TrimPrefix(path, current)
	}
	missing := false
	parts := strings.Split(rest, string(filepath.Separator))
	for i, part := range parts {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		if missing {
			continue
		}
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			missing = true
			continue
		}
		if err != nil {
			return "", err
		}
		if isReparsePoint(info) {
			return "", errors.New("path traverses a symbolic link or reparse point")
		}
		if i < len(parts)-1 && !info.IsDir() {
			return "", errors.New("path parent is not a directory")
		}
	}
	if mustExist {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || isReparsePoint(info) {
			return "", errors.New("path must exist as a real directory")
		}
	} else if info, err := os.Lstat(path); err == nil && (!info.IsDir() || isReparsePoint(info)) {
		return "", errors.New("path must be a real directory")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return path, nil
}

func pathsOverlap(a, b string) bool {
	within := func(path, root string) bool {
		rel, err := filepath.Rel(root, path)
		return err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
	}
	return within(a, b) || within(b, a)
}

func reapStaleTaskRoots(workRoot string) error {
	entries, err := os.ReadDir(workRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-artifactRetention)
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "task-") {
			continue
		}
		root := filepath.Join(workRoot, entry.Name())
		info, err := os.Lstat(root)
		if err != nil || !info.IsDir() || isReparsePoint(info) || !info.ModTime().Before(cutoff) {
			continue
		}
		lease, err := lockSandboxLease(filepath.Join(root, ".lease"), true)
		if errors.Is(err, errSandboxLeaseBusy) {
			continue
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if lease != nil {
			_ = lease.Close()
		}
		if err := os.RemoveAll(root); err != nil {
			return err
		}
	}
	return nil
}

func cleanupExpiredTaskRoots(workRoot string) error {
	canonical, err := canonicalPathNoLinks(workRoot, false)
	if err != nil || canonical != filepath.Clean(workRoot) {
		return errors.New("sandbox task reaper root is not canonical")
	}
	return reapStaleTaskRoots(canonical)
}

func extractBoundedTar(archivePath, destination string, c Config) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return errors.New("sandbox archive could not be opened")
	}
	defer f.Close()
	tr := tar.NewReader(f)
	var total int64
	files, entries := 0, 0
	seen := map[string]bool{}
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return errors.New("sandbox archive is malformed")
		}
		name := strings.TrimPrefix(header.Name, "./")
		if name == "." || name == "" {
			if header.Typeflag == tar.TypeDir && header.Size == 0 {
				continue
			}
			return errors.New("sandbox archive contains an invalid root entry")
		}
		key := strings.ToLower(name)
		if !validPortableRelativePath(name) || len(name) > 4096 || seen[key] {
			return errors.New("sandbox archive contains an unsafe or duplicate path")
		}
		seen[key] = true
		entries++
		if entries > c.MaxFiles*4+100 {
			return errors.New("sandbox archive exceeds its entry-count limit")
		}
		path := filepath.Join(destination, filepath.FromSlash(name))
		switch header.Typeflag {
		case tar.TypeDir:
			if header.Size != 0 {
				return errors.New("sandbox archive directory has unexpected data")
			}
			if err := os.MkdirAll(path, 0755); err != nil {
				return errors.New("sandbox archive directory could not be staged")
			}
		case tar.TypeReg, 0: // zero is the historical regular-file typeflag.
			if header.Size < 0 || header.Size > c.MaxBytes-total {
				return errors.New("sandbox archive exceeds its uncompressed size limit")
			}
			files++
			if files > c.MaxFiles {
				return errors.New("sandbox archive exceeds its file-count limit")
			}
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				return errors.New("sandbox archive parent directory could not be staged")
			}
			mode := os.FileMode(0644)
			if header.Mode&0111 != 0 {
				mode = 0755
			}
			out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return errors.New("sandbox archive file could not be staged")
			}
			_, copyErr := io.CopyN(out, tr, header.Size)
			closeErr := out.Close()
			if copyErr != nil || closeErr != nil {
				return errors.New("sandbox archive file was incomplete")
			}
			total += header.Size
		default:
			return errors.New("sandbox archive contains a link or special file")
		}
	}
	return nil
}

func replaceWorkspace(current, next string) error {
	backup := current + ".previous"
	if _, err := os.Lstat(backup); err == nil {
		return errors.New("sandbox workspace backup path already exists")
	}
	if err := os.Chmod(next, 0755); err != nil {
		return errors.New("validated sandbox workspace permissions could not be set")
	}
	if err := os.Rename(current, backup); err != nil {
		return errors.New("sandbox workspace could not be staged for replacement")
	}
	if err := os.Rename(next, current); err != nil {
		_ = os.Rename(backup, current)
		return errors.New("validated sandbox workspace could not be installed")
	}
	if err := os.RemoveAll(backup); err != nil {
		return errors.New("previous sandbox workspace could not be removed")
	}
	return nil
}

type cappedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	remaining := b.limit - b.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	return original, nil
}

func randomContainerName() (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	return "prizm-sandbox-" + token, nil
}

func randomToken() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}

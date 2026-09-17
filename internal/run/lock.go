// Package run provides Prizm's execution runner and run management.
//
// V14d adds run locking to prevent concurrent runs from corrupting the same
// run directory. The lock is file-based (flock) for portability across
// processes. A crashed process leaves a stale lock file with a PID, which
// the --recover flag can bypass.
package run

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gofrs/flock"
)

// ErrRunLocked is returned when another process holds the lock on a run directory.
var ErrRunLocked = fmt.Errorf("run: directory is locked by another process")

// RunLock manages file-based locking for a run directory.
// This prevents concurrent processes from writing to the same run directory.
//
// The lock uses flock (file locking) which is portable across processes
// on the same machine. It stores the locking PID in a .lock file for
// diagnostics.
type RunLock struct {
	runDir string
	lock   *flock.Flock
}

// NewRunLock creates a new RunLock for the given run directory.
func NewRunLock(runDir string) *RunLock {
	lockPath := filepath.Join(runDir, ".lock")
	return &RunLock{
		runDir: runDir,
		lock:   flock.New(lockPath),
	}
}

// Acquire attempts to acquire an exclusive lock on the run directory.
// Returns ErrRunLocked if another process is already running.
// The --recover flag should use TryAcquire instead, which skips the lock.
func (l *RunLock) Acquire(ctx context.Context) error {
	// Ensure run directory exists
	if err := os.MkdirAll(l.runDir, 0755); err != nil {
		return fmt.Errorf("run lock: create dir: %w", err)
	}

	// Try to acquire the lock with a timeout
	locked, err := l.lock.TryLock()
	if err != nil {
		return fmt.Errorf("run lock: acquire: %w", err)
	}
	if !locked {
		// Another process holds the lock
		holderPID := l.readLockHolder()
		return fmt.Errorf("%w: PID %s", ErrRunLocked, holderPID)
	}

	// Write our PID to a sidecar file for diagnostics. On Windows, writing
	// into the locked file itself fails because the flock handle owns it.
	if err := l.writeLockHolder(); err != nil {
		l.Release()
		return fmt.Errorf("run lock: write PID: %w", err)
	}

	return nil
}

// TryAcquire attempts to acquire the lock without blocking.
// Returns true if the lock was acquired, false if it's held by another process.
// This is used by --recover mode to check if a previous run crashed.
func (l *RunLock) TryAcquire() (bool, error) {
	if err := os.MkdirAll(l.runDir, 0755); err != nil {
		return false, fmt.Errorf("run lock: create dir: %w", err)
	}

	locked, err := l.lock.TryLock()
	if err != nil {
		return false, fmt.Errorf("run lock: try acquire: %w", err)
	}

	if locked {
		if err := l.writeLockHolder(); err != nil {
			l.Release()
			return false, fmt.Errorf("run lock: write PID: %w", err)
		}
	}

	return locked, nil
}

// Release releases the lock and removes the PID file.
func (l *RunLock) Release() error {
	err := l.lock.Unlock()
	os.Remove(l.pidPath())
	os.Remove(filepath.Join(l.runDir, ".lock"))
	return err
}

// IsLocked checks if the run directory is currently locked by another process.
// It checks the lock file for a PID that's still alive. This avoids the
// flock re-acquire problem where the same process can re-lock its own file.
func (l *RunLock) IsLocked() bool {
	data, err := os.ReadFile(l.pidPath())
	if err != nil {
		if os.IsNotExist(err) {
			return false // no lock file
		}
		return true // can't read, assume locked
	}

	pidStr := strings.TrimSpace(string(data))
	if pidStr == "" {
		return false
	}

	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return false
	}
	if pid == os.Getpid() {
		return true
	}

	// Check if the process is still alive
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	// Signal 0 doesn't kill the process, just checks if it exists
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return false // process is dead, lock is stale
	}

	return true // process is alive and holding the lock
}

// readLockHolder reads the PID from the lock file.
func (l *RunLock) readLockHolder() string {
	data, err := os.ReadFile(l.pidPath())
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(data))
}

// writeLockHolder writes the current PID to the lock file.
func (l *RunLock) writeLockHolder() error {
	pid := strconv.Itoa(os.Getpid())
	return os.WriteFile(l.pidPath(), []byte(pid), 0644)
}

func (l *RunLock) pidPath() string {
	return filepath.Join(l.runDir, ".lock.pid")
}

// ForceUnlock removes the lock file and releases any held lock.
// Use with caution — this should only be called during crash recovery
// when you're certain the locking process is dead.
func (l *RunLock) ForceUnlock() error {
	if err := l.lock.Unlock(); err != nil {
		return err
	}
	if err := os.Remove(l.pidPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("run lock: remove pid file: %w", err)
	}
	lockPath := filepath.Join(l.runDir, ".lock")
	if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("run lock: remove lock file: %w", err)
	}
	return nil
}

// LockInfo contains information about a run lock.
type LockInfo struct {
	Locked   bool      // Whether the run is currently locked
	PID      string    // PID of the process holding the lock (if known)
	LockedAt time.Time // When the lock was acquired (if known)
	RunDir   string    // The run directory path
}

// Info returns information about the current lock state.
func (l *RunLock) Info() LockInfo {
	holderPID := l.readLockHolder()
	return LockInfo{
		Locked: l.IsLocked(),
		PID:    holderPID,
		RunDir: l.runDir,
	}
}

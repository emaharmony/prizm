package multiagent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	prizmrun "github.com/emaharmony/prizm/internal/run"
)

// FileRunClaimer reuses Prizm's process-safe run lock for Phase 1 ownership.
// OS lock release provides safe takeover after a process crash; Phase 1 does
// not require distributed leases.
type FileRunClaimer struct {
	Root string
}

// Acquire obtains exclusive ownership of the run directory.
func (c FileRunClaimer) Acquire(
	ctx context.Context,
	runID string,
) (ExecutionClaim, error) {
	if strings.TrimSpace(c.Root) == "" {
		return nil, errors.New("multiagent: claim root is required")
	}
	if runID == "" || runID == "." || runID == ".." ||
		filepath.Base(runID) != runID ||
		strings.ContainsAny(runID, `/\`) {
		return nil, fmt.Errorf("multiagent: unsafe run id %q", runID)
	}
	lock := prizmrun.NewRunLock(filepath.Join(c.Root, runID))
	if err := lock.Acquire(ctx); err != nil {
		if errors.Is(err, prizmrun.ErrRunLocked) {
			return nil, fmt.Errorf("%w: %s", ErrRunClaimed, runID)
		}
		return nil, fmt.Errorf("multiagent: acquire run claim: %w", err)
	}
	return &fileExecutionClaim{lock: lock}, nil
}

type fileExecutionClaim struct {
	lock *prizmrun.RunLock
	once sync.Once
	err  error
}

func (c *fileExecutionClaim) Release() error {
	c.once.Do(func() {
		c.err = c.lock.Release()
	})
	return c.err
}

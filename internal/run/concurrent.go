// Package run provides Prizm's execution runner, run locking, and concurrency management.
//
// V14e adds the RunPool for concurrent run execution with semaphore-based
// concurrency control. This allows multiple Prizm runs to execute in parallel
// while respecting resource limits.
package run

import (
	"context"
	"fmt"
	"sync/atomic"
)

// RunPool manages concurrent run execution with semaphore-based limits.
// It ensures that no more than MaxConcurrent runs execute simultaneously.
type RunPool struct {
	MaxConcurrent int
	sem           chan struct{}
	running       atomic.Int32
	total         atomic.Int32
}

// NewRunPool creates a new run pool with the given maximum concurrency.
func NewRunPool(maxConcurrent int) *RunPool {
	if maxConcurrent <= 0 {
		maxConcurrent = 4 // default
	}
	return &RunPool{
		MaxConcurrent: maxConcurrent,
		sem:           make(chan struct{}, maxConcurrent),
	}
}

// RunFunc is the function to execute in the pool.
type RunFunc func(ctx context.Context) error

// Execute runs the given function in the pool, blocking if the pool is full.
// It waits until a semaphore slot is available, then executes the function.
// Returns the function's error, or context.Canceled if the context is done.
func (p *RunPool) Execute(ctx context.Context, fn RunFunc) error {
	select {
	case p.sem <- struct{}{}:
		// Acquired a slot
		p.running.Add(1)
		p.total.Add(1)
		defer func() {
			<-p.sem // Release the slot
			p.running.Add(-1)
		}()
		return fn(ctx)
	case <-ctx.Done():
		return fmt.Errorf("run pool: context cancelled waiting for slot: %w", ctx.Err())
	}
}

// TryExecute runs the given function if a slot is available, returning
// ErrPoolFull immediately if the pool is at capacity.
func (p *RunPool) TryExecute(ctx context.Context, fn RunFunc) error {
	select {
	case p.sem <- struct{}{}:
		p.running.Add(1)
		p.total.Add(1)
		defer func() {
			<-p.sem
			p.running.Add(-1)
		}()
		return fn(ctx)
	default:
		return ErrPoolFull
	}
}

// ErrPoolFull is returned when the pool has no available slots.
var ErrPoolFull = fmt.Errorf("run pool: no available slots")

// Running returns the number of currently running tasks.
func (p *RunPool) Running() int {
	return int(p.running.Load())
}

// Total returns the total number of tasks submitted to the pool.
func (p *RunPool) Total() int {
	return int(p.total.Load())
}

// Available returns the number of available slots.
func (p *RunPool) Available() int {
	return p.MaxConcurrent - int(p.running.Load())
}

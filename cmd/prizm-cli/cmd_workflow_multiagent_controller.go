package main

import (
	"context"
	"fmt"
	"log"

	"github.com/emaharmony/prism/internal/workflow/multiagent"
)

// referenceMultiAgentController implements api.MultiAgentController by
// reusing this package's existing, already-tested run-opening helpers: it
// closes over the same cfg/run-dir/config-path values `prism serve` already
// builds. It knows nothing about the transport layer (internal/api never
// imports cmd/prism-cli); wiring happens the other direction, in
// cmd_serve.go, which passes a *referenceMultiAgentController satisfying
// api.MultiAgentController into api.Config.
type referenceMultiAgentController struct {
	runDir     string
	configPath string
}

// newReferenceMultiAgentController builds the controller `prism serve` wires
// into api.Config.MultiAgentController.
func newReferenceMultiAgentController(runDir, configPath string) *referenceMultiAgentController {
	return &referenceMultiAgentController{runDir: runDir, configPath: configPath}
}

// Cancel and Pause do not need live agent wiring: both only persist a flag
// checked at the next role boundary, so they reuse RunLocator's read-only-
// safe inspection path (the same one the dashboard's GET endpoints already
// use), never constructing a live agent/provider/tool registry.
func (c *referenceMultiAgentController) Cancel(ctx context.Context, runID, reason string) error {
	locator := multiagent.RunLocator{Root: c.runDir}
	runtime, store, eventStore, err := locator.OpenInspection(ctx, runID)
	if err != nil {
		return err
	}
	defer store.Close()
	defer eventStore.Close()
	_, err = runtime.Cancel(ctx, runID, reason)
	return err
}

// Pause reuses the same read-only-safe inspection path as Cancel: it never
// forces a state transition itself, so it needs no live role runner either.
func (c *referenceMultiAgentController) Pause(ctx context.Context, runID, reason string) error {
	locator := multiagent.RunLocator{Root: c.runDir}
	runtime, store, eventStore, err := locator.OpenInspection(ctx, runID)
	if err != nil {
		return err
	}
	defer store.Close()
	defer eventStore.Close()
	_, err = runtime.Pause(ctx, runID, reason)
	return err
}

// Resume needs live agent wiring, since DurableRuntime.Resume actually
// re-invokes the role runner. It loads the run's manifest (the same way
// executeReferenceWorkflowResume does), builds a fully-wired live runtime via
// the existing openLiveReferenceRuntime, and then drives Resume in a
// detached goroutine so the HTTP handler that called this does not block for
// a potentially multi-minute run: the goroutine intentionally uses
// context.Background(), not ctx, because ctx belongs to the HTTP request and
// would be cancelled the moment that request/response cycle completes, which
// happens (by design) well before Resume finishes.
//
// Resume validates synchronously — by opening the manifest and the live
// runtime before returning — so a caller still gets an immediate, accurate
// error for an unknown run, a claim conflict, or a broken config/agent
// wiring; only the actual Resume() execution is detached.
func (c *referenceMultiAgentController) Resume(ctx context.Context, runID string) error {
	manifest, err := loadReferenceManifest(c.runDir, runID)
	if err != nil {
		return err
	}
	rt, err := openLiveReferenceRuntime(c.runDir, c.configPath, manifest)
	if err != nil {
		return fmt.Errorf("multiagent: open live runtime for run %q: %w", runID, err)
	}
	go func() {
		defer rt.close()
		// context.Background(): this resume must outlive the HTTP
		// request/response cycle that triggered it.
		if _, resumeErr := rt.runtime.Resume(context.Background(), runID); resumeErr != nil {
			// Not returned to the caller: the HTTP handler has already
			// responded by the time this completes. Progress/failure is
			// observable via the run's own SSE event stream; this log line
			// is only a local operator breadcrumb, matching how prism serve
			// logs other detached background failures (e.g. the API server
			// goroutine in cmd_serve.go).
			log.Printf("[WARN] multiagent resume for run %s ended: %v", runID, resumeErr)
		}
	}()
	return nil
}

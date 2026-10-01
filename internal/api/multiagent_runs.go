package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

// multiAgentSSEBackfillBatch bounds each backfill page when a client
// (re)connects to the run-scoped SSE stream. 500 keeps most reconnects to a
// single round trip while keeping any one query fast; a longer backlog pages
// automatically before the stream begins live-tailing.
const multiAgentSSEBackfillBatch = 500

const multiAgentSnapshotEventPageSize = 10000

// multiAgentSSEPollInterval is how often the live tail polls the event store
// for events past the last-sent cursor once backfill completes. There is no
// push path from SQLiteEventStore, so this endpoint polls rather than
// blocking on a channel, the same tradeoff genericEventStream makes via
// NextMsg's 100ms non-blocking poll.
const multiAgentSSEPollInterval = 750 * time.Millisecond

// multiAgentRunsConfigured reports whether a run locator root was wired in
// api.Config. Mirrors the s.nc == nil pattern used elsewhere in this file
// for optional subsystems: unset → 503, not a panic or a silently empty list.
func (s *Server) multiAgentRunsConfigured() bool {
	return strings.TrimSpace(s.multiAgentRuns.Root) != ""
}

// validMultiAgentRunID rejects run IDs RunLocator should never be asked to
// resolve: empty, ".", "..", or containing a path separator. RunLocator
// builds filesystem paths via filepath.Join(Root, runID, ...) without
// sanitizing runID itself, so this guard belongs at the API boundary before
// any locator call — the same trust boundary FileRunClaimer.Acquire enforces
// internally for the write path (durable_claim.go).
func validMultiAgentRunID(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" || id == "." || id == ".." {
		return false
	}
	return !strings.ContainsAny(id, `/\`)
}

// writeMultiAgentRunError classifies a RunLocator error into the right HTTP
// status: every "this run does not exist" path in run_locator.go — a
// missing manifest, or (since this milestone) a missing database — wraps the
// underlying os.ErrNotExist with %w, so errors.Is is a reliable, single test
// across both. Anything else (corrupt manifest, corrupt database, decode
// failure) is a server-side problem, not a 404.
func (s *Server) writeMultiAgentRunError(w http.ResponseWriter, err error) {
	if errors.Is(err, os.ErrNotExist) {
		writeJSONError(w, "run not found", http.StatusNotFound)
		return
	}
	writeJSONError(w, err.Error(), http.StatusInternalServerError)
}

// fetchAllMultiAgentRunEvents pages through a run's complete event history
// in ascending ID order. See multiAgentSnapshotEventPageSize for why this
// does not truncate.
func fetchAllMultiAgentRunEvents(ctx context.Context, eventStore *event.SQLiteEventStore, runID string) ([]event.Event, error) {
	var all []event.Event
	cursor := ""
	for {
		batch, err := eventStore.Query(ctx, event.EventFilter{
			RunID:   runID,
			AfterID: cursor,
			Limit:   multiAgentSnapshotEventPageSize,
		})
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < multiAgentSnapshotEventPageSize {
			return all, nil
		}
		cursor = batch[len(batch)-1].ID
	}
}

// handleMultiAgentRuns lists every durable multi-agent run RunLocator can
// discover under its configured root.
func (s *Server) handleMultiAgentRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.multiAgentRunsConfigured() {
		writeJSONError(w, "multi-agent run locator not configured", http.StatusServiceUnavailable)
		return
	}

	runs, err := s.multiAgentRuns.ListRuns(r.Context())
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if runs == nil {
		runs = []multiagent.RunSummary{}
	}
	writeJSON(w, runs)
}

// handleMultiAgentRunSub dispatches everything under
// /api/v1/multiagent/runs/{id}/...: the dashboard snapshot and the
// run-scoped SSE stream. Mirrors handleAgentDetail's split-on-suffix idiom.
func (s *Server) handleMultiAgentRunSub(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/multiagent/runs/")
	if rest == "" {
		http.Error(w, "run id required", http.StatusBadRequest)
		return
	}
	parts := strings.Split(rest, "/")

	switch {
	case len(parts) == 2 && parts[1] == "snapshot":
		s.handleMultiAgentSnapshot(w, r, parts[0])
	case len(parts) == 3 && parts[1] == "events" && parts[2] == "stream":
		s.handleMultiAgentEventStream(w, r, parts[0])
	case len(parts) == 2 && parts[1] == "resume":
		s.handleMultiAgentResume(w, r, parts[0])
	case len(parts) == 2 && parts[1] == "cancel":
		s.handleMultiAgentCancel(w, r, parts[0])
	case len(parts) == 2 && parts[1] == "pause":
		s.handleMultiAgentPause(w, r, parts[0])
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// handleMultiAgentSnapshot returns the complete dashboard read model
// (multiagent.DashboardRunSnapshot) for one run: reference status fields,
// the execution graph, budget visualization, and waiting/cancellation state.
func (s *Server) handleMultiAgentSnapshot(w http.ResponseWriter, r *http.Request, runID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.multiAgentRunsConfigured() {
		writeJSONError(w, "multi-agent run locator not configured", http.StatusServiceUnavailable)
		return
	}
	if !validMultiAgentRunID(runID) {
		writeJSONError(w, "invalid run id", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	definition, err := s.multiAgentRuns.Definition(runID)
	if err != nil {
		s.writeMultiAgentRunError(w, err)
		return
	}

	runtime, runStore, eventStore, err := s.multiAgentRuns.OpenInspection(ctx, runID)
	if err != nil {
		s.writeMultiAgentRunError(w, err)
		return
	}
	defer runStore.Close()
	defer eventStore.Close()

	record, err := runtime.Inspect(ctx, runID)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	allEvents, err := fetchAllMultiAgentRunEvents(ctx, eventStore, runID)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, multiagent.BuildDashboardSnapshot(definition, record, allEvents))
}

// handleMultiAgentEventStream is an SSE tail of one run's canonical event
// history: a backfill of everything after the resolved cursor, then a
// polling live tail for anything appended afterward. Framing mirrors
// handleEventStream (the generic NATS bus relay) exactly — connected event,
// 30s heartbeat, http.Flusher, context-cancellation cleanup — but the id
// field here is the event's real ULID (event.Event.ID), not a synthesized
// timestamp, because every event in this store already carries a stable,
// sortable id.
func (s *Server) handleMultiAgentEventStream(w http.ResponseWriter, r *http.Request, runID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.multiAgentRunsConfigured() {
		writeJSONError(w, "multi-agent run locator not configured", http.StatusServiceUnavailable)
		return
	}
	if !validMultiAgentRunID(runID) {
		writeJSONError(w, "invalid run id", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	// Resolve the run before upgrading to SSE: an unknown run must fail with
	// a normal 404 response, not an SSE stream that opens and then
	// immediately errors.
	_, runStore, eventStore, err := s.multiAgentRuns.OpenInspection(ctx, runID)
	if err != nil {
		s.writeMultiAgentRunError(w, err)
		return
	}
	defer runStore.Close()
	defer eventStore.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Cursor precedence: the standard SSE Last-Event-ID reconnect header
	// (sent automatically by EventSource on reconnect) wins over the
	// ?after= query param, which only matters on a first connect that has no
	// prior cursor to resume from.
	cursor := r.URL.Query().Get("after")
	if lastEventID := r.Header.Get("Last-Event-ID"); lastEventID != "" {
		cursor = lastEventID
	}

	fmt.Fprint(w, "event: connected\ndata: {}\n\n")
	flusher.Flush()

	// Backfill: page through the full history after cursor, in ascending ID
	// order, before starting the live tail — a long-running run's history
	// still arrives complete rather than capped to one page.
	for {
		batch, err := eventStore.Query(ctx, event.EventFilter{
			RunID: runID, AfterID: cursor, Limit: multiAgentSSEBackfillBatch,
		})
		if err != nil {
			log.Printf("[API] multiagent SSE backfill query failed for run %s: %v", runID, err)
			return
		}
		for _, evt := range batch {
			writeMultiAgentSSEEvent(w, flusher, evt)
			cursor = evt.ID
		}
		if len(batch) < multiAgentSSEBackfillBatch {
			break
		}
	}

	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()
	poll := time.NewTicker(multiAgentSSEPollInterval)
	defer poll.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, "event: heartbeat\ndata: {}\n\n")
			flusher.Flush()
		case <-poll.C:
			batch, err := eventStore.Query(ctx, event.EventFilter{
				RunID: runID, AfterID: cursor, Limit: multiAgentSSEBackfillBatch,
			})
			if err != nil {
				log.Printf("[API] multiagent SSE poll query failed for run %s: %v", runID, err)
				return
			}
			for _, evt := range batch {
				writeMultiAgentSSEEvent(w, flusher, evt)
				cursor = evt.ID
			}
		}
	}
}

// writeMultiAgentSSEEvent writes one canonical event as an SSE frame,
// event: {type}\ndata: {json}\nid: {ulid}\n\n, matching the framing
// convention handleEventStream established for the generic bus relay.
func writeMultiAgentSSEEvent(w http.ResponseWriter, flusher http.Flusher, evt event.Event) {
	data, err := json.Marshal(evt)
	if err != nil {
		log.Printf("[API] multiagent SSE marshal failed for event %s: %v", evt.ID, err)
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\nid: %s\n\n", evt.Type, string(data), evt.ID)
	flusher.Flush()
}

// --- Multi-agent operator controls (Phase 2 Milestone 9) ---
//
// These three routes are the only mutating multi-agent endpoints; approve/
// reject continue to go through the existing, unrelated
// POST /api/v1/approvals/{id}/grant|deny. All three follow
// handleWorkflowFeedback's fire-and-forget pattern: validate, call the
// controller, respond immediately with a status token — the caller observes
// actual progress/completion via the existing SSE stream
// (GET .../events/stream), not this response.

// handleMultiAgentResume re-invokes the role runner for a paused/waiting
// run. The controller is expected to return once the resume has been
// durably kicked off, not once the run finishes (a resume may execute one or
// more further roles and can run for minutes).
func (s *Server) handleMultiAgentResume(w http.ResponseWriter, r *http.Request, runID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.multiAgentController == nil {
		writeJSONError(w, "multi-agent controller not configured", http.StatusServiceUnavailable)
		return
	}
	if !validMultiAgentRunID(runID) {
		writeJSONError(w, "invalid run id", http.StatusBadRequest)
		return
	}
	if err := s.multiAgentController.Resume(r.Context(), runID); err != nil {
		s.writeMultiAgentControlError(w, err)
		return
	}
	writeJSON(w, map[string]string{"status": "resuming"})
}

// handleMultiAgentCancel persists an operator cancellation request. An
// active owner observes it at the next role boundary; an idle run becomes
// terminal immediately.
func (s *Server) handleMultiAgentCancel(w http.ResponseWriter, r *http.Request, runID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.multiAgentController == nil {
		writeJSONError(w, "multi-agent controller not configured", http.StatusServiceUnavailable)
		return
	}
	if !validMultiAgentRunID(runID) {
		writeJSONError(w, "invalid run id", http.StatusBadRequest)
		return
	}
	reason, ok := s.readMultiAgentControlReason(w, r)
	if !ok {
		return
	}
	if err := s.multiAgentController.Cancel(r.Context(), runID, reason); err != nil {
		s.writeMultiAgentControlError(w, err)
		return
	}
	writeJSON(w, map[string]string{"status": "cancel_requested"})
}

// handleMultiAgentPause persists an operator pause request. Unlike cancel, a
// paused run never becomes terminal and remains resumable.
func (s *Server) handleMultiAgentPause(w http.ResponseWriter, r *http.Request, runID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.multiAgentController == nil {
		writeJSONError(w, "multi-agent controller not configured", http.StatusServiceUnavailable)
		return
	}
	if !validMultiAgentRunID(runID) {
		writeJSONError(w, "invalid run id", http.StatusBadRequest)
		return
	}
	reason, ok := s.readMultiAgentControlReason(w, r)
	if !ok {
		return
	}
	if err := s.multiAgentController.Pause(r.Context(), runID, reason); err != nil {
		s.writeMultiAgentControlError(w, err)
		return
	}
	writeJSON(w, map[string]string{"status": "pause_requested"})
}

// readMultiAgentControlReason decodes the optional {"reason": "..."} body
// shared by cancel/pause. A missing or empty body is valid — reason is
// documentation for the operator, not a safety gate, so it defaults to "".
// The bool return is false only once this has already written the error
// response (malformed JSON → 400).
func (s *Server) readMultiAgentControlReason(w http.ResponseWriter, r *http.Request) (string, bool) {
	if r.Body == nil || r.ContentLength == 0 {
		return "", true
	}
	var req struct {
		Reason string `json:"reason"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, s.maxRequestBytes))
	if err := decoder.Decode(&req); err != nil {
		if errors.Is(err, io.EOF) {
			return "", true
		}
		writeJSONError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return "", false
	}
	return req.Reason, true
}

// writeMultiAgentControlError classifies a MultiAgentController error into
// the right HTTP status. Errors from the multiagent package propagate
// through the controller unwrapped (or wrapped with %w), so errors.Is
// against the package's sentinel values works regardless of which layer
// produced them.
func (s *Server) writeMultiAgentControlError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, os.ErrNotExist), errors.Is(err, multiagent.ErrRunNotFound):
		writeJSONError(w, "run not found", http.StatusNotFound)
	case errors.Is(err, multiagent.ErrRunClaimed):
		writeJSONError(w, err.Error(), http.StatusConflict)
	case errors.Is(err, multiagent.ErrRunAlreadyTerminal):
		writeJSONError(w, err.Error(), http.StatusConflict)
	default:
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
	}
}

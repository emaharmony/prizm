package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prism/internal/cost"
	"github.com/emaharmony/prism/internal/event"
	"github.com/emaharmony/prism/internal/workflow/multiagent"
)

// fixedOutcomeRunner is a minimal RoleRunner that always returns the
// scripted outcome for the visited role, driving DefaultReferenceDefinition
// straight through planner -> developer -> tester -> reviewer -> completed
// without any real agent execution. It mirrors the shape of the multiagent
// package's own (unexported) happyDurableRunner test helper, rewritten here
// because that helper is not exported across the package boundary.
type fixedOutcomeRunner struct {
	outcomes map[multiagent.Role]multiagent.TransitionOutcome
}

func (f fixedOutcomeRunner) RunRole(_ context.Context, req multiagent.RoleRunRequest) (multiagent.RoleRunResult, error) {
	outcome, ok := f.outcomes[req.RoleConfig.Role]
	if !ok {
		return multiagent.RoleRunResult{}, fmt.Errorf("fixedOutcomeRunner: no scripted outcome for role %q", req.RoleConfig.Role)
	}
	result := multiagent.RoleRunResult{
		Outcome:         outcome,
		TokenUsage:      cost.TokenUsage{TotalTokens: 10},
		LocalIterations: 1,
	}
	// Every DefaultReferenceDefinition outcome scripted here is non-terminal
	// except the reviewer's OutcomeReviewApproved (routes to
	// TerminalConditionCompleted); the supervisor requires an outgoing
	// handoff on non-terminal transitions and rejects one on terminal
	// transitions (supervisor.go's applyTransition / createHandoff).
	if outcome != multiagent.OutcomeReviewApproved {
		result.OutgoingHandoff = &multiagent.HandoffDraft{
			Objective: "scripted handoff for API tests",
			Reason:    "fixedOutcomeRunner always succeeds",
		}
	}
	return result, nil
}

func happyOutcomeRunner() fixedOutcomeRunner {
	return fixedOutcomeRunner{outcomes: map[multiagent.Role]multiagent.TransitionOutcome{
		multiagent.RolePlanner:   multiagent.OutcomePlanReady,
		multiagent.RoleDeveloper: multiagent.OutcomeImplementationReady,
		multiagent.RoleTester:    multiagent.OutcomeTestsPassed,
		multiagent.RoleReviewer:  multiagent.OutcomeReviewApproved,
	}}
}

// multiAgentManifest mirrors run_locator.go's unexported runManifest shape.
// It has to be redeclared here rather than reused because runManifest is
// unexported and this test lives in a different package.
type multiAgentManifest struct {
	SchemaVersion int                   `json:"schema_version"`
	RunID         string                `json:"run_id"`
	WorkflowID    string                `json:"workflow_id"`
	Definition    multiagent.Definition `json:"definition"`
}

// seedMultiAgentRun writes a real, durable multi-agent run to
// root/<runID>/ — a manifest plus a SQLite database populated by actually
// driving DurableRuntime.Run to completion — using the same on-disk layout
// RunLocator (and the CLI) expect: <root>/<runID>/multiagent_manifest.json
// and <root>/<runID>/multiagent.db.
func seedMultiAgentRun(t *testing.T, root, runID string) multiagent.RunState {
	t.Helper()
	definition := multiagent.DefaultReferenceDefinition()
	dbPath := filepath.Join(root, runID, "multiagent.db")

	runStore, err := multiagent.NewSQLiteDurableRunStore(dbPath)
	if err != nil {
		t.Fatalf("new durable store: %v", err)
	}
	eventStore, err := event.NewSQLiteEventStore(dbPath)
	if err != nil {
		t.Fatalf("new event store: %v", err)
	}

	graph, err := multiagent.CompatAdaptDefinition(definition)
	if err != nil {
		t.Fatalf("adapt definition: %v", err)
	}
	runtime, err := multiagent.NewDurableRuntime(
		graph,
		happyOutcomeRunner(),
		runStore,
		multiagent.FileRunClaimer{Root: root},
		eventStore,
		multiagent.DurableRuntimeOptions{},
	)
	if err != nil {
		t.Fatalf("new durable runtime: %v", err)
	}

	state, err := runtime.Run(context.Background(), multiagent.RunRequest{
		RunID: runID,
		Task:  multiagent.TaskReference{ID: "task_" + runID, Description: "seed run for API tests"},
	})
	if err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if err := runStore.Close(); err != nil {
		t.Fatalf("close seed run store: %v", err)
	}
	if err := eventStore.Close(); err != nil {
		t.Fatalf("close seed event store: %v", err)
	}

	manifest := multiAgentManifest{
		SchemaVersion: 1,
		RunID:         runID,
		WorkflowID:    definition.ID,
		Definition:    definition,
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, runID, "multiagent_manifest.json"), data, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	return state
}

// loadAllEventsDirect opens its own short-lived event store connection on
// the seeded run's database to establish ground truth for stream ordering
// assertions, independent of whatever the handler under test does. It is
// closed before returning so it never holds the SQLite file open across into
// the handler's own OpenInspection call.
func loadAllEventsDirect(t *testing.T, dbPath, runID string) []event.Event {
	t.Helper()
	store, err := event.NewSQLiteEventStore(dbPath)
	if err != nil {
		t.Fatalf("open event store for ground truth: %v", err)
	}
	defer store.Close()
	events, err := store.Query(context.Background(), event.EventFilter{RunID: runID, Limit: 10000})
	if err != nil {
		t.Fatalf("query ground truth events: %v", err)
	}
	return events
}

func newMultiAgentTestServer(t *testing.T, root string) *Server {
	t.Helper()
	return NewServer(Config{
		Addr:           ":0",
		MultiAgentRuns: multiagent.RunLocator{Root: root},
	})
}

// fakeMultiAgentController is a scriptable MultiAgentController double for
// the mutating-route tests: it never touches agents/providers/tools (the
// real cmd/prism-cli implementation is exercised separately, since this
// package cannot import package main), it only records what was called and
// returns whatever error the test configured.
type fakeMultiAgentController struct {
	resumeErr error
	cancelErr error
	pauseErr  error

	resumeCalls []string
	cancelCalls []struct{ runID, reason string }
	pauseCalls  []struct{ runID, reason string }
}

func (f *fakeMultiAgentController) Resume(_ context.Context, runID string) error {
	f.resumeCalls = append(f.resumeCalls, runID)
	return f.resumeErr
}

func (f *fakeMultiAgentController) Cancel(_ context.Context, runID, reason string) error {
	f.cancelCalls = append(f.cancelCalls, struct{ runID, reason string }{runID, reason})
	return f.cancelErr
}

func (f *fakeMultiAgentController) Pause(_ context.Context, runID, reason string) error {
	f.pauseCalls = append(f.pauseCalls, struct{ runID, reason string }{runID, reason})
	return f.pauseErr
}

func newMultiAgentControlTestServer(t *testing.T, root string, controller *fakeMultiAgentController) *Server {
	t.Helper()
	return NewServer(Config{
		Addr:                 ":0",
		MultiAgentRuns:       multiagent.RunLocator{Root: root},
		MultiAgentController: controller,
	})
}

func decodeJSONBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response body %q: %v", w.Body.String(), err)
	}
	return body
}

// --- POST /api/v1/multiagent/runs/{id}/resume ---

func TestAPI_MultiAgentResume_Unconfigured(t *testing.T) {
	s := NewServer(Config{Addr: ":0"}) // no MultiAgentController wired

	req := httptest.NewRequest(http.MethodPost, "/api/v1/multiagent/runs/run-x/resume", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAPI_MultiAgentResume_Success(t *testing.T) {
	controller := &fakeMultiAgentController{}
	s := newMultiAgentControlTestServer(t, t.TempDir(), controller)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/multiagent/runs/run-resume-1/resume", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := decodeJSONBody(t, w)["status"]; got != "resuming" {
		t.Fatalf("status = %v, want resuming", got)
	}
	if len(controller.resumeCalls) != 1 || controller.resumeCalls[0] != "run-resume-1" {
		t.Fatalf("resume calls = %v, want [run-resume-1]", controller.resumeCalls)
	}
}

func TestAPI_MultiAgentResume_UnknownRun(t *testing.T) {
	controller := &fakeMultiAgentController{resumeErr: multiagent.ErrRunNotFound}
	s := newMultiAgentControlTestServer(t, t.TempDir(), controller)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/multiagent/runs/does-not-exist/resume", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAPI_MultiAgentResume_ClaimConflict(t *testing.T) {
	controller := &fakeMultiAgentController{resumeErr: fmt.Errorf("wrap: %w", multiagent.ErrRunClaimed)}
	s := newMultiAgentControlTestServer(t, t.TempDir(), controller)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/multiagent/runs/run-busy/resume", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAPI_MultiAgentResume_MalformedRunID(t *testing.T) {
	controller := &fakeMultiAgentController{}
	s := newMultiAgentControlTestServer(t, t.TempDir(), controller)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/multiagent/runs/%20/resume", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if len(controller.resumeCalls) != 0 {
		t.Fatalf("controller must not be called for a malformed run id, got %v", controller.resumeCalls)
	}
}

func TestAPI_MultiAgentResume_MethodNotAllowed(t *testing.T) {
	controller := &fakeMultiAgentController{}
	s := newMultiAgentControlTestServer(t, t.TempDir(), controller)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/multiagent/runs/run-x/resume", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d: %s", w.Code, w.Body.String())
	}
}

// --- POST /api/v1/multiagent/runs/{id}/cancel ---

func TestAPI_MultiAgentCancel_Unconfigured(t *testing.T) {
	s := NewServer(Config{Addr: ":0"})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/multiagent/runs/run-x/cancel", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAPI_MultiAgentCancel_SuccessWithReason(t *testing.T) {
	controller := &fakeMultiAgentController{}
	s := newMultiAgentControlTestServer(t, t.TempDir(), controller)

	body := strings.NewReader(`{"reason": "operator requested stop"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/multiagent/runs/run-cancel-1/cancel", body)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := decodeJSONBody(t, w)["status"]; got != "cancel_requested" {
		t.Fatalf("status = %v, want cancel_requested", got)
	}
	if len(controller.cancelCalls) != 1 ||
		controller.cancelCalls[0].runID != "run-cancel-1" ||
		controller.cancelCalls[0].reason != "operator requested stop" {
		t.Fatalf("cancel calls = %+v", controller.cancelCalls)
	}
}

func TestAPI_MultiAgentCancel_NoBodyDefaultsReason(t *testing.T) {
	controller := &fakeMultiAgentController{}
	s := newMultiAgentControlTestServer(t, t.TempDir(), controller)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/multiagent/runs/run-cancel-2/cancel", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for an omitted body, got %d: %s", w.Code, w.Body.String())
	}
	if len(controller.cancelCalls) != 1 || controller.cancelCalls[0].reason != "" {
		t.Fatalf("cancel calls = %+v, want empty reason", controller.cancelCalls)
	}
}

func TestAPI_MultiAgentCancel_MalformedJSON(t *testing.T) {
	controller := &fakeMultiAgentController{}
	s := newMultiAgentControlTestServer(t, t.TempDir(), controller)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/multiagent/runs/run-cancel-3/cancel", strings.NewReader(`{not json`))
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if len(controller.cancelCalls) != 0 {
		t.Fatalf("controller must not be called for malformed JSON, got %+v", controller.cancelCalls)
	}
}

func TestAPI_MultiAgentCancel_UnknownRun(t *testing.T) {
	controller := &fakeMultiAgentController{cancelErr: multiagent.ErrRunNotFound}
	s := newMultiAgentControlTestServer(t, t.TempDir(), controller)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/multiagent/runs/does-not-exist/cancel", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAPI_MultiAgentCancel_ClaimConflict(t *testing.T) {
	controller := &fakeMultiAgentController{cancelErr: multiagent.ErrRunClaimed}
	s := newMultiAgentControlTestServer(t, t.TempDir(), controller)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/multiagent/runs/run-busy/cancel", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

// --- POST /api/v1/multiagent/runs/{id}/pause ---

func TestAPI_MultiAgentPause_Unconfigured(t *testing.T) {
	s := NewServer(Config{Addr: ":0"})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/multiagent/runs/run-x/pause", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAPI_MultiAgentPause_Success(t *testing.T) {
	controller := &fakeMultiAgentController{}
	s := newMultiAgentControlTestServer(t, t.TempDir(), controller)

	body := strings.NewReader(`{"reason": "coffee break"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/multiagent/runs/run-pause-1/pause", body)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := decodeJSONBody(t, w)["status"]; got != "pause_requested" {
		t.Fatalf("status = %v, want pause_requested", got)
	}
	if len(controller.pauseCalls) != 1 ||
		controller.pauseCalls[0].runID != "run-pause-1" ||
		controller.pauseCalls[0].reason != "coffee break" {
		t.Fatalf("pause calls = %+v", controller.pauseCalls)
	}
}

func TestAPI_MultiAgentPause_AlreadyTerminal(t *testing.T) {
	controller := &fakeMultiAgentController{pauseErr: multiagent.ErrRunAlreadyTerminal}
	s := newMultiAgentControlTestServer(t, t.TempDir(), controller)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/multiagent/runs/run-done/pause", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAPI_MultiAgentPause_UnknownRun(t *testing.T) {
	controller := &fakeMultiAgentController{pauseErr: multiagent.ErrRunNotFound}
	s := newMultiAgentControlTestServer(t, t.TempDir(), controller)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/multiagent/runs/does-not-exist/pause", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

// --- Auth: the mutating multi-agent routes must join requiresAuth's
// generic POST coverage the same way every other mutating endpoint does.

func TestAuthMiddleware_MultiAgentControlRoutesRequireAuth(t *testing.T) {
	s := &Server{authToken: "secret-token"}
	h := s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, path := range []string{
		"/api/v1/multiagent/runs/run-x/resume",
		"/api/v1/multiagent/runs/run-x/cancel",
		"/api/v1/multiagent/runs/run-x/pause",
	} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: no token: expected 401, got %d", path, rec.Code)
		}

		req2 := httptest.NewRequest(http.MethodPost, path, nil)
		req2.Header.Set("Authorization", "Bearer secret-token")
		rec2 := httptest.NewRecorder()
		h.ServeHTTP(rec2, req2)
		if rec2.Code != http.StatusOK {
			t.Fatalf("%s: valid token: expected 200, got %d", path, rec2.Code)
		}
	}
}

// --- GET /api/v1/multiagent/runs ---

func TestAPI_MultiAgentRuns_Unconfigured(t *testing.T) {
	s := NewServer(Config{Addr: ":0"}) // no MultiAgentRuns wired

	req := httptest.NewRequest(http.MethodGet, "/api/v1/multiagent/runs", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAPI_MultiAgentRuns_Empty(t *testing.T) {
	s := newMultiAgentTestServer(t, t.TempDir())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/multiagent/runs", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var runs []multiagent.RunSummary
	if err := json.Unmarshal(w.Body.Bytes(), &runs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("expected no runs, got %+v", runs)
	}
}

func TestAPI_MultiAgentRuns_Populated(t *testing.T) {
	root := t.TempDir()
	state := seedMultiAgentRun(t, root, "run-list-1")
	s := newMultiAgentTestServer(t, root)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/multiagent/runs", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var runs []multiagent.RunSummary
	if err := json.Unmarshal(w.Body.Bytes(), &runs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(runs) != 1 || runs[0].RunID != state.RunID || runs[0].Status != state.Status {
		t.Fatalf("runs = %+v, want one entry matching %+v", runs, state)
	}
}

// --- GET /api/v1/multiagent/runs/{id}/snapshot ---

func TestAPI_MultiAgentSnapshot_Unconfigured(t *testing.T) {
	s := NewServer(Config{Addr: ":0"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/multiagent/runs/anything/snapshot", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAPI_MultiAgentSnapshot_KnownRun(t *testing.T) {
	root := t.TempDir()
	state := seedMultiAgentRun(t, root, "run-snap-1")
	s := newMultiAgentTestServer(t, root)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/multiagent/runs/run-snap-1/snapshot", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var snapshot multiagent.DashboardRunSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snapshot.RunID != state.RunID {
		t.Fatalf("snapshot.RunID = %q, want %q", snapshot.RunID, state.RunID)
	}
	if snapshot.Status != state.Status {
		t.Fatalf("snapshot.Status = %q, want %q", snapshot.Status, state.Status)
	}
	if snapshot.SchemaVersion != multiagent.DashboardSnapshotSchemaVersion {
		t.Fatalf("snapshot.SchemaVersion = %d, want %d", snapshot.SchemaVersion, multiagent.DashboardSnapshotSchemaVersion)
	}
	if len(snapshot.Graph.Nodes) == 0 {
		t.Fatal("snapshot.Graph.Nodes is empty, want the reference workflow's roles/terminals")
	}
	if snapshot.EventCursor == "" {
		t.Fatal("snapshot.EventCursor is empty, want the last event's ULID")
	}
}

func TestAPI_MultiAgentSnapshot_UnknownRun(t *testing.T) {
	root := t.TempDir()
	s := newMultiAgentTestServer(t, root)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/multiagent/runs/does-not-exist/snapshot", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAPI_MultiAgentSnapshot_MalformedRunID(t *testing.T) {
	root := t.TempDir()
	s := newMultiAgentTestServer(t, root)

	// ".", ".." and an empty segment (double slash) as a whole path segment
	// are already defused one layer up by net/http.ServeMux's own
	// path-cleaning redirect, so they never reach a handler with an intact
	// (or empty) run id to validate — those aren't useful cases here. What
	// does reach handleMultiAgentSnapshot exactly as given: a whitespace-only
	// segment (trims to empty) and a segment containing a literal backslash
	// (not a URL path separator, so ServeMux passes it through untouched).
	cases := []string{"%20", `back\slash`}
	for _, id := range cases {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/multiagent/runs/"+id+"/snapshot", nil)
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("id %q: expected 400, got %d: %s", id, w.Code, w.Body.String())
		}
	}
}

// --- GET /api/v1/multiagent/runs/{id}/events/stream ---

func TestAPI_MultiAgentEventStream_UnknownRunReturns404BeforeSSE(t *testing.T) {
	root := t.TempDir()
	s := newMultiAgentTestServer(t, root)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/multiagent/runs/does-not-exist/events/stream", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct == "text/event-stream" {
		t.Fatalf("must not upgrade to SSE for an unknown run, got Content-Type %q", ct)
	}
}

// removeAllWithRetry deletes path, retrying briefly on failure. A just-
// cancelled SSE client request's server-side handler goroutine closes its
// SQLite file handles (runStore/eventStore) asynchronously, a moment after
// the client-side cancel()/Body.Close() call returns -- on Windows,
// removing a run's database file while that handle is still closing races
// against this test and can transiently fail with "used by another
// process". Retrying briefly absorbs that race without weakening what the
// test actually verifies (that a *fully* deleted run 404s on reconnect).
func removeAllWithRetry(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := os.RemoveAll(path); err == nil {
			return
		} else {
			lastErr = err
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("remove %s after retrying: %v", path, lastErr)
}

// streamServer starts a real listening HTTP server: httptest.ResponseRecorder
// cannot support the goroutine + flush + client-disconnect semantics an SSE
// stream needs, so these tests need a real listener and a real client whose
// request can be cancelled.
func streamServer(t *testing.T, root string) *httptest.Server {
	t.Helper()
	s := newMultiAgentTestServer(t, root)
	srv := httptest.NewServer(s.mux)
	t.Cleanup(srv.Close)
	return srv
}

// sseFrame is one parsed SSE event (excluding "connected"/"heartbeat", which
// readSSEEvents filters out as connection bookkeeping rather than run data).
type sseFrame struct {
	name string
	id   string
}

// readSSEEvents reads SSE frames from reader until it has collected
// wantEvents non-bookkeeping frames or deadline elapses.
func readSSEEvents(t *testing.T, reader *bufio.Reader, wantEvents int, deadline time.Duration) []sseFrame {
	t.Helper()
	frames := make(chan sseFrame, 64)
	stop := make(chan struct{})
	go func() {
		var curName, curID string
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			switch {
			case strings.HasPrefix(line, "event: "):
				curName = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "id: "):
				curID = strings.TrimPrefix(line, "id: ")
			case line == "":
				if curName != "" && curName != "connected" && curName != "heartbeat" {
					select {
					case frames <- sseFrame{name: curName, id: curID}:
					case <-stop:
						return
					}
				}
				curName, curID = "", ""
			}
		}
	}()
	defer close(stop)

	var got []sseFrame
	timeout := time.After(deadline)
	for len(got) < wantEvents {
		select {
		case f := <-frames:
			got = append(got, f)
		case <-timeout:
			t.Fatalf("timed out waiting for %d SSE events, got %d", wantEvents, len(got))
		}
	}
	return got
}

func TestAPI_MultiAgentEventStream_BackfillOrderMatchesHistory(t *testing.T) {
	root := t.TempDir()
	seedMultiAgentRun(t, root, "run-stream-1")

	dbPath := filepath.Join(root, "run-stream-1", "multiagent.db")
	groundTruth := loadAllEventsDirect(t, dbPath, "run-stream-1")
	if len(groundTruth) == 0 {
		t.Fatal("seeded run produced no events")
	}

	srv := streamServer(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/multiagent/runs/run-stream-1/events/stream", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	got := readSSEEvents(t, bufio.NewReader(resp.Body), len(groundTruth), 5*time.Second)
	if len(got) != len(groundTruth) {
		t.Fatalf("got %d events, want %d", len(got), len(groundTruth))
	}
	for i, f := range got {
		if f.id != groundTruth[i].ID {
			t.Fatalf("event[%d].id = %q, want %q (order mismatch)", i, f.id, groundTruth[i].ID)
		}
		if f.name != groundTruth[i].Type {
			t.Fatalf("event[%d].name = %q, want %q", i, f.name, groundTruth[i].Type)
		}
	}
}

func TestAPI_MultiAgentEventStream_LastEventIDHonoredOnReconnect(t *testing.T) {
	root := t.TempDir()
	seedMultiAgentRun(t, root, "run-stream-2")
	dbPath := filepath.Join(root, "run-stream-2", "multiagent.db")
	groundTruth := loadAllEventsDirect(t, dbPath, "run-stream-2")
	if len(groundTruth) < 3 {
		t.Fatalf("need at least 3 events for a meaningful reconnect test, got %d", len(groundTruth))
	}

	// Simulate a client that already saw the first 3 events, disconnected,
	// and is now reconnecting: EventSource resends the last id it saw via
	// the standard Last-Event-ID header.
	cursor := groundTruth[2].ID
	want := groundTruth[3:]

	srv := streamServer(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/multiagent/runs/run-stream-2/events/stream", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Last-Event-ID", cursor)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	got := readSSEEvents(t, bufio.NewReader(resp.Body), len(want), 5*time.Second)
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d", len(got), len(want))
	}
	for i, f := range got {
		if f.id != want[i].ID {
			t.Fatalf("event[%d].id = %q, want %q", i, f.id, want[i].ID)
		}
	}
}

func TestAPI_MultiAgentEventStream_AfterQueryParamFallback(t *testing.T) {
	root := t.TempDir()
	seedMultiAgentRun(t, root, "run-stream-3")
	dbPath := filepath.Join(root, "run-stream-3", "multiagent.db")
	groundTruth := loadAllEventsDirect(t, dbPath, "run-stream-3")
	if len(groundTruth) < 2 {
		t.Fatalf("need at least 2 events for an ?after= test, got %d", len(groundTruth))
	}

	cursor := groundTruth[0].ID
	want := groundTruth[1:]

	srv := streamServer(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		srv.URL+"/api/v1/multiagent/runs/run-stream-3/events/stream?after="+cursor, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	got := readSSEEvents(t, bufio.NewReader(resp.Body), len(want), 5*time.Second)
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d", len(got), len(want))
	}
	for i, f := range got {
		if f.id != want[i].ID {
			t.Fatalf("event[%d].id = %q, want %q", i, f.id, want[i].ID)
		}
	}
}

func TestAPI_MultiAgentEventStream_LastEventIDWinsOverAfterParam(t *testing.T) {
	root := t.TempDir()
	seedMultiAgentRun(t, root, "run-stream-4")
	dbPath := filepath.Join(root, "run-stream-4", "multiagent.db")
	groundTruth := loadAllEventsDirect(t, dbPath, "run-stream-4")
	if len(groundTruth) < 3 {
		t.Fatalf("need at least 3 events, got %d", len(groundTruth))
	}

	// ?after= points at the very start (would replay everything); the
	// Last-Event-ID header should win and skip ahead instead.
	want := groundTruth[2:]

	srv := streamServer(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		srv.URL+"/api/v1/multiagent/runs/run-stream-4/events/stream?after=", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Last-Event-ID", groundTruth[1].ID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()

	got := readSSEEvents(t, bufio.NewReader(resp.Body), len(want), 5*time.Second)
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d", len(got), len(want))
	}
	for i, f := range got {
		if f.id != want[i].ID {
			t.Fatalf("event[%d].id = %q, want %q", i, f.id, want[i].ID)
		}
	}
}

// --- Milestone 7: hardening matrix item 7 (terminal run) ---

// TestAPI_MultiAgentEventStream_TerminalRunIdlesAfterBackfill covers matrix
// item 7 ("terminal run"): seedMultiAgentRun always drives the reference
// workflow to a terminal (completed) state via happyOutcomeRunner, so no
// event will ever be appended after backfill. The handler must still keep
// the connection open and idle (heartbeat/poll ticking with nothing new to
// send) rather than erroring out, hanging in an unrecoverable way, or
// closing the stream just because the run is done.
func TestAPI_MultiAgentEventStream_TerminalRunIdlesAfterBackfill(t *testing.T) {
	root := t.TempDir()
	state := seedMultiAgentRun(t, root, "run-terminal-1")
	if !state.Status.Terminal() {
		t.Fatalf("seeded run must reach a terminal state for this test, got status %q", state.Status)
	}
	dbPath := filepath.Join(root, "run-terminal-1", "multiagent.db")
	groundTruth := loadAllEventsDirect(t, dbPath, "run-terminal-1")
	if len(groundTruth) == 0 {
		t.Fatal("seeded run produced no events")
	}

	srv := streamServer(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/multiagent/runs/run-terminal-1/events/stream", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	reader := bufio.NewReader(resp.Body)
	got := readSSEEvents(t, reader, len(groundTruth), 5*time.Second)
	if len(got) != len(groundTruth) {
		t.Fatalf("got %d events, want %d", len(got), len(groundTruth))
	}

	// Nothing more will ever arrive for a terminal run. Confirm the
	// connection stays open (no error, no close, no additional frames)
	// rather than the handler erroring out once backfill drains, by racing
	// a raw byte read against a timeout: getting bytes/EOF/an error here
	// before the timeout would mean the handler misbehaved.
	readDone := make(chan error, 1)
	go func() {
		_, err := reader.ReadByte()
		readDone <- err
	}()
	select {
	case err := <-readDone:
		t.Fatalf("stream produced data or closed (err=%v) while idling on a terminal run; want it to stay open with no further frames until the client disconnects", err)
	case <-time.After(1500 * time.Millisecond):
		// Expected: still idling, connection open, nothing sent since backfill.
	}
}

// --- Milestone 7: hardening matrix item 11 (deleted/unavailable run) ---

// TestAPI_MultiAgentSnapshot_RunDeletedAfterPriorSuccess covers matrix item
// 11 ("deleted/unavailable run") for the snapshot endpoint: a run that was
// successfully inspected once, then had its backing manifest/database
// removed (e.g. operator cleanup), must 404 cleanly on the next fetch
// instead of panicking, hanging, or returning stale/malformed data.
func TestAPI_MultiAgentSnapshot_RunDeletedAfterPriorSuccess(t *testing.T) {
	root := t.TempDir()
	seedMultiAgentRun(t, root, "run-snap-deleted-1")
	s := newMultiAgentTestServer(t, root)

	req1 := httptest.NewRequest(http.MethodGet, "/api/v1/multiagent/runs/run-snap-deleted-1/snapshot", nil)
	w1 := httptest.NewRecorder()
	s.mux.ServeHTTP(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("initial fetch: expected 200, got %d: %s", w1.Code, w1.Body.String())
	}

	if err := os.RemoveAll(filepath.Join(root, "run-snap-deleted-1")); err != nil {
		t.Fatalf("remove run directory: %v", err)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/multiagent/runs/run-snap-deleted-1/snapshot", nil)
	w2 := httptest.NewRecorder()
	s.mux.ServeHTTP(w2, req2)
	if w2.Code != http.StatusNotFound {
		t.Fatalf("after deletion: expected 404, got %d: %s", w2.Code, w2.Body.String())
	}
}

// TestAPI_MultiAgentEventStream_ReconnectAfterRunDeleted covers matrix item
// 11 for the SSE endpoint: a client that previously connected and observed
// the full backfill (an operator actively watching this run), then had the
// connection drop and the run's files deleted in the meantime, must get a
// clean 404 on reconnect (mirroring what EventSource's native auto-reconnect
// would do) rather than a hang, a panic, or an SSE stream that upgrades and
// then errors.
func TestAPI_MultiAgentEventStream_ReconnectAfterRunDeleted(t *testing.T) {
	root := t.TempDir()
	seedMultiAgentRun(t, root, "run-deleted-1")
	dbPath := filepath.Join(root, "run-deleted-1", "multiagent.db")
	groundTruth := loadAllEventsDirect(t, dbPath, "run-deleted-1")
	if len(groundTruth) == 0 {
		t.Fatal("seeded run produced no events")
	}

	srv := streamServer(t, root)

	ctx1, cancel1 := context.WithCancel(context.Background())
	req1, err := http.NewRequestWithContext(ctx1, http.MethodGet, srv.URL+"/api/v1/multiagent/runs/run-deleted-1/events/stream", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp1, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp1.StatusCode)
	}
	got := readSSEEvents(t, bufio.NewReader(resp1.Body), len(groundTruth), 5*time.Second)
	if len(got) != len(groundTruth) {
		t.Fatalf("got %d events, want %d", len(got), len(groundTruth))
	}
	resp1.Body.Close()
	cancel1()

	// No connection is open now (fully closed above); the run's backing
	// files are removed, simulating operator cleanup between viewing
	// sessions.
	removeAllWithRetry(t, filepath.Join(root, "run-deleted-1"))

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	req2, err := http.NewRequestWithContext(ctx2, http.MethodGet, srv.URL+"/api/v1/multiagent/runs/run-deleted-1/events/stream", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 after run deletion, got %d", resp2.StatusCode)
	}
	if ct := resp2.Header.Get("Content-Type"); ct == "text/event-stream" {
		t.Fatalf("must not upgrade to SSE for a deleted run, got Content-Type %q", ct)
	}
}

// --- Auth: the multiagent SSE stream must join the SSE special-case in
// requiresAuth/authorized (token via header OR ?token= query param), the
// same way the generic /api/v1/events/stream endpoint does.

func TestAuthMiddleware_MultiAgentEventStreamRequiresAuth(t *testing.T) {
	s := &Server{authToken: "secret-token"}
	h := s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// No token.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/multiagent/runs/run-x/events/stream", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: expected 401, got %d", rec.Code)
	}

	// Invalid token.
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/multiagent/runs/run-x/events/stream?token=wrong", nil)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("invalid token: expected 401, got %d", rec2.Code)
	}

	// Valid token via query param (EventSource can't set headers).
	req3 := httptest.NewRequest(http.MethodGet, "/api/v1/multiagent/runs/run-x/events/stream?token=secret-token", nil)
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("valid token: expected 200, got %d", rec3.Code)
	}
}

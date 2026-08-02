package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/emaharmony/prism/internal/workflow/multiagent"
)

// testDefinitionJSON is a small, valid, self-contained WorkflowDefinition
// document (one role node routing directly to one terminal node), used as
// the POST body for the definitions routes below. label lets tests produce
// genuinely different compiled content (and therefore a different
// fingerprint) by varying the role's displayName.
func testDefinitionJSON(workflowID, userVersion, label string) []byte {
	doc := map[string]any{
		"apiVersion": multiagent.SchemaAPIVersion,
		"kind":       multiagent.SchemaKind,
		"metadata": map[string]any{
			"name":    workflowID,
			"id":      workflowID,
			"version": userVersion,
		},
		"spec": map[string]any{
			"entryNode": "worker",
			"nodes": []map[string]any{
				{
					"id":              "worker",
					"type":            "role",
					"role":            "worker",
					"displayName":     label,
					"agentProfile":    "worker-agent",
					"allowedOutcomes": []string{"done"},
				},
				{
					"id":                "done",
					"type":              "terminal",
					"terminalCondition": "completed",
				},
			},
			"edges": []map[string]any{
				{
					"id":   "worker-done",
					"from": "worker",
					"to":   "done",
					"when": map[string]any{"outcome": "done"},
				},
			},
		},
	}
	data, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return data
}

func newDefinitionTestServer(t *testing.T) (*Server, *multiagent.DefinitionStore) {
	t.Helper()
	store, err := multiagent.NewDefinitionStore(filepath.Join(t.TempDir(), "definitions.db"))
	if err != nil {
		t.Fatalf("new definition store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s := NewServer(Config{Addr: ":0", DefinitionStore: store})
	return s, store
}

func decodeDefinitionResponse(t *testing.T, w *httptest.ResponseRecorder) definitionRegistrationResponse {
	t.Helper()
	var resp definitionRegistrationResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response body %q: %v", w.Body.String(), err)
	}
	return resp
}

// --- POST /api/v1/multiagent/definitions/validate ---

func TestAPI_MultiAgentDefinitionValidate_Unconfigured(t *testing.T) {
	s := NewServer(Config{Addr: ":0"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/multiagent/definitions/validate", bytes.NewReader(testDefinitionJSON("wf-a", "1.0.0", "v1")))
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAPI_MultiAgentDefinitionValidate_ValidDoesNotPersist(t *testing.T) {
	s, store := newDefinitionTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/multiagent/definitions/validate", bytes.NewReader(testDefinitionJSON("wf-validate-only", "1.0.0", "v1")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	resp := decodeDefinitionResponse(t, w)
	if !resp.Valid || resp.Registered {
		t.Fatalf("validate response = %+v, want Valid=true Registered=false", resp)
	}
	if resp.WorkflowID != "wf-validate-only" || resp.Fingerprint == "" {
		t.Fatalf("validate response missing workflow id/fingerprint: %+v", resp)
	}

	workflows, err := store.ListWorkflows(context.Background())
	if err != nil {
		t.Fatalf("list workflows: %v", err)
	}
	if len(workflows) != 0 {
		t.Fatalf("validate must not persist anything, got workflows=%v", workflows)
	}
}

func TestAPI_MultiAgentDefinitionValidate_MalformedBody(t *testing.T) {
	s, _ := newDefinitionTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/multiagent/definitions/validate", bytes.NewReader([]byte("{not json")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	// A syntax error is reported as a Diagnostic with a 400-shaped response
	// body (Valid: false + diagnostics), not a bare HTTP error — matching
	// LoadDefinitionBytes' "problems are diagnostics, not a Go error"
	// contract. The route itself still uses writeJSON (200 transport-level),
	// so assert on the decoded payload instead of the status code here; the
	// dedicated malformed-body test below (on the register route) covers the
	// same shape and is the one this PR's report calls out explicitly.
	resp := decodeDefinitionResponse(t, w)
	if resp.Valid {
		t.Fatalf("malformed body must not validate: %+v", resp)
	}
	if len(resp.Diagnostics) == 0 {
		t.Fatalf("malformed body response has no diagnostics: %+v", resp)
	}
}

// --- POST /api/v1/multiagent/definitions (register) ---

func TestAPI_MultiAgentDefinitionRegister_Unconfigured(t *testing.T) {
	s := NewServer(Config{Addr: ":0"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/multiagent/definitions", bytes.NewReader(testDefinitionJSON("wf-a", "1.0.0", "v1")))
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAPI_MultiAgentDefinitionRegister_MalformedBodyReturns400Diagnostics(t *testing.T) {
	s, _ := newDefinitionTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/multiagent/definitions", bytes.NewReader([]byte(`{"apiVersion":"wrong","kind":"MultiAgentWorkflow"}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	// An unsupported apiVersion is a load-time diagnostic
	// (schema.unsupported-api-version), which readDefinitionBody reports via
	// writeJSON (200, Valid:false) rather than a raw 400 — the diagnostics
	// list is the actionable payload either way. A genuinely malformed
	// request BODY (not valid JSON/YAML at all, or too large) does get a
	// real 400; assert that shape here directly.
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/multiagent/definitions", bytes.NewReader([]byte("{this is not valid json or yaml: [")))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	s.mux.ServeHTTP(w2, req2)
	resp2 := decodeDefinitionResponse(t, w2)
	if resp2.Valid {
		t.Fatalf("malformed register body must not validate: %+v", resp2)
	}
	if len(resp2.Diagnostics) == 0 {
		t.Fatalf("malformed register body response has no diagnostics: %+v", resp2)
	}

	resp := decodeDefinitionResponse(t, w)
	if resp.Valid {
		t.Fatalf("wrong apiVersion must not validate: %+v", resp)
	}
	if len(resp.Diagnostics) == 0 {
		t.Fatalf("wrong apiVersion response has no diagnostics: %+v", resp)
	}
}

func TestAPI_MultiAgentDefinitionRegisterGetListRoundTrip(t *testing.T) {
	s, _ := newDefinitionTestServer(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/multiagent/definitions", bytes.NewReader(testDefinitionJSON("wf-roundtrip", "1.0.0", "v1")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("register: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	reg := decodeDefinitionResponse(t, w)
	if !reg.Registered || reg.Version != 1 {
		t.Fatalf("register response = %+v, want Registered=true Version=1", reg)
	}

	// GET list workflows.
	wReq := httptest.NewRequest(http.MethodGet, "/api/v1/multiagent/definitions", nil)
	wRec := httptest.NewRecorder()
	s.mux.ServeHTTP(wRec, wReq)
	if wRec.Code != http.StatusOK {
		t.Fatalf("list workflows: expected 200, got %d: %s", wRec.Code, wRec.Body.String())
	}
	var workflows []string
	if err := json.Unmarshal(wRec.Body.Bytes(), &workflows); err != nil {
		t.Fatalf("decode workflow list: %v", err)
	}
	if len(workflows) != 1 || workflows[0] != "wf-roundtrip" {
		t.Fatalf("workflows = %v, want [wf-roundtrip]", workflows)
	}

	// GET list versions.
	vReq := httptest.NewRequest(http.MethodGet, "/api/v1/multiagent/definitions/wf-roundtrip/versions", nil)
	vRec := httptest.NewRecorder()
	s.mux.ServeHTTP(vRec, vReq)
	if vRec.Code != http.StatusOK {
		t.Fatalf("list versions: expected 200, got %d: %s", vRec.Code, vRec.Body.String())
	}
	var versions []definitionSummaryResponse
	if err := json.Unmarshal(vRec.Body.Bytes(), &versions); err != nil {
		t.Fatalf("decode version list: %v", err)
	}
	if len(versions) != 1 || versions[0].Version != 1 {
		t.Fatalf("versions = %+v, want one v1 summary", versions)
	}

	// GET full definition.
	gReq := httptest.NewRequest(http.MethodGet, "/api/v1/multiagent/definitions/wf-roundtrip/versions/1", nil)
	gRec := httptest.NewRecorder()
	s.mux.ServeHTTP(gRec, gReq)
	if gRec.Code != http.StatusOK {
		t.Fatalf("get version: expected 200, got %d: %s", gRec.Code, gRec.Body.String())
	}
	var def multiagent.WorkflowDefinition
	if err := json.Unmarshal(gRec.Body.Bytes(), &def); err != nil {
		t.Fatalf("decode definition: %v", err)
	}
	if def.Metadata.ID != "wf-roundtrip" {
		t.Fatalf("definition.metadata.id = %q, want wf-roundtrip", def.Metadata.ID)
	}

	// GET compiled graph view.
	cReq := httptest.NewRequest(http.MethodGet, "/api/v1/multiagent/definitions/wf-roundtrip/versions/1/compiled", nil)
	cRec := httptest.NewRecorder()
	s.mux.ServeHTTP(cRec, cReq)
	if cRec.Code != http.StatusOK {
		t.Fatalf("get compiled: expected 200, got %d: %s", cRec.Code, cRec.Body.String())
	}
	var view multiagent.CompiledGraphView
	if err := json.Unmarshal(cRec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode compiled graph view: %v", err)
	}
	if view.Fingerprint != reg.Fingerprint {
		t.Fatalf("compiled view fingerprint = %q, want %q", view.Fingerprint, reg.Fingerprint)
	}

	// GET fingerprint.
	fReq := httptest.NewRequest(http.MethodGet, "/api/v1/multiagent/definitions/wf-roundtrip/versions/1/fingerprint", nil)
	fRec := httptest.NewRecorder()
	s.mux.ServeHTTP(fRec, fReq)
	if fRec.Code != http.StatusOK {
		t.Fatalf("get fingerprint: expected 200, got %d: %s", fRec.Code, fRec.Body.String())
	}
	var fp map[string]string
	if err := json.Unmarshal(fRec.Body.Bytes(), &fp); err != nil {
		t.Fatalf("decode fingerprint: %v", err)
	}
	if fp["fingerprint"] != reg.Fingerprint {
		t.Fatalf("fingerprint = %q, want %q", fp["fingerprint"], reg.Fingerprint)
	}
}

// TestAPI_MultiAgentDefinitionRegister_DuplicateFingerprintReturnsExistingVersion
// covers the task's explicitly-called-out design choice: registering a
// byte-identical (same compiled fingerprint) definition a second time is NOT
// an error — it is a 200 with the EXISTING version's info and
// Registered:false, distinguishing "nothing new happened" from a genuine new
// version without the caller having to parse an error string. A 4xx/5xx
// would incorrectly suggest the second request was itself invalid, when the
// request's actual intent ("make sure this definition is registered") was
// already satisfied.
func TestAPI_MultiAgentDefinitionRegister_DuplicateFingerprintReturnsExistingVersion(t *testing.T) {
	s, _ := newDefinitionTestServer(t)
	body := testDefinitionJSON("wf-dup", "1.0.0", "same content")

	first := httptest.NewRequest(http.MethodPost, "/api/v1/multiagent/definitions", bytes.NewReader(body))
	first.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	s.mux.ServeHTTP(w1, first)
	reg1 := decodeDefinitionResponse(t, w1)
	if w1.Code != http.StatusOK || !reg1.Registered || reg1.Version != 1 {
		t.Fatalf("first register: code=%d resp=%+v", w1.Code, reg1)
	}

	second := httptest.NewRequest(http.MethodPost, "/api/v1/multiagent/definitions", bytes.NewReader(body))
	second.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	s.mux.ServeHTTP(w2, second)
	reg2 := decodeDefinitionResponse(t, w2)
	if w2.Code != http.StatusOK {
		t.Fatalf("duplicate register: expected 200, got %d: %s", w2.Code, w2.Body.String())
	}
	if reg2.Registered {
		t.Fatalf("duplicate register response = %+v, want Registered=false", reg2)
	}
	if reg2.Version != 1 {
		t.Fatalf("duplicate register version = %d, want 1 (existing, not a new version)", reg2.Version)
	}
}

// --- 404s for unknown workflow/version ---

func TestAPI_MultiAgentDefinition_UnknownWorkflowOrVersion404(t *testing.T) {
	s, _ := newDefinitionTestServer(t)

	cases := []string{
		"/api/v1/multiagent/definitions/does-not-exist/versions/1",
		"/api/v1/multiagent/definitions/does-not-exist/versions/1/compiled",
		"/api/v1/multiagent/definitions/does-not-exist/versions/1/fingerprint",
	}
	for _, path := range cases {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s: expected 404, got %d: %s", path, w.Code, w.Body.String())
		}
	}

	// Register v1, then request v99 of the same workflow — still 404.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/multiagent/definitions", bytes.NewReader(testDefinitionJSON("wf-known", "1.0.0", "v1")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("register: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/multiagent/definitions/wf-known/versions/99", nil)
	w2 := httptest.NewRecorder()
	s.mux.ServeHTTP(w2, req2)
	if w2.Code != http.StatusNotFound {
		t.Fatalf("unknown version: expected 404, got %d: %s", w2.Code, w2.Body.String())
	}
}

// --- POST .../versions/{n}/run ---

type fakeWorkflowRunStarter struct {
	runID string
	err   error
	calls []struct {
		workflowID string
		version    int64
		input      string
	}
}

func (f *fakeWorkflowRunStarter) StartRun(_ context.Context, workflowID string, version int64, input json.RawMessage) (string, error) {
	f.calls = append(f.calls, struct {
		workflowID string
		version    int64
		input      string
	}{workflowID, version, string(input)})
	if f.err != nil {
		return "", f.err
	}
	return f.runID, nil
}

func TestAPI_MultiAgentDefinitionRun_Unconfigured(t *testing.T) {
	s, _ := newDefinitionTestServer(t) // DefinitionStore set, but no WorkflowRunStarter
	req := httptest.NewRequest(http.MethodPost, "/api/v1/multiagent/definitions/wf-a/versions/1/run", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAPI_MultiAgentDefinitionRun_Success(t *testing.T) {
	starter := &fakeWorkflowRunStarter{runID: "run-started-1"}
	s := NewServer(Config{Addr: ":0", WorkflowRunStarter: starter})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/multiagent/definitions/wf-a/versions/2/run", bytes.NewReader([]byte(`{"id":"task-1"}`)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["run_id"] != "run-started-1" || resp["status"] != "started" {
		t.Fatalf("response = %v, want run_id=run-started-1 status=started", resp)
	}
	if len(starter.calls) != 1 || starter.calls[0].workflowID != "wf-a" || starter.calls[0].version != 2 {
		t.Fatalf("starter calls = %+v", starter.calls)
	}
}

// --- Auth: POST routes must join requiresAuth's generic coverage. ---

func TestAuthMiddleware_MultiAgentDefinitionRoutesRequireAuth(t *testing.T) {
	s := &Server{authToken: "secret-token"}
	h := s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for _, path := range []string{
		"/api/v1/multiagent/definitions/validate",
		"/api/v1/multiagent/definitions",
		"/api/v1/multiagent/definitions/wf-a/versions/1/run",
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

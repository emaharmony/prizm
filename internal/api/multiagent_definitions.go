package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/emaharmony/prism/internal/workflow/multiagent"
)

// WorkflowRunStarter is the narrow, transport-facing seam POST
// .../definitions/{id}/versions/{n}/run calls through — the definition-
// registry-backed sibling of MultiAgentController (multiagent_controller.go).
// It intentionally knows nothing about agents, providers, tools, or how a
// live runtime is assembled: that composition lives in cmd/prism-cli, which
// implements this interface by reusing the exact same composition PR6's
// `prism graph run` CLI command uses (see cmd/prism-cli/cmd_graph_run.go's
// graphRunStarter). Implementations are expected to return once the run has
// been durably created rather than blocking for the run's full duration —
// the caller observes progress via the existing SSE stream — mirroring
// MultiAgentController.Resume's documented contract.
type WorkflowRunStarter interface {
	StartRun(ctx context.Context, workflowID string, version int64, input json.RawMessage) (runID string, err error)
}

// definitionRegistrationResponse is the JSON shape returned by both the
// validate-only and register routes. Registered is false for /validate
// (nothing was persisted) and for a register call that resolved to
// ErrDefinitionUnchanged (an existing version was returned, not a new one).
type definitionRegistrationResponse struct {
	WorkflowID    string                 `json:"workflow_id"`
	Version       int64                  `json:"version,omitempty"`
	UserVersion   string                 `json:"user_version,omitempty"`
	SchemaVersion string                 `json:"schema_version,omitempty"`
	Fingerprint   string                 `json:"fingerprint,omitempty"`
	CreatedAt     time.Time              `json:"created_at,omitempty"`
	Registered    bool                   `json:"registered"`
	Valid         bool                   `json:"valid"`
	Diagnostics   multiagent.Diagnostics `json:"diagnostics,omitempty"`
}

// definitionSummaryResponse mirrors multiagent.DefinitionSummary with JSON
// tags matching this API's naming convention (multiagent.DefinitionSummary
// itself carries no json tags, since it is not directly serialized anywhere
// else in the package).
type definitionSummaryResponse struct {
	Version       int64     `json:"version"`
	UserVersion   string    `json:"user_version"`
	SchemaVersion string    `json:"schema_version"`
	Fingerprint   string    `json:"fingerprint"`
	SourceRef     string    `json:"source_ref,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

func (s *Server) definitionStoreConfigured() bool {
	return s.definitionStore != nil
}

// handleMultiAgentDefinitions dispatches GET (list workflow ids) and POST
// (register) on the exact "/api/v1/multiagent/definitions" path.
func (s *Server) handleMultiAgentDefinitions(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleListWorkflows(w, r)
	case http.MethodPost:
		s.handleRegisterDefinition(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleMultiAgentDefinitionSub dispatches every "/api/v1/multiagent/
// definitions/..." route with at least one path segment after the prefix,
// mirroring handleMultiAgentRunSub's suffix-dispatch style.
func (s *Server) handleMultiAgentDefinitionSub(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/multiagent/definitions/")
	if rest == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if rest == "validate" {
		s.handleValidateDefinition(w, r)
		return
	}

	parts := strings.Split(rest, "/")
	switch {
	case len(parts) == 2 && parts[1] == "versions":
		s.handleListVersions(w, r, parts[0])
	case len(parts) == 3 && parts[1] == "versions":
		s.handleGetVersion(w, r, parts[0], parts[2])
	case len(parts) == 4 && parts[1] == "versions" && parts[3] == "compiled":
		s.handleGetCompiledVersion(w, r, parts[0], parts[2])
	case len(parts) == 4 && parts[1] == "versions" && parts[3] == "fingerprint":
		s.handleGetVersionFingerprint(w, r, parts[0], parts[2])
	case len(parts) == 4 && parts[1] == "versions" && parts[3] == "run":
		s.handleRunVersion(w, r, parts[0], parts[2])
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// handleValidateDefinition loads+compiles the request body WITHOUT
// persisting anything, regardless of outcome — the register route below is
// the only one that ever writes to the definition store.
func (s *Server) handleValidateDefinition(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.definitionStoreConfigured() {
		writeJSONError(w, "multi-agent definition store not configured", http.StatusServiceUnavailable)
		return
	}
	def, idx, diags, ok := s.readDefinitionBody(w, r)
	if !ok {
		return
	}
	graph, compileDiags, err := multiagent.Compile(def, idx, multiagent.CompileOptions{})
	diags = append(diags, compileDiags...)
	if err != nil {
		writeJSON(w, definitionRegistrationResponse{Valid: false, Diagnostics: diags})
		return
	}
	writeJSON(w, definitionRegistrationResponse{
		WorkflowID:    graph.WorkflowID(),
		UserVersion:   graph.WorkflowVersion(),
		SchemaVersion: graph.SchemaVersion(),
		Fingerprint:   graph.Fingerprint(),
		Valid:         true,
		Diagnostics:   diags,
	})
}

// handleRegisterDefinition loads+compiles the request body and, only if
// compilation succeeds, registers it. A duplicate-fingerprint registration
// (multiagent.ErrDefinitionUnchanged) is reported as a 200 with the EXISTING
// version's info and Registered: false — it is not a client error (the
// request was well-formed and the intent, "make sure this definition is
// registered," was already satisfied), so a 4xx/5xx would be misleading;
// Registered: false is how a caller distinguishes "nothing new happened"
// from an actual new version without parsing prose.
func (s *Server) handleRegisterDefinition(w http.ResponseWriter, r *http.Request) {
	if !s.definitionStoreConfigured() {
		writeJSONError(w, "multi-agent definition store not configured", http.StatusServiceUnavailable)
		return
	}
	def, idx, diags, ok := s.readDefinitionBody(w, r)
	if !ok {
		return
	}
	graph, compileDiags, err := multiagent.Compile(def, idx, multiagent.CompileOptions{})
	diags = append(diags, compileDiags...)
	if err != nil {
		writeJSON(w, definitionRegistrationResponse{Valid: false, Diagnostics: diags})
		return
	}

	sourceRef := r.URL.Query().Get("source_ref")
	reg, err := s.definitionStore.Register(r.Context(), def, graph, sourceRef)
	switch {
	case err == nil:
		writeJSON(w, definitionResponseFromRegistration(reg, true, diags))
	case errors.Is(err, multiagent.ErrDefinitionUnchanged):
		writeJSON(w, definitionResponseFromRegistration(reg, false, diags))
	default:
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
	}
}

func definitionResponseFromRegistration(reg multiagent.RegisteredDefinition, registered bool, diags multiagent.Diagnostics) definitionRegistrationResponse {
	return definitionRegistrationResponse{
		WorkflowID:    reg.WorkflowID,
		Version:       reg.Version,
		UserVersion:   reg.UserVersion,
		SchemaVersion: reg.SchemaVersion,
		Fingerprint:   reg.Fingerprint,
		CreatedAt:     reg.CreatedAt,
		Registered:    registered,
		Valid:         true,
		Diagnostics:   diags,
	}
}

// handleListWorkflows lists every workflow_id with at least one registered
// version.
func (s *Server) handleListWorkflows(w http.ResponseWriter, r *http.Request) {
	if !s.definitionStoreConfigured() {
		writeJSONError(w, "multi-agent definition store not configured", http.StatusServiceUnavailable)
		return
	}
	ids, err := s.definitionStore.ListWorkflows(r.Context())
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if ids == nil {
		ids = []string{}
	}
	writeJSON(w, ids)
}

// handleListVersions lists every registered version summary for one
// workflow_id, oldest first.
func (s *Server) handleListVersions(w http.ResponseWriter, r *http.Request, workflowID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.definitionStoreConfigured() {
		writeJSONError(w, "multi-agent definition store not configured", http.StatusServiceUnavailable)
		return
	}
	if !validWorkflowID(workflowID) {
		writeJSONError(w, "invalid workflow id", http.StatusBadRequest)
		return
	}
	versions, err := s.definitionStore.ListVersions(r.Context(), workflowID)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]definitionSummaryResponse, 0, len(versions))
	for _, v := range versions {
		out = append(out, definitionSummaryResponse{
			Version: v.Version, UserVersion: v.UserVersion, SchemaVersion: v.SchemaVersion,
			Fingerprint: v.Fingerprint, SourceRef: v.SourceRef, CreatedAt: v.CreatedAt,
		})
	}
	writeJSON(w, out)
}

// handleGetVersion returns the full WorkflowDefinition for one registered
// version.
func (s *Server) handleGetVersion(w http.ResponseWriter, r *http.Request, workflowID, versionStr string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.definitionStoreConfigured() {
		writeJSONError(w, "multi-agent definition store not configured", http.StatusServiceUnavailable)
		return
	}
	reg, ok := s.resolveDefinitionVersion(w, r, workflowID, versionStr)
	if !ok {
		return
	}
	writeJSON(w, reg.Definition)
}

// handleGetCompiledVersion returns the CompiledGraphView JSON for one
// registered version.
func (s *Server) handleGetCompiledVersion(w http.ResponseWriter, r *http.Request, workflowID, versionStr string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.definitionStoreConfigured() {
		writeJSONError(w, "multi-agent definition store not configured", http.StatusServiceUnavailable)
		return
	}
	reg, ok := s.resolveDefinitionVersion(w, r, workflowID, versionStr)
	if !ok {
		return
	}
	writeJSON(w, multiagent.BuildCompiledGraphView(reg.Graph))
}

// handleGetVersionFingerprint returns just the fingerprint string for one
// registered version.
func (s *Server) handleGetVersionFingerprint(w http.ResponseWriter, r *http.Request, workflowID, versionStr string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.definitionStoreConfigured() {
		writeJSONError(w, "multi-agent definition store not configured", http.StatusServiceUnavailable)
		return
	}
	reg, ok := s.resolveDefinitionVersion(w, r, workflowID, versionStr)
	if !ok {
		return
	}
	writeJSON(w, map[string]string{"fingerprint": reg.Fingerprint})
}

// handleRunVersion starts a pinned run against exactly (workflowID,
// version) — never "latest" — via WorkflowRunStarter. The request body, if
// any, is passed through verbatim as the run's task input.
func (s *Server) handleRunVersion(w http.ResponseWriter, r *http.Request, workflowID, versionStr string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.workflowRunStarter == nil {
		writeJSONError(w, "multi-agent workflow run starter not configured", http.StatusServiceUnavailable)
		return
	}
	if !validWorkflowID(workflowID) {
		writeJSONError(w, "invalid workflow id", http.StatusBadRequest)
		return
	}
	version, err := strconv.ParseInt(versionStr, 10, 64)
	if err != nil || version <= 0 {
		writeJSONError(w, "invalid version", http.StatusBadRequest)
		return
	}

	var input json.RawMessage
	if r.Body != nil && r.ContentLength != 0 {
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.maxRequestBytes))
		if err != nil {
			writeJSONError(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if len(data) > 0 {
			if !json.Valid(data) {
				writeJSONError(w, "invalid JSON body", http.StatusBadRequest)
				return
			}
			input = json.RawMessage(data)
		}
	}

	runID, err := s.workflowRunStarter.StartRun(r.Context(), workflowID, version, input)
	if err != nil {
		s.writeDefinitionRunError(w, err)
		return
	}
	writeJSON(w, map[string]string{"run_id": runID, "status": "started"})
}

// resolveDefinitionVersion validates workflowID/versionStr, loads the
// registered definition, and writes the appropriate error response (400/404/
// 500) on failure — returning ok == false in every case where the caller
// must not proceed further.
func (s *Server) resolveDefinitionVersion(w http.ResponseWriter, r *http.Request, workflowID, versionStr string) (multiagent.RegisteredDefinition, bool) {
	if !validWorkflowID(workflowID) {
		writeJSONError(w, "invalid workflow id", http.StatusBadRequest)
		return multiagent.RegisteredDefinition{}, false
	}
	version, err := strconv.ParseInt(versionStr, 10, 64)
	if err != nil || version <= 0 {
		writeJSONError(w, "invalid version", http.StatusBadRequest)
		return multiagent.RegisteredDefinition{}, false
	}
	reg, err := s.definitionStore.Get(r.Context(), workflowID, version)
	if err != nil {
		if errors.Is(err, multiagent.ErrDefinitionNotFound) {
			writeJSONError(w, "definition not found", http.StatusNotFound)
		} else {
			writeJSONError(w, err.Error(), http.StatusInternalServerError)
		}
		return multiagent.RegisteredDefinition{}, false
	}
	return reg, true
}

// readDefinitionBody reads and loads (but does not validate business rules
// beyond what LoadDefinitionBytes itself checks — apiVersion/kind/unknown
// fields/syntax) the request body as a WorkflowDefinition, format selected
// by Content-Type ("application/yaml"/"application/x-yaml"/"text/yaml"/
// "text/x-yaml" decode as YAML; everything else, including an absent or
// "application/json" Content-Type, decodes as JSON). On any load-time
// problem (malformed body, unsupported schema version, unknown field) this
// writes a 400 response with the diagnostics and returns ok == false; a
// caller that gets ok == true still owns running Compile (and its own
// diagnostics) before deciding whether the definition is usable.
func (s *Server) readDefinitionBody(w http.ResponseWriter, r *http.Request) (multiagent.WorkflowDefinition, *multiagent.PositionIndex, multiagent.Diagnostics, bool) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.maxRequestBytes))
	if err != nil {
		writeJSONError(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return multiagent.WorkflowDefinition{}, nil, nil, false
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		writeJSONError(w, "request body must contain a workflow definition", http.StatusBadRequest)
		return multiagent.WorkflowDefinition{}, nil, nil, false
	}
	format := definitionFormatFromContentType(r.Header.Get("Content-Type"))
	def, idx, diags, err := multiagent.LoadDefinitionBytes(data, format, "request-body")
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return multiagent.WorkflowDefinition{}, nil, nil, false
	}
	if diags.HasErrors() {
		writeJSON(w, definitionRegistrationResponse{Valid: false, Diagnostics: diags})
		return multiagent.WorkflowDefinition{}, nil, nil, false
	}
	return def, idx, diags, true
}

func definitionFormatFromContentType(contentType string) string {
	ct := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	switch ct {
	case "application/yaml", "application/x-yaml", "text/yaml", "text/x-yaml":
		return "yaml"
	default:
		return "json"
	}
}

// validWorkflowID rejects workflow ids this API should never be asked to
// resolve: empty, ".", "..", or containing a path separator — mirroring
// validMultiAgentRunID's exact rationale (RunLocator/DefinitionStore never
// treat an id as a filesystem path themselves, but rejecting these shapes
// early keeps the same defensive posture as the run-id validator right next
// to it).
func validWorkflowID(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" || id == "." || id == ".." {
		return false
	}
	return !strings.ContainsAny(id, `/\`)
}

// writeDefinitionRunError classifies a WorkflowRunStarter error into the
// right HTTP status. errors.Is against multiagent's own sentinels works
// regardless of whether cmd/prism-cli's implementation wraps them with %w.
func (s *Server) writeDefinitionRunError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, multiagent.ErrDefinitionNotFound):
		writeJSONError(w, "definition not found", http.StatusNotFound)
	default:
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
	}
}

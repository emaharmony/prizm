// Package api provides the Prizm HTTP API server (REST + SSE).
//
// Endpoints:
//
//	GET /api/v1/status          — System status
//	GET /api/v1/agents           — List agents
//	GET /api/v1/agents/{id}      — Agent detail
//	POST /api/v1/agents/{id}/invoke — Single-shot agent invocation (requires agent opt-in)
//	GET /api/v1/agents/{id}/invocations/{invocation_id} — Poll invocation result
//	GET /api/v1/sessions          — List sessions
//	GET /api/v1/sessions/{id}    — Session detail
//	GET /api/v1/tasks             — List tasks
//	GET /api/v1/tasks/{id}       — Task detail
//	POST /api/v1/autopatch        — Start bug diagnosis + patch proposal
//	GET /api/v1/approvals         — List pending approvals
//	POST /api/v1/approvals/{id}/grant — Grant approval
//	POST /api/v1/approvals/{id}/deny   — Deny approval
//	GET /api/v1/events/stream    — SSE event stream
//	GET /api/v1/workflows        — List workflow types
//	GET /api/v1/workflows/{type} — SVG diagram
//	GET /api/v1/editor          — Get editor state (current config as graph)
//	PUT /api/v1/editor          — Validate + preview YAML from editor state
//	GET /api/v1/editor/nodes    — List nodes
//	POST /api/v1/editor/nodes    — Add a node
//	PUT /api/v1/editor/nodes/{id} — Update a node
//	DELETE /api/v1/editor/nodes/{id} — Delete a node
//	GET /api/v1/editor/edges    — List edges
//	POST /api/v1/editor/edges    — Add an edge
//	DELETE /api/v1/editor/edges/{id} — Delete an edge
//	POST /api/v1/editor/save     — Validate + generate YAML
//	GET /api/v1/costs             — Cost summary
//	GET /api/v1/usage             — Token-usage time series + breakdowns (?range=)
//	GET /api/v1/config            — Curated prizm.yaml settings + scheduler jobs
//	GET /api/v1/config/agents     — Per-agent editable fields
//	PUT /api/v1/config/agents/{id} — Surgically edit one agent's personality/rules
//	GET /api/v1/workspace/files   — List shared workspace markdown files
//	GET /api/v1/workspace/files/{name} — Read one workspace file
//	PUT /api/v1/workspace/files/{name} — Write one workspace file (jailed, atomic)
//	PUT /api/v1/config/settings   — Surgically edit curated prizm.yaml settings
//	PUT /api/v1/config/scheduler  — Surgically edit prizm.scheduler jobs
//	POST /api/v1/config/cron/validate — Validate a cron expression
//	GET /api/v1/config/actions    — Known wake actions (cron presets)
//	GET /                          — Embedded dashboard UI (when folded into serve)
package api

import (
	contextctx "context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emaharmony/prizm/internal/agentns"
	"github.com/emaharmony/prizm/internal/autopatch"
	costpkg "github.com/emaharmony/prizm/internal/cost"
	"github.com/emaharmony/prizm/internal/context"
	"github.com/emaharmony/prizm/internal/delegation"
	"github.com/emaharmony/prizm/internal/editor"
	"github.com/emaharmony/prizm/internal/invocation"
	"github.com/emaharmony/prizm/internal/memory"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/remembrance"
	"github.com/emaharmony/prizm/internal/session"
	"github.com/emaharmony/prizm/internal/sessionreset"
	"github.com/emaharmony/prizm/internal/task"
	"github.com/emaharmony/prizm/internal/usage"
	"github.com/emaharmony/prizm/internal/workflow"
	v2 "github.com/emaharmony/prizm/internal/workflow/v2"
	"github.com/emaharmony/prizm/internal/toolloop"
	"github.com/emaharmony/prizm/internal/tool"
	"github.com/emaharmony/prizm/internal/workstart"
	"github.com/nats-io/nats.go"
)

// MemoryInjectorInterface is the interface for smart memory injection.
// The cmd/prizm-cli package provides the concrete MemoryInjector implementation
// with query planning, embedding search, and grounding-aware formatting.
// The API package uses this interface to avoid importing cmd types.
type MemoryInjectorInterface interface {
	InjectMemoriesInt(ctx contextctx.Context, mode int, userMessage string, sessionMsgCount int, maxTokens int) string
}

// CoreIdentityInterface is the interface for the permanent core identity block.
// The cmd/prizm-cli package provides the concrete CoreIdentityBlock implementation.
type CoreIdentityInterface interface {
	Build(memStore *memory.MarkdownStore) string
}

// Server provides the Prizm HTTP API.
type Server struct {
	addr        string
	orch        *orchestrator.Orchestrator
	store       *task.Store
	sessions    *session.Manager
	engine      *delegation.Engine
	approval    *delegation.ApprovalManager
	tracker     *delegation.Tracker
	autopatch   AutoPatchStarter
	nc          *nats.Conn
	mux         *http.ServeMux
	editorMu    sync.RWMutex
	editorWS    *editor.EditorState
	providers   *provider.ProviderRegistry
	invocations *invocation.Store
	// invokeIdleTimeout resets a stateful invoke conversation after this idle
	// window (0 = no idle safety net). See SessionConfig.InvokeIdleTimeoutHours.
	invokeIdleTimeout time.Duration

	// authToken, when non-empty, is the bearer token required on
	// state-changing endpoints and the SSE stream. Empty disables auth
	// (only safe on a loopback bind, enforced by config validation).
	authToken string
	// allowedOrigins is the CORS origin allowlist. Empty = same-origin only.
	allowedOrigins []string
	// configDir jails editor config writes (POST /editor/save).
	configDir string
	// workflowConfigPath is the gated-loop workflow definition file the
	// dashboard workflow editor reads and writes. Empty → read-only default.
	workflowConfigPath string

	// CtxBuilder builds the system prompt (SOUL.md, USER.md, context files) for invoke.
	ctxBuilder *context.Builder

	// memStoreForInvoke is the MarkdownStore used for memory injection in invokes.
	memStoreForInvoke *memory.MarkdownStore
	// memInjectorForInvoke is the smart memory injector (query planner + embedding + grounding).
	// Falls back to memStoreForInvoke.Search when nil.
	memInjectorForInvoke MemoryInjectorInterface
	// coreIdentityForInject is the permanent core identity block for invoke prompts.
	coreIdentityForInject CoreIdentityInterface

	// toolRegForInvoke is the tool registry for invoke tool loops.
	// When set, the invoke path uses toolloop.RunChatLoop so the agent can call
	// tools (memory_search, etc.) instead of a single-shot LLM call.
	toolRegForInvoke *tool.Registry
	// toolExecForInvoke is the tool executor for invoke tool loops.
	toolExecForInvoke *tool.Executor

	// memStore is the local MarkdownStore for the memories API.
	memStore *memory.MarkdownStore

	// remClient is the Remembrance client for the memories API.
	remClient *remembrance.Client
	// configPath is the prizm.yaml file the config/scheduler editors read and
	// surgically write. Empty → config editing disabled (endpoints 400).
	configPath string
	// schedulerActions are the known wake actions offered as cron-job presets.
	schedulerActions []SchedulerAction
	// staticUI serves the embedded dashboard pages at / when non-nil (folded
	// into `prizm serve`).
	staticUI http.Handler
	// usage is the token-usage store backing GET /api/v1/usage. Nil → 503.
	usage *usage.Store
	// usageWindows maps a usage range key to its window/bucket spec. Nil → the
	// built-in DefaultUsageWindows.
	usageWindows map[string]WindowSpec
	// workspace is the root directory the workspace file editor reads/writes
	// (jailed). Empty → workspace file endpoints 400.
	workspace string
	// maxRequestBytes caps the JSON body on mutating endpoints. 0 → 1 MiB.
	maxRequestBytes int64
	// maxWorkspaceFileBytes caps a single workspace file write. 0 → 4 MiB.
	maxWorkspaceFileBytes int64
}

// SchedulerAction describes a wake action a cron job can trigger. Presented in
// the dashboard cron editor's action dropdown.
type SchedulerAction struct {
	Key     string `json:"key"`
	SkipLLM bool   `json:"skip_llm"`
}

// Config holds API server configuration.
type Config struct {
	Addr      string // listen address (e.g., ":8081")
	Orch      *orchestrator.Orchestrator
	Store     *task.Store
	Sessions  *session.Manager
	Engine    *delegation.Engine
	Approval  *delegation.ApprovalManager
	Tracker   *delegation.Tracker
	AutoPatch AutoPatchStarter
	NATS      *nats.Conn
	// Providers resolves an agent's configured model to an LLM provider for
	// single-shot invocations (POST /api/v1/agents/{id}/invoke). Nil disables
	// the invocation endpoints (503).
	Providers *provider.ProviderRegistry
	// InvokeIdleTimeout resets a stateful invoke conversation (a call carrying a
	// conversation_id) after this idle window. Zero disables the safety net.
	InvokeIdleTimeout time.Duration

	// AuthToken is the bearer token required on mutating endpoints + SSE.
	AuthToken string
	// AllowedOrigins is the CORS origin allowlist (empty = same-origin only).
	AllowedOrigins []string
	// ConfigDir jails editor config writes to this directory.
	ConfigDir string
	// WorkflowConfigPath is the gated-loop workflow definition file path.
	WorkflowConfigPath string
	// ConfigPath is the prizm.yaml path the config/scheduler editors read/write.
	ConfigPath string
	// SchedulerActions are the known wake actions offered as cron-job presets.
	SchedulerActions []SchedulerAction
	// StaticUI, when non-nil, serves the embedded dashboard pages at /.
	StaticUI http.Handler
	// Usage is the token-usage store backing GET /api/v1/usage. Nil disables it.
	Usage *usage.Store
	// UsageWindows overrides the range→window/bucket mapping for GET
	// /api/v1/usage. Nil uses DefaultUsageWindows.
	UsageWindows map[string]WindowSpec
	// Workspace is the root directory the workspace file editor reads/writes.
	// Empty disables the workspace file endpoints.
	Workspace string
	// MaxRequestBytes caps the JSON body on mutating endpoints. 0 → 1 MiB.
	MaxRequestBytes int64
	// MaxWorkspaceFileBytes caps a single workspace file write. 0 → 4 MiB.
	MaxWorkspaceFileBytes int64
	// CtxBuilder builds the system prompt (SOUL.md, USER.md, context files) for invoke.
	// When nil, invokes use a minimal system prompt (ConversationPostfix only).
	CtxBuilder *context.Builder

	// MemStoreForInvoke is the local MarkdownStore for memory injection in invokes.
	MemStoreForInvoke *memory.MarkdownStore

	// MemInjectorForInvoke is the smart memory injector (query planner + embedding + grounding).
	// When nil, falls back to MemStoreForInvoke.Search (keyword only, no grounding).
	MemInjectorForInvoke MemoryInjectorInterface

	// CoreIdentityForInvoke is the core identity block for permanent facts in the system prompt.
	// When nil, no core identity block is injected.
	CoreIdentityForInvoke CoreIdentityInterface

	// ToolRegForInvoke is the tool registry for invoke tool loops. When nil, invoke
	// falls back to a single-shot LLM call with no tool support.
	ToolRegForInvoke *tool.Registry
	// ToolExecForInvoke is the tool executor for invoke tool loops.
	ToolExecForInvoke *tool.Executor

	// MemStore is the local MarkdownStore for the memories API. Nil → memories endpoints return empty.
	MemStore *memory.MarkdownStore

	// RemClient is the Remembrance client for the memories API. Nil → Remembrance source disabled.
	RemClient *remembrance.Client
}

// AutoPatchStarter is the API surface needed from the autopatch service.
type AutoPatchStarter interface {
	Enabled() bool
	Start(ctx contextctx.Context, req autopatch.Request) (*task.Task, error)
}

// NewServer creates a new API server.
func NewServer(cfg Config) *Server {
	s := &Server{
		addr:               cfg.Addr,
		orch:               cfg.Orch,
		store:              cfg.Store,
		sessions:           cfg.Sessions,
		engine:             cfg.Engine,
		approval:           cfg.Approval,
		tracker:            cfg.Tracker,
		autopatch:          cfg.AutoPatch,
		nc:                 cfg.NATS,
		authToken:          cfg.AuthToken,
		allowedOrigins:     cfg.AllowedOrigins,
		configDir:          cfg.ConfigDir,
		workflowConfigPath: cfg.WorkflowConfigPath,
		configPath:         cfg.ConfigPath,
		schedulerActions:   cfg.SchedulerActions,
		staticUI:           cfg.StaticUI,
		usage:              cfg.Usage,
		usageWindows:       cfg.UsageWindows,
		workspace:          cfg.Workspace,
		providers:          cfg.Providers,
		invocations:        invocation.NewStore(),
		invokeIdleTimeout:  cfg.InvokeIdleTimeout,
		mux:                http.NewServeMux(),

		maxRequestBytes:       cfg.MaxRequestBytes,
		maxWorkspaceFileBytes: cfg.MaxWorkspaceFileBytes,
		memStore:             cfg.MemStore,
		remClient:            cfg.RemClient,
		ctxBuilder:              cfg.CtxBuilder,
		memStoreForInvoke:        cfg.MemStoreForInvoke,
		memInjectorForInvoke:    cfg.MemInjectorForInvoke,
		coreIdentityForInject:   cfg.CoreIdentityForInvoke,
		toolRegForInvoke:        cfg.ToolRegForInvoke,
		toolExecForInvoke:       cfg.ToolExecForInvoke,
	}
	if s.maxRequestBytes <= 0 {
		s.maxRequestBytes = 1 << 20 // 1 MiB
	}
	if s.maxWorkspaceFileBytes <= 0 {
		s.maxWorkspaceFileBytes = 4 << 20 // 4 MiB
	}
	if s.usageWindows == nil {
		s.usageWindows = DefaultUsageWindows()
	}
	s.routes()
	return s
}

// routes registers all API endpoints.
func (s *Server) routes() {
	s.mux.HandleFunc("/api/v1/status", s.handleStatus)
	s.mux.HandleFunc("/api/v1/agents", s.handleAgents)
	s.mux.HandleFunc("/api/v1/agents/", s.handleAgentDetail)
	s.mux.HandleFunc("/api/v1/sessions", s.handleSessions)
	s.mux.HandleFunc("/api/v1/sessions/", s.handleSessionDetail)
	s.mux.HandleFunc("/api/v1/tasks", s.handleTasks)
	s.mux.HandleFunc("/api/v1/tasks/", s.handleTaskDetail)
	s.mux.HandleFunc("/api/v1/autopatch", s.handleAutoPatch)
	s.mux.HandleFunc("/api/v1/approvals", s.handleApprovals)
	s.mux.HandleFunc("/api/v1/approvals/", s.handleApprovalAction)
	s.mux.HandleFunc("/api/v1/events/stream", s.handleEventStream)
	s.mux.HandleFunc("/api/v1/workflows", s.handleWorkflows)
	s.mux.HandleFunc("/api/v1/workflows/start", s.handleWorkflowStart)
	s.mux.HandleFunc("/api/v1/workflows/feedback", s.handleWorkflowFeedback)
	s.mux.HandleFunc("/api/v1/workflows/runs", s.handleWorkflowRuns)
	s.mux.HandleFunc("/api/v1/workflows/runs/", s.handleWorkflowRunDetail)
	s.mux.HandleFunc("/api/v1/workflows/", s.handleWorkflowSVG)
	s.mux.HandleFunc("/api/v1/workflow-config", s.handleWorkflowConfig)
	s.mux.HandleFunc("/api/v1/editor", s.handleEditorState)
	s.mux.HandleFunc("/api/v1/editor/nodes", s.handleEditorNodes)
	s.mux.HandleFunc("/api/v1/editor/nodes/", s.handleEditorNodeCRUD)
	s.mux.HandleFunc("/api/v1/editor/edges", s.handleEditorEdges)
	s.mux.HandleFunc("/api/v1/editor/edges/", s.handleEditorEdgeCRUD)
	s.mux.HandleFunc("/api/v1/editor/save", s.handleEditorSave)
	s.mux.HandleFunc("/api/v1/costs", s.handleCosts)
	s.mux.HandleFunc("/api/v1/usage", s.handleUsage)

	// Config + scheduler editors (write prizm.yaml surgically).
	s.mux.HandleFunc("/api/v1/config", s.handleConfig)
	s.mux.HandleFunc("/api/v1/config/settings", s.handleConfigSettings)
	s.mux.HandleFunc("/api/v1/config/scheduler", s.handleConfigScheduler)
	s.mux.HandleFunc("/api/v1/config/cron/validate", s.handleCronValidate)
	s.mux.HandleFunc("/api/v1/config/actions", s.handleSchedulerActions)
	s.mux.HandleFunc("/api/v1/config/agents", s.handleConfigAgents)
	s.mux.HandleFunc("/api/v1/config/agents/", s.handleConfigAgentUpdate)

	// Workspace markdown editor (shared context files).
	s.mux.HandleFunc("/api/v1/workspace/files", s.handleWorkspaceFiles)
	s.mux.HandleFunc("/api/v1/workspace/files/", s.handleWorkspaceFile)

	// Memory visualizer API (local + optional Remembrance).
	s.mux.HandleFunc("/api/v1/memories", s.handleMemories)
	s.mux.HandleFunc("/api/v1/memories/categories", s.handleMemoriesCategories)
	s.mux.HandleFunc("/api/v1/memories/stats", s.handleMemoriesStats)
	s.mux.HandleFunc("/api/v1/memories/", s.handleMemoriesDetail)

	// Serve the embedded dashboard UI at / when wired (folded into `prizm
	// serve`). Specific /api/v1/... patterns above win under ServeMux
	// longest-match, so this only catches UI/static paths.
	if s.staticUI != nil {
		s.mux.Handle("/", s.staticUI)
	}
}

// Start starts the API server with timeouts and panic recovery.
func (s *Server) Start() error {
	log.Printf("[API] starting on %s", s.addr)

	if s.authToken == "" {
		log.Printf("[API] WARNING: no api.auth_token set — state-changing endpoints are unauthenticated (loopback bind expected)")
	}
	handler := s.corsMiddleware(s.authMiddleware(panicRecovery(s.mux)))

	srv := &http.Server{
		Addr:           s.addr,
		Handler:        handler,
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   30 * time.Second,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 1 << 20, // 1MB
	}

	return srv.ListenAndServe()
}

// panicRecovery wraps a handler with panic recovery.
func panicRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				log.Printf("[API] panic recovered: %v", err)
				http.Error(w, `{"error":"internal server error"}`, http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// corsMiddleware adds CORS headers for browser-based editors, reflecting only
// allowlisted origins. With an empty allowlist no CORS headers are emitted, so
// cross-origin browsers are blocked (same-origin requests still work).
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && s.originAllowed(origin) {
			// Echo the specific origin (never a bare "*" alongside auth) so the
			// response is valid for credentialed cross-origin requests.
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// originAllowed reports whether origin is in the configured CORS allowlist.
func (s *Server) originAllowed(origin string) bool {
	for _, o := range s.allowedOrigins {
		if o == "*" || strings.EqualFold(o, origin) {
			return true
		}
	}
	return false
}

// authMiddleware enforces the bearer token on protected requests when a token
// is configured. When no token is set it is a no-op (loopback-only mode).
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.authToken != "" && requiresAuth(r) && !s.authorized(r) {
			writeJSONError(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requiresAuth reports whether a request targets a protected operation: any
// state-changing method, or the SSE event stream (which can expose all bus
// traffic). Read-only GETs remain open for local observation.
func requiresAuth(r *http.Request) bool {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch:
		return true
	}
	return r.URL.Path == "/api/v1/events/stream"
}

// authorized validates the bearer token in constant time. Browsers using
// EventSource cannot set headers, so a ?token= query param is also accepted
// for the SSE stream.
func (s *Server) authorized(r *http.Request) bool {
	want := []byte(s.authToken)
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		got := []byte(strings.TrimPrefix(h, "Bearer "))
		return subtle.ConstantTimeCompare(got, want) == 1
	}
	if tok := r.URL.Query().Get("token"); tok != "" {
		return subtle.ConstantTimeCompare([]byte(tok), want) == 1
	}
	return false
}

// StartWithHandler returns the http.Handler without starting a server.
// Useful for embedding in another server or testing.
func (s *Server) Handler() http.Handler {
	return s.mux
}

// --- Status ---

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	status := map[string]any{
		"status":    "running",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"version":   "0.26.0",
	}

	if s.orch != nil {
		status["agents"] = len(s.orch.Agents.List())
	}

	if s.store != nil && s.tracker != nil {
		if tracker, err := s.tracker.TaskStatus(); err == nil {
			status["tasks"] = tracker.ByStatus
			status["tasks_total"] = tracker.Total
		}
	}

	writeJSON(w, status)
}

// --- Agents ---

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.orch == nil {
		writeJSON(w, []any{})
		return
	}

	writeJSON(w, s.orch.Config.Agents)
}

// handleAgentDetail dispatches everything under /api/v1/agents/{id}...:
// plain agent detail, single-shot invocation, and invocation polling.
func (s *Server) handleAgentDetail(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/agents/")
	if rest == "" {
		http.Error(w, "agent id required", http.StatusBadRequest)
		return
	}
	parts := strings.Split(rest, "/")

	switch {
	case len(parts) == 1:
		s.handleAgentGet(w, r, parts[0])
	case len(parts) == 2 && parts[1] == "invoke":
		s.handleAgentInvoke(w, r, parts[0])
	case len(parts) == 3 && parts[1] == "invocations" && parts[2] != "":
		s.handleAgentInvocationDetail(w, r, parts[0], parts[2])
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (s *Server) handleAgentGet(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.orch == nil {
		writeJSONError(w, "orchestrator not available", http.StatusServiceUnavailable)
		return
	}

	agent, err := s.orch.GetAgent(id)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusNotFound)
		return
	}

	writeJSON(w, agent)
}

// --- Agent Invocation API ---
//
// A minimal, general "ask one configured agent one question, get a
// structured result" primitive for external processes (addons) that can't
// or shouldn't import Prizm's internal Go packages. See internal/invocation
// for the rationale and the single-shot call shape (no session, no tool
// loop — just a resolved provider/model, one prompt in, one result out).

type invokeRequest struct {
	Prompt    string         `json:"prompt"`
	MaxTokens int            `json:"max_tokens,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	// ConversationID, when set, turns the call into a multi-turn conversation:
	// prior turns for this id are threaded into the prompt and the new turn is
	// persisted. Empty preserves the original single-shot, memoryless behavior.
	// If empty, metadata["conversation_id"] then metadata["channel_id"] are used
	// as fallbacks, so callers already sending channel context need no change.
	ConversationID string `json:"conversation_id,omitempty"`
	// Reset forces a fresh conversation (drops prior memory) before this turn.
	Reset bool `json:"reset,omitempty"`
}

// invokeResetAck is the reply returned for a bare "stop/reset" message, which
// clears memory without spending an LLM call.
const invokeResetAck = "Okay — starting fresh. What would you like to talk about?"

// invokeChannel is the synthetic session channel used to key stateful invoke
// conversations, keeping them separate from real chat/bot channels.
const invokeChannel = "invoke"

func (s *Server) handleAgentInvoke(w http.ResponseWriter, r *http.Request, agentID string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.orch == nil {
		writeJSONError(w, "orchestrator not available", http.StatusServiceUnavailable)
		return
	}
	if s.providers == nil {
		writeJSONError(w, "invocation is not configured on this server", http.StatusServiceUnavailable)
		return
	}

	agentCfg, err := s.orch.GetAgent(agentID)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusNotFound)
		return
	}
	if !agentCfg.InvocableViaAPI {
		writeJSONError(w, fmt.Sprintf("agent %q is not invocable via API", agentID), http.StatusForbidden)
		return
	}

	var req invokeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, s.maxRequestBytes)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := invocation.ValidateRequest(req.Prompt); err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = invocation.DefaultMaxTokens
	}

	conversationID := resolveConversationID(req)

	// Stateless path (no conversation id, or sessions unavailable): unchanged
	// single-shot behavior — one prompt in, one result out, nothing persisted.
	if conversationID == "" || s.sessions == nil {
		inv := s.invocations.Create(agentID)
		go s.runInvocation(*agentCfg, inv.ID, s.singleShotMessages(*agentCfg, req.Prompt), "", maxTokens)
		s.writeInvocationAccepted(w, inv)
		return
	}

	// Stateful path: resume (or start) a conversation keyed by conversationID.
	kind := sessionreset.Classify(req.Prompt)
	reset := req.Reset || kind != sessionreset.None
	sess, err := s.resolveInvokeSession(agentID, conversationID, reset)
	if err != nil {
		log.Printf("[API] resolve invoke session: %v", err)
		writeJSONError(w, "could not load conversation", http.StatusInternalServerError)
		return
	}

	// A bare "stop/reset" command clears memory and acknowledges without an LLM
	// call. A "switch" carries the new topic, so it falls through to a normal turn
	// on the now-fresh session.
	if kind == sessionreset.Stop {
		inv := s.invocations.Create(agentID)
		result := map[string]any{"text": invokeResetAck}
		s.invocations.Complete(inv.ID, result)
		s.publishInvocationEvent(agentID, inv.ID, invocation.StatusCompleted, result, "")
		s.writeInvocationAccepted(w, inv)
		return
	}

	if _, err := s.sessions.AddMessage(sess.ID, "user", req.Prompt, ""); err != nil {
		log.Printf("[API] add invoke user message: %v", err)
		writeJSONError(w, "could not save your message", http.StatusInternalServerError)
		return
	}

	// Build the message list synchronously (sess.Messages now includes this turn)
	// so the background call doesn't race concurrent session mutation.
	messages := s.invokeSessionMessages(*agentCfg, sess)

	inv := s.invocations.Create(agentID)
	go s.runInvocation(*agentCfg, inv.ID, messages, sess.ID, maxTokens)
	s.writeInvocationAccepted(w, inv)
}

// writeInvocationAccepted returns the 202 + pending invocation body.
func (s *Server) writeInvocationAccepted(w http.ResponseWriter, inv *invocation.Invocation) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	if err := json.NewEncoder(w).Encode(inv); err != nil {
		log.Printf("[API] json encode error: %v", err)
	}
}

// resolveConversationID reads the conversation key from the dedicated field,
// falling back to common metadata keys so callers already sending channel
// context enable memory without changing their request shape.
func resolveConversationID(req invokeRequest) string {
	if id := strings.TrimSpace(req.ConversationID); id != "" {
		return id
	}
	for _, key := range []string{"conversation_id", "channel_id"} {
		if v, ok := req.Metadata[key].(string); ok {
			if id := strings.TrimSpace(v); id != "" {
				return id
			}
		}
	}
	return ""
}

// resolveInvokeSession returns the conversation's live session, creating a fresh
// one when reset is requested, when none exists, or when the existing one has
// been idle past the configured safety-net window.
func (s *Server) resolveInvokeSession(agentID, conversationID string, reset bool) (*session.Session, error) {
	if !reset {
		sess, err := s.sessions.FindActiveWithin(invokeChannel, conversationID, conversationID, s.invokeIdleTimeout)
		if err != nil {
			return nil, err
		}
		if sess != nil {
			return sess, nil
		}
	}
	return s.sessions.Create(agentID, invokeChannel, conversationID, conversationID)
}

// resolveConversationPostfixForInvoke picks the behavior directive for invoke.
// If SOUL.md was loaded (hasSoul=true), return "" — SOUL.md is the personality authority.
// Otherwise fall back to the agent's conversation_postfix or a default.
func resolveConversationPostfixForInvoke(agentCfg orchestrator.AgentConfig, hasSoul bool) string {
	if hasSoul {
		return ""
	}
	if agentCfg.ConversationPostfix != "" {
		return agentCfg.ConversationPostfix
	}
	return "Stay present in the conversation. Be engaged and responsive."
}

// singleShotMessages builds the full system prompt (SOUL.md, context files,
// memory injection) plus the single user turn.
// Falls back to minimal prompt (ConversationPostfix only) if ctxBuilder is nil.
func (s *Server) buildInvokeSystemPrompt(agentCfg orchestrator.AgentConfig, searchQuery string) string {
	if s.ctxBuilder == nil {
		if agentCfg.ConversationPostfix != "" {
			return agentCfg.ConversationPostfix
		}
		return ""
	}

	var sb strings.Builder

	// Layer 1: Identity (SOUL.md)
	identityContent := ""
	hasSoul := false
	builder := context.NewBuilder(s.ctxBuilder.WorkspaceRoot).WithNamedContexts([]string{"soul", "identity"})
	if injected, err := builder.Build(); err == nil {
		for _, f := range injected.Files {
			if f.Name == "soul" && f.Content != "" {
				identityContent = f.Content
				hasSoul = true
			}
		}
	}
	if identityContent == "" {
		identityContent = fmt.Sprintf("You are %s, a %s assistant.", agentCfg.ID, agentCfg.Role)
	}
	sb.WriteString("## Who You Are\n")
	sb.WriteString(identityContent + "\n\n")

	// V83: Core Identity Block — permanent identity facts always in prompt.
	if s.coreIdentityForInject != nil {
		coreBlock := s.coreIdentityForInject.Build(s.memStoreForInvoke)
		if coreBlock != "" {
			sb.WriteString(coreBlock + "\n\n")
		}
	}

	// Layer 2: Context files (USER.md, HEARTBEAT.md, AGENTS.md, etc.)
	if len(agentCfg.Context) > 0 {
		budget := 4000
		if s.orch != nil && s.orch.Config.Prizm.ContextTokenBudget > 0 {
			budget = s.orch.Config.Prizm.ContextTokenBudget
		}
		otherContexts := make([]string, 0, len(agentCfg.Context))
		for _, c := range agentCfg.Context {
			if c != "soul" && c != "identity" {
				otherContexts = append(otherContexts, c)
			}
		}
		if len(otherContexts) > 0 {
			ctxBuilder := context.NewBuilder(s.ctxBuilder.WorkspaceRoot).
				WithNamedContexts(otherContexts).
			WithTokenBudget(budget)
			if injected, err := ctxBuilder.BuildCached(); err == nil && injected.FormattedString != "" {
				sb.WriteString("## Context\n")
				sb.WriteString(injected.FormattedString + "\n")
			}
		}
	}

	// V83: Memory injection — use smart injector with grounding-aware format when available.
	// Falls back to bare keyword search when injector is nil.
	if s.memInjectorForInvoke != nil {
		// Smart path: query planner + embedding + grounding-aware format
		memBlock := s.memInjectorForInvoke.InjectMemoriesInt(contextctx.Background(), 0, searchQuery, 1, 800)
		if memBlock != "" {
			sb.WriteString(memBlock + "\n")
		}
	} else if s.memStoreForInvoke != nil {
		// Legacy fallback: bare keyword search, no grounding
		ctx := contextctx.Background()
		memories, err := s.memStoreForInvoke.Search(ctx, searchQuery, 10)
		if err == nil && len(memories) > 0 {
			sb.WriteString("## Memories\n")
			for i, mem := range memories {
				if i >= 5 {
					break
				}
				sb.WriteString(fmt.Sprintf("- %s\n", mem.Content))
			}
			sb.WriteString("\n")
		}
	}

	// Layer 4: Conversation postfix (behavior)
	postfix := resolveConversationPostfixForInvoke(agentCfg, hasSoul)
	if postfix != "" {
		sb.WriteString("## How You Respond\n")
		sb.WriteString(postfix + "\n\n")
	}

	return sb.String()
}

// singleShotMessages builds the full system prompt plus the single user turn.
func (s *Server) singleShotMessages(agentCfg orchestrator.AgentConfig, prompt string) []provider.ChatMessage {
	messages := make([]provider.ChatMessage, 0, 2)
	systemPrompt := s.buildInvokeSystemPrompt(agentCfg, prompt)
	if systemPrompt != "" {
		messages = append(messages, provider.ChatMessage{Role: "system", Content: systemPrompt})
	}
	return append(messages, provider.ChatMessage{Role: "user", Content: prompt})
}

// lastUserMessage returns the content of the last user message in the session,
// or empty string if none.
func lastUserMessage(sess *session.Session) string {
	for i := len(sess.Messages) - 1; i >= 0; i-- {
		if sess.Messages[i].Role == "user" {
			return sess.Messages[i].Content
		}
	}
	return ""
}

// invokeSessionMessages builds the full system prompt plus conversation history.
func (s *Server) invokeSessionMessages(agentCfg orchestrator.AgentConfig, sess *session.Session) []provider.ChatMessage {
	messages := make([]provider.ChatMessage, 0, len(sess.Messages)+2)
	systemPrompt := s.buildInvokeSystemPrompt(agentCfg, lastUserMessage(sess))
	if systemPrompt != "" {
		messages = append(messages, provider.ChatMessage{Role: "system", Content: systemPrompt})
	}
	for _, m := range sess.Messages {
		switch m.Role {
		case "user":
			messages = append(messages, provider.ChatMessage{Role: "user", Content: m.Content})
		case "agent":
			messages = append(messages, provider.ChatMessage{Role: "assistant", Content: m.Content})
		case "system":
			messages = append(messages, provider.ChatMessage{Role: "system", Content: m.Content})
		}
	}
	return messages
}

// invokeSink is a toolloop.Sink that captures the final response for invoke.
// It silently absorbs progress and tool results, keeping only the last content.
type invokeSink struct {
	mu      sync.Mutex
	content string
}

func (s *invokeSink) OnToolCall(name string, args map[string]any) {}
func (s *invokeSink) OnProgress(content string) {
	s.mu.Lock()
	s.content = content
	s.mu.Unlock()
}
func (s *invokeSink) OnToolResult(name string, result string, summary toolloop.CallSummary) {}
func (s *invokeSink) OnError(err error)                    {}
func (s *invokeSink) OnComplete(content string, modelInfo toolloop.ModelInfo) {
	s.mu.Lock()
	s.content = content
	s.mu.Unlock()
}

// runInvocation performs the LLM call in the background and records the outcome.
// When toolRegForInvoke is set, it uses toolloop.RunChatLoop so the agent can call
// tools (memory_search, etc.) instead of a single-shot LLM call. When nil, it falls
// back to the original single-shot behavior.
func (s *Server) runInvocation(agentCfg orchestrator.AgentConfig, invocationID string, messages []provider.ChatMessage, sessionID string, maxTokens int) {
	ctx, cancel := contextctx.WithTimeout(contextctx.Background(), 5*time.Minute)
	defer cancel()

	chatProv, err := s.providers.GetChatProviderForAgent(agentCfg.ID, agentCfg.Model)
	if err != nil {
		s.invocations.Fail(invocationID, err.Error())
		s.publishInvocationEvent(agentCfg.ID, invocationID, invocation.StatusFailed, nil, err.Error())
		return
	}

	// Tool loop path: agent can call memory_search and other tools
	if s.toolRegForInvoke != nil && s.toolExecForInvoke != nil {
		s.runInvocationWithToolLoop(ctx, agentCfg, invocationID, messages, sessionID, maxTokens, chatProv)
		return
	}

	// Fallback: original single-shot path (no tool support)
	resp, err := chatProv.ChatGenerate(ctx, provider.ChatGenerateRequest{
		RunID:     "invoke-" + invocationID,
		Agent:     agentCfg.ID,
		Model:     agentCfg.Model,
		Messages:  messages,
		MaxTokens: maxTokens,
	})
	if err != nil {
		s.invocations.Fail(invocationID, err.Error())
		s.publishInvocationEvent(agentCfg.ID, invocationID, invocation.StatusFailed, nil, err.Error())
		return
	}

	if sessionID != "" && s.sessions != nil {
		if _, aerr := s.sessions.AddMessage(sessionID, "agent", resp.Content, agentCfg.ID); aerr != nil {
			log.Printf("[API] persist invoke agent reply: %v", aerr)
		}
	}

	result := invocation.ParseResult(resp.Content)
	s.invocations.Complete(invocationID, result)
	s.publishInvocationEvent(agentCfg.ID, invocationID, invocation.StatusCompleted, result, "")
}

// runInvocationWithToolLoop uses toolloop.RunChatLoop so the agent can call tools.
func (s *Server) runInvocationWithToolLoop(ctx contextctx.Context, agentCfg orchestrator.AgentConfig, invocationID string, messages []provider.ChatMessage, sessionID string, maxTokens int, chatProv provider.ChatProvider) {
	// Build tool definitions from the registry
	toolInfos := s.toolRegForInvoke.ListWithDescriptions()
	chatTools := make([]provider.ChatTool, 0, len(toolInfos))

	for _, ti := range toolInfos {
		params := map[string]any{
			"type":       "object",
			"properties": make(map[string]any),
		}
		required := make([]string, 0)

		for pname, spec := range ti.Schema.Input {
			props := map[string]any{
				"type":        spec.Type,
				"description": spec.Description,
			}
			params["properties"].(map[string]any)[pname] = props
			if spec.Required {
				required = append(required, pname)
			}
		}
		if len(required) > 0 {
			params["required"] = required
		}

		chatTools = append(chatTools, provider.ChatTool{
			Type: "function",
			Function: provider.FunctionDef{
				Name:        ti.Name,
				Description: ti.Description,
				Parameters:  params,
			},
		})
	}

	sink := &invokeSink{}
	cfg := toolloop.Config{
		MaxIterations: 3,
		Timeout:       3 * time.Minute,
		NudgeAfter:    3,
	}

	result, err := toolloop.RunChatLoop(ctx, messages, chatTools, chatProv, &agentCfg, s.toolExecForInvoke, sink, cfg, nil)
	if err != nil {
		s.invocations.Fail(invocationID, err.Error())
		s.publishInvocationEvent(agentCfg.ID, invocationID, invocation.StatusFailed, nil, err.Error())
		return
	}

	finalContent := result.Content
	if finalContent == "" {
		sink.mu.Lock()
		finalContent = sink.content
		sink.mu.Unlock()
	}

	if sessionID != "" && s.sessions != nil {
		if _, aerr := s.sessions.AddMessage(sessionID, "agent", finalContent, agentCfg.ID); aerr != nil {
			log.Printf("[API] persist invoke agent reply: %v", aerr)
		}
	}

	invResult := invocation.ParseResult(finalContent)
	s.invocations.Complete(invocationID, invResult)
	s.publishInvocationEvent(agentCfg.ID, invocationID, invocation.StatusCompleted, invResult, "")
}

// publishInvocationEvent re-broadcasts completion on <agent-id>.invocation.completed
// so external callers can watch GET /api/v1/events/stream?subject=<agent-id>.invocation.>
// instead of polling — no new streaming code needed, the SSE endpoint is
// already a generic NATS bridge.
func (s *Server) publishInvocationEvent(agentID, invocationID string, status invocation.Status, result map[string]any, errMsg string) {
	if s.nc == nil {
		return
	}
	payload := map[string]any{
		"invocation_id": invocationID,
		"status":        status,
	}
	if result != nil {
		payload["result"] = result
	}
	if errMsg != "" {
		payload["error"] = errMsg
	}
	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[API] marshal invocation event failed: %v", err)
		return
	}
	subject := agentns.New(agentID).EventType("invocation.completed")
	if err := s.nc.Publish(subject, data); err != nil {
		log.Printf("[API] publish invocation event failed: %v", err)
	}
}

func (s *Server) handleAgentInvocationDetail(w http.ResponseWriter, r *http.Request, agentID, invocationID string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	inv, ok := s.invocations.Get(invocationID)
	if !ok || inv.AgentID != agentID {
		writeJSONError(w, "invocation not found", http.StatusNotFound)
		return
	}
	writeJSON(w, inv)
}

// --- Sessions ---

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.sessions == nil {
		writeJSON(w, []any{})
		return
	}

	sessions, err := s.sessions.ListActive()
	if err != nil {
		writeJSONError(w, "failed to list sessions", http.StatusInternalServerError)
		return
	}

	writeJSON(w, sessions)
}

func (s *Server) handleSessionDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/api/v1/sessions/")
	if id == "" {
		http.Error(w, "session id required", http.StatusBadRequest)
		return
	}

	if s.sessions == nil {
		writeJSONError(w, "session manager not available", http.StatusServiceUnavailable)
		return
	}

	sess, err := s.sessions.Get(id)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusNotFound)
		return
	}

	writeJSON(w, sess)
}

// --- Tasks ---

func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.store == nil {
		writeJSON(w, []any{})
		return
	}

	// Optional status filter
	statusFilter := r.URL.Query().Get("status")

	var tasks []*task.Task
	var err error

	if statusFilter != "" {
		tasks, err = s.store.ListByStatus(task.Status(statusFilter))
	} else {
		// List all tasks by querying each status
		for _, st := range []task.Status{
			task.StatusCreated, task.StatusAssigned, task.StatusInProgress,
			task.StatusCompleted, task.StatusFailed, task.StatusCancelled,
		} {
			stTasks, serr := s.store.ListByStatus(st)
			if serr != nil {
				continue
			}
			tasks = append(tasks, stTasks...)
		}
		err = nil
	}

	if err != nil {
		writeJSONError(w, "failed to list tasks", http.StatusInternalServerError)
		return
	}

	writeJSON(w, tasks)
}

func (s *Server) handleTaskDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/api/v1/tasks/")
	if id == "" {
		http.Error(w, "task id required", http.StatusBadRequest)
		return
	}

	if s.store == nil {
		writeJSONError(w, "task store not available", http.StatusServiceUnavailable)
		return
	}

	tsk, err := s.store.Get(id)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusNotFound)
		return
	}

	writeJSON(w, tsk)
}

// --- Autopatch ---

// handleWorkflowStart triggers the gated loop for {project, prompt} by
// publishing to prizm.workflow.start, which the serve-mode WakeHandler consumes.
func (s *Server) handleWorkflowStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.nc == nil {
		writeJSONError(w, "NATS not connected — cannot start workflow", http.StatusServiceUnavailable)
		return
	}
	var req workstart.Request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, s.maxRequestBytes)).Decode(&req); err != nil {
		writeJSONError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeJSONError(w, "prompt is required", http.StatusBadRequest)
		return
	}
	cfg := (*orchestrator.Config)(nil)
	if s.orch != nil {
		cfg = s.orch.Config
	}
	resolved, err := workstart.Resolve(cfg, req)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if resolved.NeedsLocation {
		w.WriteHeader(http.StatusConflict)
		writeJSON(w, map[string]any{
			"error":          "location_required",
			"reason":         resolved.Reason,
			"recommendation": resolved.Recommendation,
			"question":       resolved.Question,
		})
		return
	}
	req.Project = resolved.ProjectID
	req.RepoPath = resolved.RepoPath
	req.Channel = resolved.Channel
	req.Bootstrap = req.Bootstrap || resolved.Project == nil
	if req.Source == "" {
		req.Source = "api"
	}
	payload, _ := json.Marshal(req)
	if err := s.nc.Publish("prizm.workflow.start", payload); err != nil {
		writeJSONError(w, "failed to publish start request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	writeJSON(w, map[string]any{"status": "started", "project": req.Project, "repo_path": req.RepoPath})
}

type workflowRunSummary struct {
	RunID        string            `json:"run_id"`
	WorkflowName string            `json:"workflow_name"`
	Status       v2.WorkflowStatus `json:"status"`
	ProjectID    string            `json:"project_id,omitempty"`
	RepoPath     string            `json:"repo_path,omitempty"`
	Channel      string            `json:"channel,omitempty"`
	CurrentPhase string            `json:"current_phase,omitempty"`
	StartedAt    string            `json:"started_at,omitempty"`
	UpdatedAt    string            `json:"updated_at,omitempty"`
	CompletedAt  string            `json:"completed_at,omitempty"`
}

func (s *Server) handleWorkflowRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	dir := s.workflowStateDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, map[string]any{"runs": []workflowRunSummary{}})
			return
		}
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	seen := map[string]bool{}
	runs := make([]workflowRunSummary, 0)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "workflow-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		state, err := v2.LoadWorkflowState(filepath.Join(dir, name))
		if err != nil || state.RunID == "" || seen[state.RunID] {
			continue
		}
		seen[state.RunID] = true
		runs = append(runs, summarizeWorkflowRun(state))
	}
	if current, err := v2.LoadCurrentWorkflowState(dir); err == nil && current.RunID != "" && !seen[current.RunID] {
		runs = append(runs, summarizeWorkflowRun(current))
	}
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].UpdatedAt > runs[j].UpdatedAt
	})
	writeJSON(w, map[string]any{"runs": runs})
}

func (s *Server) handleWorkflowRunDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	runID := strings.TrimPrefix(r.URL.Path, "/api/v1/workflows/runs/")
	runID = strings.TrimSpace(runID)
	if runID == "" {
		writeJSONError(w, "run id required", http.StatusBadRequest)
		return
	}
	dir := s.workflowStateDir()
	if runID == "current" {
		state, err := v2.LoadCurrentWorkflowState(dir)
		if err != nil {
			writeJSONError(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, state)
		return
	}
	state, err := v2.LoadWorkflowState(filepath.Join(dir, "workflow-"+runID+".json"))
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, state)
}

func (s *Server) workflowStateDir() string {
	cfg := s.loadWorkflowConfig()
	if cfg != nil && cfg.Global.StatePersistenceDir != "" {
		return cfg.Global.StatePersistenceDir
	}
	return "runs/gated-loop"
}

func summarizeWorkflowRun(state *v2.WorkflowState) workflowRunSummary {
	return workflowRunSummary{
		RunID:        state.RunID,
		WorkflowName: state.WorkflowName,
		Status:       state.Status,
		ProjectID:    state.ProjectID,
		RepoPath:     state.RepoPath,
		Channel:      state.Channel,
		CurrentPhase: state.CurrentPhase(),
		StartedAt:    state.StartedAt,
		UpdatedAt:    state.UpdatedAt,
		CompletedAt:  state.CompletedAt,
	}
}

// loadWorkflowConfig returns the configured gated-loop workflow, or the built-in default.
func (s *Server) loadWorkflowConfig() *v2.WorkflowConfig {
	if s.workflowConfigPath != "" {
		if c, err := v2.LoadConfig(s.workflowConfigPath); err == nil {
			return c
		}
	}
	return v2.DefaultConfig()
}

// handleWorkflowConfig serves and persists the gated-loop workflow definition
// (phases + gates) for the dashboard workflow editor.
func (s *Server) handleWorkflowConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, s.loadWorkflowConfig())
	case http.MethodPut:
		if s.workflowConfigPath == "" {
			writeJSONError(w, "no workflow_config path configured — set prizm.workflow_config to enable editing", http.StatusBadRequest)
			return
		}
		var cfg v2.WorkflowConfig
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, s.maxRequestBytes)).Decode(&cfg); err != nil {
			writeJSONError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if errs := v2.ValidateConfig(&cfg); len(errs) > 0 {
			writeJSONError(w, "validation failed: "+strings.Join(errs, "; "), http.StatusBadRequest)
			return
		}
		yamlBytes, err := v2.MarshalConfigYAML(&cfg)
		if err != nil {
			writeJSONError(w, "could not serialize config: "+err.Error(), http.StatusInternalServerError)
			return
		}
		// Atomic write to the server-configured path (not user-supplied).
		tmp := s.workflowConfigPath + ".tmp"
		if err := os.WriteFile(tmp, yamlBytes, 0644); err != nil {
			writeJSONError(w, "write failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := os.Rename(tmp, s.workflowConfigPath); err != nil {
			os.Remove(tmp)
			writeJSONError(w, "rename failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"status": "saved", "path": s.workflowConfigPath, "phases": len(cfg.Phases)})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleWorkflowFeedback releases a paused feedback gate (approve / request
// changes / reject) by publishing the decision the engine waits on.
func (s *Server) handleWorkflowFeedback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.nc == nil {
		writeJSONError(w, "NATS not connected", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Decision   string         `json:"decision"`
		Reviewer   string         `json:"reviewer"`
		Notes      string         `json:"notes"`
		WorkflowID string         `json:"workflow_id"`
		Dimensions map[string]any `json:"dimensions"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, s.maxRequestBytes)).Decode(&req); err != nil {
		writeJSONError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Decision == "" {
		writeJSONError(w, "decision is required (approved | changes_requested | rejected)", http.StatusBadRequest)
		return
	}
	msgType := "feedback_response"
	if req.Reviewer != "" {
		msgType = "review_response"
	}
	payload, _ := json.Marshal(map[string]any{
		"type":        msgType,
		"decision":    req.Decision,
		"reviewer":    req.Reviewer,
		"notes":       req.Notes,
		"workflow_id": req.WorkflowID,
		"dimensions":  req.Dimensions,
	})
	if err := s.nc.Publish("prizm.workflow.feedback.response", payload); err != nil {
		writeJSONError(w, "publish failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"status": "sent", "decision": req.Decision})
}

func (s *Server) handleAutoPatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.autopatch == nil || !s.autopatch.Enabled() {
		writeJSONError(w, "autopatch is disabled", http.StatusServiceUnavailable)
		return
	}
	var req autopatch.Request
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, s.maxRequestBytes)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Source == "" {
		req.Source = "manual"
	}
	if req.SubmittedBy == "" {
		req.SubmittedBy = "api:user"
	}
	tsk, err := s.autopatch.Start(r.Context(), req)
	if err != nil {
		switch {
		case errors.Is(err, autopatch.ErrDirtyWorktree):
			writeJSONError(w, err.Error(), http.StatusConflict)
		case errors.Is(err, autopatch.ErrDisabled):
			writeJSONError(w, err.Error(), http.StatusServiceUnavailable)
		default:
			writeJSONError(w, err.Error(), http.StatusBadRequest)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusAccepted)
	if err := json.NewEncoder(w).Encode(tsk); err != nil {
		log.Printf("[API] json encode error: %v", err)
	}
}

// --- Approvals ---

func (s *Server) handleApprovals(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.approval == nil {
		writeJSON(w, []any{})
		return
	}

	approvals, err := s.approval.ListPendingApprovals()
	if err != nil {
		writeJSONError(w, "failed to list approvals", http.StatusInternalServerError)
		return
	}

	writeJSON(w, approvals)
}

func (s *Server) handleApprovalAction(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/approvals/")

	// Expected: {id}/grant or {id}/deny
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		http.Error(w, "expected /api/v1/approvals/{id}/grant or /api/v1/approvals/{id}/deny", http.StatusBadRequest)
		return
	}

	taskID := parts[0]
	action := parts[1]

	if s.approval == nil {
		writeJSONError(w, "approval manager not available", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()

	switch action {
	case "grant":
		// Read resolved_by from query param or JSON body
		resolvedBy := r.URL.Query().Get("by")
		if resolvedBy == "" {
			resolvedBy = "api:user"
		}

		if err := s.approval.GrantApproval(ctx, taskID, resolvedBy); err != nil {
			writeJSONError(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]string{"status": "granted", "task_id": taskID})

	case "deny":
		resolvedBy := r.URL.Query().Get("by")
		if resolvedBy == "" {
			resolvedBy = "api:user"
		}
		reason := r.URL.Query().Get("reason")
		if reason == "" {
			reason = "denied via API"
		}

		if err := s.approval.DenyApproval(ctx, taskID, resolvedBy, reason); err != nil {
			writeJSONError(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]string{"status": "denied", "task_id": taskID})

	default:
		http.Error(w, "unknown action: use 'grant' or 'deny'", http.StatusBadRequest)
	}
}

// --- Event Stream (SSE) ---

func (s *Server) handleEventStream(w http.ResponseWriter, r *http.Request) {
	if s.nc == nil {
		writeJSONError(w, "NATS not connected", http.StatusServiceUnavailable)
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Optional subject filter — restrict to safe prefixes for security
	subject := r.URL.Query().Get("subject")
	if subject == "" {
		subject = ">"
	}

	// The SSE stream is auth-gated by authMiddleware (requiresAuth), so an
	// authenticated caller may subscribe to any subject. A wildcard ">"
	// subscription streams all bus traffic; log it for audit.
	if subject == ">" {
		log.Printf("[API] SSE: wildcard subject subscription from %s", r.RemoteAddr)
	}

	// Subscribe to NATS
	sub, err := s.nc.SubscribeSync(subject)
	if err != nil {
		writeJSONError(w, fmt.Sprintf("subscribe failed: %v", err), http.StatusInternalServerError)
		return
	}
	defer sub.Unsubscribe()

	// Send connected event
	fmt.Fprintf(w, "event: connected\ndata: {\"subject\":\"%s\"}\n\n", subject)
	flusher.Flush()

	// Heartbeat ticker
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	// Event loop
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			fmt.Fprintf(w, "event: heartbeat\ndata: {\"ts\":\"%s\"}\n\n", time.Now().UTC().Format(time.RFC3339))
			flusher.Flush()
		default:
			// Non-blocking check for NATS messages
			msg, err := sub.NextMsg(100 * time.Millisecond)
			if err != nil {
				if err == nats.ErrTimeout {
					continue
				}
				return
			}

			// Parse event data
			var data map[string]any
			if err := json.Unmarshal(msg.Data, &data); err != nil {
				data = map[string]any{"raw": string(msg.Data)}
			}

			eventJSON, _ := json.Marshal(data)
			fmt.Fprintf(w, "event: %s\ndata: %s\nid: %s\n\n",
				msg.Subject,
				string(eventJSON),
				strconv.FormatInt(time.Now().UnixNano(), 10),
			)
			flusher.Flush()
		}
	}
}

// --- Workflows ---

func (s *Server) handleWorkflows(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Workflows are loaded from the orchestrator config
	if s.orch == nil {
		writeJSON(w, []any{})
		return
	}

	writeJSON(w, s.orch.Workflows())
}

// --- Costs ---

func (s *Server) handleCosts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	reports, err := s.workflowCostReports()
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	total := costpkg.CostReport{RunID: "all", ByProvider: map[string]float64{}, ByModel: map[string]float64{}, ByAgent: map[string]int{}}
	for _, report := range reports {
		total.TotalTokens += report.TotalTokens
		total.PromptTokens += report.PromptTokens
		total.CompletionTokens += report.CompletionTokens
		total.EstimatedCostUsd += report.EstimatedCostUsd
		total.EventCount += report.EventCount
		for k, v := range report.ByProvider {
			total.ByProvider[k] += v
		}
		for k, v := range report.ByModel {
			total.ByModel[k] += v
		}
		for k, v := range report.ByAgent {
			total.ByAgent[k] += v
		}
	}
	writeJSON(w, map[string]any{
		"run_id":             total.RunID,
		"total_tokens":       total.TotalTokens,
		"prompt_tokens":      total.PromptTokens,
		"completion_tokens":  total.CompletionTokens,
		"estimated_cost_usd": total.EstimatedCostUsd,
		"event_count":        total.EventCount,
		"by_provider":        total.ByProvider,
		"by_model":           total.ByModel,
		"by_agent":           total.ByAgent,
		"runs":               reports,
		"workflow_state_dir": s.workflowStateDir(),
		"timestamp":          time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Server) workflowCostReports() ([]costpkg.CostReport, error) {
	dir := s.workflowStateDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []costpkg.CostReport{}, nil
		}
		return nil, err
	}
	seen := map[string]bool{}
	reports := make([]costpkg.CostReport, 0)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "workflow-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		state, err := v2.LoadWorkflowState(filepath.Join(dir, name))
		if err != nil || state.RunID == "" || seen[state.RunID] {
			continue
		}
		seen[state.RunID] = true
		reports = append(reports, workflowCostReport(dir, state))
	}
	if current, err := v2.LoadCurrentWorkflowState(dir); err == nil && current.RunID != "" && !seen[current.RunID] {
		reports = append(reports, workflowCostReport(dir, current))
	}
	sort.Slice(reports, func(i, j int) bool { return reports[i].RunID > reports[j].RunID })
	return reports, nil
}

func workflowCostReport(stateDir string, state *v2.WorkflowState) costpkg.CostReport {
	report, err := costpkg.ReportFromRunDir(state.RunID, filepath.Join(stateDir, state.RunID))
	if err != nil {
		report = &costpkg.CostReport{RunID: state.RunID, ByProvider: map[string]float64{}, ByModel: map[string]float64{}, ByAgent: map[string]int{}}
	}
	if report.TotalTokens == 0 {
		report.PromptTokens = state.TotalPromptTokens
		report.CompletionTokens = state.TotalCompletionTokens
		report.TotalTokens = state.TotalPromptTokens + state.TotalCompletionTokens
	}
	report.Status = string(state.Status)
	report.MaxTokens = state.MaxTotalTokens
	if report.MaxTokens > 0 {
		report.RemainingTokens = report.MaxTokens - report.TotalTokens
		if report.RemainingTokens < 0 {
			report.RemainingTokens = 0
		}
		report.PercentUsed = float64(report.TotalTokens) / float64(report.MaxTokens) * 100
	}
	return *report
}

// --- Workflow SVG ---

func (s *Server) handleWorkflowSVG(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Path: /api/v1/workflows/{type}
	diagramType := strings.TrimPrefix(r.URL.Path, "/api/v1/workflows/")
	if diagramType == "" || diagramType == "list" {
		// List available diagram types
		writeJSON(w, map[string]any{
			"types": []string{"topology", "agents", "feedback", "delegation", "approval", "events"},
		})
		return
	}

	// Get agents from orchestrator
	var agents []orchestrator.AgentConfig
	if s.orch != nil {
		agents = s.orch.Config.Agents
	}

	cfg := workflow.DefaultConfig()

	// Query params for customization
	if r.URL.Query().Get("theme") == "light" {
		cfg.DarkTheme = false
	}
	if r.URL.Query().Get("capabilities") == "true" {
		cfg.ShowCapabilities = true
	}

	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "no-cache")

	workflow.GenerateWorkflow(w, diagramType, agents, cfg)
}

// --- Editor API ---

// handleEditorState returns the current config as an EditorState.
func (s *Server) handleEditorState(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.getEditorState(w, r)
		return
	}
	if r.Method == http.MethodPut {
		s.putEditorState(w, r)
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func (s *Server) getEditorState(w http.ResponseWriter, r *http.Request) {
	var agents []orchestrator.AgentConfig
	if s.orch != nil {
		agents = s.orch.Config.Agents
	}

	config := &orchestrator.Config{Agents: agents}
	state := editor.ConfigToEditorState(config)

	writeJSON(w, state)
}

func (s *Server) putEditorState(w http.ResponseWriter, r *http.Request) {
	var state editor.EditorState
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, s.maxRequestBytes)).Decode(&state); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	errors := editor.ValidateEditorState(&state)
	if len(errors) > 0 {
		writeJSON(w, map[string]any{"valid": false, "errors": errors})
		return
	}

	// Return the YAML preview
	yaml, err := editor.WriteConfigYAML(&state)
	if err != nil {
		http.Error(w, "yaml generation error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{
		"valid":  true,
		"yaml":   yaml,
		"agents": len(state.Nodes),
		"edges":  len(state.Edges),
	})
}

// handleEditorNodes handles GET (list) and POST (add) for nodes.
func (s *Server) handleEditorNodes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.getEditorState(w, r) // nodes are part of state
	case http.MethodPost:
		s.addNode(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) addNode(w http.ResponseWriter, r *http.Request) {
	var node editor.EditorNode
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, s.maxRequestBytes)).Decode(&node); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	state := s.getCurrentState()
	if err := state.AddNode(node); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.setCurrentState(state)
	writeJSON(w, state)
}

// handleEditorNodeCRUD handles PUT (update) and DELETE for individual nodes.
func (s *Server) handleEditorNodeCRUD(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/editor/nodes/")
	if id == "" {
		http.Error(w, "node ID required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodPut:
		s.updateNode(w, r, id)
	case http.MethodDelete:
		s.deleteNode(w, r, id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) updateNode(w http.ResponseWriter, r *http.Request, id string) {
	var updates editor.NodeUpdate
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, s.maxRequestBytes)).Decode(&updates); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	state := s.getCurrentState()
	if err := state.UpdateNode(id, updates); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	s.setCurrentState(state)
	writeJSON(w, state)
}

func (s *Server) deleteNode(w http.ResponseWriter, r *http.Request, id string) {
	state := s.getCurrentState()
	if err := state.RemoveNode(id); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	s.setCurrentState(state)
	writeJSON(w, state)
}

// handleEditorEdges handles GET (list) and POST (add) for edges.
func (s *Server) handleEditorEdges(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.getEditorState(w, r) // edges are part of state
	case http.MethodPost:
		s.addEdge(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) addEdge(w http.ResponseWriter, r *http.Request) {
	var edge editor.EditorEdge
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, s.maxRequestBytes)).Decode(&edge); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	state := s.getCurrentState()
	if err := state.AddEdge(edge); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.setCurrentState(state)
	writeJSON(w, state)
}

// handleEditorEdgeCRUD handles DELETE for individual edges.
func (s *Server) handleEditorEdgeCRUD(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/editor/edges/")
	if id == "" {
		http.Error(w, "edge ID required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodDelete:
		s.deleteEdge(w, r, id)
	case http.MethodPut:
		s.updateEdge(w, r, id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// updateEdge applies a partial update (label/type/style/action) to an edge,
// letting the visual editor assign an action to a flow line.
func (s *Server) updateEdge(w http.ResponseWriter, r *http.Request, id string) {
	var upd editor.EdgeUpdate
	if err := json.NewDecoder(r.Body).Decode(&upd); err != nil {
		writeJSONError(w, "invalid edge update body", http.StatusBadRequest)
		return
	}
	s.editorMu.Lock()
	defer s.editorMu.Unlock()
	if s.editorWS == nil {
		writeJSONError(w, "editor state not initialized", http.StatusServiceUnavailable)
		return
	}
	if err := s.editorWS.UpdateEdge(id, upd); err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"status": "updated", "id": id})
}

func (s *Server) deleteEdge(w http.ResponseWriter, r *http.Request, id string) {
	state := s.getCurrentState()
	if err := state.RemoveEdge(id); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	s.setCurrentState(state)
	writeJSON(w, state)
}

// handleEditorSave validates and optionally writes config to disk.
// POST with {"confirm": true, "path": "/path/to/prizm.yaml"} to write.
// POST with just the state to validate and preview YAML.
func (s *Server) handleEditorSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		editor.EditorState
		Confirm bool   `json:"confirm"`
		Path    string `json:"path"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, s.maxRequestBytes)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	errors := editor.ValidateEditorState(&req.EditorState)
	if len(errors) > 0 {
		writeJSON(w, map[string]any{"valid": false, "errors": errors})
		return
	}

	yaml, err := editor.WriteConfigYAML(&req.EditorState)
	if err != nil {
		http.Error(w, "yaml generation error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if req.Confirm && req.Path != "" {
		// Write to disk — requires explicit confirmation and path. The write is
		// jailed to s.configDir to prevent arbitrary-path config overwrites.
		if err := editor.WriteConfigToFile(&req.EditorState, req.Path, s.configDir); err != nil {
			http.Error(w, "write error: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{
			"valid":   true,
			"yaml":    yaml,
			"agents":  len(req.Nodes),
			"edges":   len(req.Edges),
			"written": true,
			"path":    req.Path,
			"message": "Config written to " + req.Path,
		})
		return
	}

	// Preview only
	writeJSON(w, map[string]any{
		"valid":   true,
		"yaml":    yaml,
		"agents":  len(req.Nodes),
		"edges":   len(req.Edges),
		"message": "Preview only — set confirm=true and path to write to disk",
	})
}

// getCurrentState returns the editor working state.
// If no working state exists, it initializes one from the orchestrator config.
func (s *Server) getCurrentState() *editor.EditorState {
	s.editorMu.RLock()
	ws := s.editorWS
	s.editorMu.RUnlock()

	if ws != nil {
		// Return a deep copy so mutations don't affect the stored state
		return deepCopyState(ws)
	}

	// Initialize from config
	var agents []orchestrator.AgentConfig
	if s.orch != nil {
		agents = s.orch.Config.Agents
	}
	config := &orchestrator.Config{Agents: agents}
	state := editor.ConfigToEditorState(config)

	s.editorMu.Lock()
	s.editorWS = state
	s.editorMu.Unlock()

	return deepCopyState(state)
}

// setCurrentState updates the working state and returns it.
func (s *Server) setCurrentState(state *editor.EditorState) {
	s.editorMu.Lock()
	s.editorWS = state
	s.editorMu.Unlock()
}

// deepCopyState creates a deep copy of an EditorState.
func deepCopyState(src *editor.EditorState) *editor.EditorState {
	dst := &editor.EditorState{
		Nodes: make([]editor.EditorNode, len(src.Nodes)),
		Edges: make([]editor.EditorEdge, len(src.Edges)),
	}
	copy(dst.Nodes, src.Nodes)
	copy(dst.Edges, src.Edges)
	for i := range src.Nodes {
		if src.Nodes[i].Capabilities != nil {
			dst.Nodes[i].Capabilities = make([]string, len(src.Nodes[i].Capabilities))
			copy(dst.Nodes[i].Capabilities, src.Nodes[i].Capabilities)
		}
		if src.Nodes[i].Subscriptions != nil {
			dst.Nodes[i].Subscriptions = make([]string, len(src.Nodes[i].Subscriptions))
			copy(dst.Nodes[i].Subscriptions, src.Nodes[i].Subscriptions)
		}
		if src.Nodes[i].Context != nil {
			dst.Nodes[i].Context = make([]string, len(src.Nodes[i].Context))
			copy(dst.Nodes[i].Context, src.Nodes[i].Context)
		}
	}
	return dst
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("[API] json encode error: %v", err)
	}
}

func writeJSONError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

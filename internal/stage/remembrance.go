// Package stage provides Prizm's pipeline execution engine (V14a).
//
// RemembranceStage integrates Prizm with the Remembrance memory layer.
// It performs TWO operations:
//  1. Capture: After agent output, sends the content to Remembrance for
//     gate → extract → graph → store processing.
//  2. Context: Before LLM calls, fetches relevant memories to inject
//     into the prompt.
//
// Architecture (V26):
//   - Capture is done SYNCHRONOUSLY in this stage as a belt-and-suspenders
//     alongside the NATS subscriber. The NATS subscriber in the Python
//     Remembrance service handles async capture from agent output events.
//     Both use the same (agent + session + turn) idempotency key.
//   - Context building is done before every LLM call to inject relevant
//     memories into the prompt.
//   - If Remembrance is unavailable, the stage succeeds gracefully with
//     empty context (RequireMemory=false) or fails (RequireMemory=true).
package stage

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	remcli "github.com/emaharmony/prizm/internal/remembrance"
)

// healthCheckTTL is how long a cached availability check is considered valid.
const healthCheckTTL = 30 * time.Second

// RemembranceStage integrates with the Remembrance memory layer.
//
// It handles two operations:
//   - Capture: sends agent output to Remembrance for processing
//   - Context: fetches relevant memories before LLM calls
//
// This stage is the reusable pipeline component. The `prizm serve` runtime
// uses the same client but adds session-aware caching and prompt injection
// that this generic stage can't provide. Both codepaths use the same source
// format: "prizm:<agent_id>".
//
// If memory is disabled or unavailable, the pipeline continues without
// memory context (graceful degradation).
type RemembranceStage struct {
	// MemoryEnabled controls whether memory operations are attempted.
	MemoryEnabled bool

	// RequireMemory controls whether missing memory is a failure.
	// If true, the stage fails when memory is unavailable or context
	// building fails. If false (default), missing memory is a warning.
	RequireMemory bool

	// MemoryURL is the Remembrance service URL.
	MemoryURL string

	// Capture controls whether to capture agent output to Remembrance.
	// Default: true. Set false to skip capture (context-only mode).
	Capture bool

	// Context controls whether to fetch context from Remembrance.
	// Default: true. Set false to skip context (capture-only mode).
	Context bool

	// client is initialized once in NewRemembranceStage (no lazy init,
	// avoiding data races from concurrent Execute calls).
	client *remcli.Client

	// Cached availability state (avoids health check on every Execute).
	available   bool
	lastChecked time.Time
	availMu     sync.RWMutex
}

// RemembranceStageOption configures a RemembranceStage.
type RemembranceStageOption func(*RemembranceStage)

// WithCapture enables or disables capture.
func WithCapture(capture bool) RemembranceStageOption {
	return func(s *RemembranceStage) { s.Capture = capture }
}

// WithContext enables or disables context building.
func WithContext(context bool) RemembranceStageOption {
	return func(s *RemembranceStage) { s.Context = context }
}

// NewRemembranceStage creates a RemembranceStage with the given configuration.
// The Remembrance client is initialized here (not lazily) to avoid data races
// from concurrent Execute calls.
func NewRemembranceStage(memoryEnabled bool, requireMemory bool, memoryURL string, opts ...RemembranceStageOption) *RemembranceStage {
	s := &RemembranceStage{
		MemoryEnabled: memoryEnabled,
		RequireMemory: requireMemory,
		MemoryURL:     memoryURL,
		Capture:       true,
		Context:       true,
	}
	// Initialize client eagerly to avoid data races in getClient()
	if memoryEnabled && memoryURL != "" {
		s.client = remcli.NewClient(memoryURL)
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Name returns the stage identifier.
func (s *RemembranceStage) Name() string { return "remembrance" }

// Validate checks that memory configuration is valid if enabled.
func (s *RemembranceStage) Validate(rc *RunContext) error {
	if s.MemoryEnabled && s.MemoryURL == "" {
		return fmt.Errorf("stage %s: memory_url is required when memory is enabled", s.Name())
	}
	return nil
}

// isAvailable checks if Remembrance is reachable, with a 30-second cache
// to avoid health-checking on every pipeline execution.
func (s *RemembranceStage) isAvailable() bool {
	s.availMu.RLock()
	if time.Since(s.lastChecked) < healthCheckTTL {
		available := s.available
		s.availMu.RUnlock()
		return available
	}
	s.availMu.RUnlock()

	// Cache expired — check availability
	avail := s.client.IsAvailable()

	s.availMu.Lock()
	s.available = avail
	s.lastChecked = time.Now()
	s.availMu.Unlock()

	return avail
}

// Execute runs the Remembrance stage.
//
// It performs up to two operations:
//  1. Capture: sends LLM output to Remembrance for gate → extract → store
//  2. Context: fetches relevant memories to inject into the prompt
//
// On failure, the stage either succeeds with empty context (RequireMemory=false)
// or fails (RequireMemory=true).
func (s *RemembranceStage) Execute(ctx context.Context, rc *RunContext) (*RunContext, *StageResult, error) {
	if !s.MemoryEnabled {
		return rc, &StageResult{
			StageName: s.Name(),
			Success:   true,
			Data:      map[string]any{"context": "", "source": "disabled"},
		}, nil
	}

	// Check availability (cached for 30s)
	if !s.isAvailable() {
		log.Printf("prizm: remembrance service unavailable at %s", s.MemoryURL)
		if s.RequireMemory {
			return rc, &StageResult{
				StageName: s.Name(),
				Success:   false,
				Error:     "remembrance service unavailable and memory is required",
			}, nil
		}
		// Graceful degradation
		return rc, &StageResult{
			StageName: s.Name(),
			Success:   true,
			Data:      map[string]any{"context": "", "source": "unavailable"},
		}, nil
	}

	result := map[string]any{}

	// ── 1. Capture: send agent output to Remembrance ──────────────────
	if s.Capture && rc.LLMResponse != "" {
		captureResult := s.captureOutput(rc)
		result["capture"] = captureResult
	}

	// ── 2. Context: fetch relevant memories ────────────────────────────
	if s.Context {
		contextStr, contextSource, memories, entities, ctxErr := s.buildContext(rc)
		result["context"] = contextStr
		result["source"] = contextSource
		result["memories_count"] = len(memories)
		result["entities_count"] = len(entities)

		// If context building failed and memory is required, fail the stage
		if ctxErr != nil && s.RequireMemory {
			return rc, &StageResult{
				StageName: s.Name(),
				Success:   false,
				Error:     fmt.Sprintf("remembrance context required but failed: %v", ctxErr),
			}, nil
		}
	}

	return rc, &StageResult{
		StageName: s.Name(),
		Success:   true,
		Data:      result,
	}, nil
}

// captureOutput sends the LLM response to Remembrance for processing.
// Uses (agent + session + turn) as an idempotency key to avoid duplicates
// when both the NATS subscriber and this stage process the same output.
func (s *RemembranceStage) captureOutput(rc *RunContext) map[string]any {
	content := rc.LLMResponse
	if content == "" {
		return map[string]any{"decision": "skip", "reason": "empty output"}
	}

	// Source tag identifies where the capture came from
	source := "prizm:pipeline"
	if rc.Agent != "" {
		source = fmt.Sprintf("prizm:%s", rc.Agent)
	}

	// Category from project name
	category := rc.Project

	resp, err := s.client.Capture(content, source, category, "")
	if err != nil {
		log.Printf("prizm: remembrance capture failed: %v", err)
		return map[string]any{"decision": "error", "error": err.Error()}
	}

	decision, _ := resp["decision"].(string)
	id, _ := resp["id"].(string)
	log.Printf("prizm: remembrance capture: decision=%s, id=%s", decision, id)

	return resp
}

// buildContext fetches relevant memories from Remembrance and formats
// them for injection into the LLM prompt. Returns an error if context
// building failed (used by RequireMemory to decide stage outcome).
func (s *RemembranceStage) buildContext(rc *RunContext) (contextStr string, source string, memories []map[string]any, entities []map[string]any, err error) {
	// Use the task as the search query
	query := rc.Task
	if query == "" {
		return "", "no_task", nil, nil, nil
	}

	ctxResp, ctxErr := s.client.BuildContext(query, rc.Project, rc.Agent, remcli.DefaultContextMaxTokens)
	if ctxErr != nil {
		ctxErr = fmt.Errorf("remembrance context build failed: %w", ctxErr)
		log.Printf("prizm: %v", ctxErr)
		return "", "failed", nil, nil, ctxErr
	}

	if ctxResp == nil || (ctxResp.ContextMarkdown == "" && (ctxResp.ContextJSON == nil || len(ctxResp.ContextJSON.Memories) == 0)) {
		return "", "empty", nil, nil, nil
	}

	// Use context_markdown directly if available (Python ContextPack format)
	if ctxResp.ContextMarkdown != "" {
		log.Printf("prizm: remembrance context built (%d memories, markdown)", len(ctxResp.SelectedMemories))
		// Convert structured memories for return value
		var memories []map[string]any
		if ctxResp.ContextJSON != nil {
			for _, m := range ctxResp.ContextJSON.Memories {
				memories = append(memories, map[string]any{
					"memory_id": m.MemoryID,
					"title":     m.Title,
					"summary":   m.Summary,
					"score":     m.Score,
					"reason":    m.Reason,
				})
			}
		}
		return ctxResp.ContextMarkdown, "injected", memories, nil, nil
	}

	// Fallback: build from structured memories
	var contextParts []string
	for _, m := range ctxResp.ContextJSON.Memories {
		if m.Summary != "" {
			contextParts = append(contextParts, m.Summary)
		}
		memories = append(memories, map[string]any{
			"memory_id": m.MemoryID,
			"title":     m.Title,
			"summary":   m.Summary,
			"score":     m.Score,
			"reason":    m.Reason,
		})
	}

	contextStr = strings.Join(contextParts, "\n\n")
	log.Printf("prizm: remembrance context built (%d memories, structured)", len(memories))

	return contextStr, "injected", memories, nil, nil
}

// Rollback is a no-op — memory operations have no side effects to undo.
// Capture is idempotent (same content is deduplicated), and context
// building is read-only.
func (s *RemembranceStage) Rollback(ctx context.Context, rc *RunContext) error {
	return nil
}

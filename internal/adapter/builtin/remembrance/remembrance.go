// Package remembrance provides a Prizm adapter for the Remembrance memory layer.
//
// Remembrance is a universal memory system for AI agents. It provides:
//   - Knowledge graph (entities, edges, facts)
//   - Hybrid search (FTS5 + vector + graph + RRF)
//   - Compiled truth + timeline (always-current synthesis)
//   - Dream cycle (automated maintenance)
//   - MCP + REST + CLI interfaces
//
// This adapter wraps Remembrance's REST API, allowing Prizm workflows
// to capture memories, search context, query the knowledge graph,
// and trigger the dream cycle natively.
//
// Architecture Decision: The adapter communicates via HTTP to the
// Remembrance service (default remcli.DefaultBaseURL). This keeps
// Prizm and Remembrance as separate processes — Remembrance is
// Python, Prizm is Go. The adapter is thin: it translates Prizm
// adapter calls to HTTP requests.
package remembrance

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/emaharmony/prizm/internal/adapter"
	remcli "github.com/emaharmony/prizm/internal/remembrance"
)

// Adapter implements adapter.Adapter for Remembrance memory layer.
type Adapter struct {
	client *remcli.Client
}

// Config holds configuration for the Remembrance adapter.
type Config struct {
	// BaseURL is the Remembrance service URL (default: remcli.DefaultBaseURL)
	BaseURL string `json:"base_url" yaml:"base_url"`

	// Timeout is the HTTP request timeout (default: 60s)
	Timeout time.Duration `json:"timeout" yaml:"timeout"`
}

// DefaultConfig returns the default configuration.
func DefaultConfig() Config {
	return Config{
		BaseURL: remcli.DefaultBaseURL,
		Timeout: remcli.DefaultTimeout,
	}
}

// NewAdapter creates a new Remembrance adapter with the given config.
func NewAdapter(cfg Config) *Adapter {
	if cfg.BaseURL == "" {
		cfg.BaseURL = remcli.DefaultBaseURL
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = remcli.DefaultTimeout
	}
	return &Adapter{
		client: remcli.NewClientWithTimeout(cfg.BaseURL, cfg.Timeout),
	}
}

// Name returns the adapter name.
func (a *Adapter) Name() string { return "remembrance" }

// Version returns the adapter version.
func (a *Adapter) Version() string { return "2.0.0" }

// Capabilities returns what this adapter can do.
func (a *Adapter) Capabilities() []adapter.Capability {
	return []adapter.Capability{
		{
			Action:      "capture",
			Description: "Capture a memory through the full pipeline (gate → extract → graph → store)",
			InputSchema: map[string]any{
				"text":     map[string]string{"type": "string", "description": "Text to capture"},
				"source":   map[string]string{"type": "string", "description": "Source (discord, cli, api, etc.)"},
				"category": map[string]string{"type": "string", "description": "Category override (optional)"},
				"tier":     map[string]string{"type": "string", "description": "Tier override: cold/active/persist"},
			},
		},
		{
			Action:      "search",
			Description: "Hybrid search: FTS5 + vector + graph + RRF fusion",
			InputSchema: map[string]any{
				"query":    map[string]string{"type": "string", "description": "Search query"},
				"mode":     map[string]string{"type": "string", "description": "Search mode: keyword/vector/balanced/deep"},
				"category": map[string]string{"type": "string", "description": "Category filter (optional)"},
				"tier":     map[string]string{"type": "string", "description": "Tier filter (optional)"},
				"limit":    map[string]string{"type": "integer", "description": "Max results (default: 10)"},
			},
		},
		{
			Action:      "context_build",
			Description: "Build context for a task (hybrid search + graph traversal)",
			InputSchema: map[string]any{
				"task":    map[string]string{"type": "string", "description": "Task description"},
				"project": map[string]string{"type": "string", "description": "Project name (optional)"},
				"agent":   map[string]string{"type": "string", "description": "Agent name (optional)"},
				"limit":   map[string]string{"type": "integer", "description": "Max results (default: 10)"},
			},
		},
		{
			Action:      "entity_get",
			Description: "Get an entity's compiled truth and timeline",
			InputSchema: map[string]any{
				"name": map[string]string{"type": "string", "description": "Entity name or slug"},
			},
		},
		{
			Action:      "graph_query",
			Description: "Traverse the knowledge graph from an entity",
			InputSchema: map[string]any{
				"entity":     map[string]string{"type": "string", "description": "Entity name or slug"},
				"depth":      map[string]string{"type": "integer", "description": "Number of hops (default: 1)"},
				"edge_types": map[string]string{"type": "array", "description": "Filter by edge types (optional)"},
			},
		},
		{
			Action:      "dream",
			Description: "Trigger the dream cycle manually",
			InputSchema: map[string]any{
				"phases":  map[string]string{"type": "array", "description": "Specific phases to run (optional)"},
				"dry_run": map[string]string{"type": "boolean", "description": "Report without changes (default: false)"},
			},
		},
	}
}

// Execute runs an action through the Remembrance adapter.
func (a *Adapter) Execute(ctx context.Context, action string, input map[string]any) (*adapter.Result, error) {
	switch action {
	case "capture":
		return a.executeCapture(ctx, input)
	case "search":
		return a.executeSearch(ctx, input)
	case "context_build":
		return a.executeContextBuild(ctx, input)
	case "entity_get":
		return a.executeEntityGet(ctx, input)
	case "graph_query":
		return a.executeGraphQuery(ctx, input)
	case "dream":
		return a.executeDream(ctx, input)
	default:
		return nil, fmt.Errorf("remembrance adapter: unknown action %q", action)
	}
}

// Health checks if the Remembrance service is reachable.
func (a *Adapter) Health(ctx context.Context) (*adapter.HealthResult, error) {
	if a.client.IsAvailable() {
		return &adapter.HealthResult{
			Ready:   true,
			Message: "remembrance service is reachable",
		}, nil
	}
	return &adapter.HealthResult{
		Ready:   false,
		Message: "remembrance service is not reachable",
		Details: map[string]any{
			"url": a.client.BaseURL,
		},
	}, nil
}

// ── Action Implementations ──────────────────────────────────────

func (a *Adapter) executeCapture(ctx context.Context, input map[string]any) (*adapter.Result, error) {
	text, _ := input["text"].(string)
	if text == "" {
		return &adapter.Result{Success: false, Error: "missing required field: text"}, nil
	}
	source, _ := input["source"].(string)
	category, _ := input["category"].(string)
	tier, _ := input["tier"].(string)

	resp, err := a.client.Capture(text, source, category, tier)
	if err != nil {
		return &adapter.Result{Success: false, Error: err.Error()}, nil
	}
	return &adapter.Result{Success: true, Output: resultToMap(resp)}, nil
}

func (a *Adapter) executeSearch(ctx context.Context, input map[string]any) (*adapter.Result, error) {
	query, _ := input["query"].(string)
	if query == "" {
		return &adapter.Result{Success: false, Error: "missing required field: query"}, nil
	}
	mode, _ := input["mode"].(string)
	category, _ := input["category"].(string)
	tier, _ := input["tier"].(string)
	limit := 10
	if l, ok := input["limit"]; ok {
		switch v := l.(type) {
		case float64:
			limit = int(v)
		case int:
			limit = v
		case string:
			limit, _ = strconv.Atoi(v)
		}
	}

	resp, err := a.client.Search(query, mode, category, tier, limit)
	if err != nil {
		return &adapter.Result{Success: false, Error: err.Error()}, nil
	}
	return &adapter.Result{Success: true, Output: resultToMap(resp)}, nil
}

func (a *Adapter) executeContextBuild(ctx context.Context, input map[string]any) (*adapter.Result, error) {
	task, _ := input["task"].(string)
	if task == "" {
		return &adapter.Result{Success: false, Error: "missing required field: task"}, nil
	}
	project, _ := input["project"].(string)
	agent, _ := input["agent"].(string)
	limit := 10
	if l, ok := input["limit"]; ok {
		switch v := l.(type) {
		case float64:
			limit = int(v)
		case int:
			limit = v
		}
	}

	resp, err := a.client.BuildContext(task, project, agent, limit)
	if err != nil {
		return &adapter.Result{Success: false, Error: err.Error()}, nil
	}
	return &adapter.Result{Success: true, Output: resultToMap(resp)}, nil
}

func (a *Adapter) executeEntityGet(ctx context.Context, input map[string]any) (*adapter.Result, error) {
	name, _ := input["name"].(string)
	if name == "" {
		return &adapter.Result{Success: false, Error: "missing required field: name"}, nil
	}

	resp, err := a.client.EntityGet(name)
	if err != nil {
		return &adapter.Result{Success: false, Error: err.Error()}, nil
	}
	return &adapter.Result{Success: true, Output: resultToMap(resp)}, nil
}

func (a *Adapter) executeGraphQuery(ctx context.Context, input map[string]any) (*adapter.Result, error) {
	entity, _ := input["entity"].(string)
	if entity == "" {
		return &adapter.Result{Success: false, Error: "missing required field: entity"}, nil
	}
	depth := 1
	if d, ok := input["depth"]; ok {
		switch v := d.(type) {
		case float64:
			depth = int(v)
		case int:
			depth = v
		}
	}

	var edgeTypes []string
	if et, ok := input["edge_types"]; ok {
		if arr, ok := et.([]any); ok {
			for _, v := range arr {
				if s, ok := v.(string); ok {
					edgeTypes = append(edgeTypes, s)
				}
			}
		}
	}

	resp, err := a.client.GraphQuery(entity, depth, edgeTypes)
	if err != nil {
		return &adapter.Result{Success: false, Error: err.Error()}, nil
	}
	return &adapter.Result{Success: true, Output: resultToMap(resp)}, nil
}

func (a *Adapter) executeDream(ctx context.Context, input map[string]any) (*adapter.Result, error) {
	var phases []string
	if p, ok := input["phases"]; ok {
		if arr, ok := p.([]any); ok {
			for _, v := range arr {
				if s, ok := v.(string); ok {
					phases = append(phases, s)
				}
			}
		}
	}
	dryRun, _ := input["dry_run"].(bool)

	resp, err := a.client.Dream(phases, dryRun)
	if err != nil {
		return &adapter.Result{Success: false, Error: err.Error()}, nil
	}
	return &adapter.Result{Success: true, Output: resultToMap(resp)}, nil
}

// resultToMap converts any value to map[string]any via JSON round-trip.
// This handles both map[string]any values returned directly from the client
// and typed structs.
func resultToMap(v any) map[string]any {
	data, err := json.Marshal(v)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return map[string]any{"raw": string(data)}
	}
	return result
}

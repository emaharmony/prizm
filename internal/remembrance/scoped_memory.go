package remembrance

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/memory"
)

// ScopedMemoryBackend translates the Recall/Remembrance HTTP client into the
// memory domain contract. Workflow code depends only on memory.PrimaryBackend.
type ScopedMemoryBackend struct {
	Client *Client
}

func (b ScopedMemoryBackend) Capture(ctx context.Context, mem memory.Memory) (string, error) {
	return b.CaptureIdempotent(ctx, mem, mem.ID)
}

// CaptureIdempotent binds the durable Prizm delivery key to Recall's atomic,
// scope-aware capture API. Replays return the original remote memory ID.
func (b ScopedMemoryBackend) CaptureIdempotent(ctx context.Context, mem memory.Memory, key string) (string, error) {
	if b.Client == nil {
		return "", fmt.Errorf("remembrance client is not configured")
	}
	result, err := b.Client.CaptureWithMetadataContext(ctx, CaptureRequest{
		OwnerID: mem.UserID, AgentID: mem.AgentID, SessionID: mem.SessionID, TaskID: mem.TaskID,
		Scope: "task", Category: mem.Category, Summary: mem.Summary,
		SourceRef: mem.ID, ImportanceScore: 0.5, ProjectID: mem.ProjectID,
		Title: mem.Summary, Content: mem.Content, SourceType: "prizm_scoped_memory",
		SourceAgent: mem.Source, IdempotencyKey: key,
	})
	if err != nil {
		return "", err
	}
	decision, _ := result["decision"].(string)
	switch strings.ToUpper(strings.TrimSpace(decision)) {
	case "PASS", "PERSIST", "ACTIVE", "COLD", "ACCEPT", "ACCEPTED", "STORE", "STORED":
		// These decisions represent an accepted capture across Recall's
		// compatibility and gated ingestion responses.
	default:
		return "", fmt.Errorf("recall rejected idempotent capture: decision=%q", decision)
	}
	if id, ok := result["id"].(string); ok {
		return id, nil
	}
	return "", fmt.Errorf("recall idempotent capture returned no remote ID")
}

func (b ScopedMemoryBackend) Search(_ context.Context, req memory.SearchRequest) ([]memory.Memory, error) {
	if b.Client == nil {
		return nil, fmt.Errorf("remembrance client is not configured")
	}
	scope := req.Scope
	result, err := b.Client.SearchScoped(req.Query, "keyword", req.Limit, ScopedSearchRequest{
		ProjectID: scope.ProjectID, TaskID: scope.TaskID, SessionID: scope.SessionID, AgentID: scope.AgentID,
		OwnerID: func() string {
			if scope.IncludeUserScope {
				return scope.UserID
			}
			return ""
		}(),
	})
	if err != nil {
		return nil, err
	}
	raw, _ := result["results"].([]any)
	out := make([]memory.Memory, 0, len(raw))
	for _, item := range raw {
		fields, ok := item.(map[string]any)
		if !ok {
			continue
		}
		mem := memory.Memory{ID: stringValue(fields, "id", "memory_id"), Content: stringValue(fields, "content", "text"), Summary: stringValue(fields, "summary", "title"), Category: stringValue(fields, "category"), ProjectID: stringValue(fields, "project_id"), TaskID: stringValue(fields, "task_id"), UserID: stringValue(fields, "owner_id", "user_id"), SessionID: stringValue(fields, "session_id"), AgentID: stringValue(fields, "agent_id"), Source: "recall", SupersedesID: stringValue(fields, "supersedes_id")}
		if mem.ProjectID != scope.ProjectID || mem.TaskID != scope.TaskID || (scope.IncludeUserScope && mem.UserID != scope.UserID) || (!scope.IncludeUserScope && mem.UserID != "") {
			continue
		}
		mem.CreatedAt = timeValue(fields, "created_at", "created")
		mem.KeyTopics = stringSlice(fields["topics"])
		out = append(out, mem)
	}
	return out, nil
}

func timeValue(fields map[string]any, names ...string) time.Time {
	for _, name := range names {
		switch value := fields[name].(type) {
		case string:
			if parsed, err := time.Parse(time.RFC3339, value); err == nil {
				return parsed
			}
		case float64:
			return time.Unix(int64(value), 0).UTC()
		case json.Number:
			if seconds, err := strconv.ParseInt(string(value), 10, 64); err == nil {
				return time.Unix(seconds, 0).UTC()
			}
		}
	}
	return time.Time{}
}

func stringSlice(value any) []string {
	items, ok := value.([]any)
	if !ok {
		if strings, ok := value.([]string); ok {
			return append([]string(nil), strings...)
		}
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func stringValue(fields map[string]any, names ...string) string {
	for _, name := range names {
		if value, ok := fields[name].(string); ok {
			return value
		}
	}
	return ""
}

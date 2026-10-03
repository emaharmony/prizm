package remembrance

import (
	"context"
	"fmt"
	"time"

	"github.com/emaharmony/prizm/internal/memory"
)

// ScopedMemoryBackend translates the Recall/Remembrance HTTP client into the
// memory domain contract. Workflow code depends only on memory.PrimaryBackend.
type ScopedMemoryBackend struct {
	Client *Client
}

func (b ScopedMemoryBackend) Capture(_ context.Context, mem memory.Memory) (string, error) {
	if b.Client == nil {
		return "", fmt.Errorf("remembrance client is not configured")
	}
	result, err := b.Client.CaptureWithMetadata(CaptureRequest{
		OwnerID: mem.UserID, AgentID: mem.AgentID, SessionID: mem.SessionID, TaskID: mem.TaskID,
		Scope: "task", Category: mem.Category, Summary: mem.Summary,
		SourceRef: mem.ID, ImportanceScore: 0.5, ProjectID: mem.ProjectID,
		Title: mem.Summary, Content: mem.Content, SourceType: "prizm_scoped_memory",
		SourceAgent: mem.Source,
	})
	if err != nil {
		return "", err
	}
	if id, ok := result["id"].(string); ok {
		return id, nil
	}
	return mem.ID, nil
}

func (b ScopedMemoryBackend) Search(_ context.Context, req memory.SearchRequest) ([]memory.Memory, error) {
	if b.Client == nil {
		return nil, fmt.Errorf("remembrance client is not configured")
	}
	result, err := b.Client.Search(req.Query, "keyword", "", "", req.Limit)
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
		mem := memory.Memory{ID: stringValue(fields, "id", "memory_id"), Content: stringValue(fields, "content", "text"), Summary: stringValue(fields, "summary", "title"), Category: stringValue(fields, "category"), ProjectID: stringValue(fields, "project_id"), TaskID: stringValue(fields, "task_id"), UserID: stringValue(fields, "owner_id", "user_id"), Source: "recall"}
		if mem.CreatedAt.IsZero() {
			mem.CreatedAt = time.Time{}
		}
		out = append(out, mem)
	}
	return out, nil
}

func stringValue(fields map[string]any, names ...string) string {
	for _, name := range names {
		if value, ok := fields[name].(string); ok {
			return value
		}
	}
	return ""
}

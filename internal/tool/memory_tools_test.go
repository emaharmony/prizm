package tool

import (
	"context"
	"testing"

	"github.com/emaharmony/prizm/internal/memory"
)

// mockStore implements LocalMemoryStore for testing.
type mockStore struct {
	memories []memory.Memory
	searchErr error
}

func (m *mockStore) Search(ctx context.Context, query string, limit int) ([]memory.Memory, error) {
	if m.searchErr != nil {
		return nil, m.searchErr
	}
	var results []memory.Memory
	for _, mem := range m.memories {
		results = append(results, mem)
		if len(results) >= limit {
			break
		}
	}
	return results, nil
}

func (m *mockStore) Get(ctx context.Context, id string) (*memory.Memory, error) {
	for _, mem := range m.memories {
		if mem.ID == id {
			return &mem, nil
		}
	}
	return nil, nil
}

func (m *mockStore) ListRecent(ctx context.Context, limit int) ([]memory.Memory, error) {
	if limit > 0 && limit < len(m.memories) {
		return m.memories[:limit], nil
	}
	return m.memories, nil
}

func (m *mockStore) Store(ctx context.Context, mem memory.Memory) (string, error) {
	m.memories = append(m.memories, mem)
	return mem.ID, nil
}

func (m *mockStore) Close() error { return nil }

func TestMemorySearchTool_LocalFallback(t *testing.T) {
	store := &mockStore{
		memories: []memory.Memory{
			{ID: "1", Content: "Decided to use SQLite", Category: "decision", Summary: "Use SQLite"},
		},
	}

	tool := &MemorySearchTool{LocalStore: store}
	result, err := tool.Execute(context.Background(), map[string]any{
		"query": "SQLite",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
	if result.Output["source"] != "local" {
		t.Errorf("expected source=local, got %v", result.Output["source"])
	}
}

func TestMemorySearchTool_NoStores(t *testing.T) {
	tool := &MemorySearchTool{}
	result, err := tool.Execute(context.Background(), map[string]any{
		"query": "test",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Success {
		t.Error("expected failure when no stores configured")
	}
}

func TestMemoryWriteTool_StoreDirectly(t *testing.T) {
	store := &mockStore{}
	tool := &MemoryWriteTool{Store: store}

	result, err := tool.Execute(context.Background(), map[string]any{
		"content": "Decided to use local models for memory",
		"category": "decision",
		"tier":     "active",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
	if result.Output["gate"] != "passed" {
		t.Errorf("expected gate=passed, got %v", result.Output["gate"])
	}
	if result.Output["memory_id"] == nil {
		t.Error("expected memory_id in output")
	}
	if len(store.memories) != 1 {
		t.Errorf("expected 1 stored memory, got %d", len(store.memories))
	}
}

func TestMemoryWriteTool_EmptyContent(t *testing.T) {
	tool := &MemoryWriteTool{}

	result, err := tool.Execute(context.Background(), map[string]any{
		"content": "",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Success {
		t.Error("expected failure for empty content")
	}
}
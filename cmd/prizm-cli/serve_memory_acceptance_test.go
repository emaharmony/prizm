package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/memory"
	"github.com/emaharmony/prizm/internal/remembrance"
	"github.com/emaharmony/prizm/internal/tool"
)

func TestServeScopedMemoryToolCompositionFallsBackAndSearchesByScope(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	local := memory.NewMarkdownStore(filepath.Join(t.TempDir(), "workspace"))
	events, err := event.NewSQLiteEventStore(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatalf("open event store: %v", err)
	}
	defer events.Close()

	// Port 1 is intentionally unreachable. This exercises the same composed
	// tool/facade used by serve without starting a model or public write route.
	primary := remembrance.ScopedMemoryBackend{
		Client: remembrance.NewClientWithTimeout("http://127.0.0.1:1", 150*time.Millisecond),
	}
	reconciler := memory.NewReconciler(local, primary, events, "prizm:serve-test")
	facade := &memory.Facade{Local: local, Primary: primary, Events: events, Reconciler: reconciler, Source: "prizm:serve-test"}
	registry := tool.NewRegistry()
	if err := registerScopedMemoryTool(registry, local, facade); err != nil {
		t.Fatalf("register serve memory tool: %v", err)
	}

	result, err := registry.Execute(ctx, "memory_write", map[string]any{
		"content":        "serve composition acceptance marker",
		"category":       "fact",
		"tier":           "active",
		"source":         "prizm:serve-test",
		"agent_id":       "acceptance-agent",
		"session_id":     "acceptance-session",
		"correlation_id": "acceptance-correlation",
		"project_id":     "acceptance-project",
		"task_id":        "acceptance-task",
	})
	if err != nil {
		t.Fatalf("execute composed memory_write: %v", err)
	}
	if !result.Success {
		t.Fatalf("memory_write failed: %s", result.Error)
	}
	if fallback, ok := result.Output["fallback"].(bool); !ok || !fallback {
		t.Fatalf("memory_write fallback = %#v, want true", result.Output["fallback"])
	}

	results, fallback, err := facade.Search(ctx, memory.SearchRequest{Query: "serve composition acceptance", Limit: 5, Scope: memory.Scope{
		ProjectID: "acceptance-project", TaskID: "acceptance-task", SessionID: "acceptance-session", AgentID: "acceptance-agent", CorrelationID: "acceptance-correlation",
	}})
	if err != nil {
		t.Fatalf("search composed facade: %v", err)
	}
	if !fallback || len(results) != 1 {
		t.Fatalf("scoped fallback search = fallback %v, results %d; want true, 1", fallback, len(results))
	}

	facts, err := events.Query(ctx, event.EventFilter{Type: "prizm.memory.capture.", Limit: 100})
	if err != nil {
		t.Fatalf("query lifecycle facts: %v", err)
	}
	seenPending, seenFallback := false, false
	for _, fact := range facts {
		switch fact.Type {
		case memory.EventScopedCaptureSyncPending:
			seenPending = true
		case memory.EventScopedCaptureFallback:
			seenFallback = true
		}
	}
	if !seenPending || !seenFallback {
		t.Fatalf("lifecycle facts pending=%v fallback=%v; want both", seenPending, seenFallback)
	}
}

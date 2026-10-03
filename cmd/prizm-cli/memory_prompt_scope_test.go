package main

import (
	"context"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/memory"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/remembrance"
	"github.com/emaharmony/prizm/internal/session"
)

func TestTrustedPromptMemoryScopeRequiresTrustedIdentity(t *testing.T) {
	workspace := t.TempDir()
	cc := &conversationContext{cfg: &orchestrator.Config{Prizm: orchestrator.PrizmConfig{Workspace: workspace}}}
	_, err := cc.trustedPromptMemoryScope(&orchestrator.AgentConfig{ID: "agent"}, "", "user", &session.Session{ID: "session"})
	if err == nil {
		t.Fatal("missing run identity must fail closed")
	}
	_, err = cc.trustedPromptMemoryScope(&orchestrator.AgentConfig{ID: "agent"}, "run", "", &session.Session{ID: "session"})
	if err == nil {
		t.Fatal("missing user identity must fail closed")
	}
}

func TestScopedPromptInjectionDoesNotCrossScopeOrCache(t *testing.T) {
	store := memory.NewMarkdownStore(t.TempDir())
	for _, mem := range []memory.Memory{
		{ID: "allowed", Content: "alpha scoped decision", Summary: "allowed", ProjectID: "project-a", TaskID: "run-a", SessionID: "session-a", AgentID: "agent", UserID: "user-a"},
		{ID: "other-project", Content: "alpha scoped secret", Summary: "other", ProjectID: "project-b", TaskID: "run-a", SessionID: "session-a", AgentID: "agent", UserID: "user-a"},
		{ID: "other-task", Content: "alpha scoped secret", Summary: "other", ProjectID: "project-a", TaskID: "run-b", SessionID: "session-a", AgentID: "agent", UserID: "user-a"},
		{ID: "other-session", Content: "alpha scoped secret", Summary: "other", ProjectID: "project-a", TaskID: "run-a", SessionID: "session-b", AgentID: "agent", UserID: "user-a"},
	} {
		if _, err := store.Store(context.Background(), mem); err != nil {
			t.Fatal(err)
		}
	}
	injector := NewMemoryInjector(store, nil)
	facade := &memory.Facade{Local: store}
	scopeA := memory.Scope{ProjectID: "project-a", TaskID: "run-a", SessionID: "session-a", AgentID: "agent", UserID: "user-a", IncludeUserScope: true, CorrelationID: "run-a"}
	got := injector.InjectScopedMemories(context.Background(), facade, scopeA, "alpha scoped", 1, 400)
	if !strings.Contains(got, "allowed") || strings.Contains(got, "other-project") || strings.Contains(got, "other-task") || strings.Contains(got, "other-session") {
		t.Fatalf("scoped prompt = %q", got)
	}
	scopeB := scopeA
	scopeB.ProjectID = "project-b"
	scopeB.CorrelationID = "run-a"
	if scopedQueryKey(scopeA, "alpha scoped") == scopedQueryKey(scopeB, "alpha scoped") {
		t.Fatal("cache key crossed project scope")
	}
	if got := injector.InjectScopedMemories(context.Background(), facade, scopeB, "alpha scoped", 1, 400); strings.Contains(got, "allowed") {
		t.Fatalf("cache or search leaked project-a result: %q", got)
	}
}

func TestRemembranceContextRequiresCompleteScopeMetadata(t *testing.T) {
	scope := memory.Scope{ProjectID: "project", TaskID: "run", SessionID: "session", AgentID: "agent", UserID: "user", IncludeUserScope: true, CorrelationID: "run"}
	missing := &remembrance.ContextPackResponse{ProjectID: "project", AgentID: "agent", OwnerID: "user"}
	if remembranceContextMatchesScope(missing, scope) {
		t.Fatal("context without task and session metadata was accepted")
	}
	valid := &remembrance.ContextPackResponse{ProjectID: "project", TaskID: "run", SessionID: "session", AgentID: "agent", OwnerID: "user"}
	if !remembranceContextMatchesScope(valid, scope) {
		t.Fatal("complete trusted context metadata was rejected")
	}
}

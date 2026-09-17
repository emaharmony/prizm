package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/session"
	"github.com/emaharmony/prizm/internal/task"
)

// fakeChatProvider implements both provider.Provider and provider.ChatProvider
// so it can be registered in a provider.ProviderRegistry and resolved via
// GetChatProvider, without depending on a real LLM backend. It records the
// messages of each ChatGenerate call so tests can assert what history was
// threaded into the prompt.
type fakeChatProvider struct {
	content string
	err     error

	mu       sync.Mutex
	requests [][]provider.ChatMessage
}

func (f *fakeChatProvider) Generate(ctx context.Context, req provider.GenerateRequest) (provider.GenerateResponse, error) {
	return provider.GenerateResponse{Text: f.content}, f.err
}

func (f *fakeChatProvider) ChatGenerate(ctx context.Context, req provider.ChatGenerateRequest) (provider.ChatGenerateResponse, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req.Messages)
	f.mu.Unlock()
	if f.err != nil {
		return provider.ChatGenerateResponse{}, f.err
	}
	return provider.ChatGenerateResponse{Content: f.content, Model: req.Model, Provider: "fake"}, nil
}

// lastRequest returns the messages of the most recent ChatGenerate call.
func (f *fakeChatProvider) lastRequest() []provider.ChatMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		return nil
	}
	return f.requests[len(f.requests)-1]
}

// callCount returns how many times the model was actually invoked.
func (f *fakeChatProvider) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// userContents extracts the Content of every user message, in order.
func userContents(messages []provider.ChatMessage) []string {
	var out []string
	for _, m := range messages {
		if m.Role == "user" {
			out = append(out, m.Content)
		}
	}
	return out
}

func containsContent(messages []provider.ChatMessage, want string) bool {
	for _, m := range messages {
		if strings.Contains(m.Content, want) {
			return true
		}
	}
	return false
}

var (
	_ provider.Provider     = (*fakeChatProvider)(nil)
	_ provider.ChatProvider = (*fakeChatProvider)(nil)
)

func newInvocationTestAPI(t *testing.T, agents []orchestrator.AgentConfig, chatProv *fakeChatProvider) (*Server, func()) {
	return newInvocationTestAPIWithIdle(t, agents, chatProv, 0)
}

func newInvocationTestAPIWithIdle(t *testing.T, agents []orchestrator.AgentConfig, chatProv *fakeChatProvider, invokeIdle time.Duration) (*Server, func()) {
	t.Helper()
	dir := t.TempDir()

	store, err := task.NewStore(filepath.Join(dir, "tasks.db"))
	if err != nil {
		t.Fatalf("task store: %v", err)
	}
	sessions, err := session.NewManager(filepath.Join(dir, "sessions.db"), 50, 30*time.Minute, 4, "truncate")
	if err != nil {
		t.Fatalf("session manager: %v", err)
	}

	cfg := &orchestrator.Config{
		Prizm:    orchestrator.PrizmConfig{DataDir: dir},
		Agents:   agents,
		Sessions: orchestrator.SessionConfig{MaxContextMessages: 50, CompactionStrategy: "truncate"},
	}
	orch, err := orchestrator.New(cfg)
	if err != nil {
		t.Fatalf("orchestrator: %v", err)
	}

	// A nil chatProv means "simulate a deployment where invocation was never
	// wired up" — leave Providers nil rather than an empty registry, so the
	// synchronous "not configured" guard (as opposed to an async
	// model-not-found failure) is what gets exercised.
	var providers *provider.ProviderRegistry
	if chatProv != nil {
		providers = provider.NewProviderRegistry()
		for _, a := range agents {
			providers.Register(a.Model, chatProv, provider.ModelInfo{ProviderName: a.Provider})
		}
	}

	server := NewServer(Config{
		Addr:              ":0",
		Orch:              orch,
		Store:             store,
		Sessions:          sessions,
		Providers:         providers,
		InvokeIdleTimeout: invokeIdle,
		NATS:              nil,
	})

	cleanup := func() {
		store.Close()
		sessions.Close()
	}
	return server, cleanup
}

func invokableAgent(id string) orchestrator.AgentConfig {
	return orchestrator.AgentConfig{
		ID:              id,
		Role:            "stream-clip-judge",
		Provider:        "fake",
		Model:           "fake-model",
		InvocableViaAPI: true,
	}
}

func postInvoke(t *testing.T, s *Server, agentID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+agentID+"/invoke", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	return w
}

func waitForInvocation(t *testing.T, s *Server, agentID, invocationID string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/v1/agents/%s/invocations/%s", agentID, invocationID), nil)
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("poll invocation: expected 200, got %d: %s", w.Code, w.Body.String())
		}
		var resp map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("poll invocation: parse response: %v", err)
		}
		if resp["status"] != "pending" {
			return resp
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("invocation %s did not complete within deadline", invocationID)
	return nil
}

func TestAPI_AgentInvoke_NotInvocable(t *testing.T) {
	agents := []orchestrator.AgentConfig{{ID: "lumi", Role: "lead", Provider: "fake", Model: "fake-model"}}
	s, cleanup := newInvocationTestAPI(t, agents, &fakeChatProvider{content: `{"ok":true}`})
	defer cleanup()

	w := postInvoke(t, s, "lumi", `{"prompt":"hello"}`)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAPI_AgentInvoke_AgentNotFound(t *testing.T) {
	s, cleanup := newInvocationTestAPI(t, nil, &fakeChatProvider{content: `{}`})
	defer cleanup()

	w := postInvoke(t, s, "ghost", `{"prompt":"hello"}`)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAPI_AgentInvoke_MissingPrompt(t *testing.T) {
	agents := []orchestrator.AgentConfig{invokableAgent("clippy")}
	s, cleanup := newInvocationTestAPI(t, agents, &fakeChatProvider{content: `{}`})
	defer cleanup()

	w := postInvoke(t, s, "clippy", `{"prompt":""}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAPI_AgentInvoke_SuccessJSONResult(t *testing.T) {
	agents := []orchestrator.AgentConfig{invokableAgent("clippy")}
	s, cleanup := newInvocationTestAPI(t, agents, &fakeChatProvider{
		content: `{"clip_worthy":true,"confidence":0.87,"title":"Big reaction","reason":"chat spike"}`,
	})
	defer cleanup()

	w := postInvoke(t, s, "clippy", `{"prompt":"chat velocity 4.2x baseline"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
	var accepted map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("parse accepted response: %v", err)
	}
	invocationID, _ := accepted["invocation_id"].(string)
	if invocationID == "" {
		t.Fatalf("expected invocation_id in response, got %v", accepted)
	}
	if accepted["status"] != "pending" {
		t.Errorf("expected initial status pending, got %v", accepted["status"])
	}

	final := waitForInvocation(t, s, "clippy", invocationID)
	if final["status"] != "completed" {
		t.Fatalf("expected status completed, got %v (full: %v)", final["status"], final)
	}
	result, ok := final["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result object, got %v", final["result"])
	}
	if result["clip_worthy"] != true {
		t.Errorf("expected clip_worthy=true, got %v", result["clip_worthy"])
	}
	if result["title"] != "Big reaction" {
		t.Errorf("expected title 'Big reaction', got %v", result["title"])
	}
}

func TestAPI_AgentInvoke_NonJSONResultWrapped(t *testing.T) {
	agents := []orchestrator.AgentConfig{invokableAgent("clippy")}
	s, cleanup := newInvocationTestAPI(t, agents, &fakeChatProvider{content: "not json at all"})
	defer cleanup()

	w := postInvoke(t, s, "clippy", `{"prompt":"hello"}`)
	var accepted map[string]any
	json.Unmarshal(w.Body.Bytes(), &accepted)
	invocationID := accepted["invocation_id"].(string)

	final := waitForInvocation(t, s, "clippy", invocationID)
	if final["status"] != "completed" {
		t.Fatalf("expected status completed, got %v", final["status"])
	}
	result := final["result"].(map[string]any)
	if result["text"] != "not json at all" {
		t.Errorf("expected wrapped text result, got %v", result)
	}
}

func TestAPI_AgentInvoke_ProviderErrorFails(t *testing.T) {
	agents := []orchestrator.AgentConfig{invokableAgent("clippy")}
	s, cleanup := newInvocationTestAPI(t, agents, &fakeChatProvider{err: fmt.Errorf("provider unavailable")})
	defer cleanup()

	w := postInvoke(t, s, "clippy", `{"prompt":"hello"}`)
	var accepted map[string]any
	json.Unmarshal(w.Body.Bytes(), &accepted)
	invocationID := accepted["invocation_id"].(string)

	final := waitForInvocation(t, s, "clippy", invocationID)
	if final["status"] != "failed" {
		t.Fatalf("expected status failed, got %v", final["status"])
	}
	if final["error"] == nil || final["error"] == "" {
		t.Errorf("expected a non-empty error message, got %v", final["error"])
	}
}

func TestAPI_AgentInvocationDetail_NotFound(t *testing.T) {
	agents := []orchestrator.AgentConfig{invokableAgent("clippy")}
	s, cleanup := newInvocationTestAPI(t, agents, &fakeChatProvider{content: `{}`})
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/agents/clippy/invocations/does-not-exist", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAPI_AgentInvoke_NoProvidersConfigured(t *testing.T) {
	agents := []orchestrator.AgentConfig{invokableAgent("clippy")}
	s, cleanup := newInvocationTestAPI(t, agents, nil)
	defer cleanup()

	w := postInvoke(t, s, "clippy", `{"prompt":"hello"}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503, got %d: %s", w.Code, w.Body.String())
	}
}

// invokeAndWait posts an invoke request and blocks until it reaches a terminal
// state, returning the final polled body.
func invokeAndWait(t *testing.T, s *Server, agentID, body string) map[string]any {
	t.Helper()
	w := postInvoke(t, s, agentID, body)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
	var accepted map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("parse accepted response: %v", err)
	}
	id, _ := accepted["invocation_id"].(string)
	if id == "" {
		t.Fatalf("missing invocation_id: %v", accepted)
	}
	return waitForInvocation(t, s, agentID, id)
}

// TestAPI_AgentInvoke_ConversationRetainsHistory is the core regression for the
// "prizm-eddie forgets the previous answer" bug: a second call sharing a
// conversation_id must see the first exchange threaded into the prompt.
func TestAPI_AgentInvoke_ConversationRetainsHistory(t *testing.T) {
	agents := []orchestrator.AgentConfig{invokableAgent("eddie")}
	chat := &fakeChatProvider{content: "forty-two"}
	s, cleanup := newInvocationTestAPI(t, agents, chat)
	defer cleanup()

	if final := invokeAndWait(t, s, "eddie", `{"prompt":"give me a fact","conversation_id":"c1"}`); final["status"] != "completed" {
		t.Fatalf("call 1 not completed: %v", final)
	}
	if final := invokeAndWait(t, s, "eddie", `{"prompt":"give me a more obscure one","conversation_id":"c1"}`); final["status"] != "completed" {
		t.Fatalf("call 2 not completed: %v", final)
	}

	last := chat.lastRequest()
	users := userContents(last)
	if len(users) != 2 || users[0] != "give me a fact" || users[1] != "give me a more obscure one" {
		t.Fatalf("expected both user turns in order, got %v", users)
	}
	if !containsContent(last, "forty-two") {
		t.Errorf("expected prior assistant reply in history, got %+v", last)
	}
}

// TestAPI_AgentInvoke_NoConversationIDSingleShot verifies backward compatibility:
// without a conversation_id, each call is memoryless.
func TestAPI_AgentInvoke_NoConversationIDSingleShot(t *testing.T) {
	agents := []orchestrator.AgentConfig{invokableAgent("clippy")}
	chat := &fakeChatProvider{content: "ok"}
	s, cleanup := newInvocationTestAPI(t, agents, chat)
	defer cleanup()

	invokeAndWait(t, s, "clippy", `{"prompt":"first"}`)
	invokeAndWait(t, s, "clippy", `{"prompt":"second"}`)

	last := chat.lastRequest()
	if got := userContents(last); len(got) != 1 || got[0] != "second" {
		t.Fatalf("single-shot call should carry only its own prompt, got %v", got)
	}
	if containsContent(last, "first") {
		t.Errorf("single-shot call leaked prior turn: %+v", last)
	}
}

// TestAPI_AgentInvoke_ResetFlagClearsHistory verifies reset:true drops memory.
func TestAPI_AgentInvoke_ResetFlagClearsHistory(t *testing.T) {
	agents := []orchestrator.AgentConfig{invokableAgent("eddie")}
	chat := &fakeChatProvider{content: "reply"}
	s, cleanup := newInvocationTestAPI(t, agents, chat)
	defer cleanup()

	invokeAndWait(t, s, "eddie", `{"prompt":"about ergot","conversation_id":"c1"}`)
	invokeAndWait(t, s, "eddie", `{"prompt":"about the weather","conversation_id":"c1","reset":true}`)

	last := chat.lastRequest()
	if got := userContents(last); len(got) != 1 || got[0] != "about the weather" {
		t.Fatalf("reset should start fresh, got %v", got)
	}
	if containsContent(last, "ergot") {
		t.Errorf("reset did not clear prior history: %+v", last)
	}
}

// TestAPI_AgentInvoke_SwitchPhraseResets verifies a spoken "new topic" resets
// memory but still answers the message.
func TestAPI_AgentInvoke_SwitchPhraseResets(t *testing.T) {
	agents := []orchestrator.AgentConfig{invokableAgent("eddie")}
	chat := &fakeChatProvider{content: "reply"}
	s, cleanup := newInvocationTestAPI(t, agents, chat)
	defer cleanup()

	invokeAndWait(t, s, "eddie", `{"prompt":"about ergot","conversation_id":"c1"}`)
	invokeAndWait(t, s, "eddie", `{"prompt":"new topic: about the weather","conversation_id":"c1"}`)

	last := chat.lastRequest()
	if !containsContent(last, "new topic: about the weather") {
		t.Errorf("switch message should still be sent to the model: %+v", last)
	}
	if containsContent(last, "ergot") {
		t.Errorf("switch did not clear prior history: %+v", last)
	}
}

// TestAPI_AgentInvoke_StopPhraseAcksWithoutLLM verifies a bare "stop" clears the
// conversation and replies with a canned ack without spending an LLM call.
func TestAPI_AgentInvoke_StopPhraseAcksWithoutLLM(t *testing.T) {
	agents := []orchestrator.AgentConfig{invokableAgent("eddie")}
	chat := &fakeChatProvider{content: "reply"}
	s, cleanup := newInvocationTestAPI(t, agents, chat)
	defer cleanup()

	invokeAndWait(t, s, "eddie", `{"prompt":"hello","conversation_id":"c1"}`)
	if chat.callCount() != 1 {
		t.Fatalf("expected 1 LLM call after first message, got %d", chat.callCount())
	}

	final := invokeAndWait(t, s, "eddie", `{"prompt":"stop","conversation_id":"c1"}`)
	if final["status"] != "completed" {
		t.Fatalf("stop should complete, got %v", final)
	}
	result, _ := final["result"].(map[string]any)
	if result == nil || result["text"] != invokeResetAck {
		t.Errorf("expected canned reset ack, got %v", final["result"])
	}
	if chat.callCount() != 1 {
		t.Errorf("stop must not invoke the model, call count = %d", chat.callCount())
	}
}

// TestAPI_AgentInvoke_IdleTimeoutResets verifies the idle safety net: a stale
// conversation starts fresh on the next message.
func TestAPI_AgentInvoke_IdleTimeoutResets(t *testing.T) {
	agents := []orchestrator.AgentConfig{invokableAgent("eddie")}
	chat := &fakeChatProvider{content: "reply"}
	// 1ns idle window: any prior session is immediately stale.
	s, cleanup := newInvocationTestAPIWithIdle(t, agents, chat, time.Nanosecond)
	defer cleanup()

	invokeAndWait(t, s, "eddie", `{"prompt":"about ergot","conversation_id":"c1"}`)
	time.Sleep(2 * time.Millisecond)
	invokeAndWait(t, s, "eddie", `{"prompt":"still there?","conversation_id":"c1"}`)

	last := chat.lastRequest()
	if got := userContents(last); len(got) != 1 || got[0] != "still there?" {
		t.Fatalf("idle timeout should reset, got %v", got)
	}
	if containsContent(last, "ergot") {
		t.Errorf("idle timeout did not clear stale history: %+v", last)
	}
}

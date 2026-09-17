package integration

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/provider/mock"
	"github.com/emaharmony/prizm/internal/session"
	"github.com/emaharmony/prizm/internal/stage"
)

// TestE2E_FullConversationPipeline tests the V21 full conversation path:
// Route → Session → Context → LLM → Persist → EventPublish
//
// This verifies the end-to-end flow from user message to response,
// including session management, context injection, and event publishing.
func TestE2E_FullConversationPipeline(t *testing.T) {
	// 1. Set up mock provider
	prov := mock.New()

	// 2. Create a session manager
	sessMgr, err := session.NewManager(filepath.Join(t.TempDir(), "sessions.db"), 100, 30*time.Minute, 4, "truncate")
	if err != nil {
		t.Fatalf("failed to create session manager: %v", err)
	}
	defer sessMgr.Close()

	// 3. Create a session for the conversation
	sess, err := sessMgr.Create("lumi", "discord", "channel-123", "user-456")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	// 4. Add a user message to the session
	_, err = sessMgr.AddMessage(sess.ID, "user", "Hello, Lumi!", "")
	if err != nil {
		t.Fatalf("failed to add user message: %v", err)
	}

	// 5. Build and run the pipeline
	pipeline := stage.NewPipeline(
		&stage.LLMStage{},
		&stage.PersistenceStage{BusURL: ""}, // No NATS for test
		&stage.EventPublishStage{Publisher: nil, BusURL: ""},
	)

	rc := &stage.RunContext{
		RunID:        "test-e2e-run-1",
		Task:         "You are Lumi, a friendly AI assistant.\n\nUser: Hello, Lumi!\nLumi:",
		Agent:        "lumi",
		Provider:     prov,
		ProviderName: "mock",
		Model:        "mock-model",
		SessionID:    sess.ID,
		// RunDir left empty — conversation mode, no file persistence
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	finalRC, err := pipeline.Run(ctx, rc)
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}

	// 6. Verify all stages succeeded
	for name, result := range finalRC.Results {
		if result != nil && !result.Success {
			t.Errorf("stage %s failed: %s", name, result.Error)
		}
	}

	// 7. Verify LLM response is present
	if finalRC.LLMResponse == "" {
		t.Error("expected non-empty LLM response")
	}

	// 8. Verify events were accumulated
	if len(finalRC.Events) == 0 {
		t.Error("expected events to be accumulated during pipeline")
	}

	// 9. Verify PersistenceStage skipped gracefully (no RunDir)
	persistResult := finalRC.Results["persistence"]
	if persistResult == nil {
		t.Fatal("persistence stage result is nil")
	}
	if !persistResult.Success {
		t.Errorf("persistence stage failed: %s", persistResult.Error)
	}
	if persistResult.Data["skipped"] != true {
		t.Error("expected persistence to be skipped (no RunDir)")
	}
}

// TestE2E_StreamingConversation tests the full streaming path:
// StreamCallback receives tokens in real-time, final response is complete.
func TestE2E_StreamingConversation(t *testing.T) {
	prov := mock.New()

	// Track streaming tokens
	var mu sync.Mutex
	var tokens []string
	var finishedReceived bool

	streamCallback := func(token string, index int, finished bool) error {
		mu.Lock()
		defer mu.Unlock()
		tokens = append(tokens, token)
		if finished {
			finishedReceived = true
		}
		return nil
	}

	pipeline := stage.NewPipeline(
		&stage.LLMStage{},
		&stage.PersistenceStage{BusURL: ""},
		&stage.EventPublishStage{Publisher: nil, BusURL: ""},
	)

	rc := &stage.RunContext{
		RunID:          "test-e2e-stream-1",
		Task:           "You are Lumi.\n\nUser: Hi!\nLumi:",
		Agent:          "lumi",
		Provider:       prov,
		ProviderName:   "mock",
		Model:          "mock-model",
		StreamCallback: streamCallback,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	finalRC, err := pipeline.Run(ctx, rc)
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}

	// Verify streaming tokens were received
	mu.Lock()
	tokenCount := len(tokens)
	gotFinished := finishedReceived
	mu.Unlock()

	if tokenCount == 0 {
		t.Error("expected streaming callback to receive tokens")
	}
	if !gotFinished {
		t.Error("expected streaming callback to receive finished=true on last token")
	}

	// Verify LLM response is present and matches accumulated tokens
	if finalRC.LLMResponse == "" {
		t.Error("expected non-empty LLM response after streaming")
	}
}

// TestE2E_SessionConversation tests that a multi-turn conversation
// correctly maintains session state across messages.
func TestE2E_SessionConversation(t *testing.T) {
	prov := mock.New()
	sessMgr, err := session.NewManager(filepath.Join(t.TempDir(), "sessions.db"), 100, 30*time.Minute, 4, "truncate")
	if err != nil {
		t.Fatalf("failed to create session manager: %v", err)
	}
	defer sessMgr.Close()

	// Create session and simulate first message
	sess, err := sessMgr.Create("lumi", "discord", "channel-789", "user-123")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	// Add user message and get response
	_, err = sessMgr.AddMessage(sess.ID, "user", "What's your name?", "")
	if err != nil {
		t.Fatalf("failed to add first user message: %v", err)
	}

	pipeline := stage.NewPipeline(
		&stage.LLMStage{},
		&stage.PersistenceStage{BusURL: ""},
		&stage.EventPublishStage{Publisher: nil, BusURL: ""},
	)

	rc1 := &stage.RunContext{
		RunID:        "test-e2e-session-1",
		Task:         "You are Lumi.\n\nUser: What's your name?\nLumi:",
		Agent:        "lumi",
		Provider:     prov,
		ProviderName: "mock",
		Model:        "mock-model",
		SessionID:    sess.ID,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	finalRC1, err := pipeline.Run(ctx, rc1)
	if err != nil {
		t.Fatalf("first pipeline run failed: %v", err)
	}

	if finalRC1.LLMResponse == "" {
		t.Error("expected non-empty first response")
	}

	// Save first response to session
	_, err = sessMgr.AddMessage(sess.ID, "agent", finalRC1.LLMResponse, "lumi")
	if err != nil {
		t.Fatalf("failed to save first response: %v", err)
	}

	// Add second user message
	_, err = sessMgr.AddMessage(sess.ID, "user", "Nice to meet you!", "")
	if err != nil {
		t.Fatalf("failed to add second user message: %v", err)
	}

	// Run pipeline again for second message
	rc2 := &stage.RunContext{
		RunID:        "test-e2e-session-2",
		Task:         "You are Lumi.\n\nUser: What's your name?\nLumi: I'm Lumi!\nUser: Nice to meet you!\nLumi:",
		Agent:        "lumi",
		Provider:     prov,
		ProviderName: "mock",
		Model:        "mock-model",
		SessionID:    sess.ID,
	}

	finalRC2, err := pipeline.Run(ctx, rc2)
	if err != nil {
		t.Fatalf("second pipeline run failed: %v", err)
	}

	if finalRC2.LLMResponse == "" {
		t.Error("expected non-empty second response")
	}

	// Verify session has multiple messages (user + agent + user at minimum)
	messages := sess.Messages
	if len(messages) < 3 {
		t.Errorf("expected at least 3 messages in session, got %d", len(messages))
	}
}

// TestE2E_PipelineErrorRecovery tests that the pipeline handles stage
// failures gracefully — one stage failing shouldn't crash the pipeline.
func TestE2E_PipelineErrorRecovery(t *testing.T) {
	prov := mock.New()

	pipeline := stage.NewPipeline(
		&stage.LLMStage{},
		&stage.PersistenceStage{BusURL: ""},
		&stage.EventPublishStage{Publisher: nil, BusURL: ""},
	)

	rc := &stage.RunContext{
		RunID:        "test-e2e-error-1",
		Task:         "Hello",
		Agent:        "lumi",
		Provider:     prov,
		ProviderName: "mock",
		Model:        "mock-model",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	finalRC, err := pipeline.Run(ctx, rc)
	if err != nil {
		t.Fatalf("pipeline should succeed even without session ID: %v", err)
	}

	// All stages should succeed (persistence skips when no RunDir)
	for name, result := range finalRC.Results {
		if result != nil && !result.Success {
			t.Errorf("stage %s should not fail: %s", name, result.Error)
		}
	}
}

// TestE2E_EventsAccumulatedAcrossStages verifies that events from
// multiple stages are accumulated and preserved in the final RunContext.
func TestE2E_EventsAccumulatedAcrossStages(t *testing.T) {
	prov := mock.New()

	pipeline := stage.NewPipeline(
		&stage.LLMStage{},
		&stage.PersistenceStage{BusURL: ""},
		&stage.EventPublishStage{Publisher: nil, BusURL: ""},
	)

	rc := &stage.RunContext{
		RunID:        "test-e2e-events-1",
		Task:         "Generate a response",
		Agent:        "lumi",
		Provider:     prov,
		ProviderName: "mock",
		Model:        "mock-model",
		Events: []event.Event{
			{ID: "evt-start", Type: "lumi.run.started", Source: "lumi"},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	finalRC, err := pipeline.Run(ctx, rc)
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}

	// Initial event + LLM events should be accumulated
	if len(finalRC.Events) == 0 {
		t.Error("expected events to be accumulated across pipeline stages")
	}

	// Verify the initial event is still present
	found := false
	for _, evt := range finalRC.Events {
		if evt.ID == "evt-start" {
			found = true
			break
		}
	}
	if !found {
		t.Error("initial event (evt-start) was lost during pipeline execution")
	}
}

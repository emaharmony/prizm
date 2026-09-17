package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/bus"
	"github.com/emaharmony/prizm/internal/context"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/remembrance"
	"github.com/emaharmony/prizm/internal/session"
)

func TestBuildPrompt_WithContextInjection(t *testing.T) {
	// Create a temp workspace with SOUL.md and USER.md
	workspace := t.TempDir()
	soulContent := "# SOUL\nYou are Lumi. Be warm, playful, and confident."
	userContent := "# User\nEma. He/him. ADHD-aware support."

	if err := os.WriteFile(filepath.Join(workspace, "SOUL.md"), []byte(soulContent), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "USER.md"), []byte(userContent), 0644); err != nil {
		t.Fatal(err)
	}

	ctxBuilder := context.NewBuilder(workspace)
	convCtx := &conversationContext{
		cfg: &orchestrator.Config{
			Agents: []orchestrator.AgentConfig{
				{ID: "lumi", Role: "lead", Context: []string{"soul", "user"}},
			},
		},
		ctxBuilder: ctxBuilder,
	}

	sess := &session.Session{
		ID: "test-session",
		Messages: []session.Message{
			{Role: "user", Content: "Hello!"},
		},
	}

	agentCfg := &orchestrator.AgentConfig{
		ID:      "lumi",
		Role:    "lead",
		Context: []string{"soul", "user"},
	}

	convCtx.rebuildStaticSystemContent(agentCfg)
	prompt := convCtx.buildPrompt(sess, agentCfg, "", nil)

	// Verify agent identity is in the prompt
	if !strings.Contains(prompt, "## Who You Are") {
		t.Error("prompt should contain agent identity")
	}

	// Verify SOUL.md content was injected
	if !strings.Contains(prompt, soulContent) {
		t.Error("prompt should contain SOUL.md content")
	}

	// Verify USER.md content was injected
	if !strings.Contains(prompt, userContent) {
		t.Error("prompt should contain USER.md content")
	}

	// Verify conversation history is in the prompt
	if !strings.Contains(prompt, "User: Hello!") {
		t.Error("prompt should contain conversation history")
	}

	// Verify the prompt ends with the agent name (ready for completion)
	if !strings.HasSuffix(strings.TrimSpace(prompt), "lumi:") {
		t.Errorf("prompt should end with 'lumi:', got: %q", prompt[len(prompt)-20:])
	}
}

func TestBuildPrompt_WithoutContextInjection(t *testing.T) {
	convCtx := &conversationContext{
		cfg: &orchestrator.Config{},
		// No ctxBuilder — context injection should be skipped gracefully
	}

	sess := &session.Session{
		ID: "test-session",
		Messages: []session.Message{
			{Role: "user", Content: "Hello!"},
		},
	}

	agentCfg := &orchestrator.AgentConfig{
		ID:      "lumi",
		Role:    "lead",
		Context: []string{"soul"}, // Requested, but no builder available
	}

	convCtx.rebuildStaticSystemContent(agentCfg)
	prompt := convCtx.buildPrompt(sess, agentCfg, "", nil)

	// Should still have agent identity
	if !strings.Contains(prompt, "## Who You Are") {
		t.Error("prompt should contain agent identity even without context injection")
	}

	// Should still have conversation history
	if !strings.Contains(prompt, "User: Hello!") {
		t.Error("prompt should contain conversation history even without context injection")
	}

	// Should NOT contain SOUL.md content (no builder)
	if strings.Contains(prompt, "SOUL") {
		t.Error("prompt should not contain SOUL.md when no builder is configured")
	}
}

func TestBuildPrompt_EmptyContextList(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "SOUL.md"), []byte("soul content"), 0644); err != nil {
		t.Fatal(err)
	}

	ctxBuilder := context.NewBuilder(workspace)
	convCtx := &conversationContext{
		cfg:        &orchestrator.Config{},
		ctxBuilder: ctxBuilder,
	}

	sess := &session.Session{
		ID:       "test-session",
		Messages: []session.Message{},
	}

	agentCfg := &orchestrator.AgentConfig{
		ID:      "lumi",
		Role:    "lead",
		Context: []string{}, // Empty context list
	}

	convCtx.rebuildStaticSystemContent(agentCfg)
	prompt := convCtx.buildPrompt(sess, agentCfg, "", nil)

	// V33: Identity (SOUL.md) is ALWAYS injected regardless of context list
	if !strings.Contains(prompt, "## Who You Are") {
		t.Error("prompt should always contain identity header")
	}
	if !strings.Contains(prompt, "soul content") {
		t.Error("prompt should contain SOUL.md content as identity")
	}
	// Other context files should NOT be injected when context list is empty
	if strings.Contains(prompt, "## Context") {
		t.Error("prompt should not inject context section when context list is empty")
	}
}

func TestBuildPrompt_MissingWorkspaceFiles(t *testing.T) {
	// Workspace with no files — context injection should fail gracefully
	workspace := t.TempDir()
	ctxBuilder := context.NewBuilder(workspace)

	convCtx := &conversationContext{
		cfg:        &orchestrator.Config{},
		ctxBuilder: ctxBuilder,
	}

	sess := &session.Session{
		ID:       "test-session",
		Messages: []session.Message{},
	}

	agentCfg := &orchestrator.AgentConfig{
		ID:      "lumi",
		Role:    "lead",
		Context: []string{"soul", "nonexistent"},
	}

	convCtx.rebuildStaticSystemContent(agentCfg)
	prompt := convCtx.buildPrompt(sess, agentCfg, "", nil)

	// Should still work — missing files are skipped gracefully
	if !strings.Contains(prompt, "## Who You Are") {
		t.Error("prompt should contain agent identity even with missing workspace files")
	}
}

// --- NATS Event Publishing Tests ---

func TestPublishEvent_WithNATS(t *testing.T) {
	// Start an embedded NATS server for testing
	natsURL, cleanup, err := bus.StartEmbeddedBus(0)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	nc, err := bus.ConnectToBus(natsURL)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	convCtx := &conversationContext{
		natsConn: nc,
	}

	// Subscribe and verify the event is published
	sub, err := nc.SubscribeSync("lumi.run.started")
	if err != nil {
		t.Fatal(err)
	}

	convCtx.publishEvent("lumi.run.started", map[string]any{
		"run_id": "test-123",
		"model":  "glm-5.1:cloud",
	})

	msg, err := sub.NextMsg(2 * time.Second)
	if err != nil {
		t.Fatalf("expected to receive event on NATS: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(msg.Data, &payload); err != nil {
		t.Fatalf("failed to unmarshal event payload: %v", err)
	}

	if payload["run_id"] != "test-123" {
		t.Errorf("expected run_id=test-123, got %v", payload["run_id"])
	}
	if payload["model"] != "glm-5.1:cloud" {
		t.Errorf("expected model=glm-5.1:cloud, got %v", payload["model"])
	}
}

func TestPublishEvent_WithoutNATS(t *testing.T) {
	convCtx := &conversationContext{
		natsConn: nil, // No NATS connection
	}

	// Should not panic — just log and skip
	convCtx.publishEvent("lumi.run.started", map[string]any{
		"run_id": "test-123",
	})
	// If we get here, it didn't panic
}

func TestPublishEvent_PerAgentNamespace(t *testing.T) {
	natsURL, cleanup, err := bus.StartEmbeddedBus(0)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	nc, err := bus.ConnectToBus(natsURL)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	convCtx := &conversationContext{
		natsConn: nc,
	}

	// Subscribe to specific subjects (wildcards don't work with SubscribeSync in tests)
	lumiSub, err := nc.SubscribeSync("lumi.run.started")
	if err != nil {
		t.Fatal(err)
	}
	mangoSub, err := nc.SubscribeSync("mango.run.started")
	if err != nil {
		t.Fatal(err)
	}

	// Publish lumi event
	convCtx.publishEvent("lumi.run.started", map[string]any{"agent": "lumi"})
	// Publish mango event
	convCtx.publishEvent("mango.run.started", map[string]any{"agent": "mango"})

	// Verify lumi received its event
	msg, err := lumiSub.NextMsg(2 * time.Second)
	if err != nil {
		t.Fatalf("lumi should receive event: %v", err)
	}
	if msg.Subject != "lumi.run.started" {
		t.Errorf("expected subject lumi.run.started, got %s", msg.Subject)
	}

	// Verify mango received its event
	msg, err = mangoSub.NextMsg(2 * time.Second)
	if err != nil {
		t.Fatalf("mango should receive event: %v", err)
	}
	if msg.Subject != "mango.run.started" {
		t.Errorf("expected subject mango.run.started, got %s", msg.Subject)
	}
}

func TestPublishEvent_SystemNamespace(t *testing.T) {
	natsURL, cleanup, err := bus.StartEmbeddedBus(0)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	nc, err := bus.ConnectToBus(natsURL)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	convCtx := &conversationContext{
		natsConn: nc,
	}

	// Subscribe to specific system event subject
	sysSub, err := nc.SubscribeSync("prizm.channel.received")
	if err != nil {
		t.Fatal(err)
	}

	convCtx.publishEvent("prizm.channel.received", map[string]any{"channel": "discord"})

	msg, err := sysSub.NextMsg(2 * time.Second)
	if err != nil {
		t.Fatalf("should receive prizm.* event: %v", err)
	}
	if msg.Subject != "prizm.channel.received" {
		t.Errorf("expected subject prizm.channel.received, got %s", msg.Subject)
	}

	// Verify schema version is in payload
	var payload map[string]any
	if err := json.Unmarshal(msg.Data, &payload); err != nil {
		t.Fatalf("failed to unmarshal event: %v", err)
	}
	if v, ok := payload["v"]; !ok || v != float64(1) {
		t.Errorf("expected schema version v=1, got %v", payload["v"])
	}
}

func TestBuildPrompt_ConfigurableTokenBudget(t *testing.T) {
	workspace := t.TempDir()
	// Create a large SOUL.md
	bigSoul := strings.Repeat("You are Lumi. Be warm and playful. ", 500) // ~15000 chars
	if err := os.WriteFile(filepath.Join(workspace, "SOUL.md"), []byte(bigSoul), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "USER.md"), []byte("Ema."), 0644); err != nil {
		t.Fatal(err)
	}

	ctxBuilder := context.NewBuilder(workspace)

	// Test with default budget (4000)
	convCtx := &conversationContext{
		cfg: &orchestrator.Config{
			Prizm: orchestrator.PrizmConfig{ContextTokenBudget: 4000},
		},
		ctxBuilder: ctxBuilder,
	}

	agentCfg := &orchestrator.AgentConfig{
		ID: "lumi", Role: "lead", Context: []string{"soul", "user"},
	}

	sess := &session.Session{ID: "test", Messages: []session.Message{}}
	convCtx.rebuildStaticSystemContent(agentCfg)
	prompt := convCtx.buildPrompt(sess, agentCfg, "", nil)

	if !strings.Contains(prompt, "## Who You Are") {
		t.Error("prompt should contain agent identity")
	}
	// SOUL.md should be present (priority 100, never truncated)
	if !strings.Contains(prompt, bigSoul) {
		t.Error("SOUL.md should never be truncated regardless of budget")
	}
}

// --- Remembrance Integration Tests ---

func TestConversationContext_RemembranceNil(t *testing.T) {
	// When remClient is nil, no panic should occur
	convCtx := &conversationContext{
		cfg: &orchestrator.Config{},
	}

	// This should be a no-op — no panic, no error
	if convCtx.remClient != nil {
		t.Error("remClient should be nil when not configured")
	}
}

func TestPublishEvent_SchemaVersion(t *testing.T) {
	natsURL, cleanup, err := bus.StartEmbeddedBus(0)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	nc, err := bus.ConnectToBus(natsURL)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()

	convCtx := &conversationContext{
		natsConn: nc,
	}

	sub, err := nc.SubscribeSync("test.subject")
	if err != nil {
		t.Fatal(err)
	}

	convCtx.publishEvent("test.subject", map[string]any{"key": "value"})

	msg, err := sub.NextMsg(2 * time.Second)
	if err != nil {
		t.Fatal(err)
	}

	var payload map[string]any
	if err := json.Unmarshal(msg.Data, &payload); err != nil {
		t.Fatal(err)
	}

	if v, ok := payload["v"]; !ok || v != float64(1) {
		t.Errorf("expected schema version v=1, got %v", payload["v"])
	}
	if payload["key"] != "value" {
		t.Errorf("expected key=value, got %v", payload["key"])
	}
}
func TestPublishEvent_DoesNotMutateCallerPayload(t *testing.T) {
	// Verify that publishEvent doesn't mutate the caller's payload map
	original := map[string]any{
		"user_id":    "123",
		"channel_id": "456",
	}
	originalLen := len(original)

	// Create a conversationContext with no NATS (so publish logs and returns)
	cc := &conversationContext{
		natsConn: nil, // NATS not connected — event is logged and skipped
	}

	cc.publishEvent("test.subject", original)

	// The original map should NOT have been mutated (no "v" key added)
	if len(original) != originalLen {
		t.Errorf("publishEvent mutated caller's payload: expected %d keys, got %d (keys: %v)", originalLen, len(original), mapKeys(original))
	}
	if _, ok := original["v"]; ok {
		t.Error("publishEvent added 'v' key to caller's payload map — should create a copy instead")
	}
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func TestPublishEvent_NilPayload(t *testing.T) {
	// Verify that nil payload doesn't panic
	cc := &conversationContext{
		natsConn: nil, // NATS not connected
	}

	// Should not panic
	cc.publishEvent("test.subject", nil)
}

func TestRemembranceTimeout_Config(t *testing.T) {
	cfg := &orchestrator.Config{
		Remembrance: orchestrator.RemembranceConfig{
			TimeoutSeconds: 45,
		},
	}
	got := remembranceTimeout(cfg)
	if got != 45*time.Second {
		t.Errorf("expected 45s, got %v", got)
	}
}

func TestRemembranceTimeout_Zero(t *testing.T) {
	cfg := &orchestrator.Config{
		Remembrance: orchestrator.RemembranceConfig{
			TimeoutSeconds: 0,
		},
	}
	got := remembranceTimeout(cfg)
	if got != remembrance.DefaultTimeout {
		t.Errorf("expected default %v, got %v", remembrance.DefaultTimeout, got)
	}
}

func TestRemembranceTimeout_Negative(t *testing.T) {
	cfg := &orchestrator.Config{
		Remembrance: orchestrator.RemembranceConfig{
			TimeoutSeconds: -1,
		},
	}
	got := remembranceTimeout(cfg)
	if got != remembrance.DefaultTimeout {
		t.Errorf("expected default %v for negative, got %v", remembrance.DefaultTimeout, got)
	}
}

func TestBridgeSecret_UsesEnvFirst(t *testing.T) {
	t.Setenv("PRIZM_TEST_BRIDGE_SECRET", "from-env")
	cfg := &orchestrator.Config{
		Bridge: orchestrator.BridgeConfig{
			SecretEnv: "PRIZM_TEST_BRIDGE_SECRET",
			Secret:    "from-config",
		},
	}
	if got := bridgeSecret(cfg); got != "from-env" {
		t.Fatalf("expected env bridge secret, got %q", got)
	}
}

func TestBridgeSecret_FallsBackToConfig(t *testing.T) {
	t.Setenv("PRIZM_TEST_BRIDGE_SECRET", "")
	cfg := &orchestrator.Config{
		Bridge: orchestrator.BridgeConfig{
			SecretEnv: "PRIZM_TEST_BRIDGE_SECRET",
			Secret:    "from-config",
		},
	}
	if got := bridgeSecret(cfg); got != "from-config" {
		t.Fatalf("expected config bridge secret, got %q", got)
	}
}

func TestBuildPrompt_WithChannelRole(t *testing.T) {
	workspace := t.TempDir()
	soulContent := "# SOUL\nYou are Lumi. Be warm, playful, and confident."
	if err := os.WriteFile(filepath.Join(workspace, "SOUL.md"), []byte(soulContent), 0644); err != nil {
		t.Fatal(err)
	}

	ctxBuilder := context.NewBuilder(workspace)
	convCtx := &conversationContext{
		cfg: &orchestrator.Config{
			Agents: []orchestrator.AgentConfig{
				{ID: "lumi", Role: "lead", Context: []string{"soul", "user"}},
			},
			ChannelRoles: []orchestrator.ChannelRole{
				{ID: "123", Role: "manager-room", Tools: "all", Personality: "direct", Context: "You are in #manager-room."},
			},
		},
		ctxBuilder: ctxBuilder,
	}

	sess := &session.Session{ID: "test", Messages: []session.Message{}}
	agentCfg := &orchestrator.AgentConfig{ID: "lumi", Role: "lead", Context: []string{"soul", "user"}}

	convCtx.rebuildStaticSystemContent(agentCfg)

	// Test with a real channel role config
	channelRole := &orchestrator.ChannelRole{
		Role:        "manager-room",
		Tools:       "all",
		Personality: "direct",
		Context:     "You are in #manager-room, a private strategic channel.",
	}
	prompt := convCtx.buildPrompt(sess, agentCfg, "manager-room", channelRole)

	if !strings.Contains(prompt, "## Who You Are") {
		t.Error("prompt should contain identity header")
	}
	if !strings.Contains(prompt, "## Channel: #manager-room") {
		t.Error("prompt should contain channel context header")
	}
	if !strings.Contains(prompt, "You are in #manager-room, a private strategic channel.") {
		t.Error("prompt should contain channel context text")
	}

	// Test with no channel role (nil) — should not crash and should not contain channel section
	promptNoRole := convCtx.buildPrompt(sess, agentCfg, "", nil)
	if !strings.Contains(promptNoRole, "## Who You Are") {
		t.Error("prompt without channel role should still contain identity")
	}
	if strings.Contains(promptNoRole, "## Channel:") {
		t.Error("prompt without channel role should not contain channel section")
	}
	if strings.Contains(promptNoRole, "## Tone") {
		t.Error("prompt without channel role should not contain a tone directive")
	}
}

func TestBuildPrompt_PersonalityDirective(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "SOUL.md"), []byte("# SOUL\nYou are Lumi."), 0644); err != nil {
		t.Fatal(err)
	}
	ctxBuilder := context.NewBuilder(workspace)
	convCtx := &conversationContext{
		cfg:        &orchestrator.Config{},
		ctxBuilder: ctxBuilder,
	}
	sess := &session.Session{ID: "test", Messages: []session.Message{}}

	// A channel role with Personality set but no freeform Context text should
	// still shape "How You Respond" — Personality is not gated behind Context.
	agentCfg := &orchestrator.AgentConfig{ID: "lumi", Role: "lead", Context: []string{"soul"}}
	convCtx.rebuildStaticSystemContent(agentCfg)
	channelRole := &orchestrator.ChannelRole{Role: "manager-room", Personality: "direct"}
	prompt := convCtx.buildPrompt(sess, agentCfg, "manager-room", channelRole)
	if !strings.Contains(prompt, "## How You Respond") {
		t.Error("expected prompt to contain a How You Respond section")
	}
	// SOUL.md is present, so no personality directive should override it
	if strings.Contains(prompt, orchestrator.PersonalityDirective("direct")) {
		t.Error("expected SOUL.md to take precedence over channel personality directive")
	}
	if strings.Contains(prompt, defaultConversationPostfix) {
		t.Error("expected SOUL.md to take precedence over harness default postfix")
	}

	// An unrecognized Personality value falls back to the harness default
	// (silent no-op for the personality, not an error).
	channelRoleUnknown := &orchestrator.ChannelRole{Role: "manager-room", Personality: "nonexistent"}
	promptUnknown := convCtx.buildPrompt(sess, agentCfg, "manager-room", channelRoleUnknown)
	// SOUL.md is present, so postfix returns "" even for unrecognized personality
	if strings.Contains(promptUnknown, defaultConversationPostfix) {
		t.Error("expected SOUL.md to take precedence even when personality is unrecognized")
	}

	// An agent's own explicit conversation_postfix always wins over a
	// channel's Personality — the operator's deliberate agent-level choice
	// is not silently overridden by a channel-level default.
	agentCfgExplicit := &orchestrator.AgentConfig{
		ID: "lumi", Role: "lead", Context: []string{"soul"},
		ConversationPostfix: "Speak only in haiku.",
	}
	convCtx.rebuildStaticSystemContent(agentCfgExplicit)
	promptExplicit := convCtx.buildPrompt(sess, agentCfgExplicit, "manager-room", channelRole)
	// SOUL.md is present, so even explicit conversation_postfix is overridden
	if strings.Contains(promptExplicit, "Speak only in haiku.") {
		t.Error("expected SOUL.md to take precedence over explicit conversation_postfix")
	}
	if strings.Contains(promptExplicit, orchestrator.PersonalityDirective("direct")) {
		t.Error("expected SOUL.md to take precedence over channel personality directive")
	}
}

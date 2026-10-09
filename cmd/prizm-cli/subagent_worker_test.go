package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/subagent"
	"github.com/emaharmony/prizm/internal/tool"
	v2 "github.com/emaharmony/prizm/internal/workflow/v2"
)

type scopedProbeProvider struct {
	scope provider.RunScope
}

type unscopedProbeProvider struct{}

type chatProbeProvider struct {
	captured provider.ChatGenerateRequest
}

func (p *chatProbeProvider) Generate(context.Context, provider.GenerateRequest) (provider.GenerateResponse, error) {
	return provider.GenerateResponse{}, fmt.Errorf("text generation must not be used when chat is available")
}

func (p *chatProbeProvider) ChatGenerate(_ context.Context, request provider.ChatGenerateRequest) (provider.ChatGenerateResponse, error) {
	p.captured = request
	return provider.ChatGenerateResponse{
		Content:      `{"schema_version":1,"understanding":"done"}`,
		PromptTokens: 11,
		OutputTokens: 7,
	}, nil
}

func (unscopedProbeProvider) Generate(context.Context, provider.GenerateRequest) (provider.GenerateResponse, error) {
	return provider.GenerateResponse{}, nil
}

func (unscopedProbeProvider) UsesNativeTools() bool { return true }

func (p *scopedProbeProvider) Generate(context.Context, provider.GenerateRequest) (provider.GenerateResponse, error) {
	return provider.GenerateResponse{}, fmt.Errorf("unscoped generation must not be used")
}

func (p *scopedProbeProvider) GenerateInRunScope(_ context.Context, _ provider.GenerateRequest, scope provider.RunScope) (provider.GenerateResponse, error) {
	p.scope = scope
	return provider.GenerateResponse{Text: `{"type":"final","content":"done"}`}, nil
}

func TestSubAgentResolver_MapsAgents(t *testing.T) {
	cfg := &orchestrator.Config{
		Agents: []orchestrator.AgentConfig{
			{ID: "scout", Provider: "ollama", Model: "qwen3.5:9b", Capabilities: []string{"search", "report"}},
			{ID: "atlas", Provider: "ollama", Model: "deepseek-v4-pro:cloud", Capabilities: []string{"code"}},
		},
	}
	r := newSubAgentResolver(cfg)

	rt, ok := r.Resolve("scout")
	if !ok {
		t.Fatal("scout should resolve")
	}
	if rt.Model != "qwen3.5:9b" || rt.Provider != "ollama" {
		t.Errorf("wrong runtime: %+v", rt)
	}
	if len(rt.Capabilities) != 2 {
		t.Errorf("capabilities not copied: %+v", rt.Capabilities)
	}
	// Mutating the resolved copy must not affect config (defensive copy).
	rt.Capabilities[0] = "MUT"
	if cfg.Agents[0].Capabilities[0] == "MUT" {
		t.Error("resolver aliased the config slice")
	}

	if _, ok := r.Resolve("ghost"); ok {
		t.Error("unknown agent should not resolve")
	}
}

// executorFor must root the file/builtin tools at the worktree (V58 4d), so a
// sub-agent's reads/writes are isolated to its own worktree, not the shared root.
func TestSubAgentBackend_ExecutorRootedAtWorktree(t *testing.T) {
	// Shared executor rooted at a DIFFERENT dir.
	sharedRoot := t.TempDir()
	sharedReg := tool.NewRegistry()
	tool.RegisterBuiltinsWithRoots(sharedReg, sharedRoot, subAgentWorktreeMaxFileSize, []string{sharedRoot}, []string{sharedRoot})
	defaultPolicy := tool.DefaultPolicyConfig()
	sharedExec := tool.NewExecutor(sharedReg, &defaultPolicy)
	b := &subAgentBackend{exec: sharedExec, toolReg: sharedReg}

	// A worktree with a marker file only it contains.
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "MARKER.txt"), []byte("in-worktree\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ex := b.executorFor(workDir)
	if ex == sharedExec {
		t.Fatal("expected a distinct per-worktree executor")
	}
	// list_dir "." on the worktree executor must see the worktree's file.
	res, err := ex.ExecuteWithPolicy(context.Background(), "list_dir", "scout", "subagent", "T", map[string]any{"path": "."})
	if err != nil {
		t.Fatalf("list_dir: %v", err)
	}
	if !res.Success {
		t.Fatalf("list_dir failed: %s", res.Error)
	}
	out := fmt.Sprintf("%v", res.Output)
	if !strings.Contains(out, "MARKER.txt") {
		t.Errorf("worktree executor not rooted at worktree; listing = %s", out)
	}

	// Empty workDir → shared executor (no isolation).
	if b.executorFor("") != sharedExec {
		t.Error("empty workDir should return the shared executor")
	}
}

func TestSubAgentBackendCodexUsesReadOnlyRunScope(t *testing.T) {
	repo := t.TempDir()
	worktreeRoot := filepath.Join(repo, ".prizm", "worktrees")
	workDir := filepath.Join(worktreeRoot, "run-1")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	probe := &scopedProbeProvider{}
	registry := provider.NewProviderRegistry()
	registry.Register("codex", probe, provider.ModelInfo{ProviderName: "codex"})
	backend := &subAgentBackend{providers: registry, worktreeRoot: worktreeRoot}
	llm, _, _, err := backend.Bind(subagent.AgentRuntime{AgentID: "developer", Provider: "codex", Model: "codex", WorkDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = llm(context.Background(), []v2.Message{{Content: "bounded task"}}); err != nil {
		t.Fatal(err)
	}
	if probe.scope.Workspace != workDir || !probe.scope.ReadOnly {
		t.Fatalf("codex scope=%#v", probe.scope)
	}
}

func TestSubAgentBackendUsesChatProviderForStructuredRoleOutput(t *testing.T) {
	probe := &chatProbeProvider{}
	registry := provider.NewProviderRegistry()
	registry.Register("glm-5.3:cloud", probe, provider.ModelInfo{ProviderName: "ollama"})
	backend := &subAgentBackend{providers: registry}
	llm, _, _, err := backend.Bind(subagent.AgentRuntime{AgentID: "planner", Provider: "ollama", Model: "glm-5.3:cloud"})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := llm(context.Background(), []v2.Message{
		{Role: "system", Content: "role contract"},
		{Role: "user", Content: "bounded task"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Text != `{"schema_version":1,"understanding":"done"}` || turn.PromptTokens != 11 || turn.CompletionTokens != 7 {
		t.Fatalf("turn=%#v", turn)
	}
	if probe.captured.Model != "glm-5.3:cloud" || probe.captured.Agent != "planner" || probe.captured.MaxTokens != 4096 {
		t.Fatalf("request=%#v", probe.captured)
	}
	if len(probe.captured.Messages) != 2 || probe.captured.Messages[1].Content != "bounded task" {
		t.Fatalf("messages=%#v", probe.captured.Messages)
	}
}

func TestSubAgentBackendRejectsUnscopedCodexProvider(t *testing.T) {
	registry := provider.NewProviderRegistry()
	registry.Register("codex", &scopedProbeProvider{}, provider.ModelInfo{ProviderName: "codex"})
	// Register a provider that satisfies only the base interface.
	registry.Register("unscoped", unscopedProbeProvider{}, provider.ModelInfo{ProviderName: "codex"})
	repo := t.TempDir()
	worktreeRoot := filepath.Join(repo, ".prizm", "worktrees")
	workDir := filepath.Join(worktreeRoot, "run-1")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	backend := &subAgentBackend{providers: registry, worktreeRoot: worktreeRoot}
	llm, _, _, err := backend.Bind(subagent.AgentRuntime{AgentID: "developer", Provider: "alias", Model: "unscoped", WorkDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = llm(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "cannot guarantee") {
		t.Fatalf("unscoped codex err=%v", err)
	}
}

func TestSubAgentBackendRejectsScopedProviderOutsideOwnedWorktree(t *testing.T) {
	registry := provider.NewProviderRegistry()
	registry.Register("scoped", &scopedProbeProvider{}, provider.ModelInfo{ProviderName: "alias"})
	backend := &subAgentBackend{providers: registry, worktreeRoot: filepath.Join(t.TempDir(), ".prizm", "worktrees")}
	llm, _, _, err := backend.Bind(subagent.AgentRuntime{AgentID: "developer", Provider: "alias", Model: "scoped", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = llm(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("outside worktree err=%v", err)
	}
}

func TestParseSubAgentActionAcceptsStrictRoleJSON(t *testing.T) {
	input := `{"schema_version":1,"understanding":"task","implementation_plan":["change"]}`
	action := parseSubAgentAction(input)
	if !action.Final || action.Content != input || action.Tool != "" {
		t.Fatalf("action = %#v", action)
	}
	toolAction := parseSubAgentAction(`{"type":"tool_request","tool":"read_file","input":{"path":"README.md"}}`)
	if toolAction.Final || toolAction.Tool != "read_file" {
		t.Fatalf("tool action = %#v", toolAction)
	}
}

func TestParseSubAgentActionPrioritizesToolRequestOverFinal(t *testing.T) {
	input := `{"type":"final","content":"premature"}\n{"type":"tool_request","tool":"write_file_proposal","input":{"path":"feature.txt","content":"approved"}}`
	action := parseSubAgentAction(input)
	if action.Final || action.Tool != "write_file_proposal" || action.Input["path"] != "feature.txt" {
		t.Fatalf("action = %#v", action)
	}
}

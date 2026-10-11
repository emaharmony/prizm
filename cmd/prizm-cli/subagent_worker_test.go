package main

import (
	"context"
	"encoding/json"
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
	response provider.ChatGenerateResponse
}

func (p *chatProbeProvider) Generate(context.Context, provider.GenerateRequest) (provider.GenerateResponse, error) {
	return provider.GenerateResponse{}, fmt.Errorf("text generation must not be used when chat is available")
}

func (p *chatProbeProvider) ChatGenerate(_ context.Context, request provider.ChatGenerateRequest) (provider.ChatGenerateResponse, error) {
	p.captured = request
	if p.response.OutputTokens != 0 || p.response.Content != "" {
		return p.response, nil
	}
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
	llm, _, _, err := backend.Bind(subagent.AgentRuntime{AgentID: "planner", Provider: "ollama", Model: "glm-5.3:cloud", ReasoningEffort: "max"})
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
	if probe.captured.Model != "glm-5.3:cloud" || probe.captured.Agent != "planner" || probe.captured.MaxTokens != 8192 || probe.captured.ReasoningEffort != "max" {
		t.Fatalf("request=%#v", probe.captured)
	}
	if len(probe.captured.Messages) != 2 || probe.captured.Messages[1].Content != "bounded task" {
		t.Fatalf("messages=%#v", probe.captured.Messages)
	}
}

func TestSubAgentBackendRejectsReplyCapConsumedWithoutContent(t *testing.T) {
	probe := &chatProbeProvider{response: provider.ChatGenerateResponse{OutputTokens: 8192}}
	registry := provider.NewProviderRegistry()
	registry.Register("glm-5.3:cloud", probe, provider.ModelInfo{ProviderName: "ollama"})
	backend := &subAgentBackend{providers: registry}
	llm, _, _, err := backend.Bind(subagent.AgentRuntime{AgentID: "developer", Provider: "ollama", Model: "glm-5.3:cloud"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = llm(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "8192-token reply allowance") {
		t.Fatalf("empty capped reply err=%v", err)
	}
}

func TestSubAgentReplyMaxTokensLeavesOtherModelsUnchanged(t *testing.T) {
	if got := subAgentReplyMaxTokens("qwen3.5:9b"); got != 4096 {
		t.Fatalf("qwen reply max = %d, want 4096", got)
	}
}

func TestSubAgentReasoningEffortDefaultsGLMLowOnly(t *testing.T) {
	if got := subAgentReasoningEffort("glm-5.3:cloud", ""); got != "low" {
		t.Fatalf("GLM default reasoning effort = %q, want low", got)
	}
	if got := subAgentReasoningEffort("glm-5.3:cloud", "high"); got != "high" {
		t.Fatalf("configured GLM reasoning effort = %q, want high", got)
	}
	if got := subAgentReasoningEffort("qwen3.5:9b", ""); got != "" {
		t.Fatalf("other model default reasoning effort = %q, want empty", got)
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

func TestParseSubAgentActionRejectsEmptyOrMalformedFinalEnvelope(t *testing.T) {
	for _, input := range []string{
		`{"type":"final","content":""}`,
		`analysis mentions {"type":"final","content":`,
	} {
		action := parseSubAgentAction(input)
		if action.Final || action.Tool != "" {
			t.Fatalf("input %q produced action %#v", input, action)
		}
	}
}

func TestParseSubAgentActionAcceptsReorderedCanonicalFinal(t *testing.T) {
	input := "{\n  \"content\": \"done\",\n  \"type\": \"final\"\n}"
	action := parseSubAgentAction(input)
	if !action.Final || action.Tool != "" || action.Content != "done" {
		t.Fatalf("action = %#v", action)
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
	fenced := parseSubAgentAction("```json\n{\"type\":\"final\",\"content\":\"done\"}\n```")
	if fenced.Final || fenced.Tool != "" {
		t.Fatalf("fenced final must be rejected: %#v", fenced)
	}
	roleWithMarker := `{"schema_version":1,"summary":"example contains \"type\":\"final\" text"}`
	action = parseSubAgentAction(roleWithMarker)
	if !action.Final || action.Content != roleWithMarker {
		t.Fatalf("direct role JSON with marker text = %#v", action)
	}
	roleWithToolJSON := `{"schema_version":1,"summary":"example contains {\"type\":\"tool_request\",\"tool\":\"apply_patch_proposal\"}"}`
	action = parseSubAgentAction(roleWithToolJSON)
	if !action.Final || action.Tool != "" || action.Content != roleWithToolJSON {
		t.Fatalf("direct role JSON with embedded tool JSON = %#v", action)
	}
}

func TestParseSubAgentActionPrioritizesToolRequestOverFinal(t *testing.T) {
	input := `{"type":"final","content":"premature"}\n{"type":"tool_request","tool":"write_file_proposal","input":{"path":"feature.txt","content":"approved"}}`
	action := parseSubAgentAction(input)
	if action.Final || action.Tool != "" {
		t.Fatalf("action = %#v", action)
	}
}

func TestParseSubAgentActionRejectsEmbeddedEnvelopes(t *testing.T) {
	inputs := []string{
		`prose {"type":"final","content":"premature"}`,
		"prose {\n  \"input\": {\"path\": \"README.md\"},\n  \"tool\": \"read_file\",\n  \"type\": \"tool_request\"\n}\n{\"type\":\"final\",\"content\":\"premature\"}",
		"```json\n{\"type\":\"tool_request\",\"tool\":\"read_file\",\"input\":{\"path\":\"README.md\"}}\n```",
	}
	for _, input := range inputs {
		action := parseSubAgentAction(input)
		if action.Final || action.Tool != "" {
			t.Fatalf("input %q produced action %#v", input, action)
		}
	}
}

func TestParseSubAgentActionAcceptsPrettyAtomicPatchEnvelope(t *testing.T) {
	patch := "diff --git a/a.go b/a.go\n@@ -1 +1 @@\n-func old() {}\n+func new() { println(`{ok}`) }\n"
	encodedPatch, err := json.Marshal(patch)
	if err != nil {
		t.Fatal(err)
	}
	input := "{\n  \"input\": {\"patch\": " + string(encodedPatch) + ", \"base_sha\": \"6de4579\"},\n  \"tool\": \"apply_patch_proposal\",\n  \"type\": \"tool_request\"\n}"
	action := parseSubAgentAction(input)
	if action.Final || action.Tool != "apply_patch_proposal" || action.Input["patch"] != patch || action.Input["base_sha"] != "6de4579" {
		t.Fatalf("action = %#v", action)
	}
}

func TestParseSubAgentActionRejectsTypedRoleAndMalformedAtomicPatch(t *testing.T) {
	inputs := []string{
		`{"type":"developer_result","summary":"done"}`,
		`{"type":"tool_request","tool":"apply_patch_proposal","input":{"patch":"diff"}}`,
		`{"type":"tool_request","tool":"apply_patch_proposal","input":{"patch":[],"base_sha":"abc"}}`,
		`{"type":"tool_request","tool":"apply_patch_proposal","input":{"patch":"diff","base_sha":"abc","extra":"no"}}`,
	}
	for _, input := range inputs {
		action := parseSubAgentAction(input)
		if action.Final || action.Tool != "" {
			t.Fatalf("input %s produced action %#v", input, action)
		}
	}
}

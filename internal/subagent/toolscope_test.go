package subagent

import (
	"context"
	"strings"
	"testing"

	v2 "github.com/emaharmony/prizm/internal/workflow/v2"
)

func TestCapabilityToolScope(t *testing.T) {
	coder := AgentRuntime{AgentID: "atlas", Capabilities: []string{"code", "test", "report"}}
	researcher := AgentRuntime{AgentID: "scout", Capabilities: []string{"search", "summarize", "report"}}
	scope := DefaultToolScope()

	// Read-only / research tools: allowed for everyone.
	for _, tool := range []string{"read_file", "web_search", "fetch_image", "generate_image", "analyze_image", "collect_reference_images", "git_status", "memory_search"} {
		if !scope.Allowed(researcher, tool) {
			t.Errorf("researcher should be allowed %q", tool)
		}
	}
	// Mutation / git-write: coder yes, researcher no.
	for _, tool := range []string{"write_file", "git_commit", "git_push", "create_directory"} {
		if !scope.Allowed(coder, tool) {
			t.Errorf("coder should be allowed %q", tool)
		}
		if scope.Allowed(researcher, tool) {
			t.Errorf("researcher must NOT be allowed %q", tool)
		}
	}
	// MCP build tools: require "code".
	if !scope.Allowed(coder, "mcp_blender_export") {
		t.Error("coder should be allowed mcp_blender_export")
	}
	if scope.Allowed(researcher, "mcp_blender_export") {
		t.Error("researcher must NOT be allowed mcp_blender_export")
	}
}

func TestCapabilityToolScope_ExplicitRoleAllowlist(t *testing.T) {
	scope := DefaultToolScope()
	runtime := AgentRuntime{
		AgentID:             "atlas",
		Capabilities:        []string{"code"},
		AllowedTools:        []string{"read_file"},
		EnforceAllowedTools: true,
	}
	if !scope.Allowed(runtime, "read_file") {
		t.Error("explicitly allowlisted read_file was denied")
	}
	if scope.Allowed(runtime, "git_commit") {
		t.Error("capability must not widen the explicit role allowlist")
	}
	runtime.AllowedTools = nil
	if scope.Allowed(runtime, "read_file") {
		t.Error("an enforced empty allowlist must deny every tool")
	}
}

// The runner must NOT execute a scoped-out tool, and must feed the denial back.
func TestLoopRunner_ToolScopeDeniesExecution(t *testing.T) {
	var executed []string
	backend := &scopeBackend{
		parse: func(text string) Action {
			if strings.HasPrefix(text, "WRITE") {
				return Action{Tool: "git_push", Input: map[string]any{}}
			}
			return lineParser(text)
		},
		turns: []Turn{
			{Text: "WRITE"}, // researcher tries git_push (out of role)
			{Text: "FINAL: could not push, out of role"},
		},
		onExec: func(tool string) { executed = append(executed, tool) },
	}
	r := NewLoopRunner(LoopRunnerConfig{Backend: backend, Scope: DefaultToolScope()})

	res, err := r.Run(context.Background(),
		v2.TaskPacket{TaskID: "S1"},
		AgentRuntime{AgentID: "scout", Capabilities: []string{"search", "report"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(executed) != 0 {
		t.Fatalf("scoped-out tool was executed: %v", executed)
	}
	if !strings.Contains(res.Summary, "out of role") {
		t.Errorf("summary = %q", res.Summary)
	}
	if res.ToolCalls != 1 || res.DeniedToolCalls != 1 || res.Iterations != 2 {
		t.Errorf("denial telemetry = calls %d denied %d iterations %d",
			res.ToolCalls, res.DeniedToolCalls, res.Iterations)
	}
}

// With scoping, a coder CAN run the same tool.
func TestLoopRunner_ToolScopeAllowsInRole(t *testing.T) {
	var executed []string
	backend := &scopeBackend{
		parse: func(text string) Action {
			if strings.HasPrefix(text, "WRITE") {
				return Action{Tool: "git_commit", Input: map[string]any{}}
			}
			return lineParser(text)
		},
		turns: []Turn{
			{Text: "WRITE"},
			{Text: "FINAL: committed"},
		},
		onExec: func(tool string) { executed = append(executed, tool) },
	}
	r := NewLoopRunner(LoopRunnerConfig{Backend: backend, Scope: DefaultToolScope()})

	_, err := r.Run(context.Background(),
		v2.TaskPacket{TaskID: "S2"},
		AgentRuntime{AgentID: "atlas", Capabilities: []string{"code", "report"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(executed) != 1 || executed[0] != "git_commit" {
		t.Fatalf("expected git_commit to execute, got %v", executed)
	}
}

// scopeBackend is a scripted backend that records executed tools via onExec.
type scopeBackend struct {
	turns  []Turn
	parse  Parser
	onExec func(tool string)
	i      int
}

func (b *scopeBackend) Bind(_ AgentRuntime) (LLMFunc, Parser, ToolExec, error) {
	llm := func(_ context.Context, _ []v2.Message) (Turn, error) {
		if b.i >= len(b.turns) {
			return Turn{Text: "FINAL: done"}, nil
		}
		t := b.turns[b.i]
		b.i++
		return t, nil
	}
	exec := func(_ context.Context, tool string, _ map[string]any) (string, error) {
		if b.onExec != nil {
			b.onExec(tool)
		}
		return "ok", nil
	}
	return llm, b.parse, exec, nil
}

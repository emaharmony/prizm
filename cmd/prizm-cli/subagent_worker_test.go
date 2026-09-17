package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/tool"
)

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

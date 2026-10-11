package tool

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPolicyConfigRootSelectionAndWriteAgents(t *testing.T) {
	cfg := PolicyConfig{
		WorkspaceRoot:       "workspace",
		AllowedPaths:        []string{"legacy"},
		ReadRoots:           []string{"read"},
		WriteRoots:          []string{"write"},
		OrchestratorAgentID: "lead",
		WriteAgents:         map[string]bool{"delegate": true},
	}
	if got, want := cfg.AllRoots(), []string{"workspace", "legacy"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("AllRoots() = %v, want %v", got, want)
	}
	if got, want := cfg.ReadRootsAll(), []string{"workspace", "read"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadRootsAll() = %v, want %v", got, want)
	}
	if got, want := cfg.WriteRootsAll(), []string{"workspace", "write"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("WriteRootsAll() = %v, want %v", got, want)
	}
	if !cfg.CanAgentProposeWrites("lead") || !cfg.CanAgentProposeWrites("delegate") || cfg.CanAgentProposeWrites("other") {
		t.Fatal("write-agent authorization did not honor orchestrator/delegate restrictions")
	}

	legacy := PolicyConfig{AllowedPaths: []string{"legacy"}}
	if got := legacy.ReadAllowedPaths(); !reflect.DeepEqual(got, []string{"legacy"}) {
		t.Fatalf("legacy ReadAllowedPaths() = %v", got)
	}
	if got := legacy.WriteAllowedPaths(); !reflect.DeepEqual(got, []string{"legacy"}) {
		t.Fatalf("legacy WriteAllowedPaths() = %v", got)
	}
	if !legacy.CanAgentProposeWrites("any") {
		t.Fatal("legacy empty orchestrator should preserve write proposal behavior")
	}
}

func TestEvaluatePolicyAdditionalDecisions(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name     string
		cfg      PolicyConfig
		tool     string
		agent    string
		input    map[string]any
		decision PolicyDecision
	}{
		{name: "research", tool: "web_search", decision: PolicyApproved},
		{name: "skill", tool: "use_skill", decision: PolicyApproved},
		{name: "dry run", tool: "write_file_dry_run", decision: PolicyApproved},
		{name: "patch proposal", tool: "apply_patch_proposal", input: map[string]any{"patch": "diff", "base_sha": "abc"}, decision: PolicyRequiresApproval},
		{name: "unknown", tool: "unknown_tool", decision: PolicyDenied},
		{name: "mcp default", tool: "mcp_files_read", decision: PolicyRequiresApproval},
		{name: "mcp opt in", cfg: PolicyConfig{AutoApproveMCP: true}, tool: "mcp_files_read", decision: PolicyApproved},
		{name: "direct write default", tool: "write_file", decision: PolicyDenied},
		{name: "direct write opt in", cfg: PolicyConfig{AutoApproveMutations: true}, tool: "write_file", decision: PolicyApproved},
		{name: "direct directory opt in", cfg: PolicyConfig{AutoApproveMutations: true}, tool: "create_directory", decision: PolicyApproved},
		{name: "git read", tool: "git_status", decision: PolicyApproved},
		{name: "git mutation", tool: "git_commit", decision: PolicyRequiresApproval},
		{name: "git mutation opt in", cfg: PolicyConfig{AutoApproveMutations: true}, tool: "git_push", decision: PolicyApproved},
		{name: "git denied agent", cfg: PolicyConfig{OrchestratorAgentID: "lead"}, tool: "git_add", agent: "worker", decision: PolicyDenied},
		{name: "checkout approval", tool: "git_checkout", decision: PolicyRequiresApproval},
		{name: "read project", tool: "read_project", input: map[string]any{"path": "."}, decision: PolicyApproved},
		{name: "search files", tool: "search_files", input: map[string]any{"path": "."}, decision: PolicyApproved},
		{name: "project overview", tool: "project_overview", input: map[string]any{"path": "."}, decision: PolicyApproved},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			if cfg.WorkspaceRoot == "" {
				cfg.WorkspaceRoot = root
			}
			got := EvaluatePolicyForAgent(cfg, tt.tool, tt.agent, tt.input)
			if got.Decision != tt.decision {
				t.Fatalf("decision = %s (%s), want %s", got.Decision, got.Reason, tt.decision)
			}
		})
	}
}

func TestProposalValidationAdditionalErrors(t *testing.T) {
	cfg := PolicyConfig{WorkspaceRoot: t.TempDir()}
	tests := []struct {
		name  string
		tool  string
		input map[string]any
	}{
		{name: "directory missing path", tool: "create_directory_proposal", input: map[string]any{}},
		{name: "directory non-string", tool: "create_directory_proposal", input: map[string]any{"path": 1}},
		{name: "file missing content", tool: "write_file_proposal", input: map[string]any{"path": "file.txt"}},
		{name: "file non-string content", tool: "write_file_proposal", input: map[string]any{"path": "file.txt", "content": 1}},
		{name: "file oversized", tool: "write_file_proposal", input: map[string]any{"path": "file.txt", "content": strings.Repeat("x", 1024*1024+1)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EvaluatePolicy(cfg, tt.tool, tt.input); got.Decision != PolicyDenied {
				t.Fatalf("decision = %s, want denied", got.Decision)
			}
		})
	}
}

func TestRegistryMetadataAndExecutionErrors(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(&EchoTool{}); err != nil {
		t.Fatal(err)
	}
	infos := registry.ListWithDescriptions()
	if len(infos) != 1 || infos[0].Name != "echo" || infos[0].Description == "" {
		t.Fatalf("ListWithDescriptions() = %+v", infos)
	}
	if _, err := registry.Execute(context.Background(), "missing", nil); err == nil {
		t.Fatal("Execute accepted an unknown tool")
	}
}

func TestFuzzyResolutionBoundaryBranches(t *testing.T) {
	root := t.TempDir()
	tp := ToolPaths{WorkspaceRoot: root}
	if _, err := FuzzyResolvePath(tp, filepath.Join(t.TempDir(), "definitely-missing-directory")); err == nil {
		t.Fatal("missing path should not fuzzy match")
	}
	if got := walkForMatch(root, "target", "target", tp, 4, 4); got != (fuzzyMatchResult{}) {
		t.Fatalf("max-depth walk returned %+v", got)
	}
	if got := resolvePath(root, filepath.Join("nested", "..", "file")); got != filepath.Join(root, "file") {
		t.Fatalf("resolvePath() = %q", got)
	}
}

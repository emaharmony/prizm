package tool

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/emaharmony/prizm/internal/safety"
)

func TestPolicyEchoApproved(t *testing.T) {
	cfg := DefaultPolicyConfig()
	result := EvaluatePolicy(cfg, "echo", map[string]any{"text": "hello"})
	if result.Decision != PolicyApproved {
		t.Errorf("echo should be approved, got %s", result.Decision)
	}
}

func TestPolicyReferenceImageToolsApproved(t *testing.T) {
	cfg := DefaultPolicyConfig()
	for _, name := range []string{"fetch_image", "generate_image", "analyze_image", "collect_reference_images"} {
		result := EvaluatePolicy(cfg, name, map[string]any{})
		if result.Decision != PolicyApproved {
			t.Fatalf("%s should be approved research, got %s: %s", name, result.Decision, result.Reason)
		}
	}
}
func TestPolicyWriteFileDryRunApproved(t *testing.T) {
	cfg := DefaultPolicyConfig()
	result := EvaluatePolicy(cfg, "write_file_dry_run", map[string]any{
		"path":    "test.txt",
		"content": "hello",
	})
	if result.Decision != PolicyApproved {
		t.Errorf("write_file_dry_run should be approved, got %s", result.Decision)
	}
}

func TestPolicyListDirApprovedWithinRoot(t *testing.T) {
	cfg := PolicyConfig{WorkspaceRoot: ".", MaxFileSize: 1024 * 1024}
	result := EvaluatePolicy(cfg, "list_dir", map[string]any{"path": "."})
	if result.Decision != PolicyApproved {
		t.Errorf("list_dir with '.' should be approved, got %s: %s", result.Decision, result.Reason)
	}
}

func TestPolicyListDirDeniedTraversal(t *testing.T) {
	cfg := PolicyConfig{WorkspaceRoot: ".", MaxFileSize: 1024 * 1024}
	result := EvaluatePolicy(cfg, "list_dir", map[string]any{"path": "../etc"})
	if result.Decision != PolicyDenied {
		t.Errorf("list_dir with '..' path should be denied, got %s", result.Decision)
	}
}

func TestPolicyListDirDeniedAbsolutePath(t *testing.T) {
	cfg := PolicyConfig{WorkspaceRoot: "/tmp/workspace", MaxFileSize: 1024 * 1024}
	// Absolute paths that resolve outside workspace should be denied
	result := EvaluatePolicy(cfg, "list_dir", map[string]any{"path": "/etc/passwd"})
	if result.Decision != PolicyDenied {
		t.Errorf("list_dir with absolute path outside root should be denied, got %s", result.Decision)
	}
}

func TestPolicyReadFileApprovedWithinRoot(t *testing.T) {
	cfg := PolicyConfig{WorkspaceRoot: ".", MaxFileSize: 1024 * 1024}
	result := EvaluatePolicy(cfg, "read_file", map[string]any{"path": "README.md"})
	if result.Decision != PolicyApproved {
		t.Errorf("read_file with relative path should be approved, got %s: %s", result.Decision, result.Reason)
	}
}

func TestPolicyReadFileDeniedTraversal(t *testing.T) {
	cfg := PolicyConfig{WorkspaceRoot: ".", MaxFileSize: 1024 * 1024}
	result := EvaluatePolicy(cfg, "read_file", map[string]any{"path": "../../etc/passwd"})
	if result.Decision != PolicyDenied {
		t.Errorf("read_file with '..' path should be denied, got %s", result.Decision)
	}
}

func TestPolicyUnknownToolDenied(t *testing.T) {
	cfg := DefaultPolicyConfig()
	result := EvaluatePolicy(cfg, "dangerous_tool", map[string]any{})
	if result.Decision != PolicyDenied {
		t.Errorf("unknown tool should be denied, got %s", result.Decision)
	}
}

func TestPolicyShellDenied(t *testing.T) {
	cfg := DefaultPolicyConfig()
	result := EvaluatePolicy(cfg, "shell", map[string]any{"command": "rm -rf /"})
	// V60: Shell tool is now a valid tool that requires approval in gated mode.
	// The hard blocklist is enforced internally by ShellTool.Execute().
	if result.Decision != PolicyRequiresApproval {
		t.Errorf("shell tool should require approval, got %s", result.Decision)
	}
}

func TestPolicyListDirMissingPath(t *testing.T) {
	cfg := DefaultPolicyConfig()
	result := EvaluatePolicy(cfg, "list_dir", map[string]any{})
	if result.Decision != PolicyDenied {
		t.Errorf("list_dir without path should be denied, got %s", result.Decision)
	}
}

func TestPolicyListDirNonStringPath(t *testing.T) {
	cfg := DefaultPolicyConfig()
	result := EvaluatePolicy(cfg, "list_dir", map[string]any{"path": 123})
	if result.Decision != PolicyDenied {
		t.Errorf("list_dir with non-string path should be denied, got %s", result.Decision)
	}
}

func TestPolicyReadFileMissingPath(t *testing.T) {
	cfg := DefaultPolicyConfig()
	result := EvaluatePolicy(cfg, "read_file", map[string]any{})
	if result.Decision != PolicyDenied {
		t.Errorf("read_file without path should be denied, got %s", result.Decision)
	}
}

// ── V4 Policy Tests ──────────────────────────────────────────────

func TestPolicyV4_WriteFileDeniedDirectly(t *testing.T) {
	cfg := DefaultPolicyConfig()
	result := EvaluatePolicy(cfg, "write_file", map[string]any{
		"path":    "test.txt",
		"content": "data",
	})
	if result.Decision != PolicyDenied {
		t.Errorf("direct write_file should be denied, got %s", result.Decision)
	}
}

func TestPolicyV4_WriteFileProposalRequiresApproval(t *testing.T) {
	cfg := DefaultPolicyConfig()
	result := EvaluatePolicy(cfg, "write_file_proposal", map[string]any{
		"path":    "test.txt",
		"content": "Hello world",
	})
	if result.Decision != PolicyRequiresApproval {
		t.Errorf("write_file_proposal should require_approval, got %s", result.Decision)
	}
}

func TestPolicyV4_WriteFileProposalPathTraversal(t *testing.T) {
	cfg := DefaultPolicyConfig()
	result := EvaluatePolicy(cfg, "write_file_proposal", map[string]any{
		"path":    "../../../etc/passwd",
		"content": "malicious",
	})
	if result.Decision != PolicyDenied {
		t.Errorf("write_file_proposal with '..' should be denied, got %s", result.Decision)
	}
}

func TestPolicyV4_WriteFileProposalAbsolutePath(t *testing.T) {
	cfg := DefaultPolicyConfig()
	result := EvaluatePolicy(cfg, "write_file_proposal", map[string]any{
		"path":    "/etc/passwd",
		"content": "malicious",
	})
	if result.Decision != PolicyDenied {
		t.Errorf("write_file_proposal with absolute path should be denied, got %s", result.Decision)
	}
}

func TestPolicyV4_ApplyPatchProposalDenied(t *testing.T) {
	cfg := DefaultPolicyConfig()
	result := EvaluatePolicy(cfg, "apply_patch_proposal", map[string]any{
		"patch": "some diff",
	})
	if result.Decision != PolicyDenied {
		t.Errorf("apply_patch_proposal should be denied (V5 candidate), got %s", result.Decision)
	}
}

func TestPolicyV4_ShellDenied(t *testing.T) {
	cfg := DefaultPolicyConfig()
	result := EvaluatePolicy(cfg, "shell", map[string]any{
		"command": "ls",
	})
	// V60: Shell tool is now a valid tool that requires approval in gated mode.
	if result.Decision != PolicyRequiresApproval {
		t.Errorf("shell should require approval, got %s", result.Decision)
	}
}

func TestPolicyShellSafeCommandAutoApproved(t *testing.T) {
	cfg := DefaultPolicyConfig()
	cfg.SafeShellPolicy = BuildShellPolicyFromConfig("tier_1", map[string][]string{
		"tier_1": {"git status*", "git log*", "ls*"},
	}, nil)

	result := EvaluatePolicy(cfg, "shell", map[string]any{"command": "git status"})
	if result.Decision != PolicyApproved {
		t.Errorf("safe shell command matching tier_1 allowlist should auto-approve, got %s: %s", result.Decision, result.Reason)
	}
}

func TestPolicyShellUnlistedCommandStillRequiresApproval(t *testing.T) {
	cfg := DefaultPolicyConfig()
	cfg.SafeShellPolicy = BuildShellPolicyFromConfig("tier_1", map[string][]string{
		"tier_1": {"git status*", "git log*"},
	}, nil)

	// "rm somefile" is not in the tier_1 allowlist — SafeShellPolicy fix
	// must not widen approval beyond the configured safe tier.
	result := EvaluatePolicy(cfg, "shell", map[string]any{"command": "rm somefile"})
	if result.Decision != PolicyRequiresApproval {
		t.Errorf("unlisted shell command should still require approval, got %s", result.Decision)
	}
}

func TestPolicyShellHardBlocklistSurvivesSafeShellPolicy(t *testing.T) {
	cfg := DefaultPolicyConfig()
	// Even a permissive tier_3 SafeShellPolicy must not auto-approve a
	// hard-blocklisted command — the hard blocklist is the safety floor.
	cfg.SafeShellPolicy = BuildShellPolicyFromConfig("tier_3", nil, nil)

	result := EvaluatePolicy(cfg, "shell", map[string]any{"command": "rm -rf /*"})
	if result.Decision == PolicyApproved {
		t.Errorf("hard-blocklisted command must never auto-approve, got %s: %s", result.Decision, result.Reason)
	}
}

func TestPolicyV4_WriteFileProposalMissingContent(t *testing.T) {
	cfg := DefaultPolicyConfig()
	result := EvaluatePolicy(cfg, "write_file_proposal", map[string]any{
		"path": "test.txt",
	})
	if result.Decision != PolicyDenied {
		t.Errorf("write_file_proposal without content should be denied, got %s", result.Decision)
	}
}

func TestPolicyV4_WriteFileProposalOversizedContent(t *testing.T) {
	cfg := DefaultPolicyConfig()
	// Content over 1MB
	bigContent := make([]byte, 1024*1024+1)
	for i := range bigContent {
		bigContent[i] = 'x'
	}
	result := EvaluatePolicy(cfg, "write_file_proposal", map[string]any{
		"path":    "test.txt",
		"content": string(bigContent),
	})
	if result.Decision != PolicyDenied {
		t.Errorf("write_file_proposal with oversized content should be denied, got %s", result.Decision)
	}
}

// ── Symlink Traversal Policy Tests ────────────────────────────────────

func TestPolicyV4_CreateDirectoryDeniedDirectly(t *testing.T) {
	cfg := DefaultPolicyConfig()
	result := EvaluatePolicy(cfg, "create_directory", map[string]any{"path": "new-dir"})
	if result.Decision != PolicyDenied {
		t.Errorf("direct create_directory should be denied, got %s", result.Decision)
	}
}

func TestPolicyV4_CreateDirectoryDirectAutoApproved(t *testing.T) {
	cfg := DefaultPolicyConfig()
	cfg.AutoApproveMutations = true
	result := EvaluatePolicy(cfg, "create_directory", map[string]any{"path": "new-dir"})
	if result.Decision != PolicyApproved {
		t.Errorf("auto-approved create_directory should be approved, got %s", result.Decision)
	}
}

func TestPolicyV4_CreateDirectoryProposalRequiresApproval(t *testing.T) {
	cfg := DefaultPolicyConfig()
	result := EvaluatePolicy(cfg, "create_directory_proposal", map[string]any{"path": "new-dir"})
	if result.Decision != PolicyRequiresApproval {
		t.Errorf("create_directory_proposal should require approval, got %s", result.Decision)
	}
}

func TestPolicyV4_CreateDirectoryProposalPathTraversal(t *testing.T) {
	cfg := DefaultPolicyConfig()
	result := EvaluatePolicy(cfg, "create_directory_proposal", map[string]any{"path": "../outside"})
	if result.Decision != PolicyDenied {
		t.Errorf("create_directory_proposal with '..' should be denied, got %s", result.Decision)
	}
}

func TestPolicyV4_CreateDirectoryProposalOutsideWriteRoots(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	cfg := PolicyConfig{WorkspaceRoot: workspace}
	result := EvaluatePolicy(cfg, "create_directory_proposal", map[string]any{"path": outside})
	if result.Decision != PolicyDenied {
		t.Errorf("create_directory_proposal outside write roots should be denied, got %s", result.Decision)
	}
}

func TestIsWithinRoot_BlocksSymlinkEscape(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a directory outside the workspace
	outsideDir := filepath.Join(t.TempDir(), "outside")
	os.MkdirAll(outsideDir, 0755)

	// Create symlink inside workspace pointing outside
	linkPath := filepath.Join(tmpDir, "escape_link")
	if err := os.Symlink(outsideDir, linkPath); err != nil {
		t.Skipf("symlink creation is unavailable on this host: %v", err)
	}

	absRoot, _ := filepath.Abs(tmpDir)

	// Path through symlink should be caught after resolving
	absPath := filepath.Clean(filepath.Join(absRoot, "escape_link", "file.txt"))
	if safety.IsWithinRoot(absPath, absRoot) {
		t.Error("isWithinRoot should block symlink escape")
	}
}

func TestIsWithinRoot_AllowsRegularFiles(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "normal.txt"), []byte("hello"), 0644)

	absRoot, _ := filepath.Abs(tmpDir)
	absPath := filepath.Clean(filepath.Join(absRoot, "normal.txt"))

	if !safety.IsWithinRoot(absPath, absRoot) {
		t.Error("isWithinRoot should allow regular files within root")
	}
}

func TestIsWithinRoot_AllowsSubdirectories(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, "sub", "deep"), 0755)

	absRoot, _ := filepath.Abs(tmpDir)
	absPath := filepath.Clean(filepath.Join(absRoot, "sub", "deep", "file.txt"))

	if !safety.IsWithinRoot(absPath, absRoot) {
		t.Error("isWithinRoot should allow nested subdirectories")
	}
}

func TestIsWithinRoot_BlocksParentTraversal(t *testing.T) {
	tmpDir := t.TempDir()

	absRoot, _ := filepath.Abs(tmpDir)
	absPath := filepath.Clean(filepath.Join(absRoot, "..", "etc", "passwd"))

	if safety.IsWithinRoot(absPath, absRoot) {
		t.Error("isWithinRoot should block '..' traversal")
	}
}

func TestIsWithinRoot_BlocksDirectSymlinkEscape(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a file outside the workspace
	outsideFile := filepath.Join(t.TempDir(), "outside_secret.txt")
	os.WriteFile(outsideFile, []byte("secret"), 0644)

	// Create symlink inside workspace pointing directly to outside file
	linkPath := filepath.Join(tmpDir, "secret_link")
	if err := os.Symlink(outsideFile, linkPath); err != nil {
		t.Skipf("symlink creation is unavailable on this host: %v", err)
	}

	absRoot, _ := filepath.Abs(tmpDir)
	absPath := filepath.Clean(filepath.Join(absRoot, "secret_link"))

	if safety.IsWithinRoot(absPath, absRoot) {
		t.Error("isWithinRoot should block direct symlink escape")
	}
}

func TestPolicyReadRootsAllowChildPaths(t *testing.T) {
	tmpDir := t.TempDir()
	readRoot := filepath.Join(tmpDir, "read-root")
	child := filepath.Join(readRoot, "child")
	if err := os.MkdirAll(child, 0755); err != nil {
		t.Fatal(err)
	}

	cfg := PolicyConfig{WorkspaceRoot: filepath.Join(tmpDir, "workspace"), ReadRoots: []string{readRoot}}
	result := EvaluatePolicy(cfg, "read_file", map[string]any{"path": filepath.Join(child, "notes.txt")})
	if result.Decision != PolicyApproved {
		t.Fatalf("read under child should be approved, got %s: %s", result.Decision, result.Reason)
	}
}

func TestPolicyWriteRootsAreSeparateFromReadRoots(t *testing.T) {
	tmpDir := t.TempDir()
	readRoot := filepath.Join(tmpDir, "read-root")
	writeRoot := filepath.Join(tmpDir, "write-root")
	if err := os.MkdirAll(readRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(writeRoot, 0755); err != nil {
		t.Fatal(err)
	}

	cfg := PolicyConfig{
		WorkspaceRoot: filepath.Join(tmpDir, "workspace"),
		ReadRoots:     []string{readRoot},
		WriteRoots:    []string{writeRoot},
	}

	readOnlyWrite := EvaluatePolicy(cfg, "write_file_proposal", map[string]any{
		"path":    filepath.Join(readRoot, "file.txt"),
		"content": "data",
	})
	if readOnlyWrite.Decision != PolicyDenied {
		t.Fatalf("write under read root should be denied, got %s", readOnlyWrite.Decision)
	}

	childWrite := EvaluatePolicy(cfg, "write_file_proposal", map[string]any{
		"path":    filepath.Join(writeRoot, "child", "file.txt"),
		"content": "data",
	})
	if childWrite.Decision != PolicyRequiresApproval {
		t.Fatalf("write under write-root child should require approval, got %s: %s", childWrite.Decision, childWrite.Reason)
	}

	childDir := EvaluatePolicy(cfg, "create_directory_proposal", map[string]any{
		"path": filepath.Join(writeRoot, "child-dir"),
	})
	if childDir.Decision != PolicyRequiresApproval {
		t.Fatalf("directory under write-root child should require approval, got %s: %s", childDir.Decision, childDir.Reason)
	}
}

func TestPolicyWriteProposalRestrictedToOrchestrator(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := PolicyConfig{
		WorkspaceRoot:       tmpDir,
		OrchestratorAgentID: "lumi",
	}

	denied := EvaluatePolicyForAgent(cfg, "write_file_proposal", "forge", map[string]any{
		"path":    "file.txt",
		"content": "data",
	})
	if denied.Decision != PolicyDenied {
		t.Fatalf("subagent write proposal should be denied, got %s", denied.Decision)
	}

	allowed := EvaluatePolicyForAgent(cfg, "write_file_proposal", "lumi", map[string]any{
		"path":    "file.txt",
		"content": "data",
	})
	if allowed.Decision != PolicyRequiresApproval {
		t.Fatalf("orchestrator write proposal should require approval, got %s", allowed.Decision)
	}

	dirDenied := EvaluatePolicyForAgent(cfg, "create_directory_proposal", "forge", map[string]any{
		"path": "new-dir",
	})
	if dirDenied.Decision != PolicyDenied {
		t.Fatalf("subagent directory proposal should be denied, got %s", dirDenied.Decision)
	}

	dirAllowed := EvaluatePolicyForAgent(cfg, "create_directory_proposal", "lumi", map[string]any{
		"path": "new-dir",
	})
	if dirAllowed.Decision != PolicyRequiresApproval {
		t.Fatalf("orchestrator directory proposal should require approval, got %s", dirAllowed.Decision)
	}
}

func TestMCPToolRequiresApprovalByDefault(t *testing.T) {
	cfg := DefaultPolicyConfig()
	res := EvaluatePolicy(cfg, "mcp_fs_read_file", map[string]any{"path": "x"})
	if res.Decision != PolicyRequiresApproval {
		t.Fatalf("MCP tool should require approval by default, got %s: %s", res.Decision, res.Reason)
	}
}

func TestMCPToolAutoApproveOptIn(t *testing.T) {
	cfg := DefaultPolicyConfig()
	cfg.AutoApproveMCP = true
	res := EvaluatePolicy(cfg, "mcp_fs_read_file", nil)
	if res.Decision != PolicyApproved {
		t.Fatalf("AutoApproveMCP should approve MCP tools, got %s", res.Decision)
	}
}

func TestMCPAutoApproveIsSeparateFromMutations(t *testing.T) {
	cfg := DefaultPolicyConfig()
	cfg.AutoApproveMutations = true // mutations auto-approved...
	res := EvaluatePolicy(cfg, "mcp_x_y", nil)
	// ...but MCP tools must STILL require approval (separate opt-in).
	if res.Decision != PolicyRequiresApproval {
		t.Fatalf("AutoApproveMutations must not auto-approve MCP tools, got %s", res.Decision)
	}
}

func TestNonMCPUnknownToolStillDenied(t *testing.T) {
	cfg := DefaultPolicyConfig()
	res := EvaluatePolicy(cfg, "totally_unknown_tool", nil)
	if res.Decision != PolicyDenied {
		t.Fatalf("unknown non-MCP tool should be denied, got %s", res.Decision)
	}
}

func TestAutoApproveMCPPropagatesThroughPolicyCopy(t *testing.T) {
	// Mirrors newAutoExec: a derived policy copies the base and flips mutations on.
	base := DefaultPolicyConfig()
	base.AutoApproveMCP = true // set from config in serve mode
	derived := base            // value copy (as wake_handler does)
	derived.AutoApproveMutations = true
	if !derived.AutoApproveMCP {
		t.Fatal("AutoApproveMCP must survive the policy copy into the autonomous executor")
	}
	if res := EvaluatePolicy(derived, "mcp_fs_read_file", nil); res.Decision != PolicyApproved {
		t.Fatalf("derived policy with AutoApproveMCP should approve MCP tools, got %s", res.Decision)
	}
}

func TestAutoApproveMCPDefaultsOffEvenWithMutations(t *testing.T) {
	base := DefaultPolicyConfig() // AutoApproveMCP not set
	derived := base
	derived.AutoApproveMutations = true
	if res := EvaluatePolicy(derived, "mcp_fs_read_file", nil); res.Decision != PolicyRequiresApproval {
		t.Fatalf("MCP must require approval when AutoApproveMCP is unset, got %s", res.Decision)
	}
}

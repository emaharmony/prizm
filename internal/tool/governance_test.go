package tool

import (
	"strings"
	"testing"
)

func TestGovernance_FrozenPathBlocksWriteFile(t *testing.T) {
	cfg := PolicyConfig{
		WorkspaceRoot:       ".",
		AutoApproveMutations: true, // Even with auto-approve, governance should block
		FrozenPaths:         []string{"schema.prizma", "internal/db/"},
		FrozenPathReasons: map[string]string{
			"schema.prizma":  "Path schema.prizma is frozen per BASSBOOK-SCHEMA-FREEZE.md",
			"internal/db/":   "Directory internal/db/ is frozen per BASSBOOK-SCHEMA-FREEZE.md",
		},
	}

	// write_file_proposal targeting a frozen exact path should be DENIED
	result := EvaluatePolicyForAgent(cfg, "write_file_proposal", "lumi", map[string]any{
		"path": "schema.prizma",
	})
	if result.Decision != PolicyDenied {
		t.Errorf("expected DENIED for write to frozen path schema.prizma, got %s: %s", result.Decision, result.Reason)
	}
	if result.Reason == "" {
		t.Error("expected denial reason to mention governance freeze")
	}
}

func TestGovernance_FrozenDirectoryBlocksWrite(t *testing.T) {
	cfg := PolicyConfig{
		WorkspaceRoot:       ".",
		AutoApproveMutations: true,
		FrozenPaths:         []string{"internal/db/"},
		FrozenPathReasons: map[string]string{
			"internal/db/": "Directory internal/db/ is frozen",
		},
	}

	// Write to a file inside a frozen directory
	result := EvaluatePolicyForAgent(cfg, "write_file_proposal", "lumi", map[string]any{
		"path": "internal/db/schema.go",
	})
	if result.Decision != PolicyDenied {
		t.Errorf("expected DENIED for write inside frozen directory, got %s: %s", result.Decision, result.Reason)
	}
}

func TestGovernance_FrozenGlobBlocksWrite(t *testing.T) {
	cfg := PolicyConfig{
		WorkspaceRoot:       ".",
		AutoApproveMutations: true,
		FrozenPaths:         []string{"*.sql"},
		FrozenPathReasons: map[string]string{
			"*.sql": "SQL migration files are frozen",
		},
	}

	// Write to a .sql file matching the glob
	result := EvaluatePolicyForAgent(cfg, "write_file_proposal", "lumi", map[string]any{
		"path": "migrations/001_init.sql",
	})
	if result.Decision != PolicyDenied {
		t.Errorf("expected DENIED for write to frozen glob *.sql, got %s: %s", result.Decision, result.Reason)
	}
}

func TestGovernance_NonFrozenPathAllowed(t *testing.T) {
	cfg := PolicyConfig{
		WorkspaceRoot:       ".",
		AutoApproveMutations: true,
		FrozenPaths:         []string{"schema.prizma"},
		FrozenPathReasons: map[string]string{
			"schema.prizma": "schema.prizma is frozen",
		},
	}

	// Write to a non-frozen file should still be approved (auto-approve is on)
	result := EvaluatePolicyForAgent(cfg, "write_file_proposal", "lumi", map[string]any{
		"path": "src/main.go",
	})
	if result.Decision != PolicyApproved {
		t.Errorf("expected APPROVED for write to non-frozen path, got %s: %s", result.Decision, result.Reason)
	}
}

func TestGovernance_BypassesAutoApprove(t *testing.T) {
	// This is the critical test: even with AutoApproveMutations=true,
	// a frozen path must be DENIED. This proves governance > auto-approve.
	cfg := PolicyConfig{
		WorkspaceRoot:       ".",
		AutoApproveMutations: true,
		FrozenPaths:         []string{"schema.prizma"},
		FrozenPathReasons: map[string]string{
			"schema.prizma": "schema.prizma is frozen per SCHEMA-FREEZE.md",
		},
	}

	// Direct write_file (normally denied anyway, but with auto-approve it would be approved)
	// Governance should deny it BEFORE auto-approve is checked
	result := EvaluatePolicyForAgent(cfg, "write_file", "lumi", map[string]any{
		"path": "schema.prizma",
	})
	if result.Decision != PolicyDenied {
		t.Errorf("expected DENIED for direct write to frozen path even with auto-approve, got %s", result.Decision)
	}
	if result.Reason == "" {
		t.Error("expected governance reason in denial")
	}
}

func TestGovernance_NoFrozenPathsNormalOperation(t *testing.T) {
	// When no frozen paths are configured, everything works normally
	cfg := PolicyConfig{
		WorkspaceRoot:       ".",
		AutoApproveMutations: true,
	}

	result := EvaluatePolicyForAgent(cfg, "write_file_proposal", "lumi", map[string]any{
		"path": "anyfile.go",
	})
	if result.Decision != PolicyApproved {
		t.Errorf("expected APPROVED with no frozen paths, got %s: %s", result.Decision, result.Reason)
	}
}

func TestGovernance_ReadOnlyToolsNotBlocked(t *testing.T) {
	// Read tools should never be blocked by governance (reading a frozen file is fine)
	cfg := PolicyConfig{
		WorkspaceRoot:       ".",
		AutoApproveMutations: true,
		FrozenPaths:         []string{"schema.prizma"},
		FrozenPathReasons: map[string]string{
			"schema.prizma": "schema.prizma is frozen",
		},
	}

	result := EvaluatePolicyForAgent(cfg, "read_file", "lumi", map[string]any{
		"path": "schema.prizma",
	})
	// read_file goes through evaluatePathPolicy which may approve or require approval
	// but it should NOT be denied for governance reasons
	if result.Decision == PolicyDenied && result.Reason != "" && strings.Contains(result.Reason, "governance") {
		t.Errorf("read_file should not be blocked by governance, got: %s", result.Reason)
	}
}

func TestGovernance_GitAddBlockedOnFrozenPath(t *testing.T) {
	cfg := PolicyConfig{
		WorkspaceRoot:       ".",
		AutoApproveMutations: true,
		FrozenPaths:         []string{"schema.prizma"},
		FrozenPathReasons: map[string]string{
			"schema.prizma": "schema.prizma is frozen",
		},
	}

	result := EvaluatePolicyForAgent(cfg, "git_add", "lumi", map[string]any{
		"path": "schema.prizma",
	})
	if result.Decision != PolicyDenied {
		t.Errorf("expected DENIED for git_add on frozen path, got %s: %s", result.Decision, result.Reason)
	}
}


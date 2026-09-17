// Package tool implements Prizm's tool execution system (V3+) — the controlled way
// an AI agent interacts with the outside world.
//
// The key insight: the LLM doesn't decide what tools can do. A deterministic Policy
// decides. There are three possible decisions:
//   - "allowed" → the tool runs immediately, no human involvement
//   - "requires_approval" → the tool creates a pending approval; a human must approve before it executes
//   - "denied" → the tool is blocked entirely
//
// Why is the LLM NOT in charge of policy? Because an LLM can be tricked via prompt
// injection. If the LLM could decide "yes, write to /etc/passwd", a clever prompt
// could make it say yes. By keeping policy deterministic, Prizm is safe even if
// the model is compromised.
//
// Current policies (V4):
//
//	echo, list_dir, read_file → allowed (read-only, no risk)
//	write_file_dry_run → allowed (preview only, writes nothing)
//	write_file_proposal → requires_approval (creates a pending approval, human must approve)
//	write_file (direct, no proposal) → denied (bypassing approval is never allowed)
package tool

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/emaharmony/prizm/internal/safety"
)

// PolicyConfig holds the configuration for policy decisions.
type PolicyConfig struct {
	// WorkspaceRoot is the primary root directory that list_dir and read_file are
	// allowed to access. Any path outside this root (and AllowedPaths) is denied.
	WorkspaceRoot string
	// AllowedPaths is a list of additional directory roots the agent can access.
	// The workspace root is always implicitly included. Paths are resolved to
	// absolute paths at evaluation time.
	AllowedPaths []string
	// ReadRoots is the recursive root list for read/search/list tools. If empty,
	// AllowedPaths is used for backward compatibility.
	ReadRoots []string
	// WriteRoots is the recursive root list for approval-gated mutations. If
	// empty, AllowedPaths is used for backward compatibility.
	WriteRoots []string
	// MaxFileSize is the maximum file size in bytes that read_file will allow.
	// Defaults to 1MB if zero.
	MaxFileSize int64
	// OrchestratorAgentID is the only agent allowed to request mutation
	// proposals by default. Empty preserves legacy behavior.
	OrchestratorAgentID string
	// WriteAgents explicitly allows extra agents to request mutation proposals.
	WriteAgents map[string]bool
	// AutoApproveMutations skips the approval gate for mutation tools
	// (write_file_proposal, git_add, git_commit, git_push). Used by
	// autonomous wake handler actions where no human is present to approve.
	AutoApproveMutations bool

	// AutoApproveMCP opts in to autonomous execution of external MCP tools
	// (mcp_<server>_<tool>). It is deliberately SEPARATE from AutoApproveMutations:
	// MCP tools are remote and their descriptions/schemas are attacker-influenced
	// text reaching the model, so they default to approval-required and only run
	// unattended when an operator explicitly turns this on.
	AutoApproveMCP bool

	// SafeShellPolicy classifies shell commands that are safe enough to
	// auto-run even in gated mode (e.g. the tier_1 allowlist: read-only
	// git/build/test commands). It is deliberately separate from
	// AutoApproveMutations — a command that fails this check still falls
	// through to the normal requires_approval gate rather than being denied.
	// The hard blocklist inside EvaluateShellPolicy is always enforced
	// first, regardless of this policy's tier.
	SafeShellPolicy ShellPolicy

	// FrozenPaths is a list of file paths that are governance-frozen. Any write
	// operation targeting these paths is denied regardless of other policy
	// settings. Populated by the governance loader on startup. Supports exact
	// paths, directory prefixes (ending in /), and glob patterns.
	FrozenPaths []string
	// FrozenPathReasons maps frozen path patterns to their governance reason.
	FrozenPathReasons map[string]string
}

// DefaultPolicyConfig returns a PolicyConfig with sensible defaults.
func DefaultPolicyConfig() PolicyConfig {
	return PolicyConfig{
		WorkspaceRoot: ".",
		MaxFileSize:   1024 * 1024, // 1MB
	}
}

// AllRoots returns all allowed root directories (workspace root + allowed paths).
// Used by tools to check path containment.
func (pc PolicyConfig) AllRoots() []string {
	roots := []string{pc.WorkspaceRoot}
	roots = append(roots, pc.AllowedPaths...)
	return roots
}

// ReadAllowedPaths returns non-workspace roots for read-only tools.
func (pc PolicyConfig) ReadAllowedPaths() []string {
	if len(pc.ReadRoots) > 0 {
		return append([]string(nil), pc.ReadRoots...)
	}
	return append([]string(nil), pc.AllowedPaths...)
}

// WriteAllowedPaths returns non-workspace roots for approval-gated mutations.
func (pc PolicyConfig) WriteAllowedPaths() []string {
	if len(pc.WriteRoots) > 0 {
		return append([]string(nil), pc.WriteRoots...)
	}
	return append([]string(nil), pc.AllowedPaths...)
}

// ReadRootsAll returns workspace plus configured read roots.
func (pc PolicyConfig) ReadRootsAll() []string {
	roots := []string{pc.WorkspaceRoot}
	roots = append(roots, pc.ReadAllowedPaths()...)
	return roots
}

// WriteRootsAll returns workspace plus configured write roots.
func (pc PolicyConfig) WriteRootsAll() []string {
	roots := []string{pc.WorkspaceRoot}
	roots = append(roots, pc.WriteAllowedPaths()...)
	return roots
}

// CanAgentProposeWrites reports whether an agent may request approval-gated mutations.
func (pc PolicyConfig) CanAgentProposeWrites(agentID string) bool {
	if pc.WriteAgents != nil && pc.WriteAgents[agentID] {
		return true
	}
	if pc.OrchestratorAgentID == "" {
		return true
	}
	return agentID == pc.OrchestratorAgentID
}

// PolicyResult captures the decision and reason for a policy evaluation.
type PolicyResult struct {
	Decision PolicyDecision
	Reason   string
}

// EvaluatePolicy checks whether a tool call is allowed under the current policy.
// Policy is deterministic — the LLM never decides policy.
//
// Rules:
//   - echo → always approved
//   - list_dir → approved if path is within workspace root
//   - read_file → approved if path is within workspace root and file size ≤ MaxFileSize
//   - write_file_dry_run → always approved (no mutation)
//   - write_file_proposal/create_directory_proposal → requires_approval (mutation gate)
//   - apply_patch_proposal → denied (V5 candidate, not implemented)
//   - write_file/create_directory (direct) → denied unless AutoApproveMutations is set
//   - anything else → denied
//   - Path traversal with ".." or absolute paths outside workspace → denied
func EvaluatePolicy(cfg PolicyConfig, toolName string, input map[string]any) PolicyResult {
	return EvaluatePolicyForAgent(cfg, toolName, "", input)
}

// EvaluatePolicyForAgent checks whether a tool call is allowed for the given agent.
func EvaluatePolicyForAgent(cfg PolicyConfig, toolName, agentID string, input map[string]any) PolicyResult {
	switch toolName {
	case "echo":
		return PolicyResult{Decision: PolicyApproved, Reason: "echo is always approved"}

	// RESEARCH-phase read-only tools — no code mutation, always approved. The
	// image tools write only into the configured references directory (not the
	// codebase), so the Researcher can gather reference art autonomously.
	case "web_search", "memory_search", "fetch_image", "generate_image", "analyze_image", "collect_reference_images":
		return PolicyResult{Decision: PolicyApproved, Reason: fmt.Sprintf("%s is read-only research, always approved", toolName)}

	// use_skill only returns a skill's instructions (no mutation) — always approved.
	// Scripts a skill bundles still run through the mutation tools' own gates.
	case "use_skill":
		return PolicyResult{Decision: PolicyApproved, Reason: "use_skill returns skill instructions, read-only"}

	// State management tools — track working state, not code mutation.
	case "set_active_task", "clear_active_task", "state_get":
		return PolicyResult{Decision: PolicyApproved, Reason: fmt.Sprintf("%s is state management, always approved", toolName)}

	// State mutation tools — persist working state, not code. Approval-gated
	// because they mutate the agent's durable working state; a human should
	// confirm before recording a decision, blocking an item, or rewriting context.
	case "update_context", "record_decision", "add_blocked", "unblock":
		if cfg.AutoApproveMutations {
			return PolicyResult{Decision: PolicyApproved, Reason: fmt.Sprintf("auto-approve: %s approved for autonomous wake action", toolName)}
		}
		return PolicyResult{Decision: PolicyRequiresApproval, Reason: fmt.Sprintf("%s mutates working state, requires approval", toolName)}

	case "write_file_dry_run":
		return PolicyResult{Decision: PolicyApproved, Reason: "write_file_dry_run is a read-only preview, no mutation"}

	case "write_file_proposal":
		if frozenPath, reason := cfg.checkFrozenPath(input); frozenPath != "" {
			return PolicyResult{Decision: PolicyDenied, Reason: fmt.Sprintf("governance: %s (frozen path: %s)", reason, frozenPath)}
		}
		if agentID != "" && !cfg.CanAgentProposeWrites(agentID) {
			return PolicyResult{Decision: PolicyDenied, Reason: fmt.Sprintf("agent %q is not allowed to propose file mutations; route write requests through the orchestrator", agentID)}
		}
		if cfg.AutoApproveMutations {
			return PolicyResult{Decision: PolicyApproved, Reason: "auto-approve: mutation tools approved for autonomous wake action"}
		}
		return evaluateV4ProposalPolicy(cfg, toolName, input)

	case "create_directory_proposal":
		if agentID != "" && !cfg.CanAgentProposeWrites(agentID) {
			return PolicyResult{Decision: PolicyDenied, Reason: fmt.Sprintf("agent %q is not allowed to propose directory mutations; route write requests through the orchestrator", agentID)}
		}
		if cfg.AutoApproveMutations {
			return PolicyResult{Decision: PolicyApproved, Reason: "auto-approve: directory creation proposal approved for autonomous wake action"}
		}
		return evaluateDirectoryProposalPolicy(cfg, toolName, input)

	case "apply_patch_proposal":
		return PolicyResult{Decision: PolicyDenied, Reason: "apply_patch_proposal is not implemented (V5 candidate)"}

	case "write_file":
		if cfg.AutoApproveMutations {
			if frozenPath, reason := cfg.checkFrozenPath(input); frozenPath != "" {
				return PolicyResult{Decision: PolicyDenied, Reason: fmt.Sprintf("governance: %s (frozen path: %s)", reason, frozenPath)}
			}
			return PolicyResult{Decision: PolicyApproved, Reason: "auto-approve: direct write approved for autonomous wake action"}
		}
		return PolicyResult{Decision: PolicyDenied, Reason: "direct write_file is denied — use write_file_proposal for approval-gated mutations"}

	case "create_directory":
		if cfg.AutoApproveMutations {
			return PolicyResult{Decision: PolicyApproved, Reason: "auto-approve: direct directory creation approved for autonomous wake action"}
		}
		return PolicyResult{Decision: PolicyDenied, Reason: "direct create_directory is denied — use create_directory_proposal for approval-gated mutations"}

	case "list_dir":
		return evaluatePathPolicy(cfg, toolName, input)

	case "read_file":
		return evaluatePathPolicy(cfg, toolName, input)

	case "read_project":
		return evaluatePathPolicy(cfg, toolName, input)

	case "search_files":
		return evaluatePathPolicy(cfg, toolName, input)

	case "project_overview":
		return evaluatePathPolicy(cfg, toolName, input)

	// V28: Git read-only tools — always approved
	case "git_status", "git_log", "git_diff", "git_branch_list":
		return PolicyResult{Decision: PolicyApproved, Reason: fmt.Sprintf("%s is read-only git, always approved", toolName)}

	// Git branch operations — require approval but auto-approved for autonomous wake
	case "git_checkout":
		if agentID != "" && !cfg.CanAgentProposeWrites(agentID) {
			return PolicyResult{Decision: PolicyDenied, Reason: fmt.Sprintf("agent %q is not allowed to propose git mutations; route write requests through the orchestrator", agentID)}
		}
		if cfg.AutoApproveMutations {
			return PolicyResult{Decision: PolicyApproved, Reason: "auto-approve: git checkout approved for autonomous wake action"}
		}
		return PolicyResult{Decision: PolicyRequiresApproval, Reason: "git_checkout is a git mutation, requires approval"}

	// V28: Git mutation tools — require approval
	case "git_add", "git_commit", "git_push", "create_pr":
		// Governance check: deny if targeting a frozen path
		if frozenPath, reason := cfg.checkFrozenPath(input); frozenPath != "" {
			return PolicyResult{Decision: PolicyDenied, Reason: fmt.Sprintf("governance: %s (frozen path: %s)", reason, frozenPath)}
		}
		if agentID != "" && !cfg.CanAgentProposeWrites(agentID) {
			return PolicyResult{Decision: PolicyDenied, Reason: fmt.Sprintf("agent %q is not allowed to propose git mutations; route write requests through the orchestrator", agentID)}
		}
		if cfg.AutoApproveMutations {
			return PolicyResult{Decision: PolicyApproved, Reason: "auto-approve: git mutation approved for autonomous wake action"}
		}
		return PolicyResult{Decision: PolicyRequiresApproval, Reason: fmt.Sprintf("%s is a git mutation, requires approval", toolName)}

	// V60: Shell tool — policy is enforced internally by ShellTool.Execute()
	// which checks the hard blocklist and tier-based allowlist.
	// External policy allows it through; the tool itself enforces safety.
	case "plan_create", "plan_list", "plan_update", "plan_approve", "plan_reject":
		return PolicyResult{Decision: PolicyApproved, Reason: fmt.Sprintf("plan management: %s is allowed", toolName)}

	case "shell":
		if agentID != "" && !cfg.CanAgentProposeWrites(agentID) {
			return PolicyResult{Decision: PolicyDenied, Reason: fmt.Sprintf("agent %q is not allowed to use shell; route through the orchestrator", agentID)}
		}
		if cfg.AutoApproveMutations {
			return PolicyResult{Decision: PolicyApproved, Reason: "auto-approve: shell tool approved for free mode / autonomous wake action"}
		}
		if command, _ := input["command"].(string); command != "" {
			if safeResult := EvaluateShellPolicy(cfg.SafeShellPolicy, command); safeResult.Allowed {
				return PolicyResult{Decision: PolicyApproved, Reason: fmt.Sprintf("auto-approve: safe command — %s", safeResult.Reason)}
			}
		}
		return PolicyResult{Decision: PolicyRequiresApproval, Reason: "shell tool requires approval in gated mode"}

	default:
		// V49: external MCP tools (mcp_<server>_<tool>). Remote and untrusted, so
		// they are gated behind approval by default — never silently denied (which
		// would make them unusable) nor auto-run. Operators opt into unattended MCP
		// execution explicitly via AutoApproveMCP.
		if strings.HasPrefix(toolName, "mcp_") {
			if cfg.AutoApproveMCP {
				return PolicyResult{Decision: PolicyApproved, Reason: fmt.Sprintf("auto-approve: MCP tool %q approved (AutoApproveMCP)", toolName)}
			}
			return PolicyResult{Decision: PolicyRequiresApproval, Reason: fmt.Sprintf("MCP tool %q is external; requires approval", toolName)}
		}
		return PolicyResult{Decision: PolicyDenied, Reason: fmt.Sprintf("tool %q is not in the approved list", toolName)}
	}
}

// evaluateDirectoryProposalPolicy validates a create_directory_proposal.
func evaluateDirectoryProposalPolicy(cfg PolicyConfig, toolName string, input map[string]any) PolicyResult {
	pathVal, ok := input["path"]
	if !ok {
		return PolicyResult{Decision: PolicyDenied, Reason: fmt.Sprintf("%s requires a 'path' parameter", toolName)}
	}

	pathStr, ok := pathVal.(string)
	if !ok || strings.TrimSpace(pathStr) == "" {
		return PolicyResult{Decision: PolicyDenied, Reason: fmt.Sprintf("%s 'path' must be a non-empty string", toolName)}
	}

	if strings.Contains(pathStr, "..") {
		return PolicyResult{Decision: PolicyDenied, Reason: "path traversal with '..' is blocked"}
	}

	if _, err := safety.ResolveAndContainMulti(cfg.WriteRootsAll(), pathStr); err != nil {
		return PolicyResult{Decision: PolicyDenied, Reason: err.Error()}
	}

	return PolicyResult{Decision: PolicyRequiresApproval, Reason: fmt.Sprintf("%s requires explicit approval to apply the mutation", toolName)}
}

// evaluateV4ProposalPolicy validates a write_file_proposal or similar V4 mutation proposal.
func evaluateV4ProposalPolicy(cfg PolicyConfig, toolName string, input map[string]any) PolicyResult {
	pathVal, ok := input["path"]
	if !ok {
		return PolicyResult{Decision: PolicyDenied, Reason: fmt.Sprintf("%s requires a 'path' parameter", toolName)}
	}

	pathStr, ok := pathVal.(string)
	if !ok {
		return PolicyResult{Decision: PolicyDenied, Reason: fmt.Sprintf("%s 'path' must be a string", toolName)}
	}

	// Block path traversal
	if strings.Contains(pathStr, "..") {
		return PolicyResult{Decision: PolicyDenied, Reason: "path traversal with '..' is blocked"}
	}

	// Resolve and check configured bounds. Relative paths resolve against the
	// workspace root; absolute paths are allowed only inside configured roots.
	if _, err := safety.ResolveAndContainMulti(cfg.WriteRootsAll(), pathStr); err != nil {
		return PolicyResult{Decision: PolicyDenied, Reason: err.Error()}
	}

	// Check content parameter exists
	_, ok = input["content"]
	if !ok {
		return PolicyResult{Decision: PolicyDenied, Reason: fmt.Sprintf("%s requires a 'content' parameter", toolName)}
	}

	contentStr, ok := input["content"].(string)
	if !ok {
		return PolicyResult{Decision: PolicyDenied, Reason: fmt.Sprintf("%s 'content' must be a string", toolName)}
	}

	if len(contentStr) > 1024*1024 {
		return PolicyResult{Decision: PolicyDenied, Reason: "content exceeds maximum size of 1MB"}
	}

	return PolicyResult{Decision: PolicyRequiresApproval, Reason: fmt.Sprintf("%s requires explicit approval to apply the mutation", toolName)}
}

// evaluatePathPolicy handles the path-based policy checks for list_dir and read_file.
func evaluatePathPolicy(cfg PolicyConfig, toolName string, input map[string]any) PolicyResult {
	pathVal, ok := input["path"]
	if !ok {
		return PolicyResult{Decision: PolicyDenied, Reason: fmt.Sprintf("%s requires a 'path' parameter", toolName)}
	}

	pathStr, ok := pathVal.(string)
	if !ok {
		return PolicyResult{Decision: PolicyDenied, Reason: fmt.Sprintf("%s 'path' must be a string", toolName)}
	}

	// Block path traversal with ".."
	if strings.Contains(pathStr, "..") {
		return PolicyResult{Decision: PolicyDenied, Reason: "path traversal with '..' is blocked"}
	}

	// Resolve all allowed roots to absolute form
	allRoots := cfg.ReadRootsAll()
	absRoots := make([]string, len(allRoots))
	for i, r := range allRoots {
		absR, err := filepath.Abs(r)
		if err != nil {
			return PolicyResult{Decision: PolicyDenied, Reason: fmt.Sprintf("invalid root path: %q", r)}
		}
		absRoots[i] = absR
	}

	var absPath string
	if safety.IsAbsolutePath(pathStr) {
		// Absolute path — check against all allowed roots
		absPath = filepath.Clean(pathStr)
	} else {
		// Relative path — resolve against workspace root (primary)
		absPath = filepath.Clean(filepath.Join(absRoots[0], pathStr))
	}

	// Check that the resolved path is within at least one allowed root
	if !safety.IsWithinAnyRoot(absPath, absRoots) {
		return PolicyResult{Decision: PolicyDenied, Reason: fmt.Sprintf("path %q is not within any allowed root", pathStr)}
	}

	return PolicyResult{Decision: PolicyApproved, Reason: fmt.Sprintf("%s path is within allowed roots", toolName)}
}

// resolvePath joins the workspace root with the given path and cleans it.
func resolvePath(workspaceRoot, relPath string) string {
	return filepath.Clean(filepath.Join(workspaceRoot, relPath))
}

// Path containment is now provided by internal/safety.IsWithinRoot.
// The local implementation has been removed to eliminate duplication.
// All path containment checks should use safety.IsWithinRoot or safety.ResolveAndContain.

// checkFrozenPath checks if the tool input targets a frozen path.
// Returns the matched frozen path pattern and reason if found.
func (cfg PolicyConfig) checkFrozenPath(input map[string]any) (string, string) {
	if len(cfg.FrozenPaths) == 0 {
		return "", ""
	}
	// Extract the target path from tool input
	targetPath, ok := extractPathFromInput(input)
	if !ok || targetPath == "" {
		return "", ""
	}
	for _, frozenPath := range cfg.FrozenPaths {
		if pathMatchesFrozen(targetPath, frozenPath) {
			reason := fmt.Sprintf("path %s is frozen", frozenPath)
			if cfg.FrozenPathReasons != nil {
				if r, ok := cfg.FrozenPathReasons[frozenPath]; ok {
					reason = r
				}
			}
			return frozenPath, reason
		}
	}
	return "", ""
}

// extractPathFromInput extracts the file path from a tool's input parameters.
func extractPathFromInput(input map[string]any) (string, bool) {
	for _, key := range []string{"path", "file_path", "target_path", "filepath", "filename"} {
		if v, ok := input[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s, true
			}
		}
	}
	return "", false
}

// pathMatchesFrozen checks if a target path matches a frozen path pattern.
// Supports exact paths, directory prefixes (ending in /), and glob patterns.
func pathMatchesFrozen(target, frozen string) bool {
	target = filepath.Clean(target)
	isDir := strings.HasSuffix(frozen, "/")
	cleanFrozen := filepath.Clean(frozen)

	if isDir {
		if strings.HasPrefix(target, cleanFrozen+string(filepath.Separator)) || target == cleanFrozen {
			return true
		}
		return false
	}

	if target == cleanFrozen {
		return true
	}

	if matched, err := filepath.Match(cleanFrozen, target); err == nil && matched {
		return true
	}

	base := filepath.Base(target)
	if matched, err := filepath.Match(cleanFrozen, base); err == nil && matched {
		return true
	}

	return false
}

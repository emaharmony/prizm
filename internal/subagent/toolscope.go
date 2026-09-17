package subagent

import "strings"

// toolscope.go enforces per-agent tool scoping: a role-appropriate second layer
// on top of the deterministic tool policy engine. It keeps an autonomous
// sub-agent inside its lane — a researcher can gather references and read code
// but cannot commit/push or drive Blender; only agents with the "code"
// capability may mutate files, run git writes, or use external MCP build tools.
//
// This is advisory-to-the-model AND enforced: a disallowed tool call is never
// executed; the denial is fed back so the agent adapts instead of failing.

// ToolScope decides whether an agent may invoke a given tool.
type ToolScope interface {
	Allowed(runtime AgentRuntime, tool string) bool
}

// roleGatedTools maps a tool to the capability an agent must hold to use it.
// Tools absent from this map are unrestricted at the scope layer (read-only /
// research tools, cross-agent messaging, planning tools) — the policy engine
// still gates anything genuinely dangerous.
var roleGatedTools = map[string]string{
	"write_file":                "code",
	"write_file_proposal":       "code",
	"write_file_direct":         "code",
	"create_directory":          "code",
	"create_directory_proposal": "code",
	"create_directory_direct":   "code",
	"git_add":                   "code",
	"git_commit":                "code",
	"git_push":                  "code",
}

// CapabilityToolScope allows a tool when the agent holds the capability that
// tool requires (per roleGatedTools + the mcp_ prefix rule). Unlisted tools are
// allowed.
type CapabilityToolScope struct{}

// DefaultToolScope returns the standard capability-based tool scope.
func DefaultToolScope() ToolScope { return CapabilityToolScope{} }

func (CapabilityToolScope) Allowed(runtime AgentRuntime, tool string) bool {
	if runtime.EnforceAllowedTools {
		allowed := false
		for _, candidate := range runtime.AllowedTools {
			if candidate == tool {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}

	required := requiredCapability(tool)
	if required == "" {
		return true
	}
	return hasCapability(runtime, required)
}

// requiredCapability returns the capability a tool demands, or "" if unrestricted.
func requiredCapability(tool string) string {
	if c, ok := roleGatedTools[tool]; ok {
		return c
	}
	// External MCP build tools (e.g. mcp_blender_*) are code/asset work.
	if strings.HasPrefix(tool, "mcp_") {
		return "code"
	}
	return ""
}

func hasCapability(runtime AgentRuntime, cap string) bool {
	for _, c := range runtime.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
}

// Package orchestrator provides the persistent daemon that runs Prizm as a
// live service. Config holds the prizm.yaml configuration.
package orchestrator

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/agent"
	"github.com/emaharmony/prizm/internal/cost"
	"gopkg.in/yaml.v3"
)

// Config represents the full Prizm configuration loaded from prizm.yaml.
//
// The config defines agents, channels (Discord, Telegram, etc.),
// registered actions, and service settings. It is the single source of
// truth for how Prizm runs — no OpenClaw dependency.
//
// Agent IDs become event namespace prefixes. If no ID is provided,
// the system auto-generates: prizm1, prizm2, prizm3, etc.
type Config struct {
	// Prizm holds top-level service settings.
	Prizm PrizmConfig `yaml:"prizm"`

	// API configures HTTP API authentication and CORS.
	API APIServerConfig `yaml:"api"`

	// Cost configures per-model token pricing overrides used for cost estimation.
	Cost CostConfig `yaml:"cost"`

	// Usage configures the dashboard token-usage tracker's time windows.
	Usage UsageConfig `yaml:"usage"`

	// Bridge configures signed cross-Prizm protocol subjects.
	Bridge BridgeConfig `yaml:"bridge"`

	// Codex configures subscription-backed Codex CLI task delegation.
	Codex CodexConfig `yaml:"codex"`

	// ClaudeCode configures the Claude Code CLI sub-agent reviewer that can
	// fulfill gated-loop FEEDBACK_PRE/FEEDBACK_POST gates automatically.
	ClaudeCode ClaudeCodeConfig `yaml:"claude_code"`

	// Autopatch configures diagnose-and-propose patch tasks.
	Autopatch AutopatchConfig `yaml:"autopatch"`

	// FactoryMonitor configures local Roblox Factory status notifications.
	FactoryMonitor FactoryMonitorConfig `yaml:"factory_monitor"`

	// Shell configures the shell tool access control for free mode.
	Shell ShellConfig `yaml:"shell"`

	// Agents defines the agents Prizm should register.
	// Each agent gets its own event namespace based on its ID.
	Agents []AgentConfig `yaml:"agents"`

	// Projects defines the assignable projects the gated loop can work on.
	// Replaces hardcoded repo paths so projects are dynamic/assignable.
	Projects []ProjectConfig `yaml:"projects"`

	// Channels defines messaging channels (Discord, Telegram, etc.).
	Channels []ChannelConfig `yaml:"channels"`

	// Actions defines event-triggered actions (webhook-style).
	Actions []ActionConfig `yaml:"actions"`

	// Sessions configures session management.
	Sessions SessionConfig `yaml:"sessions"`

	// Users maps external channel identities to stable owner identities.
	Users []UserConfig `yaml:"users"`

	// Remembrance configures the memory service.
	Remembrance RemembranceConfig `yaml:"remembrance"`

	// Memory configures the local memory store.
	Memory MemoryConfig `yaml:"memory"`

	// ChannelRoles maps Discord channel IDs to role names that determine
	// which state action applies. Role names must match state_actions keys.
	ChannelRoles []ChannelRole `yaml:"channel_roles"`

	// MCPServers declares external Model Context Protocol tool servers whose tools
	// are registered into the policy-gated tool registry at serve startup.
	MCPServers []MCPServerConfig `yaml:"mcp_servers"`

	// MCPAutoApprove opts in to UNATTENDED execution of external MCP tools (skips
	// the approval gate for mcp_<server>_<tool>). Default false: MCP tools require
	// approval. Separate from any mutation auto-approve — remote tools are
	// higher-trust-risk, so enabling autonomous MCP execution is an explicit choice.
	MCPAutoApprove bool `yaml:"mcp_auto_approve"`
}

// MCPServerConfig declares one external MCP (Model Context Protocol) tool server.
// Its tools are registered as mcp_<name>_<tool> and run through the same policy
// engine as built-in tools.
type MCPServerConfig struct {
	Name    string   `yaml:"name"`    // logical server name (namespaces its tools)
	Command string   `yaml:"command"` // executable to spawn, e.g. "npx"
	Args    []string `yaml:"args"`    // arguments, e.g. ["-y","@modelcontextprotocol/server-filesystem","/repo"]
	Env     []string `yaml:"env"`     // extra KEY=VALUE environment entries
	Enabled bool     `yaml:"enabled"` // skip when false
}

// PrizmConfig holds top-level service settings.
type PrizmConfig struct {
	// InstanceID identifies this Prizm process in cross-Prizm messages.
	InstanceID string `yaml:"instance_id"`

	// NATSURL is the NATS server URL. Empty means embedded.
	NATSURL string `yaml:"nats_url"`

	// DataDir is where SQLite databases and run artifacts are stored.
	DataDir string `yaml:"data_dir"`

	// RunsDir is the directory where per-run artifacts and approval records are
	// written. Relative to the working directory unless absolute. Default "runs".
	// Kept separate from DataDir because the `prizm approval`/`prizm runs` CLIs
	// read this tree (default ./runs); change it here to relocate both writers.
	RunsDir string `yaml:"runs_dir"`

	// Workspace is the root directory for context injection (SOUL.md, AGENTS.md, etc.).
	// Defaults to $HOME/.openclaw/workspace if empty.
	Workspace string `yaml:"workspace"`

	// OllamaURL is the base URL for local Ollama-compatible agents.
	// Default: http://localhost:11434.
	OllamaURL string `yaml:"ollama_url"`

	// ContextTokenBudget is the max tokens for workspace context injection.
	// TTS holds text-to-speech (Voicebox) configuration.
	// When enabled, Prizm generates voice messages alongside text responses.
	TTS TTSConfig `yaml:"tts"`
	// Default: 4000. Higher = more context but less room for conversation.
	ContextTokenBudget int `yaml:"context_token_budget"`

	// LLMTimeoutSeconds is the serve-mode timeout for each live LLM call.
	// Default: 1200 seconds (20 minutes). Local inference can be slow.
	LLMTimeoutSeconds int `yaml:"llm_timeout_seconds"`

	// Port is the health check server port. Default 8321.
	Port int `yaml:"port"`

	// BindHost is the network interface the HTTP API, health, and dashboard
	// servers bind to. Default "127.0.0.1" (loopback only). Setting a
	// non-loopback host (e.g. "0.0.0.0") exposes Prizm on the network and
	// requires api.auth_token (or api.auth_token_env) to be set — Validate
	// rejects a non-loopback bind without a token.
	BindHost string `yaml:"bind_host"`

	// LogLevel sets verbosity: debug, info, warn, error.
	LogLevel string `yaml:"log_level"`

	// Memory holds local memory store configuration (MarkdownStore).
	Memory MemoryConfig `yaml:"memory"`

	// ContextCompression configures identity context compression.
	// When enabled, the context agent compresses SOUL.md/AGENTS.md/etc. into
	// a ~300-token task-relevant block instead of dumping ~15KB raw.
	ContextCompression *agent.CompressionConfig `yaml:"context_compression"`

	// AllowedPaths is a list of additional directory roots the agent can access
	// beyond the workspace root. Paths are absolute or relative to CWD.
	// The workspace root is always implicitly allowed.
	// Example: ["/Users/ema/projects/repos", "/tmp/prizm-data"]
	AllowedPaths []string `yaml:"allowed_paths"`

	// ReadRoots grants recursive read/search/list access beyond the workspace root.
	// When empty, AllowedPaths is used for backward compatibility.
	ReadRoots []string `yaml:"read_roots"`

	// WriteRoots grants recursive approval-gated mutation access beyond the workspace root.
	// When empty, AllowedPaths is used for backward compatibility.
	WriteRoots []string `yaml:"write_roots"`

	// Scheduler configures cron-style scheduled tasks that fire NATS events.
	// V32: Event-driven wake replaces heartbeat babysitting.
	Scheduler SchedulerConfig `yaml:"scheduler"`

	// WorkflowConfig is the path to a gated-loop workflow definition (YAML or JSON).
	// When set and loadable, it overrides the built-in 7-phase DefaultConfig.
	// See examples/workflows/gated-loop.yaml.
	WorkflowConfig string `yaml:"workflow_config"`

	// AgentLoop controls which tool loop strategy agents use.
	// "classic" = capped iteration loop (default), "agentic" = while-true with doom detection.
	AgentLoop string `yaml:"agent_loop"`

	// ContextMode controls how workspace context is injected into prompts.
	// "full" (default) = all context files loaded into system prompt.
	// "open_book" = only file summaries/index loaded; model uses read_file on demand.
	ContextMode string `yaml:"context_mode"`
}

// APIServerConfig configures HTTP API authentication and CORS.
type APIServerConfig struct {
	// AuthToken is a static bearer token required on state-changing endpoints
	// (POST/PUT/DELETE/PATCH) and the SSE event stream. Empty disables auth,
	// which is only permitted when the server binds to loopback.
	AuthToken string `yaml:"auth_token"`

	// AuthTokenEnv names an environment variable holding the bearer token.
	// When set and non-empty it takes priority over AuthToken so the secret
	// can stay out of tracked config.
	AuthTokenEnv string `yaml:"auth_token_env"`

	// AllowedOrigins is the CORS origin allowlist for browser-based editors.
	// Empty means no cross-origin requests are permitted (same-origin only).
	// Use "*" only for trusted local development.
	AllowedOrigins []string `yaml:"allowed_origins"`

	// MaxRequestBytes caps the JSON request body accepted on mutating API
	// endpoints (invoke, editor, config, workflow, …). Default 1048576 (1 MiB).
	MaxRequestBytes int64 `yaml:"max_request_bytes"`

	// MaxWorkspaceFileBytes caps a single workspace markdown file write via the
	// workspace editor endpoint. Default 4194304 (4 MiB).
	MaxWorkspaceFileBytes int64 `yaml:"max_workspace_file_bytes"`
}

// ResolveAuthToken returns the effective API bearer token, preferring the
// environment variable named by AuthTokenEnv over the inline AuthToken.
func (a APIServerConfig) ResolveAuthToken() string {
	if a.AuthTokenEnv != "" {
		if v := os.Getenv(a.AuthTokenEnv); v != "" {
			return v
		}
	}
	return a.AuthToken
}

// CostConfig configures token-cost estimation overrides.
type CostConfig struct {
	// Pricing overrides or extends the built-in per-model price table. The key is
	// the model ID; values are USD per 1K tokens. Models omitted here keep their
	// built-in price (or are treated as free/local if unknown).
	Pricing map[string]ModelPricing `yaml:"pricing"`
}

// ModelPricing is a per-model price override in USD per 1K tokens.
type ModelPricing struct {
	Input  float64 `yaml:"input"`  // prompt price per 1K tokens
	Output float64 `yaml:"output"` // completion price per 1K tokens
}

// CostPricingOverrides converts the configured pricing table into the form the
// cost package consumes. Returns nil when no overrides are configured.
func (c *Config) CostPricingOverrides() map[string]cost.ModelPrice {
	if c == nil || len(c.Cost.Pricing) == 0 {
		return nil
	}
	out := make(map[string]cost.ModelPrice, len(c.Cost.Pricing))
	for model, p := range c.Cost.Pricing {
		out[model] = cost.ModelPrice{Input: p.Input, Output: p.Output}
	}
	return out
}

// UsageConfig configures the dashboard token-usage tracker's time windows.
type UsageConfig struct {
	// Windows overrides the built-in range→window/bucket mapping. Each entry names
	// a range (session|day|week|month|year|lifetime) and its lookback window and
	// bucket width as Go durations (e.g. window "168h", bucket "6h"). Days are
	// expressed in hours (30d = "720h"). Ranges omitted here keep their defaults.
	// The session range ignores window (it always starts at process start) and
	// lifetime ignores window (it always starts at 0); both still honor bucket.
	Windows []UsageWindowConfig `yaml:"windows"`
}

// UsageWindowConfig overrides one usage range's window and bucket width.
type UsageWindowConfig struct {
	Range  string `yaml:"range"`  // session|day|week|month|year|lifetime
	Window string `yaml:"window"` // lookback duration (Go duration); ignored for session/lifetime
	Bucket string `yaml:"bucket"` // bucket width (Go duration)
}

// IsLoopbackHost reports whether host refers to the loopback interface.
// An empty host is treated as loopback because BindAddr defaults to 127.0.0.1.
func IsLoopbackHost(host string) bool {
	switch strings.TrimSpace(host) {
	case "", "127.0.0.1", "localhost", "::1", "[::1]":
		return true
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// BindAddr returns the host:port listen address for a server, defaulting the
// host to loopback when prizm.bind_host is unset.
func (c *Config) BindAddr(port int) string {
	host := c.Prizm.BindHost
	if strings.TrimSpace(host) == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// BridgeConfig configures signed cross-Prizm NATS subjects.
type BridgeConfig struct {
	// Enabled controls whether the cross-Prizm protocol listener starts.
	Enabled bool `yaml:"enabled"`

	// Mode documents the topology. "shared_nats" means both Prizms use the same broker.
	Mode string `yaml:"mode"`

	// AllowedSubjects is the explicit protocol subject allowlist.
	AllowedSubjects []string `yaml:"allowed_subjects"`

	// SecretEnv names the environment variable containing the shared HMAC secret.
	SecretEnv string `yaml:"secret_env"`

	// Secret is an optional local fallback HMAC secret. Prefer SecretEnv for
	// shared or production environments so the secret stays out of tracked config.
	Secret string `yaml:"secret"`

	// LeaderInstance is the Prizm instance currently allowed to coordinate a
	// cross-Prizm thread. It is configurable so leadership can move between
	// environments without changing code.
	LeaderInstance string `yaml:"leader_instance"`

	// ConfidenceThreshold is the receiver-rated threshold required before a
	// task is accepted without clarification.
	ConfidenceThreshold float64 `yaml:"confidence_threshold"`

	// MaxClarificationRounds caps back-and-forth clarification turns before the
	// task needs human input.
	MaxClarificationRounds int `yaml:"max_clarification_rounds"`

	// TargetProfiles define addressable cross-Prizm destinations for commands.
	TargetProfiles []BridgeTargetProfile `yaml:"target_profiles"`

	// Factory configures optional Roblox Factory task handoff for task_request messages.
	Factory FactoryBridgeConfig `yaml:"factory"`
}

// BridgeTargetProfile maps a human command target to a Prizm instance and adapter.
type BridgeTargetProfile struct {
	Name         string   `yaml:"name"`
	InstanceID   string   `yaml:"instance_id"`
	Adapter      string   `yaml:"adapter"`
	Capabilities []string `yaml:"capabilities"`
}

// CodexConfig configures local Codex CLI task execution.
type CodexConfig struct {
	Enabled        bool     `yaml:"enabled"`
	Executable     string   `yaml:"executable"`
	Model          string   `yaml:"model"`
	Profile        string   `yaml:"profile"`
	Workspace      string   `yaml:"workspace"`
	Sandbox        string   `yaml:"sandbox"`
	ApprovalPolicy string   `yaml:"approval_policy"`
	TimeoutMinutes int      `yaml:"timeout_minutes"`
	MaxConcurrency int      `yaml:"max_concurrency"`
	CaptureDiff    bool     `yaml:"capture_diff"`
	ExtraArgs      []string `yaml:"extra_args"`
}

// ClaudeCodeConfig configures the Claude Code CLI sub-agent reviewer. When
// enabled, a reviewer service watches for paused gated-loop feedback gates and,
// if the gate requires the configured reviewer name, runs `claude -p` to produce
// an approve / changes_requested verdict automatically.
type ClaudeCodeConfig struct {
	Enabled        bool     `yaml:"enabled"`
	Executable     string   `yaml:"executable"`      // CLI binary (default: claude)
	Model          string   `yaml:"model"`           // optional --model override
	ReviewerName   string   `yaml:"reviewer_name"`   // gate approver/reviewer name this fulfills (default: claude)
	TimeoutMinutes int      `yaml:"timeout_minutes"` // per-review timeout (default: 10)
	AllowedTools   string   `yaml:"allowed_tools"`   // --allowedTools whitelist (read-only review tools)
	ExtraArgs      []string `yaml:"extra_args"`      // additional CLI args
}

// AutopatchConfig configures the bug diagnosis and patch proposal loop.
type AutopatchConfig struct {
	Enabled              bool     `yaml:"enabled"`
	Mode                 string   `yaml:"mode"` // "propose" (patch artifact) or "pr" (open a pull request)
	RequireCleanWorktree *bool    `yaml:"require_clean_worktree"`
	MaxAttempts          int      `yaml:"max_attempts"`
	ValidationProfiles   []string `yaml:"validation_profiles"`
	WorkerOrder          []string `yaml:"worker_order"`
	LocalAgent           string   `yaml:"local_agent"`
	WorktreeRoot         string   `yaml:"worktree_root"`
	BaseBranch           string   `yaml:"base_branch"` // PR base branch in "pr" mode (empty → repo default)
}

// FactoryMonitorConfig configures local Factory queue status notifications.
type FactoryMonitorConfig struct {
	Enabled           bool   `yaml:"enabled"`
	Root              string `yaml:"root"`
	NotifyChannelID   string `yaml:"notify_channel_id"`
	PollSeconds       int    `yaml:"poll_seconds"`
	StuckAfterMinutes int    `yaml:"stuck_after_minutes"`
}

// ShellConfig configures the shell tool access control for free mode.
type ShellConfig struct {
	// MasterUserID is the Discord user ID that can trigger free mode.
	// Only this user gets direct shell access in free-mode channels.
	MasterUserID string `yaml:"master_user_id"`

	// Allowlists maps tier names to glob-style command patterns.
	// tier_1: build/test/git only
	// tier_2: expanded dev commands
	// tier_3: full access ("*" pattern)
	Allowlists map[string][]string `yaml:"allowlists"`

	// Defaults holds default values for shell tool execution.
	Defaults ShellDefaults `yaml:"defaults"`
}

// ShellDefaults holds default values for shell tool execution.
type ShellDefaults struct {
	// TimeoutSeconds is the default command timeout in seconds.
	TimeoutSeconds int `yaml:"timeout_seconds"`

	// MaxOutputBytes is the maximum stdout bytes to return.
	MaxOutputBytes int `yaml:"max_output_bytes"`

	// BlockedPatterns are additional patterns to block beyond the hard blocklist.
	BlockedPatterns []string `yaml:"blocked_patterns"`
}

// FactoryBridgeConfig configures report/validation-only handoff to Roblox Factory.
type FactoryBridgeConfig struct {
	Enabled            bool   `yaml:"enabled"`
	Root               string `yaml:"root"`
	Project            string `yaml:"project"`
	ProjectPath        string `yaml:"project_path"`
	ApprovalMode       string `yaml:"approval_mode"`
	RunCodex           bool   `yaml:"run_codex"`
	VisionReview       string `yaml:"vision_review"`
	PlaytestMode       string `yaml:"playtest_mode"`
	EnableUIGeneration bool   `yaml:"enable_ui_generation"`
	UIGenerationDryRun bool   `yaml:"ui_generation_dry_run"`
}

// AgentConfig defines a single agent in prizm.yaml.
//
// ID becomes the event namespace prefix. If omitted, auto-generated
// as prizm1, prizm2, etc. No hardcoded names except "prizm" for system.
type AgentConfig struct {
	// ID is the agent's unique identifier and event namespace prefix.
	// If omitted, auto-generated as prizm1, prizm2, etc.
	ID string `yaml:"id"`

	// Role describes what this agent does: lead, coder, researcher, etc.
	Role string `yaml:"role"`

	// Provider is the LLM provider: ollama, openai, anthropic, gemini.
	Provider string `yaml:"provider"`

	// Model is the model identifier: glm-5.1:cloud, gpt-4o, etc.
	Model string `yaml:"model"`

	// Fallbacks are attempted in order after the primary model fails. Each
	// fallback names its provider explicitly so a model ID never silently routes
	// through the wrong backend.
	Fallbacks []ModelFallback `yaml:"fallbacks,omitempty"`
	// Context lists which context sources to inject: soul, agents, user, etc.
	Context []string `yaml:"context"`

	// ConversationPostfix is appended to the system prompt to shape conversation behavior.
	// It primes the model to stay present, ask follow-ups, and avoid premature closures.
	// Example: "Stay curious. Ask follow-up questions. Don't wrap up unless the topic is genuinely resolved."
	ConversationPostfix string `yaml:"conversation_postfix"`

	// Primary marks this agent as the default for unaddressed messages.
	Primary bool `yaml:"primary"`

	// Capabilities lists what this agent can do.
	Capabilities []string `yaml:"capabilities"`

	// AgentLoop overrides the global agent_loop setting for this agent.
	// Values: "classic" (default), "agentic". Empty means use global setting.
	AgentLoop string `yaml:"agent_loop"`

	// ContextMode overrides the global context_mode setting for this agent.
	// Values: "full" (default), "open_book". Empty means use global setting.
	ContextMode string `yaml:"context_mode"`

	// Subscriptions lists NATS subjects this agent subscribes to
	// for receiving delegated tasks and results.
	// e.g., "mango.task.created" — Mango receives tasks from Lumi.
	Subscriptions []string `yaml:"subscriptions"`

	// ListenToAgents lists bot user IDs that this agent should respond to.
	// By default, Prizm ignores messages from other bots. Adding a bot ID here
	// tells Prizm to treat messages from that bot as agent-to-agent communication.
	// The message is processed with a modified prompt that frames it as peer input.
	ListenToAgents []string `yaml:"listen_to_agents"`

	// StateActions maps context names (channel roles, "agent", etc.) to prompt
	// instructions that are injected when the agent is in that state.
	// Key examples: "manager-room", "build-room", "fun", "agent".
	// The "inject" field is appended to the system prompt after conversation_postfix
	// but before tool instructions.
	StateActions map[string]StateAction `yaml:"state_actions"`

	// InvocableViaAPI allows this agent to be called by external processes
	// through POST /api/v1/agents/{id}/invoke. Defaults to false — an agent
	// must opt in explicitly, since this is a new network-reachable surface
	// that lets any caller with API access trigger a real (billed) LLM call.
	InvocableViaAPI bool `yaml:"invocable_via_api"`

	// FirstClassTools gives the agent direct tool access without per-call
	// policy evaluation. When true, the agent's system prompt includes tool
	// descriptions as first-class capabilities (like OpenClaw) and tool calls
	// execute directly without going through the policy gate loop.
	// This is intended for trusted agents in free mode — the policy engine
	// still applies in gated mode. Use this for agents that need fluid, low-latency
	// tool access (reading files, running commands, searching memory) without
	// the overhead of the tool-loop + policy evaluation cycle.
	// Requires: the agent must be in a channel with mode: free or be triggered
	// by an autonomous action (auto_patch, project_work, etc.).
	FirstClassTools bool `yaml:"first_class_tools"`
}

// ModelFallback describes one ordered provider/model fallback for an agent.
type ModelFallback struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
}

// ProjectConfig describes an assignable project the gated loop can work on.
// It replaces hardcoded repo paths and channel IDs so projects are dynamic.
type ProjectConfig struct {
	// ID is the project's unique identifier (e.g. "bassbook").
	ID string `yaml:"id"`

	// RepoPath is the absolute path to the project's git repository.
	RepoPath string `yaml:"repo_path"`

	// StateFile is an optional project state file the agent reads for task
	// assignment (e.g. "PROJECT_STATE.md"). Relative to RepoPath if not absolute.
	StateFile string `yaml:"state_file"`

	// DefaultBranch names the project's main branch. Enforced by
	// GitCommitTool and GitPushTool (internal/tool/git.go): those mutation
	// tools refuse to commit/push while the resolved repo's current branch
	// — or, for push, an explicitly targeted branch param — equals this
	// value (see Config.ProtectedBranch). This check is unconditional; it
	// applies even when AutoApproveMutations/Free Mode is active.
	// git_checkout is unaffected — checking out the branch is not itself a
	// mutation. Defaults to "main" when empty. Note: this is a single
	// global value resolved from the default project; in multi-project
	// deployments with differing default_branch values, only the default
	// project's branch name is protected across every repo any git tool
	// touches.
	DefaultBranch string `yaml:"default_branch"`

	// Channel is the messaging channel ID where results/feedback are posted.
	Channel string `yaml:"channel"`

	// WorkflowConfig optionally overrides the global workflow definition for
	// this project (path to a gated-loop YAML/JSON file).
	WorkflowConfig string `yaml:"workflow_config"`

	// TokenBudget optionally overrides the workflow's global max_total_tokens for
	// this project. -1 = unlimited, 0 uses the workflow/default cap, >0 = explicit cap.
	TokenBudget int `yaml:"token_budget"`

	// Orchestrator optionally names the agent ID whose model drives this
	// project's gated loop. Empty falls back to the primary agent. Use this to
	// point a project at a Claude Code (subscription) brain, e.g. an agent with
	// provider "claude_code", without changing the global default.
	Orchestrator string `yaml:"orchestrator"`

	// WorktreeIsolation runs each gated loop in its own git worktree under
	// <repo>/.prizm/worktrees/<run-id> on a fresh prizm/<run-id> branch (V56).
	// Parallel runs on the same repo cannot collide, and the main worktree is
	// never touched. Default false (runs share the main worktree, isolated by
	// feature branch only).
	WorktreeIsolation bool `yaml:"worktree_isolation"`

	// Default marks this project as the one used when no project is specified.
	Default bool `yaml:"default"`
}

// FindProject returns the project with the given ID, or nil if not found.
func (c *Config) FindProject(id string) *ProjectConfig {
	for i := range c.Projects {
		if c.Projects[i].ID == id {
			return &c.Projects[i]
		}
	}
	return nil
}

// DefaultProject returns the project marked default, or the first project, or nil.
func (c *Config) DefaultProject() *ProjectConfig {
	for i := range c.Projects {
		if c.Projects[i].Default {
			return &c.Projects[i]
		}
	}
	if len(c.Projects) > 0 {
		return &c.Projects[0]
	}
	return nil
}

// ProtectedBranch returns the branch name git mutation tools must refuse to
// commit/push to directly: the default project's configured DefaultBranch,
// or "main" if unset or no project is configured. This is a single global
// value — in multi-project deployments with differing default_branch
// values, only the default project's branch name is protected across every
// repo a git tool touches.
func (c *Config) ProtectedBranch() string {
	if c != nil {
		if p := c.DefaultProject(); p != nil && p.DefaultBranch != "" {
			return p.DefaultBranch
		}
	}
	return "main"
}

// StateAction defines behavior modifiers for a specific context state.
type StateAction struct {
	// Inject is the text to append to the system prompt when this state is active.
	// It is inserted after conversation_postfix and before tool instructions.
	Inject string `yaml:"inject"`
}

// SchedulerConfig configures cron-style scheduled tasks.
// V32: Event-driven wake replaces heartbeat babysitting.
type SchedulerConfig struct {
	// Enabled controls whether the scheduler runs. Default: false.
	Enabled bool `yaml:"enabled"`

	// Jobs defines the scheduled tasks.
	Jobs []SchedulerJobConfig `yaml:"jobs"`
}

// SchedulerJobConfig defines a single scheduled task.
type SchedulerJobConfig struct {
	// Name is a human-readable identifier (e.g., "daily-review").
	Name string `yaml:"name"`

	// Schedule is a cron expression (minute hour dayOfMonth month dayOfWeek).
	// Example: "0 3 * * *" for daily at 3:00 AM.
	Schedule string `yaml:"schedule"`

	// Event is the NATS subject to publish when the job fires.
	// Example: "prizm.task.scheduled"
	Event string `yaml:"event"`

	// Payload is the JSON payload for the event.
	Payload map[string]any `yaml:"payload"`

	// Enabled controls whether this job is active. Default: true.
	Enabled bool `yaml:"enabled"`
}

// ChannelRole maps a Discord channel ID to a role name that determines
// which state action (if any) applies when the agent is in that channel.
// V33: Includes tool filtering, personality, and structured channel context.
// Project focus is NOT hardcoded — the agent switches context dynamically
// based on conversation. The channel provides vibes, not project scope.
// V60: Adds Mode (gated/free) and ShellPolicy for freedom mode.
type ChannelRole struct {
	// ID is the Discord channel ID.
	ID string `yaml:"id"`

	// Role is the state action key to activate (e.g., "manager-room", "fun").
	Role string `yaml:"role"`

	// Mode controls the operating mode for this channel.
	// "gated" (default) = phase-gated workflow with proposal/approval.
	// "free" = direct execution, no phase gates, all tools available.
	Mode string `yaml:"mode,omitempty"`

	// ShellPolicy controls the shell access tier for this channel.
	// "none" | "tier_1" | "tier_2" | "tier_3".
	// Only used when Mode is "free" or when shell tool is registered.
	ShellPolicy string `yaml:"shell_policy,omitempty"`

	// Tools controls which tools are available in this channel.
	// "all" = all tools, "read-only" = only read tools, "none" = no tools.
	// When empty, defaults to "all".
	Tools string `yaml:"tools,omitempty"`

	// Personality controls the agent's communication style in this channel.
	// "direct" = make decisions, push back, no menus
	// "terse" = structured data, concise, no pleasantries
	// "bubbly" = exaggerated personality, playful, enthusiastic
	// "social" = warm, conversational, present
	// When empty, uses the agent's conversation_postfix.
	Personality string `yaml:"personality,omitempty"`
	// TTS enables voice messages in this channel. Overrides global TTS setting.
	// When true and global TTS is enabled, responses are also sent as voice messages.
	TTS bool `yaml:"tts,omitempty"`

	// Context is structured channel context that replaces state_actions.inject.
	// It provides rich context about where the agent is, who it's talking to,
	// and what's expected — but NOT which project (that's dynamic).
	// When empty, falls back to state_actions.inject.
	Context string `yaml:"context,omitempty"`

	// TaggedOnly means the agent only responds when directly mentioned (@Lumi).
	// When true, messages that don't mention the bot are skipped entirely.
	TaggedOnly bool `yaml:"tagged_only,omitempty"`
}

// personalityDirectives maps each recognized ChannelRole.Personality value to
// a one-line tone instruction injected into the prompt (see PersonalityDirective).
// Keep this in sync with the Personality field's doc comment above.
var personalityDirectives = map[string]string{
	"direct": "Make decisions and state them plainly. Push back when something's wrong instead of hedging. Skip menus of options — pick one and say why.",
	"terse":  "Be concise. Prefer structured data over prose. No pleasantries, no throat-clearing, no filler words.",
	"bubbly": "Playful and enthusiastic — exaggerated, expressive, high energy is welcome here.",
	"social": "Warm and conversational. Be present, ask follow-up questions, don't rush to wrap up.",
}

// PersonalityDirective returns the prompt instruction for a ChannelRole's
// Personality value, or "" if personality is empty or not recognized. An
// unrecognized value is treated as no directive rather than an error, so a
// typo in prizm.yaml degrades to today's silent-no-op behavior instead of
// failing config load.
func PersonalityDirective(personality string) string {
	return personalityDirectives[personality]
}

// ChannelConfig defines a messaging channel connection.
type ChannelConfig struct {
	// Type is the channel type: discord, telegram, webchat.
	Type string `yaml:"type"`

	// Token is the bot authentication token.
	Token string `yaml:"token"`

	// Channels is a list of channel/room IDs to listen on.
	Channels []string `yaml:"channels"`
}

// UserConfig maps channel-specific user IDs to one durable Prizm owner ID.
type UserConfig struct {
	ID          string              `yaml:"id"`
	DisplayName string              `yaml:"display_name"`
	Default     bool                `yaml:"default"`
	Aliases     map[string][]string `yaml:"aliases"`
}

// ResolveOwnerID maps an external channel user ID to a stable owner ID.
// If no alias matches, the configured default owner is used. If no default is
// configured, Prizm falls back to the external ID to avoid cross-user leakage.
func (c *Config) ResolveOwnerID(channelType, externalID string) string {
	if c == nil {
		return externalID
	}
	for _, u := range c.Users {
		if userHasAlias(u, channelType, externalID) {
			return u.ID
		}
	}
	for _, u := range c.Users {
		if u.Default && u.ID != "" {
			return u.ID
		}
	}
	return externalID
}

// OwnerAliases returns all known local and external IDs that may appear in old
// session rows for the same owner.
func (c *Config) OwnerAliases(ownerID string) []string {
	seen := map[string]bool{}
	var aliases []string
	add := func(v string) {
		if v != "" && !seen[v] {
			seen[v] = true
			aliases = append(aliases, v)
		}
	}
	add(ownerID)
	if c == nil {
		return aliases
	}
	for _, u := range c.Users {
		if u.ID != ownerID {
			continue
		}
		for _, values := range u.Aliases {
			for _, v := range values {
				add(v)
			}
		}
	}
	return aliases
}

func userHasAlias(u UserConfig, channelType, externalID string) bool {
	if u.ID == externalID {
		return true
	}
	for _, v := range u.Aliases[channelType] {
		if v == externalID {
			return true
		}
	}
	return false
}

// ResolveChannelRole returns the state action key for a given channel ID.
// Returns empty string if no role is configured for the channel.
func (c *Config) ResolveChannelRole(channelID string) string {
	cr := c.ResolveChannelRoleConfig(channelID)
	if cr != nil {
		return cr.Role
	}
	return ""
}

// ResolveChannelRoleConfig returns the full ChannelRole config for a given channel ID.
// Returns nil if no role is configured for the channel.
func (c *Config) ResolveChannelRoleConfig(channelID string) *ChannelRole {
	for i := range c.ChannelRoles {
		if c.ChannelRoles[i].ID == channelID {
			return &c.ChannelRoles[i]
		}
	}
	return nil
}

// ResolveStateAction returns the StateAction for a given key.
// It searches all agents (primary first) for a matching state action.
func (c *Config) ResolveStateAction(agentID, key string) *StateAction {
	for _, a := range c.Agents {
		if a.ID == agentID {
			if sa, ok := a.StateActions[key]; ok {
				return &sa
			}
		}
	}
	return nil
}

// ActionConfig defines an event-triggered action.
type ActionConfig struct {
	// Trigger is the event type pattern to match.
	// Supports wildcards: "*.tool.completed", "lumi.agent.output"
	Trigger string `yaml:"trigger"`

	// Action is the action to execute when the trigger fires.
	// Format: "<namespace>.<action>" e.g., "remembrance.gate.extract"
	Action string `yaml:"action"`

	// Enabled controls whether this action is active.
	Enabled bool `yaml:"enabled"`
}

// SessionConfig controls session management behavior.
type SessionConfig struct {
	// IdleTimeoutMinutes is how long before an idle session resets.
	IdleTimeoutMinutes int `yaml:"idle_timeout_minutes"`

	// DailyResetHour is the hour (0-23) at which sessions reset daily.
	DailyResetHour int `yaml:"daily_reset_hour"`

	// MaxContextMessages is the maximum messages in a session before truncation.
	MaxContextMessages int `yaml:"max_context_messages"`

	// CompactionStrategy controls how sessions are compacted.
	// "truncate" removes oldest messages (V20). "summarize" uses Remembrance (V21).
	CompactionStrategy string `yaml:"compaction_strategy"`

	// Persistence enables durable resume of prior sessions from SQLite.
	Persistence bool `yaml:"persistence"`

	// ResumeAfterIdle reuses the latest session even after IdleTimeoutMinutes.
	ResumeAfterIdle bool `yaml:"resume_after_idle"`

	// KeepArchivedMessages preserves compacted messages in SQLite instead of deleting them.
	KeepArchivedMessages bool `yaml:"keep_archived_messages"`

	// ContinuityScope controls session reuse. "owner_agent" keeps one live
	// conversation per owner and agent across channels. "channel_user" preserves
	// the historical channel+user behavior.
	ContinuityScope string `yaml:"continuity_scope"`

	// RecallWindowMode controls recent local memory bounds. "calendar_week"
	// recalls from the current week start. "rolling_days" uses ShortTermWindowDays.
	RecallWindowMode string `yaml:"recall_window_mode"`

	// RecallTimezone is the IANA timezone for calendar week recall. "Local"
	// uses the machine local timezone.
	RecallTimezone string `yaml:"recall_timezone"`

	// ShortTermWindowDays controls local recent-memory lookup across sessions.
	ShortTermWindowDays int `yaml:"short_term_window_days"`

	// VerbatimRecentMessages caps exact recent local memory injected from other
	// sessions. Current session history remains controlled by MaxContextMessages.
	VerbatimRecentMessages int `yaml:"verbatim_recent_messages"`

	// InvokeIdleTimeoutHours resets a stateful Agent Invocation API conversation
	// (POST /api/v1/agents/{id}/invoke with a conversation_id) after this many idle
	// hours, as a safety net so abandoned threads don't linger forever. Distinct
	// from IdleTimeoutMinutes, which governs the built-in chat/bot pipeline. 0
	// disables the safety net (a conversation stays open until an explicit
	// stop/switch or the MaxContextMessages compaction cap).
	InvokeIdleTimeoutHours int `yaml:"invoke_idle_timeout_hours"`
}

// RemembranceConfig configures the memory service connection.
type RemembranceConfig struct {
	// Enabled controls whether Remembrance integration is active.
	Enabled bool `yaml:"enabled"`

	// URL is the Remembrance service URL.
	URL string `yaml:"url"`

	// TimeoutSeconds is the HTTP timeout for Remembrance requests.
	// Default: 60 seconds. Capture may run synchronous extraction before returning.
	TimeoutSeconds int `yaml:"timeout_seconds"`
}

// MemoryConfig configures the local memory store (Phase 1: MarkdownStore).
type MemoryConfig struct {
	// StoreType is "markdown" (Phase 1) or "sqlite" (Phase 2, future).
	StoreType string `yaml:"store_type"`

	// StorePath is the path to the memory directory, relative to workspace root.
	StorePath string `yaml:"store_path"`

	// GateModel is the primary Ollama model for gate decisions.
	GateModel string `yaml:"gate_model"`

	// ExtractModel is the primary Ollama model for memory extraction.
	ExtractModel string `yaml:"extract_model"`

	// ModelFallbackChain is the ordered list of models to try for gate/extract.
	ModelFallbackChain []string `yaml:"model_fallback_chain"`

	// OllamaURL is the Ollama API endpoint.
	OllamaURL string `yaml:"ollama_url"`

	// AutoCapture controls whether conversation turns are automatically gated.
	AutoCapture bool `yaml:"auto_capture"`

	// MinImportance is the gate threshold (0.0-1.0).
	MinImportance float64 `yaml:"min_importance"`

	// MaxMemoriesPerTurn limits extraction from a single turn.
	MaxMemoriesPerTurn int `yaml:"max_memories_per_turn"`

	// RecallSync controls whether to push memories to Recall after local storage.
	RecallSync string `yaml:"recall_sync"` // "async" or "off"

	// QueryPlanner controls the model-driven keyword extraction for memory search.
	QueryPlannerEnabled  bool          `yaml:"query_planner_enabled"`
	QueryPlannerModel    string        `yaml:"query_planner_model"`
	QueryPlannerTimeoutS int           `yaml:"query_planner_timeout_s"`
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Prizm: PrizmConfig{
			InstanceID:         "prizm",
			NATSURL:            "",
			Port:               8321,
			BindHost:           "127.0.0.1",
			DataDir:            filepath.Join(os.Getenv("HOME"), ".prizm", "data"),
			RunsDir:            "runs",
			OllamaURL:          "http://localhost:11434",
			LogLevel:           "info",
			ContextTokenBudget: 4000,
			LLMTimeoutSeconds:  1200,
		},
		API: APIServerConfig{
			MaxRequestBytes:       1 << 20, // 1 MiB
			MaxWorkspaceFileBytes: 4 << 20, // 4 MiB
		},
		Sessions: SessionConfig{
			IdleTimeoutMinutes:     30,
			DailyResetHour:         4,
			MaxContextMessages:     100,
			CompactionStrategy:     "summarize",
			Persistence:            true,
			ResumeAfterIdle:        true,
			KeepArchivedMessages:   true,
			ContinuityScope:        "owner_agent",
			RecallWindowMode:       "calendar_week",
			RecallTimezone:         "Local",
			ShortTermWindowDays:    7,
			VerbatimRecentMessages: 40,
			InvokeIdleTimeoutHours: 36,
		},
		Remembrance: RemembranceConfig{
			Enabled:        false,
			URL:            "http://localhost:18790",
			TimeoutSeconds: 60,
		},
		Memory: MemoryConfig{
			StoreType:          "markdown",
			StorePath:          "memory",
			GateModel:          "nemotron-3-nano:4b",
			ExtractModel:       "nemotron-3-nano:4b",
			ModelFallbackChain: []string{"nemotron-3-nano:4b", "qwen3.5:4b"},
			OllamaURL:          "http://localhost:11434",
			AutoCapture:        true,
			MinImportance:      0.5,
			MaxMemoriesPerTurn: 3,
			RecallSync:         "async",
		},
		Bridge: BridgeConfig{
			Enabled: false,
			Mode:    "shared_nats",
			AllowedSubjects: []string{
				"prizm.cross.context_sync",
				"prizm.cross.task_request",
				"prizm.cross.status_request",
				"prizm.cross.validation_request",
				"prizm.cross.task_response",
				"prizm.cross.task_accept",
				"prizm.cross.task_reject",
				"prizm.cross.clarification",
				"prizm.cross.task_progress",
				"prizm.cross.task_result",
				"prizm.cross.task_cancel",
			},
			LeaderInstance:         "lumi-ceo",
			ConfidenceThreshold:    0.75,
			MaxClarificationRounds: 1,
			TargetProfiles: []BridgeTargetProfile{
				{
					Name:       "generic",
					InstanceID: "astraea-manager",
					Adapter:    "generic",
					Capabilities: []string{
						"plan",
						"review",
						"report",
					},
				},
				{
					Name:       "factory",
					InstanceID: "astraea-manager",
					Adapter:    "factory",
					Capabilities: []string{
						"roblox_factory",
						"validation",
						"report",
					},
				},
				{
					Name:       "codex",
					InstanceID: "astraea-manager",
					Adapter:    "codex",
					Capabilities: []string{
						"code",
						"test",
						"review",
						"report",
					},
				},
			},
			SecretEnv: "PRIZM_BRIDGE_SECRET",
			Factory: FactoryBridgeConfig{
				Enabled:            false,
				Root:               `D:\_projects_\roblox-factory`,
				Project:            "eggventura",
				ProjectPath:        `D:\Projects\Roblox\eggventura`,
				ApprovalMode:       "report_only",
				RunCodex:           false,
				VisionReview:       "none",
				PlaytestMode:       "none",
				UIGenerationDryRun: true,
			},
		},
		Codex: CodexConfig{
			Enabled:        false,
			Executable:     "",
			Model:          "",
			Profile:        "",
			Workspace:      "",
			Sandbox:        "workspace-write",
			ApprovalPolicy: "on-request",
			TimeoutMinutes: 30,
			MaxConcurrency: 1,
			CaptureDiff:    true,
		},
		Autopatch: AutopatchConfig{
			Enabled:              false,
			Mode:                 "propose",
			RequireCleanWorktree: boolPtr(true),
			MaxAttempts:          2,
			ValidationProfiles:   []string{"go_test_all"},
			WorkerOrder:          []string{"codex", "local_agent"},
			LocalAgent:           "forge",
			WorktreeRoot:         filepath.Join(".prizm", "worktrees"),
		},
		FactoryMonitor: FactoryMonitorConfig{
			Enabled:           false,
			Root:              `D:\_projects_\roblox-factory`,
			PollSeconds:       30,
			StuckAfterMinutes: 30,
		},
		Shell: ShellConfig{
			Defaults: ShellDefaults{
				TimeoutSeconds: 30,
				MaxOutputBytes: 10240,
				BlockedPatterns: []string{
					"rm -rf /",
					"rm -rf ~",
					"rm -rf *",
					"mkfs*",
					"dd if=*of=/dev/*",
					"> /dev/sd*",
					"chmod 777",
				},
			},
		},
	}
}

// Validate checks that the configuration is valid.
func (c *Config) Validate() error {
	// Validate agents
	seenIDs := make(map[string]bool)
	autoGenCounter := 0

	// Validate global context_mode and agent_loop
	validContextModes := map[string]bool{"full": true, "open_book": true}
	if c.Prizm.ContextMode != "" && !validContextModes[c.Prizm.ContextMode] {
		return fmt.Errorf("config: prizm.context_mode must be \"full\" or \"open_book\", got %q", c.Prizm.ContextMode)
	}
	validAgentLoops := map[string]bool{"classic": true, "agentic": true}
	if c.Prizm.AgentLoop != "" && !validAgentLoops[c.Prizm.AgentLoop] {
		return fmt.Errorf("config: prizm.agent_loop must be \"classic\" or \"agentic\", got %q", c.Prizm.AgentLoop)
	}

	for i, a := range c.Agents {
		id := a.ID
		if id == "" {
			autoGenCounter++
			id = fmt.Sprintf("prizm%d", autoGenCounter)
		}

		if a.ContextMode != "" && !validContextModes[a.ContextMode] {
			return fmt.Errorf("config: agent[%d] %q context_mode must be \"full\" or \"open_book\", got %q", i, id, a.ContextMode)
		}
		if a.AgentLoop != "" && !validAgentLoops[a.AgentLoop] {
			return fmt.Errorf("config: agent[%d] %q agent_loop must be \"classic\" or \"agentic\", got %q", i, id, a.AgentLoop)
		}

		// Agent ID must be alphanumeric + hyphens (same rule as agent.Agent.Validate)
		if !isValidAgentID(id) {
			return fmt.Errorf("config: agent[%d] id %q must be alphanumeric + hyphens only", i, id)
		}

		if seenIDs[id] {
			return fmt.Errorf("config: duplicate agent id %q", id)
		}
		seenIDs[id] = true

		if a.Role == "" {
			return fmt.Errorf("config: agent[%d] %q missing role", i, id)
		}

		if a.Model == "" {
			return fmt.Errorf("config: agent[%d] %q missing model", i, id)
		}
		seenTargets := map[string]bool{a.Provider + "::" + a.Model: true}
		for j, fallback := range a.Fallbacks {
			if fallback.Provider == "" || fallback.Model == "" {
				return fmt.Errorf("config: agent[%d] %q fallback[%d] requires provider and model", i, id, j)
			}
			key := fallback.Provider + "::" + fallback.Model
			if seenTargets[key] {
				return fmt.Errorf("config: agent[%d] %q fallback[%d] duplicates a prior target", i, id, j)
			}
			seenTargets[key] = true
		}
	}

	// Validate channels
	for i, ch := range c.Channels {
		if ch.Type == "" {
			return fmt.Errorf("config: channel[%d] missing type", i)
		}
		if ch.Token == "" {
			return fmt.Errorf("config: channel[%d] missing token", i)
		}
	}

	// Validate actions
	for i, act := range c.Actions {
		if act.Trigger == "" {
			return fmt.Errorf("config: action[%d] missing trigger", i)
		}
		if act.Action == "" {
			return fmt.Errorf("config: action[%d] missing action", i)
		}
	}

	// Validate sessions
	if c.Sessions.MaxContextMessages < 1 {
		return fmt.Errorf("config: max_context_messages must be >= 1")
	}
	if c.Sessions.CompactionStrategy != "truncate" && c.Sessions.CompactionStrategy != "summarize" {
		return fmt.Errorf("config: compaction_strategy must be 'truncate' or 'summarize'")
	}
	if c.Sessions.ContinuityScope == "" {
		c.Sessions.ContinuityScope = "owner_agent"
	}
	if c.Sessions.ContinuityScope != "owner_agent" && c.Sessions.ContinuityScope != "channel_user" {
		return fmt.Errorf("config: continuity_scope must be 'owner_agent' or 'channel_user'")
	}
	if c.Sessions.RecallWindowMode == "" {
		c.Sessions.RecallWindowMode = "calendar_week"
	}
	if c.Sessions.RecallWindowMode != "calendar_week" && c.Sessions.RecallWindowMode != "rolling_days" {
		return fmt.Errorf("config: recall_window_mode must be 'calendar_week' or 'rolling_days'")
	}
	if c.Sessions.RecallTimezone == "" {
		c.Sessions.RecallTimezone = "Local"
	}
	if c.Sessions.RecallTimezone != "Local" {
		if _, err := time.LoadLocation(c.Sessions.RecallTimezone); err != nil {
			return fmt.Errorf("config: recall_timezone %q is invalid: %w", c.Sessions.RecallTimezone, err)
		}
	}
	if c.Sessions.ShortTermWindowDays < 0 {
		return fmt.Errorf("config: short_term_window_days must be >= 0")
	}
	if c.Sessions.VerbatimRecentMessages < 0 {
		return fmt.Errorf("config: verbatim_recent_messages must be >= 0")
	}
	if c.Prizm.LLMTimeoutSeconds < 0 {
		return fmt.Errorf("config: llm_timeout_seconds must be >= 0")
	}
	if c.Remembrance.TimeoutSeconds < 0 {
		return fmt.Errorf("config: remembrance.timeout_seconds must be >= 0")
	}
	if c.Prizm.RunsDir == "" {
		c.Prizm.RunsDir = "runs"
	}
	// API request-body caps: default when unset, reject negatives.
	if c.API.MaxRequestBytes == 0 {
		c.API.MaxRequestBytes = 1 << 20
	}
	if c.API.MaxRequestBytes < 0 {
		return fmt.Errorf("config: api.max_request_bytes must be > 0")
	}
	if c.API.MaxWorkspaceFileBytes == 0 {
		c.API.MaxWorkspaceFileBytes = 4 << 20
	}
	if c.API.MaxWorkspaceFileBytes < 0 {
		return fmt.Errorf("config: api.max_workspace_file_bytes must be > 0")
	}
	// Cost pricing overrides must be non-negative (0 = free/local is valid).
	for model, p := range c.Cost.Pricing {
		if p.Input < 0 || p.Output < 0 {
			return fmt.Errorf("config: cost.pricing[%q] input/output must be >= 0", model)
		}
	}
	// Usage windows: validate range keys, and that windows/buckets parse.
	for i, w := range c.Usage.Windows {
		switch w.Range {
		case "session", "day", "week", "month", "year", "lifetime":
		default:
			return fmt.Errorf("config: usage.windows[%d].range %q must be one of session|day|week|month|year|lifetime", i, w.Range)
		}
		if w.Bucket != "" {
			if d, err := time.ParseDuration(w.Bucket); err != nil || d <= 0 {
				return fmt.Errorf("config: usage.windows[%d].bucket %q must be a positive Go duration", i, w.Bucket)
			}
		}
		// window is ignored for session/lifetime, but validate it when present.
		if w.Window != "" && w.Range != "session" && w.Range != "lifetime" {
			if d, err := time.ParseDuration(w.Window); err != nil || d <= 0 {
				return fmt.Errorf("config: usage.windows[%d].window %q must be a positive Go duration", i, w.Window)
			}
		}
	}
	for i, p := range c.Projects {
		if p.TokenBudget < -1 {
			return fmt.Errorf("config: projects[%d].token_budget must be -1 (unlimited), 0 (workflow/default), or a positive cap", i)
		}
	}
	if c.Codex.Enabled {
		if c.Codex.Workspace == "" {
			c.Codex.Workspace = c.Prizm.Workspace
		}
		if c.Codex.Workspace == "" {
			c.Codex.Workspace = "."
		}
		if c.Codex.Sandbox == "" {
			c.Codex.Sandbox = "workspace-write"
		}
		if c.Codex.ApprovalPolicy == "" {
			c.Codex.ApprovalPolicy = "on-request"
		}
		if c.Codex.TimeoutMinutes == 0 {
			c.Codex.TimeoutMinutes = 30
		}
		if c.Codex.MaxConcurrency == 0 {
			c.Codex.MaxConcurrency = 1
		}
		if c.Codex.TimeoutMinutes < 1 {
			return fmt.Errorf("config: codex.timeout_minutes must be >= 1")
		}
		if c.Codex.MaxConcurrency < 1 {
			return fmt.Errorf("config: codex.max_concurrency must be >= 1")
		}
		if c.Codex.Sandbox != "read-only" && c.Codex.Sandbox != "workspace-write" && c.Codex.Sandbox != "danger-full-access" {
			return fmt.Errorf("config: codex.sandbox must be 'read-only', 'workspace-write', or 'danger-full-access'")
		}
		if c.Codex.ApprovalPolicy != "untrusted" && c.Codex.ApprovalPolicy != "on-request" && c.Codex.ApprovalPolicy != "never" {
			return fmt.Errorf("config: codex.approval_policy must be 'untrusted', 'on-request', or 'never'")
		}
	}
	if c.Autopatch.Mode == "" {
		c.Autopatch.Mode = "propose"
	}
	if c.Autopatch.RequireCleanWorktree == nil {
		c.Autopatch.RequireCleanWorktree = boolPtr(true)
	}
	if c.Autopatch.MaxAttempts == 0 {
		c.Autopatch.MaxAttempts = 2
	}
	if len(c.Autopatch.ValidationProfiles) == 0 {
		c.Autopatch.ValidationProfiles = []string{"go_test_all"}
	}
	if len(c.Autopatch.WorkerOrder) == 0 {
		c.Autopatch.WorkerOrder = []string{"codex", "local_agent"}
	}
	if c.Autopatch.WorktreeRoot == "" {
		c.Autopatch.WorktreeRoot = filepath.Join(".prizm", "worktrees")
	}
	if c.Autopatch.Enabled {
		if c.Autopatch.Mode != "propose" && c.Autopatch.Mode != "pr" {
			return fmt.Errorf("config: autopatch.mode must be 'propose' or 'pr'")
		}
		if c.Autopatch.MaxAttempts < 1 {
			return fmt.Errorf("config: autopatch.max_attempts must be >= 1")
		}
		for _, worker := range c.Autopatch.WorkerOrder {
			if worker != "codex" && worker != "local_agent" {
				return fmt.Errorf("config: autopatch.worker_order contains unknown worker %q", worker)
			}
		}
	}
	if c.FactoryMonitor.PollSeconds == 0 {
		c.FactoryMonitor.PollSeconds = 30
	}
	if c.FactoryMonitor.StuckAfterMinutes == 0 {
		c.FactoryMonitor.StuckAfterMinutes = 30
	}
	if c.FactoryMonitor.Enabled {
		if c.FactoryMonitor.Root == "" {
			return fmt.Errorf("config: factory_monitor.root is required when factory monitor is enabled")
		}
		if c.FactoryMonitor.NotifyChannelID == "" {
			return fmt.Errorf("config: factory_monitor.notify_channel_id is required when factory monitor is enabled")
		}
		if c.FactoryMonitor.PollSeconds < 1 {
			return fmt.Errorf("config: factory_monitor.poll_seconds must be >= 1")
		}
		if c.FactoryMonitor.StuckAfterMinutes < 1 {
			return fmt.Errorf("config: factory_monitor.stuck_after_minutes must be >= 1")
		}
	}
	if c.Prizm.InstanceID != "" && !isValidAgentID(c.Prizm.InstanceID) {
		return fmt.Errorf("config: prizm.instance_id %q must be alphanumeric + hyphens only", c.Prizm.InstanceID)
	}
	if c.Bridge.Enabled {
		if c.Prizm.InstanceID == "" {
			return fmt.Errorf("config: prizm.instance_id is required when bridge is enabled")
		}
		if c.Bridge.SecretEnv == "" && c.Bridge.Secret == "" {
			return fmt.Errorf("config: bridge.secret_env or bridge.secret is required when bridge is enabled")
		}
		if c.Bridge.Mode == "" {
			c.Bridge.Mode = "shared_nats"
		}
		if c.Bridge.Mode != "shared_nats" {
			return fmt.Errorf("config: bridge.mode must be 'shared_nats'")
		}
		if c.Bridge.ConfidenceThreshold < 0 || c.Bridge.ConfidenceThreshold > 1 {
			return fmt.Errorf("config: bridge.confidence_threshold must be between 0 and 1")
		}
		if c.Bridge.MaxClarificationRounds < 0 {
			return fmt.Errorf("config: bridge.max_clarification_rounds must be >= 0")
		}
		for i, profile := range c.Bridge.TargetProfiles {
			if profile.Name == "" {
				return fmt.Errorf("config: bridge.target_profiles[%d].name is required", i)
			}
			if profile.InstanceID == "" {
				return fmt.Errorf("config: bridge.target_profiles[%d].instance_id is required", i)
			}
			if profile.Adapter == "" {
				return fmt.Errorf("config: bridge.target_profiles[%d].adapter is required", i)
			}
		}
		if c.Bridge.Factory.Enabled {
			if c.Bridge.Factory.Root == "" {
				return fmt.Errorf("config: bridge.factory.root is required when factory bridge is enabled")
			}
			if c.Bridge.Factory.Project == "" {
				return fmt.Errorf("config: bridge.factory.project is required when factory bridge is enabled")
			}
			if c.Bridge.Factory.ApprovalMode == "" {
				c.Bridge.Factory.ApprovalMode = "report_only"
			}
			if c.Bridge.Factory.ApprovalMode != "report_only" && c.Bridge.Factory.ApprovalMode != "implementation" {
				return fmt.Errorf("config: bridge.factory.approval_mode must be 'report_only' or 'implementation'")
			}
			if c.Bridge.Factory.RunCodex && c.Bridge.Factory.ApprovalMode != "implementation" {
				return fmt.Errorf("config: bridge.factory.run_codex=true requires approval_mode='implementation'")
			}
		}
	}

	// Network exposure: binding to a non-loopback interface without a bearer
	// token would expose unauthenticated state-changing endpoints (approvals,
	// editor save) to the network. Fail closed.
	if !IsLoopbackHost(c.Prizm.BindHost) && c.API.ResolveAuthToken() == "" {
		return fmt.Errorf("config: prizm.bind_host %q is not loopback; set api.auth_token or api.auth_token_env to expose the API on the network", c.Prizm.BindHost)
	}

	return nil
}

// ResolveAndValidate resolves auto-generated agent IDs and validates the config.
// This is a single-pass operation — resolve first, then validate — to avoid
// the fragile two-pass approach where IDs might differ between passes.
func (c *Config) ResolveAndValidate() error {
	// Resolve auto-generated IDs first
	autoGenCounter := 0
	for i := range c.Agents {
		if c.Agents[i].ID == "" {
			autoGenCounter++
			c.Agents[i].ID = fmt.Sprintf("prizm%d", autoGenCounter)
		}
	}

	// Now validate with resolved IDs
	return c.Validate()
}

// PrimaryAgent returns the agent marked as primary, or the first agent if none
// is explicitly marked. Returns nil if no agents are configured.
func (c *Config) PrimaryAgent() *AgentConfig {
	for i, a := range c.Agents {
		if a.Primary {
			return &c.Agents[i]
		}
	}
	// Fallback: first agent
	if len(c.Agents) > 0 {
		return &c.Agents[0]
	}
	return nil
}

// LoadConfig reads a prizm.yaml file and returns the parsed Config.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	return LoadConfigFromBytes(data)
}

// LoadConfigFromBytes parses config from raw bytes (for testing).
func LoadConfigFromBytes(data []byte) (*Config, error) {
	cfg := DefaultConfig()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.ResolveEnv()

	// Resolve auto-generated IDs and validate in a single pass
	if err := cfg.ResolveAndValidate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// ResolveEnv expands ${VAR} references for secret-bearing config fields.
func (c *Config) ResolveEnv() {
	for i := range c.Channels {
		c.Channels[i].Token = os.ExpandEnv(c.Channels[i].Token)
	}
	c.Bridge.Secret = os.ExpandEnv(c.Bridge.Secret)
	c.Codex.Executable = os.ExpandEnv(c.Codex.Executable)
	c.Codex.Workspace = os.ExpandEnv(c.Codex.Workspace)
	c.ClaudeCode.Executable = os.ExpandEnv(c.ClaudeCode.Executable)
	c.FactoryMonitor.Root = os.ExpandEnv(c.FactoryMonitor.Root)
	expandList(c.Prizm.AllowedPaths)
	expandList(c.Prizm.ReadRoots)
	expandList(c.Prizm.WriteRoots)
}

// EffectiveReadRoots returns configured recursive read roots. New read_roots
// takes precedence; allowed_paths remains the legacy alias.
func (c *Config) EffectiveReadRoots() []string {
	if c == nil {
		return nil
	}
	if len(c.Prizm.ReadRoots) > 0 {
		return append([]string(nil), c.Prizm.ReadRoots...)
	}
	return append([]string(nil), c.Prizm.AllowedPaths...)
}

// EffectiveWriteRoots returns configured recursive approval-gated write roots.
// New write_roots takes precedence; allowed_paths remains the legacy alias.
func (c *Config) EffectiveWriteRoots() []string {
	if c == nil {
		return nil
	}
	if len(c.Prizm.WriteRoots) > 0 {
		return append([]string(nil), c.Prizm.WriteRoots...)
	}
	return append([]string(nil), c.Prizm.AllowedPaths...)
}

func expandList(values []string) {
	for i := range values {
		values[i] = os.ExpandEnv(values[i])
	}
}

// agentIDPattern enforces alphanumeric + hyphens, no dots.
// This matches agent.Agent's naming rule and prevents ambiguity in event namespaces.
var agentIDPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

func isValidAgentID(id string) bool {
	return agentIDPattern.MatchString(id)
}

func boolPtr(v bool) *bool {
	return &v
}

// RegisterAgents adds all configured agents to the given registry.
// Auto-generated IDs (prizm1, prizm2, etc.) are already resolved.
func (c *Config) RegisterAgents(registry *agent.Registry) error {
	for _, agentCfg := range c.Agents {
		a := &agent.Agent{
			Name:         agentCfg.ID,
			Role:         agentCfg.Role,
			Version:      "1.0.0",
			ProviderName: agentCfg.Provider,
			Model:        agentCfg.Model,
		}
		// If no capabilities are specified, add a default based on role
		if len(agentCfg.Capabilities) == 0 {
			a.Capabilities = []agent.AgentCapability{
				{Action: agentCfg.Role, Description: agentCfg.Role},
			}
		} else {
			for _, cap := range agentCfg.Capabilities {
				a.Capabilities = append(a.Capabilities, agent.AgentCapability{
					Action:      cap,
					Description: cap,
				})
			}
		}
		if err := registry.Register(a); err != nil {
			return fmt.Errorf("register agent %q: %w", agentCfg.ID, err)
		}
	}
	return nil
}

// TTSConfig holds Voicebox text-to-speech settings.
type TTSConfig struct {
	Enabled     bool   `yaml:"enabled"`
	ProfileID   string `yaml:"profile_id"`
	Engine      string `yaml:"engine"`
	VoiceboxURL string `yaml:"voicebox_url"`
	MaxChars    int    `yaml:"max_chars"`
}

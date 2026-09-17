package v2

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// WorkflowConfig is the YAML/JSON configuration for a Natural Gates workflow.
type WorkflowConfig struct {
	Name              string          `json:"name"`
	Version           int             `json:"version"`
	Description       string          `json:"description"`
	Global            GlobalConfig    `json:"global"`
	Phases            []PhaseConfig   `json:"phases"`
	ConfidenceDomains []string        `json:"confidence_domains"`
	Agents            []AgentConfig   `json:"agents"`
	FastPath          *FastPathConfig `json:"fast_path,omitempty"`
}

// DefaultRunTokenCeiling is the default prompt+completion ceiling for one
// Natural Gates workflow run.
const DefaultRunTokenCeiling = 2_000_000

// DefaultMaxTotalTokens is kept as a compatibility alias for older callers.
const DefaultMaxTotalTokens = DefaultRunTokenCeiling

// UnlimitedTokens is the explicit "no ceiling" sentinel for max_total_tokens.
// Semantics: -1 = unlimited, 0 = DefaultRunTokenCeiling, any positive = that cap.
// Values below -1 are invalid (rejected at load) so a typo cannot silently disable
// the budget.
const UnlimitedTokens = -1

type GlobalConfig struct {
	MaxTotalIterations   int    `json:"max_total_iterations"`
	MaxTotalTime         string `json:"max_total_time"`
	MaxTotalTokens       int    `json:"max_total_tokens,omitempty"`        // ceiling on prompt+completion tokens; -1 = unlimited, 0 = DefaultRunTokenCeiling, >0 = that cap
	MaxRepeatedToolCalls int    `json:"max_repeated_tool_calls,omitempty"` // abort a phase after this many identical tool calls; 0 = default (6)
	StatePersistenceDir  string `json:"state_persistence_dir"`
	EventEmission        bool   `json:"event_emission"`
	// DelegationTimeouts maps an agent name to its delegation deadline (Go duration
	// string, e.g. "15m"). The special key "default" sets the fallback for agents
	// without a specific entry. Lets a project control deadlines for its own roster.
	DelegationTimeouts map[string]string `json:"delegation_timeouts,omitempty"`
	// AutoRollback (V57) discards the run's work when the run ends in a failing
	// state: blocking verification still failing after MaxVerificationAttempts,
	// a blocking-phase fallback, or budget exhaustion with failing verification.
	// The rollback itself is performed by an injected runner (see
	// Engine.SetRollbackRunner) — typically git reset --hard to the run's start
	// SHA, or discarding the run's worktree/branch under V56 isolation. Pushed
	// commits on the run branch are NOT force-removed; branch review stays the
	// remote safety net. Default false.
	AutoRollback bool `json:"auto_rollback,omitempty"`
	// AutoApprove skips FEEDBACK_PRE and FEEDBACK_POST gates entirely. The phases
	// still enter and exit for state tracking, but never pause for external input.
	// Useful for fully autonomous loops where no human approval is needed.
	AutoApprove bool `json:"auto_approve,omitempty"`
	// MaxVerificationAttempts caps how many times a blocking verification may
	// fail before AutoRollback triggers mid-run. 0 = default (3). Only
	// meaningful when AutoRollback is true — without it, a blocking failure
	// keeps iterating until phase/budget limits as before.
	MaxVerificationAttempts int `json:"max_verification_attempts,omitempty"`
}

type PhaseConfig struct {
	Name          string   `json:"name"`
	Type          string   `json:"type"`
	Description   string   `json:"description"`
	MaxIterations int      `json:"max_iterations"`
	AllowedTools  []string `json:"allowed_tools"`
	// MaxTokens caps this phase's prompt+completion tokens. When exceeded the
	// phase stops iterating (phase.budget_exhausted) and falls through to its
	// fallback handling — softer than the run-wide global.max_total_tokens,
	// which ends the whole run. 0 = no per-phase cap.
	MaxTokens    int                 `json:"max_tokens,omitempty"`
	Gate         GateConfig          `json:"gate"`
	Fallback     FallbackConfig      `json:"fallback"`
	Verification *VerificationConfig `json:"verification,omitempty"`
}

// VerificationConfig attaches an objective build/test check to a phase. After the
// phase's work is done (and any commit/push enforcement passes), the engine runs
// the named validation profile (e.g. "go_test_all") through the V5 validation
// executor — an allowlisted command, so the model never controls what runs. When
// Blocking is true the phase cannot complete until the profile passes; the failing
// output is fed back to the model so it fixes the real problem and retries. This is
// what turns "writes plausible code" into "writes code that compiles and passes its
// tests" — the core feedback signal an autonomous dev loop needs.
type VerificationConfig struct {
	Profile  string `json:"profile"`            // validation profile name to run
	Blocking bool   `json:"blocking,omitempty"` // block phase completion until it passes
}

type GateConfig struct {
	Type              string             `json:"type"`
	Threshold         float64            `json:"threshold"`
	Weights           map[string]float64 `json:"weights,omitempty"`
	Domains           []string           `json:"domains,omitempty"`
	Approvers         []string           `json:"approvers,omitempty"`
	Mode              string             `json:"mode,omitempty"`
	RequiredReviewers []string           `json:"required_reviewers,omitempty"`
	MaxWarn           int                `json:"max_warn,omitempty"`
}

type FallbackConfig struct {
	OnMaxIterations string `json:"on_max_iterations"`
	Blocks          bool   `json:"blocks"`
}

type AgentConfig struct {
	Name         string   `json:"name"`
	Role         string   `json:"role"`
	Capabilities []string `json:"capabilities"`
	Provider     string   `json:"provider"`
	Model        string   `json:"model"`
	Availability string   `json:"availability"`
}

type FastPathConfig struct {
	Enabled    bool     `json:"enabled"`
	RiskLevels []string `json:"risk_levels"` // which risk levels get fast path
	SkipPhases []string `json:"skip_phases"` // phases to skip
}

// GetPhase returns the phase config for a given phase name.
func (c *WorkflowConfig) GetPhase(name string) *PhaseConfig {
	for i, p := range c.Phases {
		if p.Name == name {
			return &c.Phases[i]
		}
	}
	return nil
}

// LoadConfig loads a workflow config from a JSON or YAML file.
// The format is detected by file extension; .yaml/.yml are parsed via a
// YAML→JSON bridge so the struct's existing json tags (snake_case) keep
// working without duplicating yaml tags.
func LoadConfig(path string) (*WorkflowConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseConfigBytes(data, path)
}

// parseConfigBytes unmarshals workflow config bytes, choosing the decoder by
// extension. YAML is converted to a generic structure and re-encoded as JSON so
// the json struct tags drive field mapping for both formats.
func parseConfigBytes(data []byte, path string) (*WorkflowConfig, error) {
	var config WorkflowConfig
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".yaml" || ext == ".yml" {
		var raw any
		if err := yaml.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("parse yaml %s: %w", path, err)
		}
		jsonData, err := json.Marshal(normalizeYAML(raw))
		if err != nil {
			return nil, fmt.Errorf("convert yaml %s: %w", path, err)
		}
		if err := json.Unmarshal(jsonData, &config); err != nil {
			return nil, fmt.Errorf("decode yaml %s: %w", path, err)
		}
		NormalizeTokenBudgets(&config)
		if err := validateTokenBudgets(&config); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		return &config, nil
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("decode json %s: %w", path, err)
	}
	NormalizeTokenBudgets(&config)
	if err := validateTokenBudgets(&config); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &config, nil
}

// validateTokenBudgets rejects invalid token caps on the runtime load path (the
// full ValidateConfig is only used by doctor/preview/api). It guards the sentinel
// contract so a typo like -100 fails loudly instead of silently disabling the cap.
func validateTokenBudgets(cfg *WorkflowConfig) error {
	if cfg == nil {
		return nil
	}
	if cfg.Global.MaxTotalTokens < UnlimitedTokens {
		return fmt.Errorf("global.max_total_tokens must be -1 (unlimited), 0 (default), or a positive cap; got %d", cfg.Global.MaxTotalTokens)
	}
	for _, p := range cfg.Phases {
		if p.MaxTokens < 0 {
			return fmt.Errorf("phase %q: max_tokens must be >= 0 (0 = no per-phase cap); got %d", p.Name, p.MaxTokens)
		}
	}
	return nil
}

// NormalizeTokenBudgets applies finite token defaults to loaded workflow configs.
// A zero global cap means "use the built-in default cap" so omitted fields do not
// run open-ended. The explicit unlimited opt-out is -1 (UnlimitedTokens), which is
// left untouched here; any other negative is invalid and rejected by
// validateTokenBudgets on the load path.
func NormalizeTokenBudgets(cfg *WorkflowConfig) {
	if cfg == nil {
		return
	}
	if cfg.Global.MaxTotalTokens == 0 {
		cfg.Global.MaxTotalTokens = DefaultRunTokenCeiling
	}
}

// normalizeYAML converts yaml.v3's map[interface{}]interface{} values into
// map[string]interface{} so the result can be marshaled to JSON.
func normalizeYAML(v any) any {
	switch val := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(val))
		for k, item := range val {
			m[k] = normalizeYAML(item)
		}
		return m
	case map[any]any:
		m := make(map[string]any, len(val))
		for k, item := range val {
			m[fmt.Sprintf("%v", k)] = normalizeYAML(item)
		}
		return m
	case []any:
		s := make([]any, len(val))
		for i, item := range val {
			s[i] = normalizeYAML(item)
		}
		return s
	default:
		return v
	}
}

// LoadConfigFromDir loads all workflow configs from a directory.
func LoadConfigFromDir(dir string) (map[string]*WorkflowConfig, error) {
	configs := make(map[string]*WorkflowConfig)
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		ext := filepath.Ext(file.Name())
		if ext != ".json" && ext != ".yaml" && ext != ".yml" {
			continue
		}
		path := filepath.Join(dir, file.Name())
		config, err := LoadConfig(path)
		if err != nil {
			continue // skip invalid configs
		}
		configs[config.Name] = config
	}
	return configs, nil
}

// MarshalConfigYAML serializes a workflow config to YAML with snake_case keys
// (matching the json tags), so it round-trips through LoadConfig. Used by the
// dashboard workflow editor when persisting edits.
func MarshalConfigYAML(cfg *WorkflowConfig) ([]byte, error) {
	jsonBytes, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal config json: %w", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(jsonBytes, &generic); err != nil {
		return nil, fmt.Errorf("intermediate decode: %w", err)
	}
	return yaml.Marshal(generic)
}

// ValidateConfig checks a workflow config for structural problems and returns a
// list of human-readable errors (empty when valid).
func ValidateConfig(cfg *WorkflowConfig) []string {
	var errs []string
	if cfg == nil {
		return []string{"config is nil"}
	}
	if strings.TrimSpace(cfg.Name) == "" {
		errs = append(errs, "workflow name is required")
	}
	if len(cfg.Phases) == 0 {
		errs = append(errs, "at least one phase is required")
	}
	if cfg.Global.MaxTotalTokens < UnlimitedTokens {
		errs = append(errs, "global.max_total_tokens must be -1 (unlimited), 0 (default), or a positive cap")
	}
	if cfg.Global.MaxVerificationAttempts < 0 {
		errs = append(errs, "global.max_verification_attempts must be >= 0")
	}
	if cfg.Global.AutoRollback {
		// Rollback needs an objective failure signal — without a blocking
		// verification profile there is nothing to decide "failing" with.
		hasBlockingVerification := false
		for _, p := range cfg.Phases {
			if p.Verification != nil && p.Verification.Blocking && strings.TrimSpace(p.Verification.Profile) != "" {
				hasBlockingVerification = true
				break
			}
		}
		if !hasBlockingVerification {
			errs = append(errs, "global.auto_rollback requires at least one phase with blocking verification")
		}
	}
	validPhase := map[string]bool{"probe": true, "research": true, "plan": true, "feedback_pre": true, "execution": true, "feedback_post": true, "report": true}
	validGate := map[string]bool{"assumption_threshold": true, "confidence_threshold": true, "plan_completeness": true, "approval": true, "task_completion": true, "review_pass": true, "report_completeness": true, "": true}
	seen := map[string]bool{}
	for i, p := range cfg.Phases {
		if strings.TrimSpace(p.Name) == "" {
			errs = append(errs, fmt.Sprintf("phase %d: name is required", i))
		}
		if seen[p.Name] {
			errs = append(errs, fmt.Sprintf("duplicate phase name %q", p.Name))
		}
		seen[p.Name] = true
		if !validPhase[p.Type] {
			errs = append(errs, fmt.Sprintf("phase %q: unknown type %q", p.Name, p.Type))
		}
		if !validGate[p.Gate.Type] {
			errs = append(errs, fmt.Sprintf("phase %q: unknown gate type %q", p.Name, p.Gate.Type))
		}
		if p.MaxIterations < 0 {
			errs = append(errs, fmt.Sprintf("phase %q: max_iterations must be >= 0", p.Name))
		}
		if p.MaxTokens < 0 {
			errs = append(errs, fmt.Sprintf("phase %q: max_tokens must be >= 0", p.Name))
		}
		if p.Verification != nil && strings.TrimSpace(p.Verification.Profile) == "" {
			errs = append(errs, fmt.Sprintf("phase %q: verification.profile is required when verification is set", p.Name))
		}
	}
	return errs
}

// NewPhaseFromConfig creates a Phase instance from config.
func NewPhaseFromConfig(cfg PhaseConfig) Phase {
	switch cfg.Type {
	case "probe":
		return &ProbePhase{AllowedToolsList: cfg.AllowedTools, MaxIter: cfg.MaxIterations}
	case "research":
		return &ResearchPhase{AllowedToolsList: cfg.AllowedTools, MaxIter: cfg.MaxIterations}
	case "plan":
		return &PlanPhase{AllowedToolsList: cfg.AllowedTools, MaxIter: cfg.MaxIterations}
	case "feedback_pre":
		return &FeedbackPrePhase{AllowedToolsList: cfg.AllowedTools, MaxIter: cfg.MaxIterations, Approvers: cfg.Gate.Approvers}
	case "execution":
		return &ExecutionPhase{AllowedToolsList: cfg.AllowedTools, MaxIter: cfg.MaxIterations}
	case "feedback_post":
		return &FeedbackPostPhase{AllowedToolsList: cfg.AllowedTools, MaxIter: cfg.MaxIterations, Reviewers: cfg.Gate.RequiredReviewers}
	case "report":
		return &ReportPhase{AllowedToolsList: cfg.AllowedTools, MaxIter: cfg.MaxIterations}
	default:
		// Unknown phase type — default to a generic execution phase
		return &ExecutionPhase{AllowedToolsList: cfg.AllowedTools, MaxIter: cfg.MaxIterations}
	}
}

// NewGateFromConfig creates a Gate instance from config.
func NewGateFromConfig(cfg GateConfig) Gate {
	switch cfg.Type {
	case "assumption_threshold":
		weights := cfg.Weights
		if weights == nil {
			weights = map[string]float64{
				"blocker": 4.0,
				"high":    2.0,
				"medium":  1.0,
				"low":     0.5,
			}
		}
		return &AssumptionThresholdGate{Threshold: cfg.Threshold, Weights: weights}
	case "confidence_threshold":
		return &ConfidenceThresholdGate{Threshold: cfg.Threshold, Domains: cfg.Domains}
	case "plan_completeness":
		weights := cfg.Weights
		if weights == nil {
			weights = map[string]float64{
				"tasks_identified":     0.3,
				"resources_assigned":   0.3,
				"dependencies_ordered": 0.2,
				"success_criteria":     0.1,
				"risk_mitigation":      0.1,
			}
		}
		return &PlanCompletenessGate{Threshold: cfg.Threshold, Weights: weights}
	case "approval":
		return &ApprovalGate{RequiredApprovers: cfg.Approvers, Mode: cfg.Mode}
	case "task_completion":
		mode := cfg.Mode
		if mode == "" {
			mode = "all_tasks"
		}
		return &TaskCompletionGate{Mode: mode}
	case "review_pass":
		maxWarn := cfg.MaxWarn
		if maxWarn == 0 {
			maxWarn = 2
		}
		return &ReviewPassGate{RequiredReviewers: cfg.RequiredReviewers, MaxWarn: maxWarn}
	case "report_completeness":
		sections := []string{"change_summary", "proof_of_work", "impact", "next_steps", "learnings"}
		return &ReportCompletenessGate{RequiredSections: sections}
	default:
		return &TaskCompletionGate{Mode: "all_tasks"}
	}
}

// DefaultConfig returns the full 7-phase gated-loop workflow configuration:
//
//	PROBE → RESEARCH → PLAN → FEEDBACK_PRE → EXECUTION → FEEDBACK_POST → REPORT
//
// Each phase has a natural gate. FEEDBACK_PRE pauses for human approval before any
// code is written; FEEDBACK_POST pauses for review after execution and can loop back
// to EXECUTION when changes are requested. V36 safety enforcement (branch protection,
// read budget, commit/push) lives in the EXECUTION phase via the driver.
//
// This is the out-of-the-box flow; it can be overridden by a workflow config file
// (see prizm.workflow_config) loaded with LoadConfig.
func DefaultConfig() *WorkflowConfig {
	return &WorkflowConfig{
		Name:        "gated-loop",
		Version:     2,
		Description: "7-phase gated loop: PROBE → RESEARCH → PLAN → FEEDBACK_PRE → EXECUTION → FEEDBACK_POST → REPORT",
		Global: GlobalConfig{
			MaxTotalIterations:   600,                    // 10x default for autonomous loops
			MaxTotalTime:         "30m",                  // increased from 60m for autonomous loops
			MaxTotalTokens:       DefaultRunTokenCeiling, // default ceiling for autonomous loops
			MaxRepeatedToolCalls: 6,
			StatePersistenceDir:  "runs/gated-loop",
			EventEmission:        true,
		},
		ConfidenceDomains: []string{
			"codebase_understanding", "requirements_clarity", "approach_viability",
			"tool_capability", "dependency_health", "test_coverage", "edge_case_awareness",
		},
		Phases: []PhaseConfig{
			{
				Name: "PROBE", Type: "probe", Description: "Reduce assumptions by asking questions and searching",
				MaxIterations: 8,
				AllowedTools:  []string{"read_file", "list_dir", "search_files", "project_overview", "git_status", "git_log", "git_branch_list", "web_search", "memory_search", "collect_reference_images"},
				Gate:          GateConfig{Type: "assumption_threshold", Threshold: 2.0},
				Fallback:      FallbackConfig{OnMaxIterations: "proceed_with_open_assumptions", Blocks: false},
			},
			{
				Name: "RESEARCH", Type: "research", Description: "Increase confidence by searching the web, memory, and the codebase",
				MaxIterations: 8,
				AllowedTools:  []string{"read_file", "list_dir", "search_files", "project_overview", "git_status", "git_log", "web_search", "memory_search", "collect_reference_images"},
				Gate:          GateConfig{Type: "confidence_threshold", Threshold: 0.7},
				Fallback:      FallbackConfig{OnMaxIterations: "proceed_with_partial_confidence", Blocks: false},
			},
			{
				Name: "PLAN", Type: "plan", Description: "Create a structured plan with tasks and resource assignments",
				MaxIterations: 8,
				AllowedTools:  []string{"read_file", "list_dir", "search_files", "project_overview", "git_status", "git_log", "git_branch_list", "git_checkout", "memory_search"},
				Gate: GateConfig{Type: "plan_completeness", Threshold: 0.5,
					Weights: map[string]float64{"tasks_identified": 0.5, "resources_assigned": 0.5}},
				Fallback: FallbackConfig{OnMaxIterations: "proceed_with_partial_plan", Blocks: false},
			},
			{
				Name: "FEEDBACK_PRE", Type: "feedback_pre", Description: "Pause for plan approval before execution",
				MaxIterations: 1,
				AllowedTools:  []string{},
				Gate:          GateConfig{Type: "approval", Approvers: []string{"ema"}, Mode: "require_any"},
				Fallback:      FallbackConfig{OnMaxIterations: "auto_approve", Blocks: false},
			},
			{
				Name: "EXECUTION", Type: "execution", Description: "Write code with branch protection, read budget, and commit/push enforcement",
				MaxIterations: 250,
				AllowedTools:  []string{"read_file", "write_file", "create_directory", "list_dir", "search_files", "git_status", "git_log", "git_diff", "git_add", "git_commit", "git_push", "git_branch_list", "git_checkout", "project_overview", "run_validation", "delegate"},
				Gate:          GateConfig{Type: "task_completion", Mode: "partial_allowed"},
				Verification:  &VerificationConfig{Profile: "go_test_all", Blocking: true},
				Fallback:      FallbackConfig{OnMaxIterations: "proceed_with_partial_completion", Blocks: false},
			},
			{
				Name: "FEEDBACK_POST", Type: "feedback_post", Description: "Pause for post-execution review; loops back to EXECUTION on changes requested",
				MaxIterations: 1,
				AllowedTools:  []string{},
				Gate:          GateConfig{Type: "review_pass", RequiredReviewers: []string{"ema"}, MaxWarn: 2},
				Fallback:      FallbackConfig{OnMaxIterations: "wait", Blocks: true},
			},
			{
				Name: "REPORT", Type: "report", Description: "Produce a final report with proof of work",
				MaxIterations: 30,
				AllowedTools:  []string{"read_file", "git_log", "git_status"},
				Gate:          GateConfig{Type: "report_completeness"},
				Fallback:      FallbackConfig{OnMaxIterations: "auto_generate", Blocks: false},
			},
		},
		Agents:   []AgentConfig{},
		FastPath: &FastPathConfig{Enabled: false},
	}
}

package v2

import (
	"sort"
	"sync"
	"time"
)

// WorkflowStatus represents the overall status of a workflow.
type WorkflowStatus string

const (
	StatusStarted    WorkflowStatus = "started"
	StatusInProgress WorkflowStatus = "in_progress"
	StatusPaused     WorkflowStatus = "paused"
	StatusCompleted  WorkflowStatus = "completed"
	StatusFailed     WorkflowStatus = "failed"
	StatusBlocked    WorkflowStatus = "blocked"
	// StatusRolledBack marks a run whose work was discarded by the V57
	// auto-rollback (blocking verification exhausted or the run ended in a
	// failing state with auto_rollback enabled).
	StatusRolledBack WorkflowStatus = "rolled_back"
	// StatusBudgetExhausted marks a run that stopped because it hit its run-wide
	// token or wall-clock ceiling. Distinct from StatusCompleted so a consumer
	// polling the terminal status can tell a budget-killed run from a clean one.
	StatusBudgetExhausted WorkflowStatus = "budget_exhausted"
)

// PhaseStatus represents the status of a single phase.
type PhaseStatus string

const (
	PhaseStatusPending    PhaseStatus = "pending"
	PhaseStatusInProgress PhaseStatus = "in_progress"
	PhaseStatusCompleted  PhaseStatus = "completed"
	PhaseStatusFallback   PhaseStatus = "fallback"
	PhaseStatusFailed     PhaseStatus = "failed"
)

// WorkflowState is the complete state of a Natural Gates workflow run.
// Persisted to disk for resumability. This is the single source of truth.
type WorkflowState struct {
	mu                    sync.RWMutex
	WorkflowName          string                       `json:"workflow_name"`
	Version               int                          `json:"version"`
	MaxTotalTokens        int                          `json:"max_total_tokens,omitempty"`
	Status                WorkflowStatus               `json:"status"`
	RunID                 string                       `json:"run_id"`
	CorrelationID         string                       `json:"correlation_id"`
	ProjectID             string                       `json:"project_id,omitempty"`
	RepoPath              string                       `json:"repo_path,omitempty"`
	Channel               string                       `json:"channel,omitempty"`
	RequirePush           bool                         `json:"require_push,omitempty"`
	CurrentPhaseIdx       int                          `json:"current_phase_idx"`
	NextPhase             string                       `json:"next_phase,omitempty"`
	PauseReason           string                       `json:"pause_reason,omitempty"`
	PhaseStates           map[string]*PhaseState       `json:"phase_states"`
	Assumptions           []Assumption                 `json:"assumptions"`
	ConfidenceMatrix      map[string]*ConfidenceDomain `json:"confidence_matrix"`
	Plan                  *PlanGraph                   `json:"plan,omitempty"`
	Delegations           []DelegationState            `json:"delegations,omitempty"`
	Feedback              *FeedbackState               `json:"feedback,omitempty"`
	Report                *ReportState                 `json:"report,omitempty"`
	Verification          *VerificationRecord          `json:"verification,omitempty"`
	Rollback              *RollbackRecord              `json:"rollback,omitempty"`
	SystemMessages        map[string][]string          `json:"system_messages,omitempty"` // messages to inject per phase
	AgentRegistry         map[string]*AgentInfo        `json:"agent_registry,omitempty"`
	TotalPromptTokens     int                          `json:"total_prompt_tokens,omitempty"`
	TotalCompletionTokens int                          `json:"total_completion_tokens,omitempty"`
	LocalPromptTokens     int                          `json:"local_prompt_tokens,omitempty"`
	LocalCompletionTokens int                          `json:"local_completion_tokens,omitempty"`
	StartedAt             string                       `json:"started_at"`
	UpdatedAt             string                       `json:"updated_at"`
	CompletedAt           string                       `json:"completed_at,omitempty"`
}

// PhaseState tracks the state of a single phase.
type PhaseState struct {
	Status     PhaseStatus `json:"status"`
	Iterations int         `json:"iterations"`
	MaxTokens  int         `json:"max_tokens,omitempty"`
	// PromptTokens/CompletionTokens accumulate this phase's LLM usage so a
	// per-phase budget (PhaseConfig.MaxTokens) can be enforced and observers
	// can see where a run spends its tokens.
	PromptTokens     int             `json:"prompt_tokens,omitempty"`
	CompletionTokens int             `json:"completion_tokens,omitempty"`
	EnteredAt        string          `json:"entered_at"`
	ExitedAt         string          `json:"exited_at,omitempty"`
	GateResult       *GateResultData `json:"gate_result,omitempty"`
}

// GateResultData is the persisted gate result.
type GateResultData struct {
	Passed    bool     `json:"passed"`
	Score     float64  `json:"score"`
	Threshold float64  `json:"threshold"`
	Reason    string   `json:"reason"`
	Missing   []string `json:"missing,omitempty"`
}

// Assumption is a structured assumption tracked during PROBE phase.
type Assumption struct {
	ID          string  `json:"id"`
	Statement   string  `json:"statement"`
	Confidence  float64 `json:"confidence"`
	Criticality string  `json:"criticality"` // blocker|high|medium|low
	Source      string  `json:"source"`
	Status      string  `json:"status"` // open|addressed|contradicted|unresolved
	Resolution  string  `json:"resolution,omitempty"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// ConfidenceDomain is one dimension of the confidence matrix.
type ConfidenceDomain struct {
	Score       float64  `json:"score"`
	Evidence    []string `json:"evidence,omitempty"`
	LastUpdated string   `json:"last_updated"`
	UpdatedBy   string   `json:"updated_by"`
}

// PlanGraph is the structured plan from PLAN phase.
type PlanGraph struct {
	CompletenessScore float64          `json:"completeness_score"`
	Tasks             []PlanTask       `json:"tasks"`
	RiskMitigations   []RiskMitigation `json:"risk_mitigations,omitempty"`
}

// PlanTask is a single task in the plan.
type PlanTask struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Agent       string `json:"agent"`
	// Capability, when set, is the capability an agent must hold to run this
	// task. It enables capability-aware routing/validation: a task delegated to
	// an agent that lacks it fails closed. Empty = no capability requirement.
	Capability      string         `json:"capability,omitempty"`
	DependsOn       []string       `json:"depends_on,omitempty"`
	SuccessCriteria string         `json:"success_criteria"`
	RiskLevel       string         `json:"risk_level"`
	Status          string         `json:"status"` // pending|in_progress|completed|failed
	StartedAt       string         `json:"started_at,omitempty"`
	CompletedAt     string         `json:"completed_at,omitempty"`
	Artifacts       *TaskArtifacts `json:"artifacts,omitempty"`
}

// TaskArtifacts tracks the proof of work for a task.
type TaskArtifacts struct {
	FilePaths  []string `json:"file_paths,omitempty"`
	CommitHash string   `json:"commit_hash,omitempty"`
	BranchName string   `json:"branch_name,omitempty"`
	PRURL      string   `json:"pr_url,omitempty"`
}

// RiskMitigation links a low-confidence domain to a mitigation strategy.
type RiskMitigation struct {
	Domain   string `json:"domain"`
	Strategy string `json:"strategy"`
	TaskID   string `json:"task_id"`
}

// DelegationState tracks a delegated task.
type DelegationState struct {
	DelegationID      string `json:"delegation_id"`
	TaskID            string `json:"task_id"`
	Agent             string `json:"agent"`
	Status            string `json:"status"` // sent|acknowledged|in_progress|completed|failed|timed_out
	SentAt            string `json:"sent_at"`
	CompletedAt       string `json:"completed_at,omitempty"`
	ResultSummary     string `json:"result_summary,omitempty"`
	NATSCorrelationID string `json:"nats_correlation_id,omitempty"`
	RetryCount        int    `json:"retry_count,omitempty"`
	DeliveryKey       string `json:"delivery_key,omitempty"`
}

// FeedbackState tracks both feedback gates.
type FeedbackState struct {
	PreExecution  *PreExecutionFeedback  `json:"pre_execution,omitempty"`
	PostExecution *PostExecutionFeedback `json:"post_execution,omitempty"`
}

type PreExecutionFeedback struct {
	Status        string `json:"status"` // pending|approved|changes_requested|rejected|timed_out
	Approver      string `json:"approver,omitempty"`
	ApprovedAt    string `json:"approved_at,omitempty"`
	FeedbackNotes string `json:"feedback_notes,omitempty"`
	RevisionCount int    `json:"revision_count"`
}

type PostExecutionFeedback struct {
	Status        string                    `json:"status"`
	Reviewers     map[string]*ReviewerState `json:"reviewers"`
	RevisionCount int                       `json:"revision_count"`
}

type ReviewerState struct {
	Status      string            `json:"status"`               // pending|completed|timed_out
	Dimensions  map[string]string `json:"dimensions,omitempty"` // dimension → pass|warn|fail
	Notes       string            `json:"notes,omitempty"`
	SubmittedAt string            `json:"submitted_at,omitempty"`
}

// VerificationRecord captures the most recent objective build/test verification
// run during the workflow (e.g. `go test ./...`). It is the proof that the code
// the loop produced actually compiles and passes its tests — the signal the
// verification check feeds back into EXECUTION so the model fixes real failures
// instead of committing plausible-but-broken code.
type VerificationRecord struct {
	Profile  string `json:"profile"`
	Passed   bool   `json:"passed"`
	ExitCode int    `json:"exit_code"`
	Summary  string `json:"summary,omitempty"`
	Attempts int    `json:"attempts"`
	RanAt    string `json:"ran_at"`
}

// RollbackRecord captures a V57 auto-rollback: why the run's work was
// discarded and whether the rollback itself succeeded. A non-empty Error means
// rollback was attempted but failed — the branch may still hold bad commits.
type RollbackRecord struct {
	Reason string `json:"reason"`
	Error  string `json:"error,omitempty"`
	At     string `json:"at"`
}

// ReportState tracks the report completeness.
type ReportState struct {
	Sections map[string]string `json:"sections"` // section → present|missing
	Content  string            `json:"content,omitempty"`
}

// AgentInfo describes an agent in the registry.
type AgentInfo struct {
	Name         string   `json:"name"`
	Role         string   `json:"role"`
	Capabilities []string `json:"capabilities"`
	Provider     string   `json:"provider"`
	Model        string   `json:"model"`
	Availability string   `json:"availability"` // online|offline|busy
}

// NewWorkflowState creates a fresh workflow state.
func NewWorkflowState(config *WorkflowConfig) *WorkflowState {
	state := &WorkflowState{
		WorkflowName:     config.Name,
		Version:          config.Version,
		MaxTotalTokens:   config.Global.MaxTotalTokens,
		Status:           StatusStarted,
		PhaseStates:      make(map[string]*PhaseState),
		Assumptions:      make([]Assumption, 0),
		ConfidenceMatrix: make(map[string]*ConfidenceDomain),
		SystemMessages:   make(map[string][]string),
		AgentRegistry:    make(map[string]*AgentInfo),
	}

	// Initialize confidence domains
	for _, domain := range config.ConfidenceDomains {
		state.ConfidenceMatrix[domain] = &ConfidenceDomain{
			Score:       0.0,
			Evidence:    make([]string, 0),
			LastUpdated: time.Now().UTC().Format(time.RFC3339),
			UpdatedBy:   "system",
		}
	}

	// Initialize phase states
	for _, phase := range config.Phases {
		state.PhaseStates[phase.Name] = &PhaseState{
			Status:     PhaseStatusPending,
			Iterations: 0,
			MaxTokens:  phase.MaxTokens,
			EnteredAt:  "",
			ExitedAt:   "",
		}
	}

	// Initialize agent registry
	for _, agent := range config.Agents {
		state.AgentRegistry[agent.Name] = &AgentInfo{
			Name:         agent.Name,
			Role:         agent.Role,
			Capabilities: agent.Capabilities,
			Provider:     agent.Provider,
			Model:        agent.Model,
			Availability: agent.Availability,
		}
	}

	return state
}

// CurrentPhase returns the name of the current phase.

// ApplyBudgetConfig backfills budget ceilings from the workflow config. It keeps
// old persisted runs observable after a resume without overwriting ceilings that
// were already recorded in state.
func (s *WorkflowState) ApplyBudgetConfig(config *WorkflowConfig) {
	if s == nil || config == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.MaxTotalTokens == 0 {
		s.MaxTotalTokens = config.Global.MaxTotalTokens
	}
	if s.PhaseStates == nil {
		s.PhaseStates = make(map[string]*PhaseState)
	}
	for _, phase := range config.Phases {
		ps := s.PhaseStates[phase.Name]
		if ps == nil {
			ps = &PhaseState{Status: PhaseStatusPending}
			s.PhaseStates[phase.Name] = ps
		}
		if ps.MaxTokens == 0 {
			ps.MaxTokens = phase.MaxTokens
		}
	}
}
func (s *WorkflowState) CurrentPhase() string {
	if s.CurrentPhaseIdx < 0 {
		return ""
	}
	// This is set by the engine; we return from phase states in order
	for name, ps := range s.PhaseStates {
		if ps.Status == PhaseStatusInProgress {
			return name
		}
	}
	// If none in progress, return the first pending
	for name, ps := range s.PhaseStates {
		if ps.Status == PhaseStatusPending {
			return name
		}
	}
	return ""
}

// SetPhaseStatus sets the status of a phase.
func (s *WorkflowState) SetPhaseStatus(phaseName string, status PhaseStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ps, ok := s.PhaseStates[phaseName]; ok {
		ps.Status = status
		if status == PhaseStatusInProgress {
			ps.EnteredAt = time.Now().UTC().Format(time.RFC3339)
		} else if status == PhaseStatusCompleted || status == PhaseStatusFallback {
			ps.ExitedAt = time.Now().UTC().Format(time.RFC3339)
		}
		s.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
}

// IncrementPhaseIteration increments the iteration counter for a phase.
func (s *WorkflowState) IncrementPhaseIteration(phaseName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ps, ok := s.PhaseStates[phaseName]; ok {
		ps.Iterations++
	}
}

// SetPhaseGateResult records the gate evaluation result for a phase.
func (s *WorkflowState) SetPhaseGateResult(phaseName string, result GateResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ps, ok := s.PhaseStates[phaseName]; ok {
		ps.GateResult = &GateResultData{
			Passed:  result.Passed,
			Score:   result.Score,
			Reason:  result.Reason,
			Missing: result.Missing,
		}
	}
}

// AddAssumption adds or updates an assumption.
func (s *WorkflowState) AddAssumption(a Assumption) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Check if assumption already exists (by ID)
	for i, existing := range s.Assumptions {
		if existing.ID == a.ID {
			s.Assumptions[i] = a
			s.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			return
		}
	}
	s.Assumptions = append(s.Assumptions, a)
	s.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
}

// GetAssumptionScore calculates the weighted assumption score.
func (s *WorkflowState) GetAssumptionScore(weights map[string]float64) float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	score := 0.0
	for _, a := range s.Assumptions {
		if a.Status == "addressed" {
			continue
		}
		weight := weights[a.Criticality]
		score += (1.0 - a.Confidence) * weight
	}
	return score
}

// SetConfidence updates a confidence domain.
func (s *WorkflowState) SetConfidence(domain string, score float64, evidence string, updatedBy string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cd, ok := s.ConfidenceMatrix[domain]; ok {
		cd.Score = score
		if evidence != "" {
			cd.Evidence = append(cd.Evidence, evidence)
		}
		cd.LastUpdated = time.Now().UTC().Format(time.RFC3339)
		cd.UpdatedBy = updatedBy
		s.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}
}

// GetMinConfidence returns the minimum confidence across all domains (weakest-link).
func (s *WorkflowState) GetMinConfidence() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	min := 1.0
	for _, cd := range s.ConfidenceMatrix {
		if cd.Score < min {
			min = cd.Score
		}
	}
	return min
}

// SetFeedbackPreStatus updates the pre-execution feedback status.
func (s *WorkflowState) SetFeedbackPreStatus(status, approver string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Feedback == nil {
		s.Feedback = &FeedbackState{}
	}
	if s.Feedback.PreExecution == nil {
		s.Feedback.PreExecution = &PreExecutionFeedback{}
	}
	s.Feedback.PreExecution.Status = status
	s.Feedback.PreExecution.Approver = approver
	s.Feedback.PreExecution.ApprovedAt = time.Now().UTC().Format(time.RFC3339)
}

// SetReviewStatus updates a reviewer's post-execution review status.
func (s *WorkflowState) SetReviewStatus(reviewer, status string, data map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Feedback == nil {
		s.Feedback = &FeedbackState{}
	}
	if s.Feedback.PostExecution == nil {
		s.Feedback.PostExecution = &PostExecutionFeedback{
			Reviewers: make(map[string]*ReviewerState),
		}
	}
	if s.Feedback.PostExecution.Reviewers == nil {
		s.Feedback.PostExecution.Reviewers = make(map[string]*ReviewerState)
	}
	rs, ok := s.Feedback.PostExecution.Reviewers[reviewer]
	if !ok {
		rs = &ReviewerState{}
		s.Feedback.PostExecution.Reviewers[reviewer] = rs
	}
	rs.Status = status
	rs.SubmittedAt = time.Now().UTC().Format(time.RFC3339)

	// Update dimensions if provided
	if dims, ok := data["dimensions"].(map[string]string); ok {
		rs.Dimensions = dims
	}
	if notes, ok := data["notes"].(string); ok {
		rs.Notes = notes
	}
}

// AllReviewersApproved checks if all required reviewers have approved.
func (s *WorkflowState) AllReviewersApproved() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.Feedback == nil || s.Feedback.PostExecution == nil {
		return false
	}
	for _, rs := range s.Feedback.PostExecution.Reviewers {
		if rs.Status != "approved" {
			return false
		}
	}
	return true
}

// UpdateTaskStatus updates the status of a planned task.
func (s *WorkflowState) UpdateTaskStatus(taskID, status string, data map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Plan == nil {
		return
	}
	for i, task := range s.Plan.Tasks {
		if task.ID == taskID {
			s.Plan.Tasks[i].Status = status
			if status == "completed" {
				s.Plan.Tasks[i].CompletedAt = time.Now().UTC().Format(time.RFC3339)
			}
			// Update artifacts if provided
			if artifacts, ok := data["artifacts"].(map[string]any); ok {
				if s.Plan.Tasks[i].Artifacts == nil {
					s.Plan.Tasks[i].Artifacts = &TaskArtifacts{}
				}
				if fp, ok := artifacts["file_paths"].([]string); ok {
					s.Plan.Tasks[i].Artifacts.FilePaths = fp
				}
				if ch, ok := artifacts["commit_hash"].(string); ok {
					s.Plan.Tasks[i].Artifacts.CommitHash = ch
				}
				if bn, ok := artifacts["branch_name"].(string); ok {
					s.Plan.Tasks[i].Artifacts.BranchName = bn
				}
				if pr, ok := artifacts["pr_url"].(string); ok {
					s.Plan.Tasks[i].Artifacts.PRURL = pr
				}
			}
			break
		}
	}
}

// FailDelegatedTask marks any open delegation for a task as timed_out and the
// task itself as failed. Used when a delegation blows its deadline.
func (s *WorkflowState) FailDelegatedTask(taskID string) {
	s.mu.Lock()
	for i := range s.Delegations {
		if s.Delegations[i].TaskID == taskID && s.Delegations[i].Status != "completed" {
			s.Delegations[i].Status = "timed_out"
			s.Delegations[i].CompletedAt = time.Now().UTC().Format(time.RFC3339)
		}
	}
	s.mu.Unlock()
	s.UpdateTaskStatus(taskID, "failed", nil)
}

// LookupAgent returns the registered agent info for a name, if present.
func (s *WorkflowState) LookupAgent(name string) (*AgentInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	info, ok := s.AgentRegistry[name]
	return info, ok
}

// RegisteredAgents returns the names of all registered agents (empty when agents
// are resolved dynamically from the running config rather than the workflow).
func (s *WorkflowState) RegisteredAgents() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.AgentRegistry))
	for name := range s.AgentRegistry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// UpdateAgentAvailability updates an agent's availability.
func (s *WorkflowState) UpdateAgentAvailability(name, availability string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if agent, ok := s.AgentRegistry[name]; ok {
		agent.Availability = availability
	}
}

// AddSystemMessage adds a system message to be injected for a specific phase.
func (s *WorkflowState) AddSystemMessage(phaseName, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.SystemMessages[phaseName] = append(s.SystemMessages[phaseName], message)
}

// GetSystemMessages returns and clears system messages for a phase.
func (s *WorkflowState) GetSystemMessages(phaseName string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	msgs := s.SystemMessages[phaseName]
	s.SystemMessages[phaseName] = make([]string, 0)
	return msgs
}

// GetTotalPromptTokens returns the total prompt tokens used.
func (s *WorkflowState) GetTotalPromptTokens() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.TotalPromptTokens
}

// GetTotalCompletionTokens returns the total completion tokens used.
func (s *WorkflowState) GetTotalCompletionTokens() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.TotalCompletionTokens
}

// SetVerification records the outcome of a verification run, incrementing the
// attempt counter so a stuck build/test loop is visible in the proof of work.
func (s *WorkflowState) SetVerification(profile string, passed bool, exitCode int, summary string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	attempts := 1
	if s.Verification != nil {
		attempts = s.Verification.Attempts + 1
	}
	s.Verification = &VerificationRecord{
		Profile:  profile,
		Passed:   passed,
		ExitCode: exitCode,
		Summary:  summary,
		Attempts: attempts,
		RanAt:    time.Now().UTC().Format(time.RFC3339),
	}
	s.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
}

// SetRollback records a V57 auto-rollback outcome.
func (s *WorkflowState) SetRollback(reason, errMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Rollback = &RollbackRecord{
		Reason: reason,
		Error:  errMsg,
		At:     time.Now().UTC().Format(time.RFC3339),
	}
	s.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
}

// RolledBack reports whether the run's work was successfully rolled back.
func (s *WorkflowState) RolledBack() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Rollback != nil && s.Rollback.Error == ""
}

// VerificationPassed reports whether the latest verification run passed.
func (s *WorkflowState) VerificationPassed() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Verification != nil && s.Verification.Passed
}

// AddTokens adds to the token counters.
func (s *WorkflowState) AddTokens(prompt, completion int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.TotalPromptTokens += prompt
	s.TotalCompletionTokens += completion
}

// AddLocalTokens records usage by a loopback Ollama fallback. Local usage is
// reported separately and deliberately excluded from cloud/subscription caps.
func (s *WorkflowState) AddLocalTokens(prompt, completion int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LocalPromptTokens += prompt
	s.LocalCompletionTokens += completion
}

// AddPhaseTokens accumulates LLM usage on a phase's state (created on demand).
func (s *WorkflowState) AddPhaseTokens(phaseName string, prompt, completion int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ps, ok := s.PhaseStates[phaseName]
	if !ok {
		ps = &PhaseState{Status: PhaseStatusInProgress}
		s.PhaseStates[phaseName] = ps
	}
	ps.PromptTokens += prompt
	ps.CompletionTokens += completion
}

// GetPhaseTokens returns a phase's accumulated prompt+completion token total.
func (s *WorkflowState) GetPhaseTokens(phaseName string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if ps, ok := s.PhaseStates[phaseName]; ok {
		return ps.PromptTokens + ps.CompletionTokens
	}
	return 0
}

// GetTotalTokens returns prompt+completion tokens as a single atomic read, so the
// run-wide budget can't observe a torn snapshot when accounting is fed concurrently
// (e.g. delegated sub-agent usage rolled up from another goroutine).
func (s *WorkflowState) GetTotalTokens() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.TotalPromptTokens + s.TotalCompletionTokens
}

package v2

import (
	"context"
	"fmt"
	"time"
)

// defaultDelegationTimeout is the fallback deadline for any agent without a
// configured per-agent timeout. Per-agent overrides come from config (see
// ApplyTimeoutConfig), not hardcoded personas.
const defaultDelegationTimeout = 20 * time.Minute

// DelegationManager handles multi-agent task delegation via NATS.
type DelegationManager struct {
	natsSubject       string                   // e.g., "prizm.agent.openclaw"
	completionSubject string                   // e.g., "prizm.workflow.task.complete"
	timeouts          map[string]time.Duration // per-agent overrides, user-configured
	defaultTimeout    time.Duration
	maxRetries        int
}

// NewDelegationManager creates a new delegation manager.
func NewDelegationManager(natsSubject, completionSubject string) *DelegationManager {
	return &DelegationManager{
		natsSubject:       natsSubject,
		completionSubject: completionSubject,
		timeouts:          map[string]time.Duration{},
		defaultTimeout:    defaultDelegationTimeout,
		maxRetries:        2,
	}
}

// SetAgentTimeout sets a per-agent delegation timeout. No-op for empty name or
// non-positive duration.
func (dm *DelegationManager) SetAgentTimeout(agent string, d time.Duration) {
	if agent == "" || d <= 0 {
		return
	}
	dm.timeouts[agent] = d
}

// SetDefaultTimeout overrides the fallback timeout used for agents without a
// specific one.
func (dm *DelegationManager) SetDefaultTimeout(d time.Duration) {
	if d > 0 {
		dm.defaultTimeout = d
	}
}

// ApplyTimeoutConfig applies a name→duration-string map (e.g. from the workflow
// config's global.delegation_timeouts). The special key "default" sets the
// fallback. Invalid or non-positive durations are skipped. This is how a project
// controls delegation deadlines per its own roster, instead of baked-in personas.
func (dm *DelegationManager) ApplyTimeoutConfig(cfg map[string]string) {
	for name, s := range cfg {
		d, err := time.ParseDuration(s)
		if err != nil || d <= 0 {
			continue
		}
		if name == "default" {
			dm.defaultTimeout = d
			continue
		}
		dm.timeouts[name] = d
	}
}

// TaskPacket is the structured task sent to an agent via NATS.
type TaskPacket struct {
	Type                string      `json:"type"` // "task_delegation"
	TargetAgent         string      `json:"target_agent"`
	TaskID              string      `json:"task_id"`
	Description         string      `json:"description"`
	Context             TaskContext `json:"context"`
	ExpectedDeliverable string      `json:"expected_deliverable"`
	ValidationChecklist []string    `json:"validation_checklist"`
	Priority            string      `json:"priority"`
	Deadline            string      `json:"deadline"`
	// RequiredCapability, when set, is the capability the target agent must hold
	// to run this task (capability-aware routing). Empty = no requirement.
	RequiredCapability string `json:"required_capability,omitempty"`
	// MaxTokens is the delegated task ceiling inherited from the parent run.
	// -1 means unlimited, 0 means the worker default, >0 means this cap.
	MaxTokens int `json:"max_tokens,omitempty"`
}

type TaskContext struct {
	Files       []string `json:"files,omitempty"`
	Decisions   []string `json:"decisions,omitempty"`
	Constraints []string `json:"constraints,omitempty"`
}

// TaskCompletion is the response from a delegated task.
type TaskCompletion struct {
	TaskID        string              `json:"task_id"`
	Status        string              `json:"status"` // completed|failed
	OutputSummary string              `json:"output_summary"`
	Artifacts     CompletionArtifacts `json:"artifacts"`
	ReviewNotes   string              `json:"review_notes,omitempty"`
	// PromptTokens/CompletionTokens are the sub-agent's LLM usage for this task, so
	// delegated spend rolls up into the parent run's token budget (see
	// HandleTaskCompletion). Zero when the worker did not report usage.
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	Iterations       int `json:"iterations,omitempty"`
	ToolCalls        int `json:"tool_calls,omitempty"`
	DeniedToolCalls  int `json:"denied_tool_calls,omitempty"`
}

type CompletionArtifacts struct {
	FilePaths    []string `json:"file_paths,omitempty"`
	CommitHashes []string `json:"commit_hashes,omitempty"`
	PRURLs       []string `json:"pr_urls,omitempty"`
}

// timeoutFor returns the configured timeout for an agent, falling back to the
// "custom" default for unknown agents.
func (dm *DelegationManager) timeoutFor(agent string) time.Duration {
	if t := dm.timeouts[agent]; t > 0 {
		return t
	}
	if dm.defaultTimeout > 0 {
		return dm.defaultTimeout
	}
	return defaultDelegationTimeout
}

// Subject returns the NATS subject delegated task packets are published to.
func (dm *DelegationManager) Subject() string { return dm.natsSubject }

// BuildTaskPacket constructs the packet that would be sent to an agent for a task.
func (dm *DelegationManager) BuildTaskPacket(task PlanTask) TaskPacket {
	return TaskPacket{
		Type:                "task_delegation",
		TargetAgent:         task.Agent,
		TaskID:              task.ID,
		Description:         task.Description,
		ExpectedDeliverable: task.SuccessCriteria,
		Priority:            task.RiskLevel,
		Deadline:            time.Now().Add(dm.timeoutFor(task.Agent)).UTC().Format(time.RFC3339),
		RequiredCapability:  task.Capability,
	}
}

// DelegateTask records a delegation for a task and marks it in_progress, returning
// the delegation state and the packet to publish. The actual NATS publishing is
// done by the caller (wake handler), keeping this method side-effect-free w.r.t.
// the network and therefore unit-testable.
func (dm *DelegationManager) DelegateTask(ctx context.Context, task PlanTask, state *WorkflowState) (*DelegationState, TaskPacket, error) {
	packet := dm.BuildTaskPacket(task)

	delegationID := fmt.Sprintf("DEL-%s-%d", task.ID, time.Now().Unix())
	delegation := DelegationState{
		DelegationID: delegationID,
		TaskID:       task.ID,
		Agent:        task.Agent,
		Status:       "sent",
		SentAt:       time.Now().UTC().Format(time.RFC3339),
	}

	state.mu.Lock()
	state.Delegations = append(state.Delegations, delegation)
	state.mu.Unlock()

	state.UpdateTaskStatus(task.ID, "in_progress", nil)

	return &delegation, packet, nil
}

// HandleTaskCompletion processes a task completion from NATS.
func (dm *DelegationManager) HandleTaskCompletion(completion TaskCompletion, state *WorkflowState) {
	// Update delegation state
	state.mu.Lock()
	for i, del := range state.Delegations {
		if del.TaskID == completion.TaskID {
			state.Delegations[i].Status = completion.Status
			state.Delegations[i].CompletedAt = time.Now().UTC().Format(time.RFC3339)
			state.Delegations[i].ResultSummary = completion.OutputSummary
			break
		}
	}
	state.mu.Unlock()

	// Roll the sub-agent's token usage into the parent run's budget so delegated
	// spend counts against global.max_total_tokens (it was previously invisible).
	if completion.PromptTokens > 0 || completion.CompletionTokens > 0 {
		state.AddTokens(completion.PromptTokens, completion.CompletionTokens)
	}

	// Update task status
	artifactsData := map[string]any{
		"artifacts": map[string]any{
			"file_paths":  completion.Artifacts.FilePaths,
			"commit_hash": firstOrEmpty(completion.Artifacts.CommitHashes),
			"pr_url":      firstOrEmpty(completion.Artifacts.PRURLs),
		},
	}
	state.UpdateTaskStatus(completion.TaskID, completion.Status, artifactsData)
}

// delegationOpen reports whether a delegation is still outstanding.
func delegationOpen(status string) bool {
	return status == "sent" || status == "acknowledged" || status == "in_progress"
}

// RetryDelegation re-arms a timed-out delegation if it has retries left: it bumps
// the retry count, resets the clock to "sent", and returns a fresh packet to
// publish. It returns (_, false) when the task/delegation is gone or retries are
// exhausted, signalling the caller to fail the task instead.
func (dm *DelegationManager) RetryDelegation(taskID string, state *WorkflowState) (TaskPacket, bool) {
	if state.Plan == nil {
		return TaskPacket{}, false
	}
	var task *PlanTask
	idx := -1
	state.mu.RLock()
	for i := range state.Plan.Tasks {
		if state.Plan.Tasks[i].ID == taskID {
			t := state.Plan.Tasks[i]
			task = &t
			break
		}
	}
	for i := range state.Delegations {
		if state.Delegations[i].TaskID == taskID && delegationOpen(state.Delegations[i].Status) {
			idx = i
			break
		}
	}
	state.mu.RUnlock()
	if task == nil || idx == -1 {
		return TaskPacket{}, false
	}

	state.mu.Lock()
	if state.Delegations[idx].RetryCount >= dm.maxRetries {
		state.mu.Unlock()
		return TaskPacket{}, false
	}
	state.Delegations[idx].RetryCount++
	state.Delegations[idx].Status = "sent"
	state.Delegations[idx].SentAt = time.Now().UTC().Format(time.RFC3339)
	state.mu.Unlock()

	return dm.BuildTaskPacket(*task), true
}

// CheckTimeouts checks for timed-out delegations and escalates.
func (dm *DelegationManager) CheckTimeouts(state *WorkflowState) []string {
	var timedOut []string
	state.mu.RLock()
	defer state.mu.RUnlock()

	for _, del := range state.Delegations {
		if del.Status == "sent" || del.Status == "acknowledged" || del.Status == "in_progress" {
			timeout := dm.timeoutFor(del.Agent)
			sentTime, _ := time.Parse(time.RFC3339, del.SentAt)
			if time.Since(sentTime) > timeout {
				timedOut = append(timedOut, del.TaskID)
			}
		}
	}
	return timedOut
}

// firstOrEmpty returns the first element or empty string.
func firstOrEmpty(slice []string) string {
	if len(slice) > 0 {
		return slice[0]
	}
	return ""
}

package v2

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/agent"
)

// --- Phase Implementations ---

// ProbePhase — reduce assumptions by asking questions and searching.
type ProbePhase struct {
	AllowedToolsList []string
	MaxIter          int
}

func (p *ProbePhase) Name() string { return "PROBE" }
func (p *ProbePhase) Description() string {
	return "Reduce assumptions by asking questions and searching"
}
func (p *ProbePhase) AllowedTools() []string { return p.AllowedToolsList }
func (p *ProbePhase) MaxIterations() int     { return p.MaxIter }

func (p *ProbePhase) Enter(_ context.Context, state *WorkflowState) error {
	state.SetPhaseStatus("PROBE", PhaseStatusInProgress)
	return nil
}

func (p *ProbePhase) RunIteration(_ context.Context, state *WorkflowState, llmResponse string) (PhaseAction, error) {
	// Parse ASSUMPTION declarations from LLM response
	parseAssumptions(llmResponse, state)

	// Check for PROBE_COMPLETE signal
	if strings.Contains(llmResponse, "PROBE_COMPLETE") {
		return PhaseAction{Type: ActionPhaseComplete}, nil
	}

	// Check for tool requests
	if req := parseToolRequest(llmResponse); req != nil {
		if isAllowed(req.Tool, p.AllowedToolsList) {
			return PhaseAction{Type: ActionToolCall, ToolRequest: req}, nil
		}
		return PhaseAction{Type: ActionContinue}, nil
	}

	// Check for final response
	if parsed := parseFinal(llmResponse); parsed != "" {
		return PhaseAction{Type: ActionFinal, FinalContent: parsed}, nil
	}

	return PhaseAction{Type: ActionContinue}, nil
}

func (p *ProbePhase) CheckGate(state *WorkflowState) GateResult {
	// Gate is checked by the engine via the registered gate
	return GateResult{Passed: false}
}

func (p *ProbePhase) Exit(_ context.Context, state *WorkflowState) error {
	state.SetPhaseStatus("PROBE", PhaseStatusCompleted)
	return nil
}

// ResearchPhase — increase confidence by searching multiple sources.
type ResearchPhase struct {
	AllowedToolsList []string
	MaxIter          int
}

func (p *ResearchPhase) Name() string { return "RESEARCH" }
func (p *ResearchPhase) Description() string {
	return "Increase confidence by actively searching multiple sources"
}
func (p *ResearchPhase) AllowedTools() []string { return p.AllowedToolsList }
func (p *ResearchPhase) MaxIterations() int     { return p.MaxIter }

func (p *ResearchPhase) Enter(_ context.Context, state *WorkflowState) error {
	state.SetPhaseStatus("RESEARCH", PhaseStatusInProgress)
	return nil
}

func (p *ResearchPhase) RunIteration(_ context.Context, state *WorkflowState, llmResponse string) (PhaseAction, error) {
	// Parse CONFIDENCE declarations
	parseConfidence(llmResponse, state)

	// Check for RESEARCH_COMPLETE
	if strings.Contains(llmResponse, "RESEARCH_COMPLETE") {
		return PhaseAction{Type: ActionPhaseComplete}, nil
	}

	if req := parseToolRequest(llmResponse); req != nil {
		if isAllowed(req.Tool, p.AllowedToolsList) {
			return PhaseAction{Type: ActionToolCall, ToolRequest: req}, nil
		}
		return PhaseAction{Type: ActionContinue}, nil
	}

	if parsed := parseFinal(llmResponse); parsed != "" {
		return PhaseAction{Type: ActionFinal, FinalContent: parsed}, nil
	}

	return PhaseAction{Type: ActionContinue}, nil
}

func (p *ResearchPhase) CheckGate(state *WorkflowState) GateResult {
	return GateResult{Passed: false}
}

func (p *ResearchPhase) Exit(_ context.Context, state *WorkflowState) error {
	state.SetPhaseStatus("RESEARCH", PhaseStatusCompleted)
	return nil
}

// PlanPhase — create structured plan with resource assignments.
type PlanPhase struct {
	AllowedToolsList []string
	MaxIter          int
}

func (p *PlanPhase) Name() string           { return "PLAN" }
func (p *PlanPhase) Description() string    { return "Create structured plan with resource assignments" }
func (p *PlanPhase) AllowedTools() []string { return p.AllowedToolsList }
func (p *PlanPhase) MaxIterations() int     { return p.MaxIter }

func (p *PlanPhase) Enter(_ context.Context, state *WorkflowState) error {
	state.SetPhaseStatus("PLAN", PhaseStatusInProgress)
	if state.Plan == nil {
		state.Plan = &PlanGraph{Tasks: make([]PlanTask, 0), RiskMitigations: make([]RiskMitigation, 0)}
	}
	return nil
}

func (p *PlanPhase) RunIteration(_ context.Context, state *WorkflowState, llmResponse string) (PhaseAction, error) {
	// Parse TASK declarations
	parseTasks(llmResponse, state)

	// Check for PLAN_COMPLETE
	if strings.Contains(llmResponse, "PLAN_COMPLETE") {
		return PhaseAction{Type: ActionPhaseComplete}, nil
	}

	if req := parseToolRequest(llmResponse); req != nil {
		if isAllowed(req.Tool, p.AllowedToolsList) {
			return PhaseAction{Type: ActionToolCall, ToolRequest: req}, nil
		}
		return PhaseAction{Type: ActionContinue}, nil
	}

	if parsed := parseFinal(llmResponse); parsed != "" {
		return PhaseAction{Type: ActionFinal, FinalContent: parsed}, nil
	}

	return PhaseAction{Type: ActionContinue}, nil
}

func (p *PlanPhase) CheckGate(state *WorkflowState) GateResult {
	return GateResult{Passed: false}
}

func (p *PlanPhase) Exit(_ context.Context, state *WorkflowState) error {
	state.SetPhaseStatus("PLAN", PhaseStatusCompleted)
	return nil
}

// FeedbackPrePhase — get plan approval before execution. Pauses workflow.
type FeedbackPrePhase struct {
	AllowedToolsList []string
	MaxIter          int
	Approvers        []string // who must approve (from gate config); empty → human "ema"
}

func (p *FeedbackPrePhase) Name() string           { return "FEEDBACK_PRE" }
func (p *FeedbackPrePhase) Description() string    { return "Get plan approval before execution" }
func (p *FeedbackPrePhase) AllowedTools() []string { return p.AllowedToolsList }
func (p *FeedbackPrePhase) MaxIterations() int     { return p.MaxIter }

func (p *FeedbackPrePhase) Enter(_ context.Context, state *WorkflowState) error {
	state.SetPhaseStatus("FEEDBACK_PRE", PhaseStatusInProgress)
	if state.Feedback == nil {
		state.Feedback = &FeedbackState{}
	}
	if state.Feedback.PreExecution == nil {
		state.Feedback.PreExecution = &PreExecutionFeedback{Status: "pending"}
	}
	// Signal that the workflow should pause and wait for external approval
	state.Status = StatusPaused
	state.PauseReason = "Waiting for plan approval"
	return nil
}

func (p *FeedbackPrePhase) RunIteration(_ context.Context, state *WorkflowState, llmResponse string) (PhaseAction, error) {
	return PhaseAction{Type: ActionWaitExternal}, nil
}

func (p *FeedbackPrePhase) CheckGate(state *WorkflowState) GateResult {
	return GateResult{Passed: false}
}

func (p *FeedbackPrePhase) Exit(_ context.Context, state *WorkflowState) error {
	state.SetPhaseStatus("FEEDBACK_PRE", PhaseStatusCompleted)
	return nil
}

// ExecutionPhase — execute the approved plan with V36 enforcement.
type ExecutionPhase struct {
	AllowedToolsList []string
	MaxIter          int
}

func (p *ExecutionPhase) Name() string { return "EXECUTION" }
func (p *ExecutionPhase) Description() string {
	return "Execute the approved plan with V36 enforcement"
}
func (p *ExecutionPhase) AllowedTools() []string { return p.AllowedToolsList }
func (p *ExecutionPhase) MaxIterations() int     { return p.MaxIter }

func (p *ExecutionPhase) Enter(_ context.Context, state *WorkflowState) error {
	state.SetPhaseStatus("EXECUTION", PhaseStatusInProgress)
	return nil
}

func (p *ExecutionPhase) RunIteration(_ context.Context, state *WorkflowState, llmResponse string) (PhaseAction, error) {
	// Check for EXECUTION_COMPLETE
	if strings.Contains(llmResponse, "EXECUTION_COMPLETE") {
		return PhaseAction{Type: ActionPhaseComplete}, nil
	}

	if req := parseToolRequest(llmResponse); req != nil {
		if isAllowed(req.Tool, p.AllowedToolsList) {
			return PhaseAction{Type: ActionToolCall, ToolRequest: req}, nil
		}
		return PhaseAction{Type: ActionContinue}, nil
	}

	if parsed := parseFinal(llmResponse); parsed != "" {
		return PhaseAction{Type: ActionFinal, FinalContent: parsed}, nil
	}

	return PhaseAction{Type: ActionContinue}, nil
}

func (p *ExecutionPhase) CheckGate(state *WorkflowState) GateResult {
	return GateResult{Passed: false}
}

func (p *ExecutionPhase) Exit(_ context.Context, state *WorkflowState) error {
	state.SetPhaseStatus("EXECUTION", PhaseStatusCompleted)
	return nil
}

// FeedbackPostPhase — post-execution review. Pauses workflow.
type FeedbackPostPhase struct {
	AllowedToolsList []string
	MaxIter          int
	Reviewers        []string // required reviewers (from gate config); empty → human "ema"
}

func (p *FeedbackPostPhase) Name() string           { return "FEEDBACK_POST" }
func (p *FeedbackPostPhase) Description() string    { return "Post-execution review by Lumi and Mango" }
func (p *FeedbackPostPhase) AllowedTools() []string { return p.AllowedToolsList }
func (p *FeedbackPostPhase) MaxIterations() int     { return p.MaxIter }

func (p *FeedbackPostPhase) Enter(_ context.Context, state *WorkflowState) error {
	state.SetPhaseStatus("FEEDBACK_POST", PhaseStatusInProgress)
	if state.Feedback == nil {
		state.Feedback = &FeedbackState{}
	}
	if state.Feedback.PostExecution == nil {
		state.Feedback.PostExecution = &PostExecutionFeedback{
			Reviewers: make(map[string]*ReviewerState),
		}
	}
	// Seed the required reviewers from config. Defaults to the human "ema" so the
	// post-execution gate blocks on a person unless sub-agent reviewers are configured.
	if state.Feedback.PostExecution.Reviewers == nil {
		state.Feedback.PostExecution.Reviewers = make(map[string]*ReviewerState)
	}
	reviewers := p.Reviewers
	if len(reviewers) == 0 {
		reviewers = []string{"ema"}
	}
	for _, r := range reviewers {
		if _, ok := state.Feedback.PostExecution.Reviewers[r]; !ok {
			state.Feedback.PostExecution.Reviewers[r] = &ReviewerState{Status: "pending"}
		}
	}
	// Pause and wait for reviews
	state.Status = StatusPaused
	state.PauseReason = "Waiting for post-execution review"
	return nil
}

func (p *FeedbackPostPhase) RunIteration(_ context.Context, state *WorkflowState, llmResponse string) (PhaseAction, error) {
	return PhaseAction{Type: ActionWaitExternal}, nil
}

func (p *FeedbackPostPhase) CheckGate(state *WorkflowState) GateResult {
	return GateResult{Passed: false}
}

func (p *FeedbackPostPhase) Exit(_ context.Context, state *WorkflowState) error {
	state.SetPhaseStatus("FEEDBACK_POST", PhaseStatusCompleted)
	return nil
}

// ReportPhase — produce comprehensive report with proof of work.
type ReportPhase struct {
	AllowedToolsList []string
	MaxIter          int
}

func (p *ReportPhase) Name() string           { return "REPORT" }
func (p *ReportPhase) Description() string    { return "Produce comprehensive report with proof of work" }
func (p *ReportPhase) AllowedTools() []string { return p.AllowedToolsList }
func (p *ReportPhase) MaxIterations() int     { return p.MaxIter }

func (p *ReportPhase) Enter(_ context.Context, state *WorkflowState) error {
	state.SetPhaseStatus("REPORT", PhaseStatusInProgress)
	if state.Report == nil {
		state.Report = &ReportState{
			Sections: make(map[string]string),
		}
	}
	return nil
}

func (p *ReportPhase) RunIteration(_ context.Context, state *WorkflowState, llmResponse string) (PhaseAction, error) {
	// Reject truncated private reasoning before it can affect completion markers,
	// section state, tool routing, or the public report.
	if _, err := agent.PublicResponseText(llmResponse); err != nil {
		return PhaseAction{Type: ActionContinue}, nil
	}

	// Parse report sections
	parseReportSections(llmResponse, state)

	// Check for REPORT_COMPLETE
	if strings.Contains(llmResponse, "REPORT_COMPLETE") {
		return PhaseAction{Type: ActionPhaseComplete}, nil
	}

	if req := parseToolRequest(llmResponse); req != nil {
		if isAllowed(req.Tool, p.AllowedToolsList) {
			return PhaseAction{Type: ActionToolCall, ToolRequest: req}, nil
		}
		return PhaseAction{Type: ActionContinue}, nil
	}

	if parsed := parseFinal(llmResponse); parsed != "" {
		public, err := agent.PublicResponseText(parsed)
		if err != nil {
			// Do not complete or persist a report containing an unterminated
			// private-reasoning block. The next iteration may produce a clean final.
			return PhaseAction{Type: ActionContinue}, nil
		}
		state.Report.Content = public
		return PhaseAction{Type: ActionFinal, FinalContent: public}, nil
	}

	return PhaseAction{Type: ActionContinue}, nil
}

func (p *ReportPhase) CheckGate(state *WorkflowState) GateResult {
	return GateResult{Passed: false}
}

func (p *ReportPhase) Exit(_ context.Context, state *WorkflowState) error {
	state.SetPhaseStatus("REPORT", PhaseStatusCompleted)
	return nil
}

// --- Helper Functions ---

func isAllowed(tool string, allowed []string) bool {
	for _, a := range allowed {
		if a == tool {
			return true
		}
	}
	return false
}

// ToolRequest is a parsed tool request from LLM response.
type ToolRequest struct {
	Tool  string                 `json:"tool"`
	Input map[string]interface{} `json:"input"`
}

// parseToolRequest extracts a tool request from LLM response text.
func parseToolRequest(text string) *ToolRequest {
	// Look for {"type":"tool_request",...} pattern
	idx := strings.Index(text, `{"type":"tool_request"`)
	if idx == -1 {
		idx = strings.Index(text, `{"type": "tool_request"`)
		if idx == -1 {
			return nil
		}
	}
	// Find matching closing brace
	depth := 0
	end := -1
	for i := idx; i < len(text); i++ {
		if text[i] == '{' {
			depth++
		} else if text[i] == '}' {
			depth--
			if depth == 0 {
				end = i
				break
			}
		}
	}
	if end == -1 {
		return nil
	}

	// Parse the JSON
	jsonStr := text[idx : end+1]
	// Simple parsing — the agent parser has the real implementation
	// This is a lightweight version for the V2 engine
	var req ToolRequest
	if err := simpleJSONParse(jsonStr, &req); err != nil {
		return nil
	}
	// Check if it has a "tool" field
	if req.Tool == "" {
		// Try nested "function" format
		var nested struct {
			Type     string `json:"type"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if err := simpleJSONParse(jsonStr, &nested); err == nil && nested.Function.Name != "" {
			req.Tool = nested.Function.Name
		}
	}
	if req.Tool == "" {
		return nil
	}
	return &req
}

// parseFinal extracts a final response from LLM text.
func parseFinal(text string) string {
	idx := strings.Index(text, `{"type":"final"`)
	if idx == -1 {
		idx = strings.Index(text, `{"type": "final"`)
		if idx == -1 {
			return ""
		}
	}
	depth := 0
	end := -1
	for i := idx; i < len(text); i++ {
		if text[i] == '{' {
			depth++
		} else if text[i] == '}' {
			depth--
			if depth == 0 {
				end = i
				break
			}
		}
	}
	if end == -1 {
		return ""
	}
	var final struct {
		Content string `json:"content"`
	}
	simpleJSONParse(text[idx:end+1], &final)
	return final.Content
}

// parseAssumptions extracts ASSUMPTION declarations from LLM response.
func parseAssumptions(text string, state *WorkflowState) {
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ASSUMPTION:") {
			continue
		}
		// Parse: ASSUMPTION: {statement} | confidence: {0.0-1.0} | criticality: {blocker|high|medium|low}
		parts := strings.Split(line, "|")
		if len(parts) < 3 {
			continue
		}
		statement := strings.TrimPrefix(parts[0], "ASSUMPTION:")
		statement = strings.TrimSpace(statement)

		var confidence float64
		fmt.Sscanf(strings.TrimSpace(parts[1]), "confidence: %f", &confidence)

		criticality := strings.TrimSpace(parts[2])
		criticality = strings.TrimPrefix(criticality, "criticality:")
		criticality = strings.TrimSpace(criticality)

		id := fmt.Sprintf("ASM-%03d", len(state.Assumptions)+1)
		state.AddAssumption(Assumption{
			ID:          id,
			Statement:   statement,
			Confidence:  confidence,
			Criticality: criticality,
			Source:      "llm",
			Status:      "open",
			CreatedAt:   time.Now().UTC().Format(time.RFC3339),
			UpdatedAt:   time.Now().UTC().Format(time.RFC3339),
		})
	}
}

// parseConfidence extracts CONFIDENCE declarations from LLM response.
func parseConfidence(text string, state *WorkflowState) {
	lines := strings.Split(text, "\n")
	hadConfidence := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "CONFIDENCE:") {
			continue
		}
		hadConfidence = true
		// Parse: CONFIDENCE: {domain} | {0.0-1.0} | reason: {why}
		parts := strings.Split(line, "|")
		if len(parts) < 2 {
			continue
		}
		domain := strings.TrimPrefix(parts[0], "CONFIDENCE:")
		domain = strings.TrimSpace(domain)

		var score float64
		fmt.Sscanf(strings.TrimSpace(parts[1]), "%f", &score)

		reason := ""
		if len(parts) >= 3 {
			reason = strings.TrimSpace(parts[2])
			reason = strings.TrimPrefix(reason, "reason:")
			reason = strings.TrimSpace(reason)
		}

		state.SetConfidence(domain, score, reason, "llm")
	}

	// Auto-inject confidence when LLM doesn't emit CONFIDENCE declarations.
	// If the LLM did useful work (tool calls, research) but didn't score itself,
	// assign reasonable defaults so gates don't block on 0%.
	if !hadConfidence {
		hasToolCalls := strings.Contains(text, "\"type\": \"tool_request\"") ||
			strings.Contains(text, "\"type\":\"tool_request\"") ||
			strings.Contains(text, "tool_request")
		hasContent := len(strings.TrimSpace(text)) > 50

		if hasToolCalls || hasContent {
			for _, domain := range []string{"codebase_understanding", "requirements_clarity", "approach_viability", "tool_capability"} {
				cd := state.ConfidenceMatrix[domain]
				if cd != nil && cd.Score == 0.0 {
					state.SetConfidence(domain, 0.5, "auto-injected: LLM did useful work but didn't emit CONFIDENCE declaration", "system")
				}
			}
		}
	}
}

// parseTasks extracts TASK declarations from LLM response.
func parseTasks(text string, state *WorkflowState) {
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "TASK:") {
			continue
		}
		// Parse: TASK: {id} | description: {what} | agent: {who} | depends_on: [{ids}] | success: {criteria}
		parts := strings.Split(line, "|")
		if len(parts) < 3 {
			continue
		}
		id := strings.TrimPrefix(parts[0], "TASK:")
		id = strings.TrimSpace(id)

		desc := ""
		agent := ""
		success := ""
		var dependsOn []string

		for _, part := range parts[1:] {
			part = strings.TrimSpace(part)
			if strings.HasPrefix(part, "description:") {
				desc = strings.TrimSpace(strings.TrimPrefix(part, "description:"))
			} else if strings.HasPrefix(part, "agent:") {
				agent = strings.TrimSpace(strings.TrimPrefix(part, "agent:"))
			} else if strings.HasPrefix(part, "depends_on:") {
				deps := strings.TrimSpace(strings.TrimPrefix(part, "depends_on:"))
				deps = strings.Trim(deps, "[]")
				for _, d := range strings.Split(deps, ",") {
					d = strings.TrimSpace(d)
					if d != "" {
						dependsOn = append(dependsOn, d)
					}
				}
			} else if strings.HasPrefix(part, "success:") {
				success = strings.TrimSpace(strings.TrimPrefix(part, "success:"))
			}
		}

		task := PlanTask{
			ID:              id,
			Description:     desc,
			Agent:           agent,
			DependsOn:       dependsOn,
			SuccessCriteria: success,
			Status:          "pending",
		}

		// Add to plan
		state.mu.Lock()
		if state.Plan == nil {
			state.Plan = &PlanGraph{Tasks: make([]PlanTask, 0)}
		}
		// Check if task already exists
		found := false
		for i, t := range state.Plan.Tasks {
			if t.ID == id {
				state.Plan.Tasks[i] = task
				found = true
				break
			}
		}
		if !found {
			state.Plan.Tasks = append(state.Plan.Tasks, task)
		}
		state.mu.Unlock()
	}
}

// parseReportSections extracts report section markers from LLM response.
func parseReportSections(text string, state *WorkflowState) {
	sections := map[string]string{
		"## Change Summary": "change_summary",
		"## Proof of Work":  "proof_of_work",
		"## Impact":         "impact",
		"## Next Steps":     "next_steps",
		"## Learnings":      "learnings",
	}

	for marker, key := range sections {
		if strings.Contains(text, marker) {
			state.mu.Lock()
			if state.Report == nil {
				state.Report = &ReportState{Sections: make(map[string]string)}
			}
			state.Report.Sections[key] = "present"
			state.mu.Unlock()
		}
	}
}

// simpleJSONParse is a minimal JSON parser for simple objects.
// The real parsing is done by the agent parser — this is a fallback.
func simpleJSONParse(jsonStr string, target interface{}) error {
	// Use encoding/json
	return jsonUnmarshal([]byte(jsonStr), target)
}

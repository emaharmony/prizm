// Package event provides lightweight payload validation for Prizm events.
// Schema validation is opt-in: call Validate(evt) before persisting in development
// to catch missing required fields early. In production, it can be disabled.
package event

// Schema defines the expected fields for an event type.
type Schema struct {
	Required []string // Fields that must be present in Payload
	Optional []string // Fields that are allowed but not required
}

// Schemas maps event types to their expected payload schemas.
// Unknown event types are allowed (no schema enforcement).
var Schemas = map[string]Schema{
	// Task lifecycle
	"prizm.task.created":   {Required: []string{"task"}, Optional: []string{"provider", "model", "agent"}},
	"prizm.task.started":   {Required: []string{"task"}, Optional: []string{"run_id"}},
	"prizm.task.completed": {Required: []string{"status"}, Optional: []string{"duration_ms", "output_path"}},
	"prizm.task.failed":    {Required: []string{"error"}, Optional: []string{"status"}},

	// Agent lifecycle
	"prizm.agent.started":   {Required: []string{"agent"}, Optional: []string{"model"}},
	"prizm.agent.output":    {Required: []string{"content"}, Optional: []string{"tokens"}},
	"prizm.agent.completed": {Required: []string{"status"}, Optional: []string{"duration_ms"}},
	"prizm.agent.failed":    {Required: []string{"error"}, Optional: []string{}},

	// LLM
	"prizm.llm.requested": {Required: []string{"provider", "model"}, Optional: []string{"prompt_tokens"}},
	"prizm.llm.completed": {Required: []string{"provider", "model"}, Optional: []string{"completion_tokens", "total_tokens", "latency_ms", "duration_ms"}},
	"prizm.llm.failed":    {Required: []string{"error"}, Optional: []string{"provider"}},

	// Tool execution (V3)
	"prizm.tool.requested": {Required: []string{"tool_name"}, Optional: []string{"args", "policy_decision"}},
	"prizm.tool.approved":  {Required: []string{"tool_name"}, Optional: []string{"policy"}},
	"prizm.tool.denied":    {Required: []string{"tool_name"}, Optional: []string{"reason"}},
	"prizm.tool.started":   {Required: []string{"tool_name"}, Optional: []string{}},
	"prizm.tool.completed": {Required: []string{"tool_name"}, Optional: []string{"result"}},
	"prizm.tool.failed":    {Required: []string{"tool_name"}, Optional: []string{"error"}},

	// Approval gates (V4)
	"prizm.approval.requested": {Required: []string{"approval_id", "mutation_type"}, Optional: []string{"target_path"}},
	"prizm.approval.granted":   {Required: []string{"approval_id"}, Optional: []string{"approved_by"}},
	"prizm.approval.denied":    {Required: []string{"approval_id"}, Optional: []string{"reason"}},
	"prizm.approval.expired":   {Required: []string{"approval_id"}, Optional: []string{}},

	// Mutations (V4)
	"prizm.mutation.proposed":  {Required: []string{"mutation_type", "target_path"}, Optional: []string{"diff", "approval_id"}},
	"prizm.mutation.validated": {Required: []string{"approval_id"}, Optional: []string{"validation_status"}},
	"prizm.mutation.applied":   {Required: []string{"target_path"}, Optional: []string{"success", "approval_id"}},
	"prizm.mutation.failed":    {Required: []string{"target_path"}, Optional: []string{"error"}},

	// Validation (V5)
	"prizm.validation.requested": {Required: []string{"profile"}, Optional: []string{"command"}},
	"prizm.validation.started":   {Required: []string{"profile"}, Optional: []string{"working_dir"}},
	"prizm.validation.completed": {Required: []string{"profile"}, Optional: []string{"exit_code", "duration_ms"}},
	"prizm.validation.failed":    {Required: []string{"profile"}, Optional: []string{"error"}},
	"prizm.validation.skipped":   {Required: []string{"profile"}, Optional: []string{"reason"}},
	"prizm.validation.timeout":   {Required: []string{"profile"}, Optional: []string{"timeout_seconds"}},

	// Review (V5)
	"prizm.review.requested": {Required: []string{"run_id"}, Optional: []string{"mutation_id"}},
	"prizm.review.started":   {Required: []string{"reviewer"}, Optional: []string{}},
	"prizm.review.completed": {Required: []string{"recommendation"}, Optional: []string{"artifact_path"}},
	"prizm.review.failed":    {Required: []string{"error"}, Optional: []string{}},

	// Policy (V8)
	"prizm.policy.requested":         {Required: []string{"action", "resource"}, Optional: []string{}},
	"prizm.policy.evaluated":         {Required: []string{"decision"}, Optional: []string{"rules_matched"}},
	"prizm.policy.allowed":           {Required: []string{"action", "resource"}, Optional: []string{"rule"}},
	"prizm.policy.denied":            {Required: []string{"action", "resource"}, Optional: []string{"reason"}},
	"prizm.policy.approval_required": {Required: []string{"action", "resource"}, Optional: []string{"rule"}},
	"prizm.policy.failed":            {Required: []string{"error"}, Optional: []string{}},

	// Context (V2)
	"prizm.context.requested": {Required: []string{"task"}, Optional: []string{"project"}},
	"prizm.context.failed":    {Required: []string{"error"}, Optional: []string{}},

	// Memory (V1)
	"prizm.memory.context_requested": {Required: []string{"task"}, Optional: []string{"project", "agent"}},
	"prizm.memory.context_built":     {Required: []string{}, Optional: []string{"memories_count", "entities_count"}},
	"prizm.memory.context_failed":    {Required: []string{"error"}, Optional: []string{}},

	// Memory write lifecycle (Phase 1)
	"prizm.memory.gate_passed":   {Required: []string{"memory_id"}, Optional: []string{"reasoning", "model"}},
	"prizm.memory.gate_rejected": {Required: []string{}, Optional: []string{"reasoning", "model"}},
	"prizm.memory.extracted":     {Required: []string{"memory_id"}, Optional: []string{"category", "tier", "model"}},
	"prizm.memory.persisted":      {Required: []string{"memory_id"}, Optional: []string{"category", "tier", "source"}},
	"prizm.memory.synced":         {Required: []string{"memory_id"}, Optional: []string{"recall_id"}},
	"prizm.memory.sync_failed":   {Required: []string{"memory_id"}, Optional: []string{"error"}},

	// Adapter (V9)
	"prizm.adapter.registered": {Required: []string{"adapter_id"}, Optional: []string{"capabilities"}},
	"prizm.adapter.health":     {Required: []string{"adapter_id"}, Optional: []string{"status"}},
	"prizm.adapter.execute":    {Required: []string{"adapter_id", "action"}, Optional: []string{"payload"}},
	"prizm.adapter.success":    {Required: []string{"adapter_id"}, Optional: []string{"result"}},
	"prizm.adapter.failed":     {Required: []string{"adapter_id"}, Optional: []string{"error"}},

	// Projection (V10)
	"prizm.projection.started":   {Required: []string{"projection_id"}, Optional: []string{"event_type"}},
	"prizm.projection.completed": {Required: []string{"projection_id"}, Optional: []string{"state"}},
	"prizm.projection.failed":    {Required: []string{"projection_id"}, Optional: []string{"error"}},

	// Workflow (V7)
	"prizm.workflow.started":        {Required: []string{"workflow_id", "name"}, Optional: []string{"steps"}},
	"prizm.workflow.step.started":   {Required: []string{"step_id"}, Optional: []string{"step_name"}},
	"prizm.workflow.step.completed": {Required: []string{"step_id"}, Optional: []string{"result"}},
	"prizm.workflow.step.failed":    {Required: []string{"step_id"}, Optional: []string{"error"}},
	"prizm.workflow.step.skipped":   {Required: []string{"step_id"}, Optional: []string{"reason"}},
	"prizm.workflow.paused":         {Required: []string{"workflow_id"}, Optional: []string{"pending_approval_id"}},
	"prizm.workflow.resumed":        {Required: []string{"workflow_id"}, Optional: []string{"approval_id"}},
	"prizm.workflow.completed":      {Required: []string{"workflow_id"}, Optional: []string{"status"}},
	"prizm.workflow.failed":         {Required: []string{"workflow_id"}, Optional: []string{"error"}},

	// Cost (V16)
	"prizm.cost.tracked":  {Required: []string{"provider", "model"}, Optional: []string{"prompt_tokens", "completion_tokens", "estimated_cost_usd"}},
	"prizm.cost.reported": {Required: []string{"run_id"}, Optional: []string{"total_tokens", "estimated_cost_usd"}},

	// Context injection (V19)
	"prizm.context.file_read": {Required: []string{"file", "source"}, Optional: []string{"size_bytes", "estimated_tokens"}},
	"prizm.context.injected":  {Required: []string{"run_id"}, Optional: []string{"files", "total_tokens", "truncated", "truncation_applied"}},

	// System
	"prizm.system.health":         {Required: []string{"status"}, Optional: []string{"version", "uptime"}},
	"prizm.persistence.completed": {Required: []string{"run_id"}, Optional: []string{"stage", "checkpoint_id"}},
	"prizm.output.written":        {Required: []string{"path"}, Optional: []string{"size_bytes"}},
}

// ValidationError represents a schema validation failure.
type ValidationError struct {
	EventType string
	Missing   []string // Required fields that are missing
}

func (e *ValidationError) Error() string {
	return "event " + e.EventType + ": missing required fields: " + joinFields(e.Missing)
}

// Validate checks that an event's payload contains all required fields for its type.
// Returns nil if valid, a ValidationError if required fields are missing.
// Unknown event types always pass (no schema enforcement).
func Validate(evt Event) error {
	schema, ok := Schemas[evt.Type]
	if !ok {
		return nil // Unknown event type, allow it
	}

	var missing []string
	for _, field := range schema.Required {
		if _, exists := evt.Payload[field]; !exists {
			missing = append(missing, field)
		}
	}

	if len(missing) > 0 {
		return &ValidationError{
			EventType: evt.Type,
			Missing:   missing,
		}
	}

	return nil
}

// joinFields joins field names with commas for error messages.
func joinFields(fields []string) string {
	result := ""
	for i, f := range fields {
		if i > 0 {
			result += ", "
		}
		result += f
	}
	return result
}

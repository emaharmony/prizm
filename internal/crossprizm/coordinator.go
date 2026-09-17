package crossprizm

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/task"
	"github.com/nats-io/nats.go"
)

const (
	DefaultConfidenceThreshold = 0.75
	DefaultMaxClarifyRounds    = 1
)

// TaskAdapter accepts a cross-Prizm task through an external queue or runtime.
type TaskAdapter interface {
	HandleCrossPrizmTask(ctx context.Context, msg Message) (*Message, error)
}

// TargetProfile describes a remote Prizm target and optional local adapter.
type TargetProfile struct {
	Name         string
	InstanceID   string
	Adapter      string
	Capabilities []string
}

// CoordinatorConfig controls task delegation over the signed protocol.
type CoordinatorConfig struct {
	InstanceID             string
	LeaderInstance         string
	Secret                 string
	NATS                   *nats.Conn
	Store                  *task.Store
	FactoryAdapter         TaskAdapter
	CodexAdapter           TaskAdapter
	TargetProfiles         []TargetProfile
	ConfidenceThreshold    float64
	MaxClarificationRounds int
	ReportOnly             bool
}

// Coordinator owns cross-Prizm task state and command publication.
type Coordinator struct {
	cfg      CoordinatorConfig
	profiles map[string]TargetProfile
}

// DelegateRequest is the local command contract for sending work to another Prizm.
type DelegateRequest struct {
	TargetProfile  string
	TargetInstance string
	LeaderInstance string
	Task           string
	Context        map[string]any
	Priority       string
}

// NewCoordinator creates a cross-Prizm task coordinator.
func NewCoordinator(cfg CoordinatorConfig) *Coordinator {
	if cfg.ConfidenceThreshold <= 0 {
		cfg.ConfidenceThreshold = DefaultConfidenceThreshold
	}
	if cfg.MaxClarificationRounds < 0 {
		cfg.MaxClarificationRounds = DefaultMaxClarifyRounds
	}
	if cfg.LeaderInstance == "" {
		cfg.LeaderInstance = cfg.InstanceID
	}
	c := &Coordinator{cfg: cfg, profiles: make(map[string]TargetProfile)}
	for _, profile := range cfg.TargetProfiles {
		if profile.Name == "" {
			continue
		}
		c.profiles[strings.ToLower(profile.Name)] = profile
	}
	return c
}

// Handle processes verified inbound protocol messages.
func (c *Coordinator) Handle(ctx context.Context, subject string, msg Message) (*Message, error) {
	switch msg.MessageType {
	case TypeTaskRequest:
		return c.handleTaskRequest(ctx, msg)
	case TypeStatusRequest:
		return c.handleStatusRequest(msg)
	case TypeTaskCancel:
		return c.handleTaskCancel(msg)
	case TypeTaskResponse, TypeTaskAccept, TypeTaskReject, TypeClarificationReq, TypeClarificationResp, TypeTaskProgress, TypeTaskResult:
		c.recordInboundUpdate(msg)
		return nil, nil
	default:
		log.Printf("[CROSS-PRIZM] ignoring unsupported message type %q on %s", msg.MessageType, subject)
		return nil, nil
	}
}

// Delegate publishes a signed task_request to a target Prizm.
func (c *Coordinator) Delegate(ctx context.Context, req DelegateRequest) (*Message, error) {
	_ = ctx
	if c.cfg.NATS == nil || !c.cfg.NATS.IsConnected() {
		return nil, fmt.Errorf("crossprizm: NATS is not connected")
	}
	target := req.TargetInstance
	if target == "" && req.TargetProfile != "" {
		if profile, ok := c.ResolveProfile(req.TargetProfile); ok {
			target = profile.InstanceID
		}
	}
	if target == "" {
		return nil, fmt.Errorf("crossprizm: target instance or known target profile is required")
	}
	taskText := strings.TrimSpace(req.Task)
	if taskText == "" {
		return nil, fmt.Errorf("crossprizm: task is required")
	}

	msg := NewMessage(c.cfg.InstanceID, target, TypeTaskRequest)
	msg.CorrelationID = "corr-" + newNonce()
	msg.Priority = firstNonEmpty(req.Priority, "normal")
	msg.Thread = map[string]any{
		"id":           msg.CorrelationID,
		"turn":         0,
		"max_turns":    1 + c.cfg.MaxClarificationRounds,
		"leader":       firstNonEmpty(req.LeaderInstance, c.cfg.LeaderInstance),
		"next_speaker": target,
	}
	msg.Context = cloneMap(req.Context)
	msg.Request = map[string]any{
		"task":            taskText,
		"target_profile":  req.TargetProfile,
		"leader_instance": firstNonEmpty(req.LeaderInstance, c.cfg.LeaderInstance),
		"mode":            "report_only",
	}
	if err := msg.Sign(c.cfg.Secret); err != nil {
		return nil, err
	}
	payload, err := msg.Payload()
	if err != nil {
		return nil, err
	}
	if err := c.cfg.NATS.Publish(SubjectTaskRequest, payload); err != nil {
		return nil, fmt.Errorf("crossprizm: publish task request: %w", err)
	}
	return &msg, nil
}

// RequestStatus publishes a signed status_request.
func (c *Coordinator) RequestStatus(targetInstance, taskID string) (*Message, error) {
	if c.cfg.NATS == nil || !c.cfg.NATS.IsConnected() {
		return nil, fmt.Errorf("crossprizm: NATS is not connected")
	}
	if strings.TrimSpace(targetInstance) == "" || strings.TrimSpace(taskID) == "" {
		return nil, fmt.Errorf("crossprizm: target instance and task id are required")
	}
	msg := NewMessage(c.cfg.InstanceID, targetInstance, TypeStatusRequest)
	msg.CorrelationID = "corr-" + newNonce()
	msg.Request = map[string]any{"task_id": taskID}
	if err := msg.Sign(c.cfg.Secret); err != nil {
		return nil, err
	}
	payload, err := msg.Payload()
	if err != nil {
		return nil, err
	}
	if err := c.cfg.NATS.Publish(SubjectStatusRequest, payload); err != nil {
		return nil, fmt.Errorf("crossprizm: publish status request: %w", err)
	}
	return &msg, nil
}

// Cancel publishes a signed task_cancel.
func (c *Coordinator) Cancel(targetInstance, taskID string) (*Message, error) {
	if c.cfg.NATS == nil || !c.cfg.NATS.IsConnected() {
		return nil, fmt.Errorf("crossprizm: NATS is not connected")
	}
	if strings.TrimSpace(targetInstance) == "" || strings.TrimSpace(taskID) == "" {
		return nil, fmt.Errorf("crossprizm: target instance and task id are required")
	}
	msg := NewMessage(c.cfg.InstanceID, targetInstance, TypeTaskCancel)
	msg.CorrelationID = "corr-" + newNonce()
	msg.Request = map[string]any{"task_id": taskID}
	if err := msg.Sign(c.cfg.Secret); err != nil {
		return nil, err
	}
	payload, err := msg.Payload()
	if err != nil {
		return nil, err
	}
	if err := c.cfg.NATS.Publish(SubjectTaskCancel, payload); err != nil {
		return nil, fmt.Errorf("crossprizm: publish cancel: %w", err)
	}
	return &msg, nil
}

// ResolveProfile returns a target profile by name.
func (c *Coordinator) ResolveProfile(name string) (TargetProfile, bool) {
	profile, ok := c.profiles[strings.ToLower(strings.TrimSpace(name))]
	return profile, ok
}

func (c *Coordinator) handleTaskRequest(ctx context.Context, msg Message) (*Message, error) {
	requestText := RequestText(msg)
	profileName := stringValue(msg.Request, "target_profile")
	profile, hasProfile := c.ResolveProfile(profileName)
	confidence := c.rateTaskRequest(msg, requestText, hasProfile)

	if confidence < c.cfg.ConfidenceThreshold && clarificationRound(msg) < c.cfg.MaxClarificationRounds {
		return &Message{
			MessageType: TypeClarificationReq,
			Thread:      nextThread(msg, c.cfg.InstanceID, msg.From),
			Request: map[string]any{
				"question":       "Please clarify the task objective, target profile, and expected result contract.",
				"missing_fields": missingFields(requestText, profileName, hasProfile),
			},
			Response: structuredResponse("needs_clarification", "Task confidence is below the configured receiver gate.", confidence, true),
		}, nil
	}

	if profile.Adapter == "factory" || profile.Adapter == "roblox_factory" {
		if c.cfg.FactoryAdapter == nil {
			return taskReject(msg, "factory profile is configured but no Factory adapter is enabled", confidence), nil
		}
		resp, err := c.cfg.FactoryAdapter.HandleCrossPrizmTask(ctx, msg)
		if err != nil {
			return nil, err
		}
		if resp == nil {
			return taskReject(msg, "Factory adapter did not accept the task", confidence), nil
		}
		if resp.Response == nil {
			resp.Response = map[string]any{}
		}
		resp.MessageType = TypeTaskAccept
		resp.Response["summary"] = "Task accepted by Factory adapter in report-only mode."
		resp.Response["confidence"] = confidence
		resp.Response["target_profile"] = profileName
		resp.Response["needs_human_input"] = false
		return resp, nil
	}

	if profile.Adapter == "codex" || profile.Adapter == "codex_cli" {
		if c.cfg.CodexAdapter == nil {
			return taskReject(msg, "codex profile is configured but no Codex worker is enabled", confidence), nil
		}
		resp, err := c.cfg.CodexAdapter.HandleCrossPrizmTask(ctx, msg)
		if err != nil {
			return nil, err
		}
		if resp == nil {
			return taskReject(msg, "Codex worker did not produce a task result", confidence), nil
		}
		if resp.Response == nil {
			resp.Response = map[string]any{}
		}
		resp.Response["confidence"] = confidence
		resp.Response["target_profile"] = profileName
		return resp, nil
	}

	if c.cfg.Store == nil {
		return taskReject(msg, "no local task store is configured for generic handoff", confidence), nil
	}

	taskID := taskIDFromMessage(msg)
	existing, err := c.cfg.Store.Get(taskID)
	if err == nil {
		return acceptedResponse(existing.ID, "Task was already accepted; returning existing task state.", confidence, profileName), nil
	}

	now := time.Now()
	t := &task.Task{
		ID:          taskID,
		Type:        firstNonEmpty(stringValue(msg.Request, "task_type"), "cross_prizm"),
		Status:      task.StatusAssigned,
		DelegatedBy: msg.From,
		DelegatedTo: c.cfg.InstanceID,
		Description: firstNonEmpty(requestText, "Cross-Prizm task request"),
		Context: map[string]any{
			"source":              "cross_prizm",
			"target_profile":      profileName,
			"leader_instance":     stringValue(msg.Request, "leader_instance"),
			"correlation_id":      msg.CorrelationID,
			"request":             msg.Request,
			"thread":              msg.Thread,
			"receiver_confidence": confidence,
		},
		Priority:  firstNonEmpty(msg.Priority, "normal"),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := c.cfg.Store.Create(t); err != nil {
		return nil, err
	}
	return acceptedResponse(taskID, "Task accepted into the local generic handoff queue.", confidence, profileName), nil
}

func (c *Coordinator) handleStatusRequest(msg Message) (*Message, error) {
	taskID := stringValue(msg.Request, "task_id")
	if taskID == "" {
		return taskReject(msg, "status request missing task_id", 0), nil
	}
	if c.cfg.Store == nil {
		return taskReject(msg, "no local task store is configured", 0), nil
	}
	t, err := c.cfg.Store.Get(taskID)
	if err != nil {
		return taskReject(msg, fmt.Sprintf("task %q not found", taskID), 0), nil
	}
	return &Message{
		MessageType: TypeTaskResponse,
		Response: map[string]any{
			"status":                  string(t.Status),
			"summary":                 t.Description,
			"task_id":                 t.ID,
			"confidence":              1.0,
			"artifacts":               []string{},
			"blockers":                []string{},
			"next_recommended_action": "wait_for_progress_or_result",
			"needs_human_input":       false,
			"thread_id":               msg.CorrelationID,
			"correlation_id":          msg.CorrelationID,
		},
	}, nil
}

func (c *Coordinator) handleTaskCancel(msg Message) (*Message, error) {
	taskID := stringValue(msg.Request, "task_id")
	if taskID == "" {
		return taskReject(msg, "cancel request missing task_id", 0), nil
	}
	if c.cfg.Store == nil {
		return taskReject(msg, "no local task store is configured", 0), nil
	}
	if err := c.cfg.Store.UpdateStatus(taskID, task.StatusCancelled, map[string]any{
		"cancelled_by":   msg.From,
		"correlation_id": msg.CorrelationID,
		"cancelled_at":   time.Now().UTC().Format(time.RFC3339Nano),
	}); err != nil {
		return nil, err
	}
	return &Message{
		MessageType: TypeTaskResponse,
		Response: map[string]any{
			"status":                  "cancelled",
			"summary":                 "Task cancellation was accepted.",
			"task_id":                 taskID,
			"confidence":              1.0,
			"artifacts":               []string{},
			"blockers":                []string{},
			"next_recommended_action": "none",
			"needs_human_input":       false,
			"thread_id":               msg.CorrelationID,
			"correlation_id":          msg.CorrelationID,
		},
	}, nil
}

func (c *Coordinator) recordInboundUpdate(msg Message) {
	if c.cfg.Store == nil {
		return
	}
	taskID := firstNonEmpty(stringValue(msg.Response, "task_id"), stringValue(msg.Request, "task_id"))
	if taskID == "" {
		return
	}
	status := stringValue(msg.Response, "status")
	if status == "" {
		return
	}
	if _, err := c.cfg.Store.Get(taskID); err != nil {
		return
	}
	switch task.Status(status) {
	case task.StatusCompleted, task.StatusFailed, task.StatusCancelled, task.StatusInProgress:
		if err := c.cfg.Store.UpdateStatus(taskID, task.Status(status), msg.Response); err != nil {
			log.Printf("[CROSS-PRIZM] failed to record update for task %s: %v", taskID, err)
		}
	}
}

func (c *Coordinator) rateTaskRequest(msg Message, requestText string, hasProfile bool) float64 {
	if explicit := floatValue(msg.Request, "confidence"); explicit > 0 {
		return math.Min(explicit, 1)
	}
	score := 0.2
	if strings.TrimSpace(requestText) != "" {
		score += 0.35
	}
	if hasProfile {
		score += 0.3
	}
	if stringValue(msg.Request, "leader_instance") != "" || stringValue(msg.Thread, "leader") != "" {
		score += 0.1
	}
	if score > 1 {
		return 1
	}
	return score
}

// RequestText extracts a human-readable task request from a protocol message.
func RequestText(msg Message) string {
	for _, key := range []string{"task", "prompt", "content", "request", "summary"} {
		if value, ok := msg.Request[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	if len(msg.Request) > 0 {
		data, _ := json.MarshalIndent(msg.Request, "", "  ")
		return "```json\n" + string(data) + "\n```"
	}
	return ""
}

func acceptedResponse(taskID, summary string, confidence float64, profile string) *Message {
	return &Message{
		MessageType: TypeTaskAccept,
		Response: map[string]any{
			"status":                  "accepted",
			"summary":                 summary,
			"task_id":                 taskID,
			"confidence":              confidence,
			"target_profile":          profile,
			"artifacts":               []string{},
			"blockers":                []string{},
			"next_recommended_action": "wait_for_progress_or_result",
			"needs_human_input":       false,
		},
	}
}

func taskReject(msg Message, reason string, confidence float64) *Message {
	resp := structuredResponse("rejected", reason, confidence, true)
	resp["blockers"] = []string{reason}
	resp["thread_id"] = msg.CorrelationID
	resp["correlation_id"] = msg.CorrelationID
	return &Message{MessageType: TypeTaskReject, Response: resp}
}

func structuredResponse(status, summary string, confidence float64, needsHuman bool) map[string]any {
	return map[string]any{
		"status":                  status,
		"summary":                 summary,
		"confidence":              confidence,
		"artifacts":               []string{},
		"blockers":                []string{},
		"next_recommended_action": "human_clarification",
		"needs_human_input":       needsHuman,
	}
}

func nextThread(msg Message, from, to string) map[string]any {
	thread := cloneMap(msg.Thread)
	if thread == nil {
		thread = map[string]any{}
	}
	if thread["id"] == nil {
		thread["id"] = msg.CorrelationID
	}
	thread["turn"] = intValue(thread, "turn") + 1
	thread["next_speaker"] = to
	thread["last_speaker"] = from
	return thread
}

func clarificationRound(msg Message) int {
	if n := intValue(msg.Thread, "clarification_round"); n > 0 {
		return n
	}
	return intValue(msg.Thread, "turn")
}

func missingFields(requestText, profileName string, hasProfile bool) []string {
	var missing []string
	if strings.TrimSpace(requestText) == "" {
		missing = append(missing, "task")
	}
	if strings.TrimSpace(profileName) == "" {
		missing = append(missing, "target_profile")
	} else if !hasProfile {
		missing = append(missing, "known_target_profile")
	}
	return missing
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func stringValue(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	switch value := m[key].(type) {
	case string:
		return strings.TrimSpace(value)
	case fmt.Stringer:
		return strings.TrimSpace(value.String())
	default:
		return ""
	}
}

func floatValue(m map[string]any, key string) float64 {
	if m == nil {
		return 0
	}
	switch value := m[key].(type) {
	case float64:
		return value
	case float32:
		return float64(value)
	case int:
		return float64(value)
	case int64:
		return float64(value)
	case json.Number:
		f, _ := value.Float64()
		return f
	default:
		return 0
	}
}

func intValue(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	switch value := m[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		i, _ := value.Int64()
		return int(i)
	default:
		return 0
	}
}

func taskIDFromMessage(msg Message) string {
	base := firstNonEmpty(msg.CorrelationID, msg.Nonce, fmt.Sprintf("%d", time.Now().UnixNano()))
	base = strings.ToLower(safeTaskID.ReplaceAllString(base, "-"))
	base = strings.Trim(base, ".-_")
	if base == "" {
		base = "task"
	}
	if len(base) > 80 {
		base = base[:80]
	}
	return "cross-" + base
}

var safeTaskID = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

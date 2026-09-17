package tool

import (
	"context"
	"fmt"
	"log"

	"github.com/emaharmony/prizm/internal/crossprizm"
)

// SendCrossMessageTool lets the agent send signed cross-Prizm messages
// to another Prizm instance over NATS.
type SendCrossMessageTool struct {
	service *crossprizm.Service
}

// NewSendCrossMessageTool creates a tool for sending cross-Prizm messages.
func NewSendCrossMessageTool(svc *crossprizm.Service) *SendCrossMessageTool {
	return &SendCrossMessageTool{service: svc}
}

func (t *SendCrossMessageTool) Name() string { return "send_cross_message" }

func (t *SendCrossMessageTool) Description() string {
	return "Send a signed cross-Prizm message to another Prizm instance (e.g., astraea-manager on Snippy). Use this to dispatch tasks, request status, or sync context with remote agents."
}

func (t *SendCrossMessageTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"to": {
				Type:        "string",
				Description: "Instance ID of the target Prizm (e.g., 'astraea-manager')",
				Required:    true,
			},
			"message_type": {
				Type:        "string",
				Description: "Type of message: task_request, status_request, context_sync, validation_request",
				Required:    true,
			},
			"content": {
				Type:        "string",
				Description: "The message content or task description to send",
				Required:    true,
			},
			"priority": {
				Type:        "string",
				Description: "Message priority: normal (default) or high",
			},
		},
	}
}

func (t *SendCrossMessageTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	if t.service == nil {
		return ToolResult{Success: false, Error: "cross-Prizm bridge is not enabled"}, nil
	}

	to, _ := input["to"].(string)
	messageType, _ := input["message_type"].(string)
	content, _ := input["content"].(string)
	priority, _ := input["priority"].(string)

	if to == "" {
		return ToolResult{Success: false, Error: "'to' is required"}, nil
	}
	if content == "" {
		return ToolResult{Success: false, Error: "'content' is required"}, nil
	}

	// Map message type to subject
	var subject string
	switch messageType {
	case "task_request":
		subject = crossprizm.SubjectTaskRequest
	case "status_request":
		subject = crossprizm.SubjectStatusRequest
	case "context_sync":
		subject = crossprizm.SubjectContextSync
	case "validation_request":
		subject = crossprizm.SubjectValidationRequest
	default:
		return ToolResult{Success: false, Error: fmt.Sprintf("unknown message_type %q", messageType)}, nil
	}

	if priority == "" {
		priority = "normal"
	}

	msg := crossprizm.NewMessage(t.service.InstanceID(), to, messageType)
	msg.Priority = priority
	msg.Request = map[string]any{
		"prompt": content,
	}

	if err := t.service.Publish(subject, msg); err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("publish failed: %v", err)}, nil
	}

	log.Printf("[CROSS-PRIZM] sent %s to %s (subject=%s)", messageType, to, subject)

	return ToolResult{
		Success: true,
		Output: map[string]any{
			"status":       "sent",
			"from":         t.service.InstanceID(),
			"to":           to,
			"message_type": messageType,
			"subject":      subject,
			"priority":     priority,
		},
	}, nil
}

// IsReadOnly returns false — sending cross-Prizm messages is a write action.
func (t *SendCrossMessageTool) IsReadOnly() bool { return false }

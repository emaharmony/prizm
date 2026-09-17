package v2

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/nats-io/nats.go"
)

// NATSPublisher wraps a nats.Conn and provides structured publishing
// methods for the Natural Gates Workflow System. It handles marshaling
// and publishing of task delegation packets, feedback/approval requests,
// post-execution review requests, and generic events.
type NATSPublisher struct {
	conn *nats.Conn
}

// NewNATSPublisher connects to NATS and returns a publisher.
// Uses the same connection pattern as the rest of Prizm (nats.Connect with Name option).
func NewNATSPublisher(natsURL string) (*NATSPublisher, error) {
	nc, err := nats.Connect(natsURL, nats.Name("prizm-workflow-publisher"))
	if err != nil {
		return nil, fmt.Errorf("nats connect for publisher: %w", err)
	}
	return &NATSPublisher{conn: nc}, nil
}

// NewNATSPublisherFromConn creates a publisher from an existing NATS connection.
// This is the preferred path when a NATS connection already exists (e.g., in WakeHandler).
func NewNATSPublisherFromConn(nc *nats.Conn) *NATSPublisher {
	return &NATSPublisher{conn: nc}
}

// Conn returns the underlying NATS connection.
func (p *NATSPublisher) Conn() *nats.Conn {
	return p.conn
}

// Close drains and closes the NATS connection.
func (p *NATSPublisher) Close() {
	if p.conn != nil {
		p.conn.Close()
	}
}

// PublishTaskPacket marshals and publishes a task delegation packet to the given subject.
// This is used by the DelegationManager to send tasks to agents (e.g., prizm.agent.openclaw).
func (p *NATSPublisher) PublishTaskPacket(subject string, packet TaskPacket) error {
	if p.conn == nil {
		return fmt.Errorf("NATS not connected")
	}
	data, err := json.Marshal(packet)
	if err != nil {
		return fmt.Errorf("marshal task packet: %w", err)
	}
	if err := p.conn.Publish(subject, data); err != nil {
		return fmt.Errorf("publish task packet to %s: %w", subject, err)
	}
	log.Printf("[NATS-PUB] published task packet %s to %s (agent: %s)", packet.TaskID, subject, packet.TargetAgent)
	return nil
}

// PublishFeedbackRequest publishes a feedback/approval request for a workflow plan.
// This is sent during the FEEDBACK_PRE phase to request plan approval from Lumi/Ema.
func (p *NATSPublisher) PublishFeedbackRequest(subject string, workflowID string, planJSON string) error {
	if p.conn == nil {
		return fmt.Errorf("NATS not connected")
	}
	payload := map[string]any{
		"type":        "feedback_request",
		"workflow_id": workflowID,
		"phase":       "FEEDBACK_PRE",
		"plan":        planJSON,
		"timestamp":   fmt.Sprintf("%d", time.Now().Unix()),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal feedback request: %w", err)
	}
	if err := p.conn.Publish(subject, data); err != nil {
		return fmt.Errorf("publish feedback request to %s: %w", subject, err)
	}
	log.Printf("[NATS-PUB] published feedback request for workflow %s to %s", workflowID, subject)
	return nil
}

// PublishReviewRequest publishes a post-execution review request.
// This is sent during the FEEDBACK_POST phase to request code review from Lumi and Mango.
func (p *NATSPublisher) PublishReviewRequest(subject string, workflowID string, reviewPackage string) error {
	if p.conn == nil {
		return fmt.Errorf("NATS not connected")
	}
	payload := map[string]any{
		"type":               "review_request",
		"workflow_id":        workflowID,
		"phase":              "FEEDBACK_POST",
		"review_package":     reviewPackage,
		"required_reviewers": []string{"lumi", "mango"},
		"timestamp":          fmt.Sprintf("%d", time.Now().Unix()),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal review request: %w", err)
	}
	if err := p.conn.Publish(subject, data); err != nil {
		return fmt.Errorf("publish review request to %s: %w", subject, err)
	}
	log.Printf("[NATS-PUB] published review request for workflow %s to %s", workflowID, subject)
	return nil
}

// PublishEvent publishes a generic workflow event to the given subject.
// Used for observability events like phase transitions, gate checks, etc.
func (p *NATSPublisher) PublishEvent(subject string, eventType string, payload map[string]any) error {
	if p.conn == nil {
		return fmt.Errorf("NATS not connected")
	}
	event := map[string]any{
		"type":      eventType,
		"payload":   payload,
		"timestamp": fmt.Sprintf("%d", time.Now().Unix()),
	}
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	if err := p.conn.Publish(subject, data); err != nil {
		return fmt.Errorf("publish event to %s: %w", subject, err)
	}
	return nil
}

// --- NATSListener ---

// NATSListener subscribes to NATS subjects and feeds external events into
// the Natural Gates engine. It converts incoming NATS messages into
// ExternalEvent structs and sends them to the engine's external event channel.
type NATSListener struct {
	conn *nats.Conn
	subs []*nats.Subscription
}

// NewNATSListener connects to NATS and returns a listener.
func NewNATSListener(natsURL string) (*NATSListener, error) {
	nc, err := nats.Connect(natsURL, nats.Name("prizm-workflow-listener"))
	if err != nil {
		return nil, fmt.Errorf("nats connect for listener: %w", err)
	}
	return &NATSListener{conn: nc}, nil
}

// NewNATSListenerFromConn creates a listener from an existing NATS connection.
func NewNATSListenerFromConn(nc *nats.Conn) *NATSListener {
	return &NATSListener{conn: nc}
}

// Conn returns the underlying NATS connection.
func (l *NATSListener) Conn() *nats.Conn {
	return l.conn
}

// Close unsubscribes all subscriptions and closes the NATS connection.
func (l *NATSListener) Close() {
	for _, sub := range l.subs {
		if sub != nil {
			sub.Unsubscribe()
		}
	}
	if l.conn != nil {
		l.conn.Close()
	}
}

// Listen subscribes to the standard workflow NATS subjects and feeds
// incoming events into the engine's external event channel.
//
// Subscribed subjects:
//   - prizm.workflow.feedback.response — approval/review responses
//   - prizm.workflow.task.complete — task completion notifications
//   - prizm.agent.status — agent availability updates
func (l *NATSListener) Listen(engine *Engine) error {
	if l.conn == nil {
		return fmt.Errorf("NATS not connected")
	}
	if engine == nil {
		return fmt.Errorf("engine is nil")
	}

	eventCh := engine.GetExternalEventChannel()

	// Subscribe to feedback responses (approval/review)
	sub, err := l.conn.Subscribe("prizm.workflow.feedback.response", func(msg *nats.Msg) {
		var payload map[string]any
		if err := json.Unmarshal(msg.Data, &payload); err != nil {
			log.Printf("[NATS-LISTEN] failed to parse feedback response: %v", err)
			return
		}

		eventType, _ := payload["type"].(string)
		workflowID, _ := payload["workflow_id"].(string)
		decision, _ := payload["decision"].(string)
		reviewer, _ := payload["reviewer"].(string)

		// Determine if this is an approval or review response
		extType := "approval"
		if eventType == "review_response" || reviewer != "" {
			extType = "review"
		}

		evt := ExternalEvent{
			Type:          extType,
			CorrelationID: workflowID,
			Source:        "nats",
			Data: map[string]any{
				"decision":   decision,
				"reviewer":   reviewer,
				"notes":      payload["notes"],
				"dimensions": payload["dimensions"],
			},
		}

		select {
		case eventCh <- evt:
			log.Printf("[NATS-LISTEN] forwarded feedback response: type=%s workflow=%s decision=%s", extType, workflowID, decision)
		default:
			log.Printf("[NATS-LISTEN] WARN event channel full, dropping feedback response for workflow %s", workflowID)
		}
	})
	if err != nil {
		return fmt.Errorf("subscribe to prizm.workflow.feedback.response: %w", err)
	}
	l.subs = append(l.subs, sub)
	log.Printf("[NATS-LISTEN] subscribed to prizm.workflow.feedback.response")

	// Subscribe to task completion notifications
	sub, err = l.conn.Subscribe("prizm.workflow.task.complete", func(msg *nats.Msg) {
		var completion TaskCompletion
		if err := json.Unmarshal(msg.Data, &completion); err != nil {
			log.Printf("[NATS-LISTEN] failed to parse task completion: %v", err)
			return
		}

		evt := ExternalEvent{
			Type:          "task_complete",
			CorrelationID: completion.TaskID,
			Source:        "nats",
			Data: map[string]any{
				"task_id":        completion.TaskID,
				"status":         completion.Status,
				"output_summary": completion.OutputSummary,
				"artifacts": map[string]any{
					"file_paths":    completion.Artifacts.FilePaths,
					"commit_hashes": completion.Artifacts.CommitHashes,
					"pr_urls":       completion.Artifacts.PRURLs,
				},
				"review_notes": completion.ReviewNotes,
			},
		}

		select {
		case eventCh <- evt:
			log.Printf("[NATS-LISTEN] forwarded task completion: task=%s status=%s", completion.TaskID, completion.Status)
		default:
			log.Printf("[NATS-LISTEN] WARN event channel full, dropping task completion for %s", completion.TaskID)
		}
	})
	if err != nil {
		return fmt.Errorf("subscribe to prizm.workflow.task.complete: %w", err)
	}
	l.subs = append(l.subs, sub)
	log.Printf("[NATS-LISTEN] subscribed to prizm.workflow.task.complete")

	// Subscribe to agent status updates
	sub, err = l.conn.Subscribe("prizm.agent.status", func(msg *nats.Msg) {
		var payload map[string]any
		if err := json.Unmarshal(msg.Data, &payload); err != nil {
			log.Printf("[NATS-LISTEN] failed to parse agent status: %v", err)
			return
		}

		agentName, _ := payload["agent"].(string)
		availability, _ := payload["availability"].(string)

		evt := ExternalEvent{
			Type:          "agent_status",
			CorrelationID: agentName,
			Source:        "nats",
			Data: map[string]any{
				"agent":        agentName,
				"availability": availability,
				"capabilities": payload["capabilities"],
				"model":        payload["model"],
			},
		}

		select {
		case eventCh <- evt:
			log.Printf("[NATS-LISTEN] forwarded agent status: agent=%s availability=%s", agentName, availability)
		default:
			log.Printf("[NATS-LISTEN] WARN event channel full, dropping agent status for %s", agentName)
		}
	})
	if err != nil {
		return fmt.Errorf("subscribe to prizm.agent.status: %w", err)
	}
	l.subs = append(l.subs, sub)
	log.Printf("[NATS-LISTEN] subscribed to prizm.agent.status")

	return nil
}

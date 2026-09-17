// Package stage provides Prizm's pipeline execution engine.
//
// EventPublishStage publishes all accumulated events to NATS.
// It runs after PersistenceStage, ensuring events are durably stored
// before being broadcast to subscribers.
//
// This is the final stage in the conversation pipeline.
package stage

import (
	"context"

	"github.com/emaharmony/prizm/internal/event"
)

// NatsPublisher is the interface for publishing events to NATS.
// This decouples the stage from the concrete NATS connection.
type NatsPublisher interface {
	Publish(subject string, data []byte) error
}

// EventPublishStage publishes accumulated RunContext events to NATS.
type EventPublishStage struct {
	// Publisher is the NATS connection for publishing events.
	// If nil, events are not published (graceful degradation).
	Publisher NatsPublisher

	// BusURL is the NATS server URL. If empty, publishing is skipped.
	BusURL string
}

// Name returns the stage identifier.
func (s *EventPublishStage) Name() string {
	return "event_publish"
}

// Validate checks that configuration is valid.
func (s *EventPublishStage) Validate(rc *RunContext) error {
	// Publishing is optional — nil Publisher means skip
	return nil
}

// Execute publishes all accumulated events to NATS.
func (s *EventPublishStage) Execute(ctx context.Context, rc *RunContext) (*RunContext, *StageResult, error) {
	if s.Publisher == nil || s.BusURL == "" {
		// NATS not configured — skip publishing gracefully
		return rc, &StageResult{
			StageName: s.Name(),
			Success:   true,
			Data:      map[string]any{"published": false, "reason": "nats_not_configured"},
		}, nil
	}

	published := 0
	failed := 0

	for _, evt := range rc.Events {
		// Determine NATS subject from event type
		subject := eventSubject(evt)
		data, err := evt.ToJSON()
		if err != nil {
			failed++
			continue
		}

		if err := s.Publisher.Publish(subject, data); err != nil {
			failed++
			continue
		}
		published++
	}

	return rc, &StageResult{
		StageName: s.Name(),
		Success:   true,
		Data: map[string]any{
			"published": published,
			"failed":    failed,
			"total":     len(rc.Events),
		},
	}, nil
}

// Rollback is a no-op — published events can't be unpublished.
func (s *EventPublishStage) Rollback(ctx context.Context, rc *RunContext) error {
	return nil
}

// eventSubject maps an event type to a NATS subject.
// Events use the format: <source>.<action> (e.g., "lumi.llm.completed")
func eventSubject(evt event.Event) string {
	// If the event has a source that looks like an agent ID, use it as prefix
	if evt.Source != "" && evt.Source != "prizm" && evt.Source != "prizm-cli" {
		return evt.Source + "." + eventTypeToAction(evt.Type)
	}
	return evt.Type
}

// eventTypeToAction extracts the action part of an event type.
// E.g., "prizm.run.started" → "run.started"
func eventTypeToAction(eventType string) string {
	// Strip "prizm." prefix if present
	if len(eventType) > 6 && eventType[:6] == "prizm." {
		return eventType[6:]
	}
	return eventType
}

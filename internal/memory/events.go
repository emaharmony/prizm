// Package memory provides local memory storage for Prizm agents.
//
// This file defines event emission helpers for memory operations.
// All memory operations emit events via the EventStore so
// subscribers can react (e.g., sync to Recall, log metrics, update projections).
package memory

import (
	"context"
	"fmt"
	"time"

	"github.com/emaharmony/prizm/internal/event"
)

// Event types for memory operations.
const (
	EventTypeGatePassed   = "prizm.memory.gate_passed"
	EventTypeGateRejected = "prizm.memory.gate_rejected"
	EventTypeExtracted    = "prizm.memory.extracted"
	EventTypePersisted    = "prizm.memory.persisted"
	EventTypeSynced       = "prizm.memory.synced"
	EventTypeSyncFailed   = "prizm.memory.sync_failed"
)

// EventEmitter wraps an EventStore to publish memory events.
type EventEmitter struct {
	store  event.EventStore
	source string // e.g., "prizm:lumi"
}

// NewEventEmitter creates a memory event emitter.
func NewEventEmitter(store event.EventStore, source string) *EventEmitter {
	return &EventEmitter{store: store, source: source}
}

func (e *EventEmitter) emit(eventType string, payload map[string]any) error {
	if e.store == nil {
		return nil
	}
	evt := event.Event{
		ID:        event.NewID(),
		Type:      eventType,
		Source:    e.source,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Payload:   payload,
	}
	return e.store.Store(context.Background(), evt)
}

// EmitGatePassed publishes a gate-passed event.
func (e *EventEmitter) EmitGatePassed(memoryID, reasoning, model string) error {
	return e.emit(EventTypeGatePassed, map[string]any{
		"memory_id": memoryID,
		"reasoning": reasoning,
		"model":     model,
	})
}

// EmitGateRejected publishes a gate-rejected event.
func (e *EventEmitter) EmitGateRejected(reasoning, model string) error {
	return e.emit(EventTypeGateRejected, map[string]any{
		"reasoning": reasoning,
		"model":     model,
	})
}

// EmitExtracted publishes an extracted event.
func (e *EventEmitter) EmitExtracted(memoryID, category, tier, model string) error {
	return e.emit(EventTypeExtracted, map[string]any{
		"memory_id": memoryID,
		"category":  category,
		"tier":      tier,
		"model":     model,
	})
}

// EmitPersisted publishes a persisted event.
func (e *EventEmitter) EmitPersisted(memoryID, category, tier, source string) error {
	return e.emit(EventTypePersisted, map[string]any{
		"memory_id": memoryID,
		"category":  category,
		"tier":      tier,
		"source":    source,
	})
}

// EmitSynced publishes a synced event (Recall push succeeded).
func (e *EventEmitter) EmitSynced(memoryID, recallID string) error {
	return e.emit(EventTypeSynced, map[string]any{
		"memory_id": memoryID,
		"recall_id": recallID,
	})
}

// EmitSyncFailed publishes a sync-failed event (Recall push failed).
func (e *EventEmitter) EmitSyncFailed(memoryID string, syncErr error) error {
	errMsg := ""
	if syncErr != nil {
		errMsg = syncErr.Error()
	}
	return e.emit(EventTypeSyncFailed, map[string]any{
		"memory_id": memoryID,
		"error":     errMsg,
	})
}

// EmitMemoryWrite is a convenience method that emits the full lifecycle
// for a memory write: gate_passed → extracted → persisted.
func (e *EventEmitter) EmitMemoryWrite(mem Memory, model string) error {
	if err := e.EmitGatePassed(mem.ID, "", model); err != nil {
		return fmt.Errorf("emit gate_passed: %w", err)
	}
	if err := e.EmitExtracted(mem.ID, mem.Category, mem.Tier, model); err != nil {
		return fmt.Errorf("emit extracted: %w", err)
	}
	if err := e.EmitPersisted(mem.ID, mem.Category, mem.Tier, mem.Source); err != nil {
		return fmt.Errorf("emit persisted: %w", err)
	}
	return nil
}
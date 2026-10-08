package event

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const CommandSchemaVersion = 1

// CommandEventID is stable for one idempotency key so redelivery compares equal.
func CommandEventID(idempotencyKey string) string {
	digest := sha256.Sum256([]byte(idempotencyKey))
	return fmt.Sprintf("evt_command_%x", digest[:12])
}

// Command is the canonical request envelope used across workflow and transport
// boundaries. Payload remains domain-owned; this envelope owns delivery identity.
type Command struct {
	EventID        string          `json:"event_id"`
	Type           string          `json:"type"`
	RunID          string          `json:"run_id"`
	TaskID         string          `json:"task_id,omitempty"`
	DelegationID   string          `json:"delegation_id,omitempty"`
	CorrelationID  string          `json:"correlation_id"`
	CausationID    string          `json:"causation_id,omitempty"`
	IdempotencyKey string          `json:"idempotency_key"`
	Deadline       time.Time       `json:"deadline,omitempty"`
	SchemaVersion  int             `json:"schema_version"`
	Payload        json.RawMessage `json:"payload"`
}

func (c Command) Validate() error {
	if c.SchemaVersion != CommandSchemaVersion {
		return errors.New("event: unsupported command schema version")
	}
	if strings.TrimSpace(c.EventID) == "" || strings.TrimSpace(c.Type) == "" ||
		strings.TrimSpace(c.RunID) == "" || strings.TrimSpace(c.CorrelationID) == "" ||
		strings.TrimSpace(c.IdempotencyKey) == "" || len(c.Payload) == 0 || !json.Valid(c.Payload) {
		return errors.New("event: command requires event, type, run, correlation, idempotency, and valid payload")
	}
	return nil
}

type OutcomeStatus string

const (
	OutcomeAccepted  OutcomeStatus = "accepted"
	OutcomeProgress  OutcomeStatus = "progress"
	OutcomeSucceeded OutcomeStatus = "succeeded"
	OutcomeFailed    OutcomeStatus = "failed"
	OutcomeTimedOut  OutcomeStatus = "timed_out"
	OutcomeRejected  OutcomeStatus = "rejected"
)

func (s OutcomeStatus) Terminal() bool {
	return s == OutcomeSucceeded || s == OutcomeFailed || s == OutcomeTimedOut || s == OutcomeRejected
}

// Outcome is a correlated fact about one command. Sequence prevents an older
// retry from overwriting a newer terminal result.
type Outcome struct {
	EventID        string          `json:"event_id"`
	CommandEventID string          `json:"command_event_id"`
	RunID          string          `json:"run_id"`
	TaskID         string          `json:"task_id,omitempty"`
	DelegationID   string          `json:"delegation_id,omitempty"`
	CorrelationID  string          `json:"correlation_id"`
	CausationID    string          `json:"causation_id"`
	DeliveryKey    string          `json:"delivery_key"`
	Status         OutcomeStatus   `json:"status"`
	Sequence       int64           `json:"sequence"`
	OccurredAt     time.Time       `json:"occurred_at"`
	Payload        json.RawMessage `json:"payload,omitempty"`
}

func (o Outcome) Validate() error {
	if strings.TrimSpace(o.EventID) == "" || strings.TrimSpace(o.CommandEventID) == "" ||
		strings.TrimSpace(o.RunID) == "" || strings.TrimSpace(o.CorrelationID) == "" ||
		strings.TrimSpace(o.DeliveryKey) == "" || o.Sequence < 1 || o.OccurredAt.IsZero() {
		return errors.New("event: outcome identity is incomplete")
	}
	switch o.Status {
	case OutcomeAccepted, OutcomeProgress, OutcomeSucceeded, OutcomeFailed, OutcomeTimedOut, OutcomeRejected:
	default:
		return errors.New("event: invalid outcome status")
	}
	if len(o.Payload) != 0 && !json.Valid(o.Payload) {
		return errors.New("event: outcome payload is invalid JSON")
	}
	return nil
}

// Package telemetry owns typed Freewill task-outcome prediction and surprise
// semantics. It does not own host receipt validation, persistence, selection,
// or execution authority.
package telemetry

import "time"

type Outcome string

const (
	OutcomeCompleted    Outcome = "completed"
	OutcomeNotCompleted Outcome = "not_completed"
	OutcomeUnknown      Outcome = "unknown"
	OutcomeIncomparable Outcome = "incomparable"
)

type Relation string

const (
	RelationEqual        Relation = "equal"
	RelationOpposite     Relation = "opposite"
	RelationUnknown      Relation = "unknown"
	RelationIncomparable Relation = "incomparable"
)

type Source struct {
	ID          string    `json:"id"`
	Digest      string    `json:"digest"`
	Outcome     Outcome   `json:"outcome"`
	ParentRunID string    `json:"parent_run_id"`
	ChildRunID  string    `json:"child_run_id"`
	RecordedAt  time.Time `json:"recorded_at"`
}

// Prediction is an uncertain typed expectation about a repeat of the exact
// configured task context. Source refs bind the record to its prior cycle.
type Prediction struct {
	Version          int       `json:"version"`
	Kind             string    `json:"kind"`
	PredictionID     string    `json:"prediction_id"`
	PropositionKey   string    `json:"proposition_key"`
	OwnerID          string    `json:"owner_id"`
	ProjectID        string    `json:"project_id"`
	WorkspaceID      string    `json:"workspace_id"`
	AgentID          string    `json:"agent_id"`
	TaskIdentity     string    `json:"task_identity"`
	ManifestID       string    `json:"manifest_id"`
	ContextDigest    string    `json:"context_digest"`
	ExpectedOutcome  Outcome   `json:"expected_outcome"`
	SourceReceipt    Source    `json:"source_receipt"`
	SourceSelfReport Source    `json:"source_self_report"`
	Attribution      string    `json:"attribution"`
	Uncertainty      string    `json:"uncertainty"`
	RecordedAt       time.Time `json:"recorded_at"`
}

// SurpriseRevision preserves the original prediction and appends the later
// host receipt and tentative self-report comparison as one immutable revision.
type SurpriseRevision struct {
	Version                 int       `json:"version"`
	Kind                    string    `json:"kind"`
	RevisionID              string    `json:"revision_id"`
	Revision                int       `json:"revision"`
	PropositionKey          string    `json:"proposition_key"`
	OriginalPredictionID    string    `json:"original_prediction_id"`
	OriginalPredictionHash  string    `json:"original_prediction_hash"`
	PreviousRevisionID      string    `json:"previous_revision_id,omitempty"`
	PreviousRevisionHash    string    `json:"previous_revision_hash,omitempty"`
	ExpectedOutcome         Outcome   `json:"expected_outcome"`
	ReportedOutcome         Outcome   `json:"reported_outcome"`
	HostOutcome             Outcome   `json:"host_outcome"`
	Relation                Relation  `json:"relation"`
	SourceReceipt           Source    `json:"source_receipt"`
	SourceSelfReport        Source    `json:"source_self_report"`
	Attribution             string    `json:"attribution"`
	Uncertainty             string    `json:"uncertainty"`
	FurtherEvidenceRequired bool      `json:"further_evidence_required"`
	DurableInterest         bool      `json:"durable_interest"`
	RecordedAt              time.Time `json:"recorded_at"`
}

func ValidOutcome(outcome Outcome) bool {
	switch outcome {
	case OutcomeCompleted, OutcomeNotCompleted, OutcomeUnknown, OutcomeIncomparable:
		return true
	default:
		return false
	}
}

// Compare uses only bounded outcome enums. The independently host-validated
// receipt must itself be comparable before the expected/reported relation is
// called equal or opposite.
func Compare(expected, reported, host Outcome) Relation {
	if !ValidOutcome(expected) || !ValidOutcome(reported) || !ValidOutcome(host) {
		return RelationIncomparable
	}
	if expected == OutcomeIncomparable || reported == OutcomeIncomparable || host == OutcomeIncomparable {
		return RelationIncomparable
	}
	if expected == OutcomeUnknown || reported == OutcomeUnknown || host == OutcomeUnknown {
		return RelationUnknown
	}
	if expected == reported {
		return RelationEqual
	}
	return RelationOpposite
}

type LinkedRevision struct {
	Record      SurpriseRevision
	ContentID   string
	ContentHash string
}

// ValidateRevisionChain verifies append-only revision links and rejects
// branches, missing predecessors, and tampered source metadata.
func ValidateRevisionChain(predictionID, predictionHash, propositionKey string, revisions []LinkedRevision) ([]LinkedRevision, error) {
	byPrevious := make(map[string]LinkedRevision, len(revisions))
	for _, item := range revisions {
		record := item.Record
		if item.ContentID == "" || item.ContentHash == "" || record.Version != 1 || record.Kind != "preference_surprise_revision" || record.RevisionID == "" || record.PropositionKey != propositionKey || record.OriginalPredictionID != predictionID || record.OriginalPredictionHash != predictionHash || record.PreviousRevisionID == "" || record.PreviousRevisionHash == "" || record.Revision < 1 || !ValidOutcome(record.ExpectedOutcome) || !ValidOutcome(record.ReportedOutcome) || !ValidOutcome(record.HostOutcome) || !validRelation(record.Relation) || record.SourceReceipt.ID == "" || record.SourceReceipt.Digest == "" || record.SourceSelfReport.ID == "" || record.SourceSelfReport.Digest == "" || record.Attribution == "" || record.Uncertainty == "" || !record.FurtherEvidenceRequired || record.DurableInterest || record.RecordedAt.IsZero() {
			return nil, ErrInvalidRevisionChain
		}
		if _, exists := byPrevious[record.PreviousRevisionID]; exists {
			return nil, ErrInvalidRevisionChain
		}
		byPrevious[record.PreviousRevisionID] = item
	}
	previousID, previousHash, revision, previousTime := predictionID, predictionHash, 0, time.Time{}
	ordered := make([]LinkedRevision, 0, len(revisions))
	for {
		item, ok := byPrevious[previousID]
		if !ok {
			break
		}
		record := item.Record
		if record.PreviousRevisionHash != previousHash || record.Revision != revision+1 || (!previousTime.IsZero() && record.RecordedAt.Before(previousTime)) {
			return nil, ErrInvalidRevisionChain
		}
		ordered = append(ordered, item)
		previousID, previousHash, revision, previousTime = record.RevisionID, item.ContentHash, record.Revision, record.RecordedAt
	}
	if len(ordered) != len(revisions) {
		return nil, ErrInvalidRevisionChain
	}
	return ordered, nil
}

var ErrInvalidRevisionChain = errInvalidRevisionChain{}

type errInvalidRevisionChain struct{}

func (errInvalidRevisionChain) Error() string {
	return "freewill telemetry: invalid surprise revision chain"
}

func validRelation(relation Relation) bool {
	switch relation {
	case RelationEqual, RelationOpposite, RelationUnknown, RelationIncomparable:
		return true
	default:
		return false
	}
}

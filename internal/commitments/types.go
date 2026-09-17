package commitments

// CommitmentKind describes what type of follow-up a commitment represents.
type CommitmentKind string

const (
	KindEventCheckIn  CommitmentKind = "event_check_in"  // Check in about an event
	KindDeadlineCheck CommitmentKind = "deadline_check"  // Follow up before a deadline
	KindCareCheckIn   CommitmentKind = "care_check_in"   // Personal/emotional follow-up
	KindOpenLoop      CommitmentKind = "open_loop"        // Unfinished conversation topic
)

// CommitmentSensitivity describes how personal the commitment is.
type CommitmentSensitivity string

const (
	SensitivityRoutine  CommitmentSensitivity = "routine"  // Normal work follow-up
	SensitivityPersonal CommitmentSensitivity = "personal" // Somewhat personal
	SensitivityCare     CommitmentSensitivity = "care"     // Emotionally significant
)

// CommitmentStatus tracks where a commitment is in its lifecycle.
type CommitmentStatus string

const (
	StatusPending   CommitmentStatus = "pending"   // Not yet delivered
	StatusSent      CommitmentStatus = "sent"      // Delivered to user
	StatusDismissed CommitmentStatus = "dismissed"  // User dismissed
	StatusSnoozed   CommitmentStatus = "snoozed"   // Postponed
	StatusExpired   CommitmentStatus = "expired"    // Past due window
)

// CommitmentSource describes who made the commitment.
type CommitmentSource string

const (
	SourceInferredUser CommitmentSource = "inferred_user_context" // Inferred from user's words
	SourceAgentPromise CommitmentSource = "agent_promise"          // Agent promised something
)

// CommitmentRecord is a single tracked commitment.
type CommitmentRecord struct {
	ID              string                `json:"id"`
	Kind            CommitmentKind        `json:"kind"`
	Sensitivity     CommitmentSensitivity `json:"sensitivity"`
	Source          CommitmentSource      `json:"source"`
	Status          CommitmentStatus      `json:"status"`
	Reason          string                `json:"reason"`           // Why this commitment exists
	SuggestedText   string                `json:"suggested_text"`   // What to say when delivering
	DedupeKey       string                `json:"dedupe_key"`       // For deduplication
	Confidence      float64               `json:"confidence"`       // 0.0-1.0 extraction confidence
	EarliestDueMs   int64                 `json:"earliest_due_ms"`  // Unix ms, earliest delivery time
	LatestDueMs     int64                 `json:"latest_due_ms"`    // Unix ms, latest delivery time
	Timezone        string                `json:"timezone"`        // Timezone for due window
	AgentID         string                `json:"agent_id"`
	SessionKey      string                `json:"session_key"`
	Channel         string                `json:"channel"`
	SenderID        string                `json:"sender_id,omitempty"`
	SourceMessageID string                `json:"source_message_id,omitempty"`
	SourceRunID     string                `json:"source_run_id,omitempty"`
	CreatedAtMs     int64                 `json:"created_at_ms"`
	UpdatedAtMs     int64                 `json:"updated_at_ms"`
	ExpiresAtMs     int64                 `json:"expires_at_ms,omitempty"`
}

// CommitmentScope identifies which conversation a commitment belongs to.
type CommitmentScope struct {
	AgentID    string `json:"agent_id"`
	SessionKey string `json:"session_key"`
	Channel    string `json:"channel"`
	SenderID   string `json:"sender_id,omitempty"`
}

// ValidKinds, ValidSensitivities, ValidSources, ValidStatuses for validation.
var (
	ValidKinds = map[CommitmentKind]bool{
		KindEventCheckIn: true, KindDeadlineCheck: true,
		KindCareCheckIn: true, KindOpenLoop: true,
	}
	ValidSensitivities = map[CommitmentSensitivity]bool{
		SensitivityRoutine: true, SensitivityPersonal: true, SensitivityCare: true,
	}
	ValidSources = map[CommitmentSource]bool{
		SourceInferredUser: true, SourceAgentPromise: true,
	}
	ValidStatuses = map[CommitmentStatus]bool{
		StatusPending: true, StatusSent: true, StatusDismissed: true,
		StatusSnoozed: true, StatusExpired: true,
	}
)

// IsValid checks if a CommitmentRecord has valid fields.
func (r CommitmentRecord) IsValid() bool {
	if !ValidKinds[r.Kind] {
		return false
	}
	if !ValidSensitivities[r.Sensitivity] {
		return false
	}
	if !ValidSources[r.Source] {
		return false
	}
	if !ValidStatuses[r.Status] {
		return false
	}
	if r.Reason == "" {
		return false
	}
	if r.Confidence < 0 || r.Confidence > 1 {
		return false
	}
	return true
}
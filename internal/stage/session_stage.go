// Package stage provides Prizm's pipeline execution engine.
//
// SessionStage manages conversation sessions: finding or creating a session
// and recording the user's message. It's the second stage in the conversation
// pipeline, after routing.
package stage

import (
	"context"
	"fmt"
	"time"

	"github.com/emaharmony/prizm/internal/event"
)

// SessionStage finds or creates a conversation session and adds the user message.
type SessionStage struct {
	// SessionMgr handles session CRUD operations.
	SessionMgr SessionManager

	// ChannelType is the source channel (e.g., "discord").
	ChannelType string

	// ChannelID is the source channel ID.
	ChannelID string

	// UserID is the message author's ID.
	UserID string

	// RawContent is the original user message content (before routing cleanup).
	RawContent string
}

// Name returns the stage identifier.
func (s *SessionStage) Name() string {
	return "session"
}

// Validate checks that required dependencies are present.
func (s *SessionStage) Validate(rc *RunContext) error {
	if s.SessionMgr == nil {
		return fmt.Errorf("stage %s: session manager is required", s.Name())
	}
	return nil
}

// Execute finds or creates a session and adds the user message.
func (s *SessionStage) Execute(ctx context.Context, rc *RunContext) (*RunContext, *StageResult, error) {
	start := time.Now()

	// Find or create session
	sess, err := s.SessionMgr.FindActive(s.ChannelType, s.ChannelID, s.UserID)
	if err != nil {
		return rc, &StageResult{
			StageName: s.Name(),
			Success:   false,
			Error:     fmt.Sprintf("find session: %v", err),
		}, nil
	}

	if sess == nil {
		// No active session — create one
		sess, err = s.SessionMgr.Create(rc.Agent, s.ChannelType, s.ChannelID, s.UserID)
		if err != nil {
			return rc, &StageResult{
				StageName: s.Name(),
				Success:   false,
				Error:     fmt.Sprintf("create session: %v", err),
			}, nil
		}
	}

	// Add user message to session
	_, err = s.SessionMgr.AddMessage(sess.ID, "user", s.RawContent, "")
	if err != nil {
		// Non-fatal — session save failure shouldn't block the pipeline
		// Log but continue
		rc.SessionSaveFailed = true
	}

	duration := time.Since(start).Milliseconds()

	// Emit session resolved event
	evt := event.NewEvent("prizm.session.resolved", "session-stage", map[string]any{
		"run_id":       rc.RunID,
		"session_id":   sess.ID,
		"agent_id":     rc.Agent,
		"channel_type": s.ChannelType,
		"channel_id":   s.ChannelID,
		"user_id":      s.UserID,
		"duration_ms":  duration,
	})

	newRC := rc.WithEvent(evt).WithSessionID(sess.ID)

	return newRC, &StageResult{
		StageName: s.Name(),
		Success:   true,
		Data: map[string]any{
			"session_id":  sess.ID,
			"duration_ms": duration,
		},
	}, nil
}

// Rollback is a no-op — session creation has no rollback.
func (s *SessionStage) Rollback(ctx context.Context, rc *RunContext) error {
	return nil
}

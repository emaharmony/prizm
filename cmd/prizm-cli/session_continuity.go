package main

import (
	"log"
	"time"

	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/session"
)

type continuityWindow struct {
	recallStart time.Time
	weekStart   time.Time
}

func continuityLocation(cfg *orchestrator.Config) *time.Location {
	if cfg == nil || cfg.Sessions.RecallTimezone == "" || cfg.Sessions.RecallTimezone == "Local" {
		return time.Local
	}
	loc, err := time.LoadLocation(cfg.Sessions.RecallTimezone)
	if err != nil {
		return time.Local
	}
	return loc
}

func continuityWindowFor(cfg *orchestrator.Config, now time.Time) continuityWindow {
	loc := continuityLocation(cfg)
	mode := "calendar_week"
	if cfg != nil && cfg.Sessions.RecallWindowMode != "" {
		mode = cfg.Sessions.RecallWindowMode
	}
	localNow := now.In(loc)
	weekStartLocal := startOfWeek(localNow)
	weekStart := weekStartLocal.UTC()
	if mode == "rolling_days" {
		return continuityWindow{
			recallStart: now.UTC().Add(-continuityRecallWindow(cfg)),
			weekStart:   weekStart,
		}
	}
	return continuityWindow{recallStart: weekStart, weekStart: weekStart}
}

func startOfWeek(t time.Time) time.Time {
	year, month, day := t.Date()
	dayStart := time.Date(year, month, day, 0, 0, 0, 0, t.Location())
	offset := (int(dayStart.Weekday()) + 6) % 7 // Monday = 0
	return dayStart.AddDate(0, 0, -offset)
}

func continuityRecallWindow(cfg *orchestrator.Config) time.Duration {
	if cfg == nil || cfg.Sessions.ShortTermWindowDays <= 0 {
		return 7 * 24 * time.Hour
	}
	return time.Duration(cfg.Sessions.ShortTermWindowDays) * 24 * time.Hour
}

func continuityVerbatimLimit(cfg *orchestrator.Config) int {
	if cfg == nil || cfg.Sessions.VerbatimRecentMessages <= 0 {
		return 40
	}
	return cfg.Sessions.VerbatimRecentMessages
}

func getOrCreateSessionForMessage(mgr *session.Manager, cfg *orchestrator.Config, agentID, channel, channelID, externalUserID string) (*session.Session, string, error) {
	ownerID := externalUserID
	if cfg != nil {
		ownerID = cfg.ResolveOwnerID(channel, externalUserID)
	}
	if cfg != nil && cfg.Sessions.ContinuityScope == "channel_user" {
		sess, err := mgr.FindActive(channel, channelID, externalUserID)
		if err != nil || sess != nil {
			return sess, ownerID, err
		}
		sess, err = mgr.Create(agentID, channel, channelID, externalUserID)
		return sess, ownerID, err
	}
	aliases := []string{externalUserID}
	if cfg != nil {
		aliases = cfg.OwnerAliases(ownerID)
	}
	window := continuityWindowFor(cfg, time.Now())
	sess, err := mgr.GetOrCreateOwnerAgentSince(
		agentID,
		channel,
		channelID,
		ownerID,
		aliases,
		window.recallStart,
		window.weekStart,
		continuityVerbatimLimit(cfg),
	)
	return sess, ownerID, err
}

// cloneSessionWithSystemMemory injects memory content into the session
// in a cache-safe way. Instead of prepending a system message (which
// breaks the prompt cache prefix), it appends the content as a
// <system-reminder> tag in the last user message. This preserves the
// cached prefix so only the delta tokens are processed.
//
// V79: Cache-safe injection pattern (inspired by Claude Code).
// If no user message exists, falls back to a system message.
func cloneSessionWithSystemMemory(sess *session.Session, content string) *session.Session {
	if sess == nil || content == "" {
		return sess
	}

	cloned := *sess
	cloned.Messages = make([]session.Message, len(sess.Messages))
	copy(cloned.Messages, sess.Messages)

	// Find the last user message and append the memory block as a system-reminder
	for i := len(cloned.Messages) - 1; i >= 0; i-- {
		if cloned.Messages[i].Role == "user" {
			cloned.Messages[i] = session.Message{
				ID:        cloned.Messages[i].ID,
				Role:      "user",
				Content:   cloned.Messages[i].Content + "\n\n<system-reminder>\n" + content + "\n</system-reminder>",
				Timestamp: cloned.Messages[i].Timestamp,
			}
			return &cloned
		}
	}

	// No user message found — inject as system message (fallback, breaks cache)
	log.Printf("[MEMORY] WARNING: no user message found for cache-safe injection, falling back to system message")
	cloned.Messages = append([]session.Message{{
		ID:        "memory-context",
		Role:      "system",
		Content:   content,
		Timestamp: time.Now().UTC(),
	}}, cloned.Messages...)
	return &cloned
}

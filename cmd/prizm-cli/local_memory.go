package main

import (
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/remembrance"
	"github.com/emaharmony/prizm/internal/session"
)

func enqueueLocalMemoryUpdate(
	mgr *session.Manager,
	cfg *orchestrator.Config,
	remClient *remembrance.Client,
	remSem chan struct{},
	remCache *remembranceCache,
	ownerID string,
	externalUserID string,
	agentID string,
	sessionID string,
	runID string,
) {
	if mgr == nil || agentID == "" || ownerID == "" {
		return
	}
	aliases := []string{externalUserID}
	if cfg != nil {
		aliases = cfg.OwnerAliases(ownerID)
	}
	window := continuityWindowFor(cfg, time.Now())
	limit := continuityVerbatimLimit(cfg)

	go func() {
		summary, err := mgr.UpdateLocalSummary(ownerID, agentID, aliases, window.recallStart, window.weekStart, limit)
		if err != nil {
			log.Printf("[LOCAL-MEMORY] summary update failed owner=%s agent=%s: %v", ownerID, agentID, err)
			return
		}
		if summary == nil || strings.TrimSpace(summary.Summary) == "" {
			return
		}
		if remClient == nil || !shouldIngestLocalSummary(summary.Summary) {
			return
		}
		if !tryAcquireRemembranceSlot(remSem) {
			log.Printf("[REMEMBRANCE] skipped summary candidate (run %s): concurrency limit reached", runID)
			return
		}
		defer releaseRemembranceSlot(remSem)

		sourceRef := runID
		if sourceRef == "" {
			sourceRef = sessionID
		}
		result, err := remClient.CaptureWithMetadata(remembrance.CaptureRequest{
			OwnerID:         ownerID,
			AgentID:         agentID,
			SessionID:       sessionID,
			MessageIDs:      summary.SourceMessageIDs,
			Scope:           "user",
			Category:        "local_summary_candidate",
			Summary:         truncateStr(summary.Summary, 240),
			SourceRef:       sourceRef,
			ImportanceScore: localSummaryImportance(summary.Summary),
			ProjectID:       "prizm",
			Title:           "Prizm local conversation summary",
			Content:         summary.Summary,
			SourceType:      "local_summary",
			SourceAgent:     fmt.Sprintf("prizm:%s", agentID),
		})
		if err != nil {
			log.Printf("[REMEMBRANCE] summary candidate failed (run %s): %v", runID, err)
			return
		}
		if decision, ok := result["decision"]; ok {
			log.Printf("[REMEMBRANCE] summary candidate run %s: decision=%v", runID, decision)
			if decision == "PERSIST" || decision == "persist" {
				atomic.AddInt64(&dreamPersistCount, 1)
			}
		} else {
			log.Printf("[REMEMBRANCE] captured local summary candidate from run %s", runID)
		}
		if remCache != nil {
			remCache.Invalidate(fmt.Sprintf("%s:%s", agentID, sessionID))
		}
	}()
}

func tryAcquireRemembranceSlot(ch chan struct{}) bool {
	if ch == nil {
		return true
	}
	select {
	case ch <- struct{}{}:
		return true
	default:
		return false
	}
}

func releaseRemembranceSlot(ch chan struct{}) {
	if ch == nil {
		return
	}
	<-ch
}

func shouldIngestLocalSummary(summary string) bool {
	text := strings.ToLower(summary)
	markers := []string{
		"remember",
		"decided",
		"decision",
		"prefer",
		"preference",
		"always",
		"never",
		"must",
		"should",
		"project",
		"fact",
		"important",
		"owner:",
	}
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func localSummaryImportance(summary string) float64 {
	text := strings.ToLower(summary)
	if strings.Contains(text, "remember") || strings.Contains(text, "decided") || strings.Contains(text, "decision") {
		return 0.85
	}
	if strings.Contains(text, "prefer") || strings.Contains(text, "always") || strings.Contains(text, "never") {
		return 0.75
	}
	return 0.65
}

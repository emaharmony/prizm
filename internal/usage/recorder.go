package usage

import (
	"log"
	"strings"

	"github.com/emaharmony/prizm/internal/cost"
	"github.com/emaharmony/prizm/internal/provider"
)

// Recorder adapts the usage Store to the provider.UsageRecorder interface: it
// receives a per-call token record from the provider registry decorator,
// estimates the cost, and persists it. It is safe to use a nil *Recorder — every
// method is a no-op — so tracking can be disabled without nil-checks at call
// sites.
type Recorder struct {
	store *Store
}

// NewRecorder returns a Recorder backed by store. A nil store yields a no-op
// recorder.
func NewRecorder(store *Store) *Recorder {
	return &Recorder{store: store}
}

// RecordUsage implements provider.UsageRecorder. Calls with no token movement are
// dropped so failed/empty responses don't clutter the series.
func (r *Recorder) RecordUsage(u provider.UsageRecord) {
	if r == nil || r.store == nil {
		return
	}
	if u.PromptTokens == 0 && u.CompletionTokens == 0 {
		return
	}
	source := u.Source
	if source == "" {
		source = sourceFromRunID(u.RunID)
	}
	ev := Event{
		Agent:            u.Agent,
		Provider:         u.Provider,
		Model:            u.Model,
		Source:           source,
		RunID:            u.RunID,
		ConversationID:   u.ConversationID,
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		CostUSD:          cost.EstimateCostSplit(u.Model, u.PromptTokens, u.CompletionTokens),
	}
	if err := r.store.Record(ev); err != nil {
		log.Printf("[usage] record failed: %v", err)
	}
}

// sourceFromRunID infers the call source from the run ID prefix when the caller
// didn't set one. Invocations use "invoke-<id>"; everything else is treated as
// interactive chat.
func sourceFromRunID(runID string) string {
	switch {
	case strings.HasPrefix(runID, "invoke-"):
		return "invoke"
	case strings.HasPrefix(runID, "workflow"), strings.Contains(runID, "gated"):
		return "workflow"
	default:
		return "chat"
	}
}

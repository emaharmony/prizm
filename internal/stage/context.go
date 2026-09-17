// Package stage provides the pipeline stages for a Prizm run.
//
// The V19 Context stage reads workspace files and injects them into the
// LLM prompt as labeled sections. It emits events for observability and
// respects token budgets using model context window information.
package stage

import (
	"fmt"
	"time"

	"github.com/emaharmony/prizm/internal/context"
	"github.com/emaharmony/prizm/internal/event"
)

// ContextStage reads workspace files and produces formatted context
// for injection into the LLM prompt.
type ContextStage struct {
	WorkspaceRoot string
	NamedContexts []string
	AutoTask      string
	ExplicitFiles []string
	TokenBudget   int
	EventBus      interface{ Publish(evt event.Event) error }
}

// ContextResult holds the output of the context stage.
type ContextResult struct {
	ContextString string
	TotalTokens   int
	Files         []context.ContextFile
	Truncated     bool
	ContentHash   string
}

// Execute runs the context stage and returns the formatted context.
func (cs *ContextStage) Execute(runID, correlationID string) (*ContextResult, error) {
	startTime := time.Now()

	// Build context
	builder := context.NewBuilder(cs.WorkspaceRoot).
		WithNamedContexts(cs.NamedContexts).
		WithTokenBudget(cs.TokenBudget)

	if cs.AutoTask != "" {
		autoFiles := context.DiscoverFiles(cs.WorkspaceRoot, cs.AutoTask)
		builder = builder.WithAutoFiles(autoFiles)
	}

	if len(cs.ExplicitFiles) > 0 {
		builder = builder.WithExplicitFiles(cs.ExplicitFiles)
	}

	result, err := builder.Build()
	if err != nil {
		return nil, fmt.Errorf("context stage: %w", err)
	}

	// Emit events for each file read
	durationMs := time.Since(startTime).Milliseconds()
	for _, f := range result.Files {
		if cs.EventBus != nil {
			cs.EventBus.Publish(event.Event{
				ID:            event.NewID(),
				Type:          event.V19EventTypes.ContextFileRead,
				Source:        "prizm.context",
				Timestamp:     time.Now().Format(time.RFC3339),
				CorrelationID: correlationID,
				Payload: map[string]any{
					"file":             f.Path,
					"source":           f.Source,
					"size_bytes":       f.SizeBytes,
					"estimated_tokens": f.EstimatedTokens,
				},
				Metadata: event.EventMetadata{
					RunID:      runID,
					DurationMs: durationMs / int64(len(result.Files)),
				},
			})
		}
	}

	// Emit aggregated context.injected event
	if cs.EventBus != nil {
		fileNames := make([]string, len(result.Files))
		for i, f := range result.Files {
			fileNames[i] = f.Name
		}

		cs.EventBus.Publish(event.Event{
			ID:            event.NewID(),
			Type:          event.V19EventTypes.ContextInjected,
			Source:        "prizm.context",
			Timestamp:     time.Now().Format(time.RFC3339),
			CorrelationID: correlationID,
			Payload: map[string]any{
				"run_id":             runID,
				"files":              fileNames,
				"total_tokens":       result.TotalTokens,
				"truncated":          result.Truncated,
				"truncation_applied": result.Truncated,
			},
			Metadata: event.EventMetadata{
				RunID:      runID,
				DurationMs: durationMs,
				Outcome:    "success",
			},
		})
	}

	return &ContextResult{
		ContextString: result.FormattedString,
		TotalTokens:   result.TotalTokens,
		Files:         result.Files,
		Truncated:     result.Truncated,
		ContentHash:   result.ContentHash,
	}, nil
}

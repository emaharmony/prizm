// Package cost provides token usage aggregation and cost reporting for Prizm runs.
// It subscribes to LLM events and accumulates per-provider, per-model, and per-agent costs.
package cost

import (
	"sync"
)

// TokenUsage holds a detailed breakdown of LLM token consumption.
// Replaces the flat TokenCost (int) in EventMetadata with structured data.
type TokenUsage struct {
	PromptTokens     int     `json:"prompt_tokens,omitempty"`
	CompletionTokens int     `json:"completion_tokens,omitempty"`
	TotalTokens      int     `json:"total_tokens,omitempty"`
	EstimatedCostUsd float64 `json:"estimated_cost_usd,omitempty"` // plain USD
}

// CostReport aggregates token usage and cost across a single run.
type CostReport struct {
	RunID            string             `json:"run_id"`
	TotalTokens      int                `json:"total_tokens"`
	PromptTokens     int                `json:"prompt_tokens"`
	CompletionTokens int                `json:"completion_tokens"`
	EstimatedCostUsd float64            `json:"estimated_cost_usd"`
	ByProvider       map[string]float64 `json:"by_provider"` // cost per provider
	ByModel          map[string]float64 `json:"by_model"`    // cost per model
	ByAgent          map[string]int     `json:"by_agent"`    // tokens per agent
	EventCount       int                `json:"event_count"`
	DurationMs       int64              `json:"duration_ms"`
	MaxTokens        int                `json:"max_tokens,omitempty"`
	RemainingTokens  int                `json:"remaining_tokens,omitempty"`
	PercentUsed      float64            `json:"percent_used,omitempty"`
	Status           string             `json:"status,omitempty"`
}

// CostTracker accumulates token usage from LLM events.
// Thread-safe. Call Track() for each LLM completion event, then Report() for the summary.
type CostTracker struct {
	mu               sync.Mutex
	runID            string
	totalTokens      int
	promptTokens     int
	completionTokens int
	estimatedCostUsd float64
	byProvider       map[string]float64
	byModel          map[string]float64
	byAgent          map[string]int
	eventCount       int
	startTime        int64 // Unix milliseconds
}

// NewCostTracker creates a new cost tracker for the given run.
func NewCostTracker(runID string) *CostTracker {
	return &CostTracker{
		runID:      runID,
		byProvider: make(map[string]float64),
		byModel:    make(map[string]float64),
		byAgent:    make(map[string]int),
		startTime:  0,
	}
}

// Track records token usage from a single LLM completion event.
func (t *CostTracker) Track(provider, model, agent string, usage *TokenUsage) {
	if usage == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	t.totalTokens += usage.TotalTokens
	t.promptTokens += usage.PromptTokens
	t.completionTokens += usage.CompletionTokens
	t.estimatedCostUsd += usage.EstimatedCostUsd
	t.eventCount++

	if provider != "" {
		t.byProvider[provider] += usage.EstimatedCostUsd
	}
	if model != "" {
		t.byModel[model] += usage.EstimatedCostUsd
	}
	if agent != "" {
		t.byAgent[agent] += usage.TotalTokens
	}
}

// Report returns the aggregated cost report.
func (t *CostTracker) Report() *CostReport {
	t.mu.Lock()
	defer t.mu.Unlock()

	return &CostReport{
		RunID:            t.runID,
		TotalTokens:      t.totalTokens,
		PromptTokens:     t.promptTokens,
		CompletionTokens: t.completionTokens,
		EstimatedCostUsd: t.estimatedCostUsd,
		ByProvider:       copyMapFloat(t.byProvider),
		ByModel:          copyMapFloat(t.byModel),
		ByAgent:          copyMapInt(t.byAgent),
		EventCount:       t.eventCount,
	}
}

// copyMapFloat creates a shallow copy of a float64 map.
func copyMapFloat(m map[string]float64) map[string]float64 {
	result := make(map[string]float64, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}

// copyMapInt creates a shallow copy of an int map.
func copyMapInt(m map[string]int) map[string]int {
	result := make(map[string]int, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}

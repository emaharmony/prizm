package cost

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/emaharmony/prizm/internal/event"
)

// ReportFromDataDir aggregates cost events from <dataDir>/<runID>/events.jsonl.
func ReportFromDataDir(runID, dataDir string) (*CostReport, error) {
	return ReportFromRunDir(runID, filepath.Join(dataDir, runID))
}

// ReportFromRunDir aggregates cost events from <runDir>/events.jsonl.
func ReportFromRunDir(runID, runDir string) (*CostReport, error) {
	return ReportFromEventsFile(runID, filepath.Join(runDir, "events.jsonl"))
}

// ReportFromEventsFile aggregates cost events from an events.jsonl file.
func ReportFromEventsFile(runID, eventsFile string) (*CostReport, error) {
	f, err := os.Open(eventsFile)
	if err != nil {
		return nil, fmt.Errorf("open events: %w", err)
	}
	defer f.Close()
	return ReportFromEvents(runID, f)
}

// ReportFromEvents aggregates prizm.llm.* token usage from a JSONL event stream.
func ReportFromEvents(runID string, r io.Reader) (*CostReport, error) {
	tracker := NewCostTracker(runID)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var evt event.Event
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			continue
		}
		if evt.Type != "prizm.llm.completed" && evt.Type != "prizm.llm.failed" {
			continue
		}
		usage := usageFromEvent(evt)
		if usage == nil {
			continue
		}
		provider, _ := evt.Payload["provider"].(string)
		model, _ := evt.Payload["model"].(string)
		if model == "" {
			model = evt.Metadata.Model
		}
		tracker.Track(provider, model, evt.Metadata.Agent, usage)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return tracker.Report(), nil
}

func usageFromEvent(evt event.Event) *TokenUsage {
	if evt.Metadata.TokenUsage != nil {
		tu := evt.Metadata.TokenUsage
		total := tu.TotalTokens
		if total == 0 {
			total = tu.PromptTokens + tu.CompletionTokens
		}
		costUSD := tu.EstimatedCostUsd
		model, _ := evt.Payload["model"].(string)
		if model == "" {
			model = evt.Metadata.Model
		}
		if costUSD == 0 && total > 0 {
			costUSD = EstimateCost(model, total)
		}
		return &TokenUsage{PromptTokens: tu.PromptTokens, CompletionTokens: tu.CompletionTokens, TotalTokens: total, EstimatedCostUsd: costUSD}
	}
	if evt.Metadata.TokenCost > 0 {
		model, _ := evt.Payload["model"].(string)
		if model == "" {
			model = evt.Metadata.Model
		}
		return &TokenUsage{TotalTokens: evt.Metadata.TokenCost, EstimatedCostUsd: EstimateCost(model, evt.Metadata.TokenCost)}
	}
	return nil
}

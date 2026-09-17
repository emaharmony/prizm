package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/emaharmony/prizm/internal/cost"
	"github.com/emaharmony/prizm/internal/event"
	v2 "github.com/emaharmony/prizm/internal/workflow/v2"
)

// costCmdFlags and cost command implementation.
var costCmd = flag.NewFlagSet("cost", flag.ExitOnError)
var costDataDir = costCmd.String("data", "./runs", "Data directory containing run outputs")

func init() {
	// Registered in main.go switch statement
}

func runCostCommand(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: prizm cost <run_id>")
	}
	runID := args[0]
	dataDir := *costDataDir

	report, err := buildCostReport(runID, dataDir)
	if err != nil {
		report = &cost.CostReport{RunID: runID}
		if !enrichCostReportWithWorkflowState(report, runID, dataDir) {
			return fmt.Errorf("cost: %w", err)
		}
	} else {
		enrichCostReportWithWorkflowState(report, runID, dataDir)
	}

	fmt.Printf("Run: %s\n", report.RunID)
	fmt.Printf("Total tokens: %d (prompt: %d, completion: %d)\n",
		report.TotalTokens, report.PromptTokens, report.CompletionTokens)
	fmt.Printf("Estimated cost: $%.6f\n", report.EstimatedCostUsd)
	fmt.Print(formatCostBudgetLines(report))
	fmt.Printf("Events: %d\n", report.EventCount)

	if len(report.ByProvider) > 0 {
		fmt.Println("\nBy provider:")
		for provider, costVal := range report.ByProvider {
			fmt.Printf("  %s: $%.6f\n", provider, costVal)
		}
	}

	if len(report.ByModel) > 0 {
		fmt.Println("\nBy model:")
		for model, costVal := range report.ByModel {
			fmt.Printf("  %s: $%.6f\n", model, costVal)
		}
	}

	if len(report.ByAgent) > 0 {
		fmt.Println("\nBy agent:")
		for agent, tokens := range report.ByAgent {
			fmt.Printf("  %s: %d tokens\n", agent, tokens)
		}
	}

	return nil
}

func formatCostBudgetLines(report *cost.CostReport) string {
	if report == nil {
		return ""
	}
	var b strings.Builder
	if report.MaxTokens < 0 {
		b.WriteString("Token ceiling: unlimited\n")
	} else if report.MaxTokens > 0 {
		fmt.Fprintf(&b, "Token ceiling: %d (remaining: %d, used: %.1f%%)\n", report.MaxTokens, report.RemainingTokens, report.PercentUsed)
	}
	if report.Status != "" {
		fmt.Fprintf(&b, "Status: %s\n", report.Status)
	}
	return b.String()
}

func buildCostReport(runID, dataDir string) (*cost.CostReport, error) {
	return cost.ReportFromDataDir(runID, dataDir)
}

func enrichCostReportWithWorkflowState(report *cost.CostReport, runID, dataDir string) bool {
	if report == nil {
		return false
	}
	for _, path := range []string{
		filepath.Join(dataDir, "workflow-"+runID+".json"),
		filepath.Join(dataDir, "gated-loop", "workflow-"+runID+".json"),
		filepath.Join(dataDir, runID, "workflow-"+runID+".json"),
	} {
		st, err := v2.LoadWorkflowState(path)
		if err != nil {
			continue
		}
		if report.TotalTokens == 0 {
			report.PromptTokens = st.TotalPromptTokens
			report.CompletionTokens = st.TotalCompletionTokens
			report.TotalTokens = st.TotalPromptTokens + st.TotalCompletionTokens
		}
		report.Status = string(st.Status)
		report.MaxTokens = st.MaxTotalTokens
		if report.MaxTokens > 0 {
			report.RemainingTokens = report.MaxTokens - report.TotalTokens
			if report.RemainingTokens < 0 {
				report.RemainingTokens = 0
			}
			report.PercentUsed = float64(report.TotalTokens) / float64(report.MaxTokens) * 100
		}
		return true
	}
	return false
}

// Trace command implementation.
var traceCmd = flag.NewFlagSet("trace", flag.ExitOnError)
var traceDataDir = traceCmd.String("data", "./runs", "Data directory containing run outputs")

func runTraceCommand(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: prizm trace <run_id>")
	}
	runID := args[0]
	dataDir := *traceDataDir

	events, err := loadTraceEvents(runID, dataDir)
	if err != nil {
		return fmt.Errorf("trace: %w", err)
	}

	printTrace(events)
	return nil
}

func loadTraceEvents(runID, dataDir string) ([]event.Event, error) {
	eventsFile := filepath.Join(dataDir, runID, "events.jsonl")
	f, err := os.Open(eventsFile)
	if err != nil {
		return nil, fmt.Errorf("open events: %w", err)
	}
	defer f.Close()

	var events []event.Event
	decoder := json.NewDecoder(f)
	for decoder.More() {
		var evt event.Event
		if err := decoder.Decode(&evt); err != nil {
			continue
		}
		events = append(events, evt)
	}
	return events, nil
}

func printTrace(events []event.Event) {
	children := make(map[string][]*event.Event)
	var roots []*event.Event

	for i := range events {
		evt := &events[i]
		if evt.ParentID == "" {
			roots = append(roots, evt)
		} else {
			children[evt.ParentID] = append(children[evt.ParentID], evt)
		}
	}

	var totalTokens int
	var totalCost float64

	for _, root := range roots {
		printEventNode(root, "", children, &totalTokens, &totalCost)
	}

	fmt.Printf("\nTotal: %d tokens | $%.4f\n", totalTokens, totalCost)
}

func printEventNode(evt *event.Event, indent string, children map[string][]*event.Event, totalTokens *int, totalCost *float64) {
	duration := ""
	if evt.Metadata.DurationMs > 0 {
		duration = fmt.Sprintf(" [%dms]", evt.Metadata.DurationMs)
	} else if evt.Metadata.LatencyMs > 0 {
		duration = fmt.Sprintf(" [%dms]", evt.Metadata.LatencyMs)
	}

	tokens := ""
	if evt.Metadata.TokenUsage != nil && evt.Metadata.TokenUsage.TotalTokens > 0 {
		tokens = fmt.Sprintf(" %d tokens", evt.Metadata.TokenUsage.TotalTokens)
		*totalTokens += evt.Metadata.TokenUsage.TotalTokens
		*totalCost += evt.Metadata.TokenUsage.EstimatedCostUsd
	} else if evt.Metadata.TokenCost > 0 {
		tokens = fmt.Sprintf(" %d tokens", evt.Metadata.TokenCost)
		*totalTokens += evt.Metadata.TokenCost
		*totalCost += cost.EstimateCost(evt.Metadata.Model, evt.Metadata.TokenCost)
	}

	outcome := ""
	if evt.Metadata.Outcome != "" {
		outcome = fmt.Sprintf(" (%s)", evt.Metadata.Outcome)
	}

	eventType := strings.TrimPrefix(evt.Type, "prizm.")

	source := ""
	if evt.Metadata.Agent != "" {
		source = " " + evt.Metadata.Agent
	} else if evt.Metadata.Model != "" {
		source = " " + evt.Metadata.Model
	}

	payload := ""
	switch evt.Type {
	case "prizm.task.created":
		if task, ok := evt.Payload["task"].(string); ok {
			payload = " " + traceTruncate(task, 40)
		}
	case "prizm.tool.completed", "prizm.tool.requested":
		if tool, ok := evt.Payload["tool_name"].(string); ok {
			payload = " " + tool
		}
	case "prizm.mutation.proposed", "prizm.mutation.applied":
		if path, ok := evt.Payload["target_path"].(string); ok {
			payload = " " + filepath.Base(path)
		}
	case "prizm.approval.requested":
		if id, ok := evt.Payload["approval_id"].(string); ok {
			payload = " " + traceTruncate(id, 16)
		}
	}

	fmt.Printf("%s└─ %s%s%s%s%s%s\n",
		indent, eventType, source, payload, tokens, duration, outcome)

	childIndent := indent + "   "
	for _, child := range children[evt.ID] {
		printEventNode(child, childIndent, children, totalTokens, totalCost)
	}
}

func traceTruncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

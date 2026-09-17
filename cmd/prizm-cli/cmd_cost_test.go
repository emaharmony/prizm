package main

import (
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/cost"
	v2 "github.com/emaharmony/prizm/internal/workflow/v2"
)

func TestFormatCostBudgetLines(t *testing.T) {
	out := formatCostBudgetLines(&cost.CostReport{MaxTokens: 1000, RemainingTokens: 250, PercentUsed: 75, Status: "budget_exhausted"})
	for _, want := range []string{"Token ceiling: 1000", "remaining: 250", "75.0%", "Status: budget_exhausted"} {
		if !strings.Contains(out, want) {
			t.Fatalf("budget lines missing %q:\n%s", want, out)
		}
	}
}

func TestFormatCostBudgetLinesUnlimited(t *testing.T) {
	out := formatCostBudgetLines(&cost.CostReport{MaxTokens: -1})
	if !strings.Contains(out, "Token ceiling: unlimited") {
		t.Fatalf("unlimited budget line missing:\n%s", out)
	}
}

func TestEnrichCostReportWithWorkflowStateOnly(t *testing.T) {
	dir := t.TempDir()
	cfg := v2.DefaultConfig()
	cfg.Global.MaxTotalTokens = 1000
	st := v2.NewWorkflowState(cfg)
	st.RunID = "state-only"
	st.Status = v2.StatusBudgetExhausted
	st.AddTokens(150, 50)
	if err := v2.SaveWorkflowState(st, dir); err != nil {
		t.Fatalf("save workflow state: %v", err)
	}

	report := &cost.CostReport{RunID: "state-only"}
	if !enrichCostReportWithWorkflowState(report, "state-only", dir) {
		t.Fatal("expected workflow state enrichment")
	}
	if report.TotalTokens != 200 || report.MaxTokens != 1000 || report.RemainingTokens != 800 || report.PercentUsed != 20 || report.Status != string(v2.StatusBudgetExhausted) {
		t.Fatalf("unexpected enriched report: %+v", report)
	}
}

package tracker

import (
	"strings"
	"testing"
)

func TestRenderFrameStrictASCIIAndAnimation(t *testing.T) {
	s := Snapshot{
		Workflow:  "gated-loop",
		Status:    "running",
		Current:   "EXECUTION",
		TokTotal:  250000,
		TokMax:    1000000,
		Events:    5,
		LastEvent: "phase.tokens",
		Phases: []PhaseView{
			{Name: "PLAN", Status: "passed"},
			{Name: "EXECUTION", Status: "running", GateSeen: true, GateScore: 0.91, LastTool: "read_file", ToolRetry: 2},
		},
		Delegations: map[string]string{"T2": "mango:sent"},
	}

	out0 := RenderFrame(s, 0)
	out1 := RenderFrame(s, 1)
	for _, want := range []string{"PRIZM PANEL", "gated-loop", "Budget", "25%", "PLAN", "EXECUTION", "gate 0.91", "read_file", "retry 2", "Delegations", "T2"} {
		if !strings.Contains(out0, want) {
			t.Fatalf("render missing %q:\n%s", want, out0)
		}
	}
	if out0 == out1 {
		t.Fatalf("animation frame did not change:\n%s", out0)
	}
	for _, r := range out0 {
		if r > 127 {
			t.Fatalf("render contains non-ASCII rune %q in:\n%s", r, out0)
		}
	}
}

func TestRenderFrameEmptyState(t *testing.T) {
	out := RenderFrame(Snapshot{Status: "connecting"}, 0)
	if !strings.Contains(out, "waiting for workflow events") || !strings.Contains(out, "last: none") {
		t.Fatalf("empty render missing defaults:\n%s", out)
	}
}

func TestTokenMeter(t *testing.T) {
	full := TokenMeter(1000000, 1000000)
	if !strings.Contains(full, "100%") || !strings.Contains(full, "#") {
		t.Fatalf("full meter wrong: %q", full)
	}
	half := TokenMeter(500000, 1000000)
	if !strings.Contains(half, "50%") {
		t.Fatalf("half meter wrong: %q", half)
	}
	none := TokenMeter(1234, 0)
	if !strings.Contains(none, "no ceiling") {
		t.Fatalf("uncapped meter wrong: %q", none)
	}
	over := TokenMeter(2000000, 1000000)
	if !strings.Contains(over, "100%") {
		t.Fatalf("over-budget meter should clamp: %q", over)
	}
}

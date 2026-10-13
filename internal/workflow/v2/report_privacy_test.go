package v2

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestReportOutputAndArtifactExcludePrivateReasoning(t *testing.T) {
	state := NewWorkflowState(DefaultConfig())
	phase := &ReportPhase{}
	if err := phase.Enter(context.Background(), state); err != nil {
		t.Fatal(err)
	}

	// A partial private block anywhere in a final response must not complete
	// the phase or enter the report state.
	action, err := phase.RunIteration(context.Background(), state, `{"type":"final","content":"Public text <think>PRIVATE_CANARY"}`)
	if err != nil {
		t.Fatal(err)
	}
	if action.Type != ActionContinue || strings.Contains(state.Report.Content, "PRIVATE_CANARY") {
		t.Fatalf("incomplete private block was accepted: action=%+v report=%q", action, state.Report.Content)
	}

	content := "Visible <think>PRIVATE_CANARY</think> text\n```diff\n-<think>literal patch data</think>\n+kept\n```"
	action, err = phase.RunIteration(context.Background(), state, `{"type":"final","content":`+mustJSONString(t, content)+`}`)
	if err != nil {
		t.Fatal(err)
	}
	if action.Type != ActionFinal || strings.Contains(action.FinalContent, "PRIVATE_CANARY") {
		t.Fatalf("final response was not sanitized: action=%+v", action)
	}

	formatted := FormatFinalReport(state)
	if strings.Contains(formatted, "PRIVATE_CANARY") || !strings.Contains(formatted, "<think>literal patch data</think>") {
		t.Fatalf("formatted report leaked reasoning or changed fenced patch data:\n%s", formatted)
	}

	path, err := WriteReportArtifact(state, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(artifact), "PRIVATE_CANARY") || !strings.Contains(string(artifact), "<think>literal patch data</think>") {
		t.Fatalf("report artifact leaked reasoning or changed fenced patch data:\n%s", artifact)
	}

	// The formatter is also a boundary for report states created outside this
	// phase and fails closed when their reasoning block is incomplete.
	state.Report.Content = "visible <think>PRIVATE_CANARY"
	formatted = FormatFinalReport(state)
	if strings.Contains(formatted, "PRIVATE_CANARY") || strings.Contains(formatted, "### Report Content") {
		t.Fatalf("formatter did not fail closed on incomplete private block:\n%s", formatted)
	}
}

func mustJSONString(t *testing.T, value string) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

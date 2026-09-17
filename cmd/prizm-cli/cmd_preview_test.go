package main

import (
	"strings"
	"testing"

	v2 "github.com/emaharmony/prizm/internal/workflow/v2"
)

func TestRenderWorkflowPreviewDefault(t *testing.T) {
	out := renderWorkflowPreview(v2.DefaultConfig(), "built-in default")

	for _, want := range []string{
		"gated-loop preview",
		"built-in default",
		"Budgets",
		"max iterations:  60",
		"max tokens:",
		"stuck cap:",
		"Phases",
		"EXECUTION",
		"verify=go_test_all",
		"blocking",
		"Confidence domains",
		"no LLM ran",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("preview missing %q:\n%s", want, out)
		}
	}
}

func TestRenderWorkflowPreviewShowsGatesAndBlocking(t *testing.T) {
	cfg := v2.DefaultConfig()
	out := renderWorkflowPreview(cfg, "x")
	// PROBE uses assumption_threshold; FEEDBACK_PRE blocks on fallback.
	if !strings.Contains(out, "gate=assumption_threshold") {
		t.Fatalf("expected PROBE gate type:\n%s", out)
	}
	if !strings.Contains(out, "blocks on fallback") {
		t.Fatalf("expected a blocking phase to be flagged:\n%s", out)
	}
}

func TestResolvePreviewConfigFallsBackToDefault(t *testing.T) {
	// No workflow file, non-existent config path → built-in default.
	cfg, source, err := resolvePreviewConfig("", "this-config-does-not-exist.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil || cfg.Name != "gated-loop" || source != "built-in default" {
		t.Fatalf("expected built-in default, got cfg=%v source=%q", cfg, source)
	}
}

func TestResolvePreviewConfigExplicitFileError(t *testing.T) {
	if _, _, err := resolvePreviewConfig("nope.yaml", "prizm.yaml"); err == nil {
		t.Fatal("expected error for missing explicit workflow file")
	}
}

func TestRenderWorkflowPreviewZeroTokenBudgetShowsDefault(t *testing.T) {
	cfg := v2.DefaultConfig()
	cfg.Global.MaxTotalTokens = 0

	out := renderWorkflowPreview(cfg, "raw")
	if !strings.Contains(out, "max tokens:      2000.0k") {
		t.Fatalf("preview should show default token ceiling for zero budget:\n%s", out)
	}
}

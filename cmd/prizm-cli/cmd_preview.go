// Package main implements the `prizm preview` subcommand: a static preview of the
// gated-loop workflow that *would* run, so a user can see the approach — phases,
// gates, budgets, and verification — before launching and spending any budget.
//
// Usage:
//
//	prizm preview [--config prizm.yaml] [--workflow <file>]
//
// It loads the effective gated-loop config (an explicit --workflow file, else the
// project's configured workflow_config, else the built-in default) and renders it.
// It runs no LLM and starts no work — it is a read-only explainer.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/tracker"
	v2 "github.com/emaharmony/prizm/internal/workflow/v2"
)

// resolvePreviewConfig picks the gated-loop config to preview: an explicit
// --workflow file wins; otherwise the project's configured workflow_config; else
// the built-in DefaultConfig. The second return value describes the source.
func resolvePreviewConfig(workflowPath, configPath string) (*v2.WorkflowConfig, string, error) {
	if workflowPath != "" {
		cfg, err := v2.LoadConfig(workflowPath)
		if err != nil {
			return nil, "", fmt.Errorf("load workflow %s: %w", workflowPath, err)
		}
		return cfg, workflowPath, nil
	}
	if cfg, err := orchestrator.LoadConfig(configPath); err == nil && cfg.Prizm.WorkflowConfig != "" {
		wf, lerr := v2.LoadConfig(cfg.Prizm.WorkflowConfig)
		if lerr != nil {
			return nil, "", fmt.Errorf("load configured workflow %s: %w", cfg.Prizm.WorkflowConfig, lerr)
		}
		return wf, cfg.Prizm.WorkflowConfig, nil
	}
	return v2.DefaultConfig(), "built-in default", nil
}

// renderWorkflowPreview formats a gated-loop config as a human-readable preview.
// Pure (no I/O) so it is unit-testable.
func renderWorkflowPreview(cfg *v2.WorkflowConfig, source string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🔎 gated-loop preview — %s (v%d)\n", cfg.Name, cfg.Version)
	if cfg.Description != "" {
		fmt.Fprintf(&b, "%s\n", cfg.Description)
	}
	fmt.Fprintf(&b, "source: %s\n", source)
	b.WriteString(strings.Repeat("─", 60) + "\n")

	// Budgets / global guards.
	b.WriteString("Budgets\n")
	fmt.Fprintf(&b, "  max iterations:  %d\n", cfg.Global.MaxTotalIterations)
	if cfg.Global.MaxTotalTime != "" {
		fmt.Fprintf(&b, "  max time:        %s\n", cfg.Global.MaxTotalTime)
	}
	maxTokens := cfg.Global.MaxTotalTokens
	tokLabel := tracker.HumanInt(maxTokens)
	switch {
	case maxTokens == v2.UnlimitedTokens:
		tokLabel = "unlimited"
	case maxTokens <= 0:
		tokLabel = tracker.HumanInt(v2.DefaultRunTokenCeiling)
	}
	fmt.Fprintf(&b, "  max tokens:      %s\n", tokLabel)
	repeat := cfg.Global.MaxRepeatedToolCalls
	if repeat <= 0 {
		repeat = 6
	}
	fmt.Fprintf(&b, "  stuck cap:       %d identical tool calls\n", repeat)

	// Phase plan.
	b.WriteString("\nPhases\n")
	for i, p := range cfg.Phases {
		fmt.Fprintf(&b, "  %d. %-13s", i+1, p.Name)
		if p.Gate.Type != "" {
			fmt.Fprintf(&b, " gate=%s", p.Gate.Type)
			if p.Gate.Threshold > 0 {
				fmt.Fprintf(&b, "(≥%.2g)", p.Gate.Threshold)
			}
		}
		fmt.Fprintf(&b, " · max %d iter · %d tools", p.MaxIterations, len(p.AllowedTools))
		if p.Verification != nil && p.Verification.Profile != "" {
			block := ""
			if p.Verification.Blocking {
				block = ", blocking"
			}
			fmt.Fprintf(&b, " · verify=%s%s", p.Verification.Profile, block)
		}
		if p.Fallback.Blocks {
			b.WriteString(" · ⛔ blocks on fallback")
		}
		b.WriteString("\n")
	}

	if len(cfg.ConfidenceDomains) > 0 {
		fmt.Fprintf(&b, "\nConfidence domains (%d): %s\n", len(cfg.ConfidenceDomains), strings.Join(cfg.ConfidenceDomains, ", "))
	}

	b.WriteString(strings.Repeat("─", 60) + "\n")
	b.WriteString("This is a static preview — no LLM ran and no work started.\n")
	return b.String()
}

// executePreview is the `prizm preview` entry point.
func executePreview(args []string) {
	fs := flag.NewFlagSet("preview", flag.ExitOnError)
	configPath := fs.String("config", "prizm.yaml", "Path to prizm.yaml configuration file")
	workflowPath := fs.String("workflow", "", "Explicit gated-loop workflow file to preview")
	fs.Parse(args)

	cfg, source, err := resolvePreviewConfig(*workflowPath, *configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
	if errs := v2.ValidateConfig(cfg); len(errs) > 0 {
		fmt.Fprintf(os.Stderr, "⚠️  workflow has config issues:\n")
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "  - %s\n", e)
		}
	}
	fmt.Print(renderWorkflowPreview(cfg, source))
}

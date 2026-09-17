// Package main implements the `prizm scan` subcommand: run the autopatch issue
// scanner over the repo and list the ranked findings, so a user can see what
// self-patching would target before (or instead of) starting a fix.
//
// Usage:
//
//	prizm scan [--root .] [--json]
//
// It runs the default deterministic detectors (go vet, TODO/FIXME, gofmt) and
// prints findings ranked by severity. It is read-only — it never starts a patch
// or PR; that is the gated autopatch pipeline's job (prizm serve / autopatch mode).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/emaharmony/prizm/internal/autopatch"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/task"
)

// scanIssueJSON is the machine-readable view of a discovered issue for `--json`.
type scanIssueJSON struct {
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Detail   string `json:"detail,omitempty"`
	Location string `json:"location,omitempty"`
}

func scanIssuesToJSON(issues []autopatch.Issue) ([]byte, error) {
	out := make([]scanIssueJSON, 0, len(issues))
	for _, is := range issues {
		out = append(out, scanIssueJSON{
			Kind: is.Kind, Severity: is.Severity.String(), Title: is.Title,
			Detail: is.Detail, Location: is.Location,
		})
	}
	return json.MarshalIndent(out, "", "  ")
}

func scanSeverityGlyph(s autopatch.IssueSeverity) string {
	switch s {
	case autopatch.SeverityHigh:
		return "🔴"
	case autopatch.SeverityMedium:
		return "🟡"
	default:
		return "⚪"
	}
}

// renderScanIssues formats the ranked findings (pure, testable).
func renderScanIssues(issues []autopatch.Issue) string {
	var b strings.Builder
	b.WriteString("🔍 autopatch scan\n")
	b.WriteString(strings.Repeat("─", 60) + "\n")
	if len(issues) == 0 {
		b.WriteString("  ✅ no issues found\n")
		return b.String()
	}
	for _, is := range issues {
		fmt.Fprintf(&b, "  %s [%-6s] %-8s %s\n", scanSeverityGlyph(is.Severity), is.Severity, is.Kind, is.Title)
		if is.Location != "" {
			fmt.Fprintf(&b, "       ↳ %s\n", is.Location)
		}
	}
	b.WriteString(strings.Repeat("─", 60) + "\n")
	fmt.Fprintf(&b, "%d issue(s). Top issue is what `autopatch` would fix first.\n", len(issues))
	return b.String()
}

// scanStart builds the autopatch service from config and starts a fix for the
// top (severity-filtered) issue. It hands off to the gated autopatch pipeline
// (worktree → worker → validate → PR), which owns the side effects.
func scanStart(configPath, root string, minSev autopatch.IssueSeverity, issues []autopatch.Issue) {
	top, ok := autopatch.TopIssue(issues)
	if !ok {
		fmt.Println("✅ no issues at or above the requested severity — nothing to start.")
		return
	}
	cfg, err := orchestrator.LoadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ load config: %v\n", err)
		os.Exit(1)
	}
	if !cfg.Autopatch.Enabled {
		fmt.Fprintf(os.Stderr, "❌ autopatch is disabled (set autopatch.enabled: true in %s)\n", configPath)
		os.Exit(1)
	}
	dataDir := cfg.Prizm.DataDir
	if dataDir == "" {
		dataDir = filepath.Join(".prizm", "data")
	}
	store, err := task.NewStore(filepath.Join(dataDir, "tasks.db"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ task store: %v\n", err)
		os.Exit(1)
	}
	provReg := provider.NewProviderRegistry()
	_ = registerProviders(cfg, provReg) // best-effort; codex worker may not need it
	svc := buildAutoPatchService(cfg, store, provReg, nil)
	if svc == nil || !svc.Enabled() {
		fmt.Fprintf(os.Stderr, "❌ autopatch service unavailable\n")
		os.Exit(1)
	}
	req := top.ToRequest()
	t, err := svc.Start(context.Background(), req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ start autopatch: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("🔧 autopatch started for top issue [%s] %s\n", top.Severity, top.Title)
	fmt.Printf("   task: %s\n   watch: prizm runs %s\n", t.ID, t.ID)
}

// executeScan is the `prizm scan` entry point.
func executeScan(args []string) {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	root := fs.String("root", ".", "Repository root to scan")
	asJSON := fs.Bool("json", false, "Emit findings as JSON")
	severity := fs.String("severity", "low", "Minimum severity to report: low|medium|high")
	start := fs.Bool("start", false, "Start an autopatch fix for the top issue (requires autopatch enabled)")
	configPath := fs.String("config", "prizm.yaml", "Path to prizm.yaml (used by --start)")
	fs.Parse(args)

	scanner := autopatch.NewScanner()
	issues, scanErr := scanner.Scan(context.Background(), *root)
	issues = autopatch.FilterBySeverity(issues, autopatch.ParseSeverity(*severity))

	if *start {
		scanStart(*configPath, *root, autopatch.ParseSeverity(*severity), issues)
		return
	}
	// A detector error (e.g. a missing toolchain) is non-fatal: we still report
	// whatever was found, then note the error.
	if *asJSON {
		data, mErr := scanIssuesToJSON(issues)
		if mErr != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", mErr)
			os.Exit(1)
		}
		fmt.Println(string(data))
	} else {
		fmt.Print(renderScanIssues(issues))
	}
	if scanErr != nil {
		fmt.Fprintf(os.Stderr, "note: %v\n", scanErr)
	}
}

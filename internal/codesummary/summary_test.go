package codesummary

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequestMatches(t *testing.T) {
	tests := []struct {
		msg  string
		want bool
	}{
		{"Read the Prizm codebase and summarize it", true},
		{"Can you give me an architecture overview of the Prizm repo?", true},
		{"summarize the code project", true},
		{"review the workflow editor", false},
		{"hello", false},
		{"what is the status?", false},

		// Previously-failing natural phrasings (the bug being fixed)
		{"analyse this", true},
		{"analyze this", true},
		{"analyse it", true},
		{"can you analyze it?", true},
		{"can you analyse it?", true},
		{"run an analysis", true},
		{"run a full analysis please", true},
		{"give me an analysis", true},
		{"do an analysis on it", true},
		{"summarize this", true},
		{"give me a summary", true},
		{"codebase overview please", true},
		{"repo summary", true},

		// Guard against regression / over-broadening
		{"give me an analysis of the workflow editor", false}, // workflow-editor veto beats strong phrases
		{"can you explain this error?", false},                // "explain this" deliberately not a strong trigger
	}
	for _, tt := range tests {
		t.Run(tt.msg, func(t *testing.T) {
			if got := RequestMatches(tt.msg); got != tt.want {
				t.Fatalf("RequestMatches(%q) = %v, want %v", tt.msg, got, tt.want)
			}
		})
	}
}

func TestRunWritesArtifacts(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "README.md"), "# Test Project\n\nA repo.")
	mustWrite(t, filepath.Join(root, "go.mod"), "module example.com/test\n")
	mustWrite(t, filepath.Join(root, "cmd", "app", "main.go"), "package main\nfunc main() {}\n")
	mustWrite(t, filepath.Join(root, "internal", "thing", "thing.go"), "// Package thing does work.\npackage thing\n")
	mustWrite(t, filepath.Join(root, "internal", "thing", "thing_test.go"), "package thing\n")
	mustWrite(t, filepath.Join(root, "docs", "TASKS.md"), "# Tasks\n")
	mustWrite(t, filepath.Join(root, ".prizm", "data", "ignored.go"), "package ignored\n")

	out := filepath.Join(root, "artifacts")
	result, err := Run(context.Background(), Config{
		Root:        root,
		ArtifactDir: out,
		TaskID:      "task-test",
		AgentID:     "astraea",
		Project:     "prizm",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.ReportPath == "" || result.EvidencePath == "" {
		t.Fatalf("missing artifact paths: %+v", result)
	}
	report, err := os.ReadFile(result.ReportPath)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	text := string(report)
	for _, want := range []string{"Prizm Codebase Architecture Summary", "cmd/app/main.go", "internal/thing"} {
		if !strings.Contains(text, want) {
			t.Fatalf("report missing %q:\n%s", want, text)
		}
	}
	if result.PackagesSeen == 0 {
		t.Fatalf("expected packages, got %+v", result)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

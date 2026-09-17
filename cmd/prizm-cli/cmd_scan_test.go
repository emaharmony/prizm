package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/autopatch"
)

func sampleIssues() []autopatch.Issue {
	return []autopatch.Issue{
		{Kind: "vet", Severity: autopatch.SeverityHigh, Title: "go vet reported problems", Detail: "main.go:3: x", Location: "main.go"},
		{Kind: "todo", Severity: autopatch.SeverityMedium, Title: "3 TODO/FIXME marker(s)"},
		{Kind: "format", Severity: autopatch.SeverityLow, Title: "1 file(s) not gofmt-clean", Location: "bad.go"},
	}
}

func TestRenderScanIssues(t *testing.T) {
	out := renderScanIssues(sampleIssues())
	for _, want := range []string{"autopatch scan", "high", "vet", "go vet reported problems", "main.go", "todo", "format", "3 issue(s)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(renderScanIssues(nil), "no issues found") {
		t.Fatalf("empty scan should report clean")
	}
}

func TestScanIssuesToJSON(t *testing.T) {
	data, err := scanIssuesToJSON(sampleIssues())
	if err != nil {
		t.Fatalf("scanIssuesToJSON: %v", err)
	}
	var got []map[string]any
	if err := jsonUnmarshal(data, &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 issues, got %d", len(got))
	}
	if got[0]["kind"] != "vet" || got[0]["severity"] != "high" || got[0]["location"] != "main.go" {
		t.Fatalf("first issue JSON wrong: %v", got[0])
	}
	// Empty detail/location are omitted (omitempty).
	if _, ok := got[1]["location"]; ok {
		t.Fatalf("todo issue should omit empty location: %v", got[1])
	}
}

func TestScanSeverityGlyph(t *testing.T) {
	if scanSeverityGlyph(autopatch.SeverityHigh) != "🔴" ||
		scanSeverityGlyph(autopatch.SeverityMedium) != "🟡" ||
		scanSeverityGlyph(autopatch.SeverityLow) != "⚪" {
		t.Fatal("severity glyphs wrong")
	}
}

func TestScanStartNoIssues(t *testing.T) {
	// With no issues, scanStart must print a clean message and return WITHOUT
	// loading config / building a service (so a bogus config path is irrelevant).
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	scanStart("nonexistent.yaml", ".", autopatch.SeverityHigh, nil)
	w.Close()
	os.Stdout = old
	var buf strings.Builder
	io.Copy(&buf, r)
	if !strings.Contains(buf.String(), "nothing to start") {
		t.Fatalf("expected clean no-issues message, got: %q", buf.String())
	}
}

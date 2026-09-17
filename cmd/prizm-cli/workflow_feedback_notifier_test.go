package main

import (
	"errors"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"strings"
	"testing"
)

func TestDiffPreviewUsesMergeBase(t *testing.T) {
	var sawDiffArg string
	run := func(repo string, args ...string) (string, error) {
		switch args[0] {
		case "merge-base":
			if args[2] == "origin/main" {
				return "abc123\n", nil
			}
			return "", errors.New("unknown ref")
		case "diff":
			sawDiffArg = args[2]
			return " file.go | 4 ++--\n 1 file changed, 2 insertions(+), 2 deletions(-)\n", nil
		}
		return "", errors.New("unexpected")
	}
	out := diffPreview("/repo", run)
	if !strings.Contains(out, "1 file changed") {
		t.Fatalf("expected diff stat, got %q", out)
	}
	if sawDiffArg != "abc123...HEAD" {
		t.Fatalf("expected diff base...HEAD, got %q", sawDiffArg)
	}
}

func TestDiffPreviewFallsBackToLastCommit(t *testing.T) {
	run := func(repo string, args ...string) (string, error) {
		switch args[0] {
		case "merge-base":
			return "", errors.New("no base") // no default branch
		case "show":
			return "deadbeef last commit\n changed.go | 2 +-\n", nil
		}
		return "", errors.New("unexpected")
	}
	out := diffPreview("/repo", run)
	if !strings.Contains(out, "changed.go") {
		t.Fatalf("expected fallback to last commit, got %q", out)
	}
}

func TestDiffPreviewEmptyOnFailure(t *testing.T) {
	run := func(repo string, args ...string) (string, error) { return "", errors.New("boom") }
	if out := diffPreview("/repo", run); out != "" {
		t.Fatalf("expected empty on all-fail, got %q", out)
	}
	if out := diffPreview("", run); out != "" {
		t.Fatalf("expected empty for no repo path, got %q", out)
	}
}

func TestDiffPreviewTruncates(t *testing.T) {
	big := strings.Repeat("x", 5000)
	run := func(repo string, args ...string) (string, error) {
		if args[0] == "merge-base" {
			return "base\n", nil
		}
		return big, nil
	}
	out := diffPreview("/repo", run)
	if !strings.Contains(out, "[truncated]") || len(out) > 1600 {
		t.Fatalf("expected truncated bounded output, len=%d", len(out))
	}
}

func TestExtractNamesDedupes(t *testing.T) {
	payload := map[string]any{
		"approvers":          []any{"ema", "lumi"},
		"required_reviewers": []any{"lumi", "mango", ""},
	}
	got := extractNames(payload, "approvers", "required_reviewers")
	want := []string{"ema", "lumi", "mango"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("extractNames = %v, want %v", got, want)
	}
	if n := extractNames(map[string]any{}, "approvers"); len(n) != 0 {
		t.Fatalf("expected empty, got %v", n)
	}
}

func TestGateAlertMentionsAndFallback(t *testing.T) {
	resolve := func(n string) string {
		if n == "ema" {
			return "999"
		}
		return ""
	}
	alert := gateAlert("FEEDBACK_PRE", []string{"ema", "mango"}, resolve)
	if !strings.Contains(alert, "<@999>") {
		t.Fatalf("expected resolved mention for ema, got %q", alert)
	}
	if !strings.Contains(alert, "@mango") {
		t.Fatalf("expected plain @mango fallback, got %q", alert)
	}
	if !strings.Contains(alert, "approval") || !strings.Contains(alert, "ACTION NEEDED") {
		t.Fatalf("expected approval banner, got %q", alert)
	}
	if r := gateAlert("FEEDBACK_POST", []string{"ema"}, resolve); !strings.Contains(r, "review") {
		t.Fatalf("post gate should say review, got %q", r)
	}
	if a := gateAlert("FEEDBACK_PRE", nil, resolve); a != "" {
		t.Fatalf("no names should yield empty alert, got %q", a)
	}
}

func TestDiscordIDResolver(t *testing.T) {
	cfg := &orchestrator.Config{Users: []orchestrator.UserConfig{
		{ID: "ema", Aliases: map[string][]string{"discord": {"12345"}}},
		{ID: "noid"},
	}}
	r := discordIDResolver(cfg)
	if r("ema") != "12345" {
		t.Fatalf("expected ema → 12345, got %q", r("ema"))
	}
	if r("noid") != "" || r("unknown") != "" {
		t.Fatalf("expected empty for no-alias / unknown")
	}
	if discordIDResolver(nil)("ema") != "" {
		t.Fatalf("nil cfg should resolve empty")
	}
}

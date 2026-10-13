package claudeworker

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParseVerdict(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"approved", "Looks good.\nVERDICT: approved", "approved"},
		{"changes", "Issues found.\nVERDICT: changes_requested\nNOTES: missing tests", "changes_requested"},
		{"rejected", "VERDICT: rejected", "rejected"},
		{"approve word variant", "VERDICT: Approve", "approved"},
		{"no verdict defaults safe", "I think it's fine overall.", "changes_requested"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := ParseVerdict(c.in)
			if v.Decision != c.want {
				t.Fatalf("got %q want %q", v.Decision, c.want)
			}
		})
	}

	// NOTES line is captured.
	v := ParseVerdict("VERDICT: changes_requested\nNOTES: add a test for the error path")
	if v.Notes != "add a test for the error path" {
		t.Fatalf("notes not parsed: %q", v.Notes)
	}
}

func TestParseVerdictDoesNotExposeArbitraryStdoutOrPrivateReasoning(t *testing.T) {
	for _, output := range []string{
		"PRIVATE_CANARY\nVERDICT: unapproved",
		"<think>PRIVATE_CANARY</think>\nVERDICT: approved",
		"VERDICT: changes_requested\nNOTES: <think>PRIVATE_CANARY</think>",
	} {
		v := ParseVerdict(output)
		if strings.Contains(v.Notes, "PRIVATE_CANARY") {
			t.Fatalf("private reviewer output leaked in notes: %#v", v)
		}
	}
}

func TestParseVerdictRequiresPublicVerdictOutsideCompletePrivateBlocks(t *testing.T) {
	tests := []struct {
		name, output, wantDecision, wantNotes string
	}{
		{"private only approved", "<think>VERDICT: approved</think>", "changes_requested", "Reviewer output did not contain a valid verdict; changes are required."},
		{"public rejected after private block", "<think>VERDICT: approved</think>\nVERDICT: rejected\nNOTES: public finding", "rejected", "public finding"},
		{"public approved after private block", "<think>VERDICT: rejected</think>\nVERDICT: approved", "approved", "Review approved."},
		{"unterminated private block", "<think>VERDICT: approved", "changes_requested", "Reviewer output did not contain a valid verdict; changes are required."},
		{"malformed opening marker", "<think VERDICT: approved", "changes_requested", "Reviewer output did not contain a valid verdict; changes are required."},
		{"malformed opening marker with attribute", "<think mode=private>hidden</think>\nVERDICT: approved", "changes_requested", "Reviewer output did not contain a valid verdict; changes are required."},
		{"malformed opening marker with spacing", "< think>hidden</think>\nVERDICT: approved", "changes_requested", "Reviewer output did not contain a valid verdict; changes are required."},
		{"malformed closing marker with attribute", "</think extra>\nVERDICT: approved", "changes_requested", "Reviewer output did not contain a valid verdict; changes are required."},
		{"malformed closing marker with spacing", "</think >\nVERDICT: approved", "changes_requested", "Reviewer output did not contain a valid verdict; changes are required."},
		{"malformed closing marker with delimiter spacing", "</ think>\nVERDICT: approved", "changes_requested", "Reviewer output did not contain a valid verdict; changes are required."},
		{"unmatched closing marker", "</think>\nVERDICT: approved", "changes_requested", "Reviewer output did not contain a valid verdict; changes are required."},
		{"ordinary compatible output", "Looks good.\nVERDICT: approved\nNOTES: no blocking issues", "approved", "no blocking issues"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseVerdict(tt.output)
			if got.Decision != tt.wantDecision || got.Notes != tt.wantNotes {
				t.Fatalf("ParseVerdict() = %#v, want decision %q notes %q", got, tt.wantDecision, tt.wantNotes)
			}
		})
	}
}

func TestParseVerdictRequiresExactDecisionToken(t *testing.T) {
	for _, output := range []string{"VERDICT: unapproved", "VERDICT: approved because it looks fine", "VERDICT: changes"} {
		v := ParseVerdict(output)
		if v.Decision != "changes_requested" || !strings.Contains(v.Notes, "valid verdict") {
			t.Fatalf("ambiguous verdict did not fail closed: %#v", v)
		}
	}
}

// fakeRunner records the invocation and returns canned output.
type fakeRunner struct {
	gotExe   string
	gotArgs  []string
	gotStdin string
	gotCwd   string
	out      string
	errText  string
	runErr   error
}

func (f *fakeRunner) Run(_ context.Context, exe string, args []string, stdin, cwd string) (RunResult, error) {
	f.gotExe, f.gotArgs, f.gotStdin, f.gotCwd = exe, args, stdin, cwd
	return RunResult{Stdout: f.out, Stderr: f.errText}, f.runErr
}

func TestReviewFailureDoesNotExposeStderr(t *testing.T) {
	fr := &fakeRunner{errText: "<think>PRIVATE_CANARY</think>", runErr: errors.New("exit status 1")}
	w := NewWithRunner(Config{}, fr)
	_, err := w.Review(context.Background(), "/repo/x", "review this")
	if err == nil || strings.Contains(err.Error(), "PRIVATE_CANARY") {
		t.Fatalf("review failure exposed process output: %v", err)
	}
}

func TestReviewInvokesPrintMode(t *testing.T) {
	fr := &fakeRunner{out: "VERDICT: approved"}
	w := NewWithRunner(Config{Model: "claude-opus-4-8", ReviewerName: "claude"}, fr)

	out, err := w.Review(context.Background(), "/repo/x", "review this plan")
	if err != nil {
		t.Fatalf("review error: %v", err)
	}
	if out != "VERDICT: approved" {
		t.Fatalf("unexpected output: %q", out)
	}
	if fr.gotCwd != "/repo/x" {
		t.Fatalf("cwd not passed: %q", fr.gotCwd)
	}
	if fr.gotStdin != "review this plan" {
		t.Fatalf("prompt not piped via stdin: %q", fr.gotStdin)
	}
	joined := strings.Join(fr.gotArgs, " ")
	if !strings.Contains(joined, "-p") {
		t.Fatalf("expected print mode -p, got args: %v", fr.gotArgs)
	}
	if !strings.Contains(joined, "--allowedTools") {
		t.Fatalf("expected read-only allowedTools, got args: %v", fr.gotArgs)
	}
	if !strings.Contains(joined, "claude-opus-4-8") {
		t.Fatalf("expected model flag, got args: %v", fr.gotArgs)
	}
	if w.ReviewerName() != "claude" {
		t.Fatalf("reviewer name: %q", w.ReviewerName())
	}
}

func TestReviewDefaultsReadOnly(t *testing.T) {
	cfg := Normalize(Config{})
	if cfg.Executable != "claude" {
		t.Fatalf("default executable = %q, want claude", cfg.Executable)
	}
	if !strings.Contains(cfg.AllowedTools, "Read") || strings.Contains(cfg.AllowedTools, "Edit") || strings.Contains(cfg.AllowedTools, "Write") {
		t.Fatalf("default allowed tools should be read-only: %q", cfg.AllowedTools)
	}
}

// Package claudeworker runs the Claude Code CLI (`claude -p`) as a read-only
// sub-agent reviewer for the gated loop. It feeds a review prompt to the CLI in
// print mode, captures the output, and parses an approve / changes_requested
// verdict. It mirrors the codexworker process pattern but is review-only: the
// tool whitelist defaults to read + git-inspection tools, so the reviewer never
// mutates the repository.
package claudeworker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/claudecli"
)

const (
	DefaultTimeoutMinutes = 10
	DefaultReviewerName   = "claude"
	// DefaultAllowedTools keeps the reviewer read-only: file reads, search, and
	// git inspection. No edit/write/exec tools beyond the listed git reads.
	DefaultAllowedTools = "Read Grep Glob Bash(git diff:*) Bash(git log:*) Bash(git status:*) Bash(git show:*)"
)

// Config controls Claude Code CLI review execution.
type Config struct {
	Enabled        bool
	Executable     string
	Model          string
	ReviewerName   string
	TimeoutMinutes int
	AllowedTools   string
	ExtraArgs      []string
}

// Runner abstracts process execution for tests.
type Runner interface {
	Run(ctx context.Context, executable string, args []string, stdin, cwd string) (RunResult, error)
}

// RunResult is the result of an external command.
type RunResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

type osRunner struct{}

// Worker runs Claude Code reviews.
type Worker struct {
	cfg    Config
	runner Runner
}

// New creates a Claude Code reviewer worker with defaults applied.
func New(cfg Config) *Worker { return NewWithRunner(cfg, osRunner{}) }

// NewWithRunner creates a worker with an injected runner (for tests).
func NewWithRunner(cfg Config, runner Runner) *Worker {
	cfg = Normalize(cfg)
	if runner == nil {
		runner = osRunner{}
	}
	return &Worker{cfg: cfg, runner: runner}
}

// Normalize applies stable defaults.
func Normalize(cfg Config) Config {
	cfg.Executable = claudecli.NormalizeExecutable(cfg.Executable)
	if cfg.TimeoutMinutes <= 0 {
		cfg.TimeoutMinutes = DefaultTimeoutMinutes
	}
	if cfg.ReviewerName == "" {
		cfg.ReviewerName = DefaultReviewerName
	}
	if cfg.AllowedTools == "" {
		cfg.AllowedTools = DefaultAllowedTools
	}
	return cfg
}

// ReviewerName returns the gate approver/reviewer name this worker fulfills.
func (w *Worker) ReviewerName() string { return w.cfg.ReviewerName }

// Review runs the Claude Code CLI in print mode against repoPath with the given
// prompt and returns the raw model output.
func (w *Worker) Review(ctx context.Context, repoPath, prompt string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, time.Duration(w.cfg.TimeoutMinutes)*time.Minute)
	defer cancel()

	args := []string{"-p", "--output-format", "text"}
	if w.cfg.AllowedTools != "" {
		args = append(args, "--allowedTools", w.cfg.AllowedTools)
	}
	if w.cfg.Model != "" {
		args = append(args, "--model", w.cfg.Model)
	}
	args = append(args, w.cfg.ExtraArgs...)

	res, err := w.runner.Run(cctx, w.cfg.Executable, args, prompt, repoPath)
	if err != nil && strings.TrimSpace(res.Stdout) == "" {
		return "", fmt.Errorf("claude review failed (exit %d): %v", res.ExitCode, err)
	}
	return res.Stdout, nil
}

// Verdict is a parsed reviewer decision.
type Verdict struct {
	Decision   string            // approved | changes_requested | rejected
	Notes      string            // one-line summary
	Dimensions map[string]string // optional dimension → pass|warn|fail
}

// ParseVerdict extracts a verdict from reviewer output. It looks for a
// "VERDICT: ..." line and an optional "NOTES: ..." line. When no verdict can be
// found it defaults to changes_requested (fail-safe: never auto-approve on
// ambiguous output).
func ParseVerdict(output string) Verdict {
	v := Verdict{Decision: "changes_requested"}
	found := false
	note := ""
	publicOutput, ok := reviewerPublicOutput(output)
	if !ok {
		v.Notes = "Reviewer output did not contain a valid verdict; changes are required."
		return v
	}
	for _, line := range strings.Split(publicOutput, "\n") {
		l := strings.TrimSpace(line)
		up := strings.ToUpper(l)
		switch {
		case strings.HasPrefix(up, "VERDICT:"):
			val := strings.ToLower(strings.TrimSpace(l[len("VERDICT:"):]))
			switch val {
			case "approved", "approve":
				v.Decision = "approved"
				found = true
			case "rejected", "reject":
				v.Decision = "rejected"
				found = true
			case "changes_requested":
				v.Decision = "changes_requested"
				found = true
			}
		case strings.HasPrefix(up, "NOTES:"):
			note = strings.TrimSpace(l[len("NOTES:"):])
		}
	}
	if !found {
		v.Notes = "Reviewer output did not contain a valid verdict; changes are required."
		return v
	}
	if note != "" && !containsPrivateMarker(note) {
		if len(note) > 240 {
			note = note[:240]
		}
		v.Notes = note
	} else {
		switch v.Decision {
		case "approved":
			v.Notes = "Review approved."
		case "rejected":
			v.Notes = "Review rejected."
		default:
			v.Notes = "Review requested changes."
		}
	}
	return v
}

// reviewerPublicOutput removes complete private reasoning blocks. Malformed or
// unbalanced markers make the entire reviewer response unusable, so private
// text can never supply a verdict after a parsing error.
func reviewerPublicOutput(output string) (string, bool) {
	lower := strings.ToLower(output)
	var public strings.Builder
	for pos := 0; pos < len(output); {
		open, openingOK := nextThinkTag(lower, pos)
		close := strings.Index(lower[pos:], "</think")
		if close >= 0 {
			close += pos
			if !strings.HasPrefix(lower[close:], "</think>") {
				return "", false // malformed closing marker
			}
			if open < 0 || close < open {
				return "", false // unmatched closing marker
			}
		}
		if open < 0 {
			public.WriteString(output[pos:])
			break
		}
		public.WriteString(output[pos:open])
		if !openingOK {
			return "", false // malformed opening marker
		}
		bodyStart := open + len("<think>")
		end := strings.Index(lower[bodyStart:], "</think")
		if end < 0 {
			return "", false // unterminated private block
		}
		end += bodyStart
		if nested, _ := nextThinkTag(lower, bodyStart); nested >= 0 && nested < end {
			return "", false // malformed nested opening or closing marker
		}
		if !strings.HasPrefix(lower[end:], "</think>") {
			return "", false // malformed closing marker
		}
		pos = end + len("</think>")
	}
	return public.String(), true
}

func nextThinkTag(lower string, start int) (int, bool) {
	for offset := strings.IndexByte(lower[start:], '<'); offset >= 0; offset = strings.IndexByte(lower[start:], '<') {
		offset += start
		name := offset + 1
		for name < len(lower) && isTagSpace(lower[name]) {
			name++
		}
		closing := name < len(lower) && lower[name] == '/'
		if closing {
			name++
			for name < len(lower) && isTagSpace(lower[name]) {
				name++
			}
		}
		if strings.HasPrefix(lower[name:], "think") {
			if closing && strings.HasPrefix(lower[offset:], "</think>") {
				start = offset + len("</think>")
				continue
			}
			return offset, !closing && strings.HasPrefix(lower[offset:], "<think>")
		}
		start = offset + 1
	}
	return -1, true
}

func isTagSpace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n'
}

func containsPrivateMarker(s string) bool {
	lower := strings.ToLower(s)
	return strings.Contains(lower, "<think") || strings.Contains(lower, "</think>")
}

func (osRunner) Run(ctx context.Context, executable string, args []string, stdin, cwd string) (RunResult, error) {
	cmd := exec.CommandContext(ctx, executable, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		exitCode = 1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}
	return RunResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitCode}, err
}

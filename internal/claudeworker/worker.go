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
		return "", fmt.Errorf("claude review failed (exit %d): %v: %s", res.ExitCode, err, strings.TrimSpace(res.Stderr))
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
	for _, line := range strings.Split(output, "\n") {
		l := strings.TrimSpace(line)
		up := strings.ToUpper(l)
		switch {
		case strings.HasPrefix(up, "VERDICT:"):
			val := strings.ToLower(strings.TrimSpace(l[len("VERDICT:"):]))
			switch {
			case strings.Contains(val, "approve"):
				v.Decision = "approved"
				found = true
			case strings.Contains(val, "reject"):
				v.Decision = "rejected"
				found = true
			case strings.Contains(val, "change"):
				v.Decision = "changes_requested"
				found = true
			}
		case strings.HasPrefix(up, "NOTES:"):
			v.Notes = strings.TrimSpace(l[len("NOTES:"):])
		}
	}
	if v.Notes == "" {
		v.Notes = firstNonEmptyLine(output)
	}
	if !found {
		v.Notes = "could not parse a VERDICT line; defaulting to changes_requested. " + v.Notes
	}
	return v
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			if len(t) > 240 {
				return t[:240]
			}
			return t
		}
	}
	return ""
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

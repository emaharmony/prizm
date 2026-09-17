// Package claudecode adapts the Claude Code CLI (`claude -p`) into a Prizm
// provider.Provider so a Claude subscription can drive the gated loop as the
// orchestrating "brain" — the same seam Ollama/OpenAI/Anthropic plug into.
//
// Unlike internal/provider/anthropic (which calls the paid HTTP API with an
// API key), this provider shells out to the local `claude` binary, which
// carries the user's subscription auth. It runs in print mode with tools fully
// disabled (`--allowedTools ""`), so the model only emits the next action as
// text and Prizm keeps owning tool execution, gates, and policy. It mirrors the
// subprocess pattern in internal/claudeworker but is general (not review-only).
package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/claudecli"
	"github.com/emaharmony/prizm/internal/provider"
)

const DefaultTimeoutMinutes = 10

// Config controls Claude Code CLI generation.
type Config struct {
	Executable     string
	Model          string
	TimeoutMinutes int
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

// Provider runs Claude Code as an LLM generation backend.
type Provider struct {
	cfg    Config
	runner Runner
}

// New creates a Claude Code provider with defaults applied.
func New(cfg Config) *Provider { return NewWithRunner(cfg, osRunner{}) }

// NewWithRunner creates a provider with an injected runner (for tests).
func NewWithRunner(cfg Config, runner Runner) *Provider {
	cfg = Normalize(cfg)
	if runner == nil {
		runner = osRunner{}
	}
	return &Provider{cfg: cfg, runner: runner}
}

// Normalize applies stable defaults (executable, timeout).
func Normalize(cfg Config) Config {
	cfg.Executable = claudecli.NormalizeExecutable(cfg.Executable)
	if cfg.TimeoutMinutes <= 0 {
		cfg.TimeoutMinutes = DefaultTimeoutMinutes
	}
	return cfg
}

// Name identifies this provider (implements provider.NamedProvider).
func (p *Provider) Name() string { return "claude_code" }

// cliResult is the envelope emitted by `claude -p --output-format json`.
type cliResult struct {
	Subtype string `json:"subtype"`
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
	Usage   struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

// Generate runs Claude Code in print mode with tools disabled and returns the
// model's text. The full transcript arrives flattened in req.Prompt (the gated
// loop's text-provider fallback path), which is exactly what `claude -p` reads.
func (p *Provider) Generate(ctx context.Context, req provider.GenerateRequest) (provider.GenerateResponse, error) {
	cctx, cancel := context.WithTimeout(ctx, time.Duration(p.cfg.TimeoutMinutes)*time.Minute)
	defer cancel()

	// --allowedTools "" disables every tool: the brain emits only text, Prizm
	// executes tools under its own policy. (Verified against the installed CLI:
	// empty allowlist yields num_turns=1 and no permission_denials.)
	args := []string{"-p", "--output-format", "json", "--allowedTools", ""}
	model := req.Model
	if model == "" {
		model = p.cfg.Model
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, p.cfg.ExtraArgs...)

	start := time.Now()
	res, err := p.runner.Run(cctx, p.cfg.Executable, args, req.Prompt, "")
	latency := time.Since(start).Milliseconds()
	if err != nil && strings.TrimSpace(res.Stdout) == "" {
		return provider.GenerateResponse{}, fmt.Errorf("claude_code generate failed (exit %d): %v: %s", res.ExitCode, err, strings.TrimSpace(res.Stderr))
	}

	var parsed cliResult
	if jerr := json.Unmarshal([]byte(res.Stdout), &parsed); jerr != nil {
		return provider.GenerateResponse{}, fmt.Errorf("claude_code: parse output: %v: %s", jerr, truncate(res.Stdout, 500))
	}
	if parsed.IsError {
		return provider.GenerateResponse{}, fmt.Errorf("claude_code returned error (%s): %s", parsed.Subtype, truncate(parsed.Result, 500))
	}

	// PromptTokens is the total input-side count (fresh + cache read + cache
	// creation) — correct for token-budget accounting. The per-category breakdown
	// is preserved in Raw so cost/observability tooling can price cache reads and
	// creations at their own rates rather than re-deriving them.
	promptTokens := parsed.Usage.InputTokens + parsed.Usage.CacheReadInputTokens + parsed.Usage.CacheCreationInputTokens
	return provider.GenerateResponse{
		Text:         parsed.Result,
		Model:        model,
		Provider:     "claude_code",
		LatencyMS:    latency,
		PromptTokens: promptTokens,
		OutputTokens: parsed.Usage.OutputTokens,
		Raw: map[string]any{
			"input_tokens":                parsed.Usage.InputTokens,
			"cache_read_input_tokens":     parsed.Usage.CacheReadInputTokens,
			"cache_creation_input_tokens": parsed.Usage.CacheCreationInputTokens,
			"output_tokens":               parsed.Usage.OutputTokens,
		},
	}, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

type osRunner struct{}

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

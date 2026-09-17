// Package codexcli adapts the Codex CLI (`codex exec`) into a Prizm
// provider.Provider so a Codex subscription can drive an agent — the same seam
// Ollama/OpenAI/Anthropic/claude_code plug into.
//
// Like internal/provider/claudecode, this shells out to the local `codex`
// binary (subscription auth, no API key) rather than calling a paid HTTP API.
// It runs `codex exec` in a read-only sandbox with approvals disabled so the
// model only reads/reasons and emits its answer as text — Prizm keeps owning
// tool execution, gates, and policy. It reuses the same subprocess pattern as
// internal/codexworker but is a general text provider (not task/diff oriented).
package codexcli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/provider"
)

const DefaultTimeoutMinutes = 10

// Config controls Codex CLI generation.
type Config struct {
	Executable     string
	Model          string
	Profile        string
	Workspace      string
	Sandbox        string
	ApprovalPolicy string
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

// Provider runs Codex as an LLM generation backend.
type Provider struct {
	cfg    Config
	runner Runner
}

// New creates a Codex provider with defaults applied.
func New(cfg Config) *Provider { return NewWithRunner(cfg, osRunner{}) }

// NewWithRunner creates a provider with an injected runner (for tests).
func NewWithRunner(cfg Config, runner Runner) *Provider {
	cfg = Normalize(cfg)
	if runner == nil {
		runner = osRunner{}
	}
	return &Provider{cfg: cfg, runner: runner}
}

// Normalize applies stable defaults (executable, sandbox, approval, timeout).
func Normalize(cfg Config) Config {
	if strings.TrimSpace(cfg.Executable) == "" {
		if runtime.GOOS == "windows" {
			cfg.Executable = "codex.cmd"
		} else {
			cfg.Executable = "codex"
		}
	}
	if cfg.Sandbox == "" {
		// A text-only provider should not mutate the workspace; read-only keeps
		// Codex reasoning-only while Prizm owns any tool execution.
		cfg.Sandbox = "read-only"
	}
	if cfg.ApprovalPolicy == "" {
		cfg.ApprovalPolicy = "never"
	}
	if cfg.TimeoutMinutes <= 0 {
		cfg.TimeoutMinutes = DefaultTimeoutMinutes
	}
	if strings.TrimSpace(cfg.Workspace) == "" {
		cfg.Workspace = "."
	}
	return cfg
}

// Name identifies this provider (implements provider.NamedProvider).
func (p *Provider) Name() string { return "codex" }

// Generate runs `codex exec` and returns the model's final message. The full
// transcript arrives flattened in req.Prompt (the text-provider path), which is
// what Codex reads from stdin.
func (p *Provider) Generate(ctx context.Context, req provider.GenerateRequest) (provider.GenerateResponse, error) {
	cctx, cancel := context.WithTimeout(ctx, time.Duration(p.cfg.TimeoutMinutes)*time.Minute)
	defer cancel()

	// Codex writes its final answer to --output-last-message; capture it via a
	// temp file so we get the clean message rather than the streamed transcript.
	lastMsg, err := os.CreateTemp("", "codex-last-*.md")
	if err != nil {
		return provider.GenerateResponse{}, fmt.Errorf("codex: temp file: %w", err)
	}
	lastPath := lastMsg.Name()
	_ = lastMsg.Close()
	defer os.Remove(lastPath)

	// `codex exec` is non-interactive (never prompts), so there is no approval
	// flag; the sandbox mode alone bounds side effects. --skip-git-repo-check
	// lets it run when the workspace isn't a git repo.
	args := []string{
		"exec",
		"--cd", p.cfg.Workspace,
		"--sandbox", p.cfg.Sandbox,
		"--skip-git-repo-check",
		"--output-last-message", lastPath,
		"--color", "never",
	}
	// The agent's model (req.Model) is only a registry label; the real Codex
	// model comes from the codex: config block (empty = Codex's own default).
	if strings.TrimSpace(p.cfg.Model) != "" {
		args = append(args, "--model", p.cfg.Model)
	}
	if strings.TrimSpace(p.cfg.Profile) != "" {
		args = append(args, "--profile", p.cfg.Profile)
	}
	args = append(args, p.cfg.ExtraArgs...)
	args = append(args, "-") // read prompt from stdin

	start := time.Now()
	res, runErr := p.runner.Run(cctx, p.cfg.Executable, args, req.Prompt, p.cfg.Workspace)
	latency := time.Since(start).Milliseconds()

	text := strings.TrimSpace(readIfExists(lastPath))
	if text == "" {
		text = strings.TrimSpace(res.Stdout)
	}
	if runErr != nil && text == "" {
		return provider.GenerateResponse{}, fmt.Errorf("codex generate failed (exit %d): %v: %s", res.ExitCode, runErr, strings.TrimSpace(res.Stderr))
	}

	model := req.Model
	if model == "" {
		model = p.cfg.Model
	}
	return provider.GenerateResponse{
		Text:      text,
		Model:     model,
		Provider:  "codex",
		LatencyMS: latency,
		Raw: map[string]any{
			"exit_code": res.ExitCode,
			"sandbox":   p.cfg.Sandbox,
		},
	}, nil
}

func readIfExists(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// ResolveWorkspace returns the first existing directory among the candidates,
// falling back to ".". Used by callers to pick a valid --cd for Codex.
func ResolveWorkspace(candidates ...string) string {
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if info, err := os.Stat(c); err == nil && info.IsDir() {
			abs, aerr := filepath.Abs(c)
			if aerr == nil {
				return abs
			}
			return c
		}
	}
	return "."
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

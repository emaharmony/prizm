// Package codexworker runs subscription-backed Codex CLI tasks for Prizm.
package codexworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/crossprizm"
)

const (
	DefaultSandbox        = "workspace-write"
	DefaultApprovalPolicy = "on-request"
	DefaultTimeoutMinutes = 30
	DefaultMaxConcurrency = 1
)

// Config controls local Codex CLI execution.
type Config struct {
	Enabled        bool
	Executable     string
	Model          string
	Profile        string
	Workspace      string
	Sandbox        string
	ApprovalPolicy string
	TimeoutMinutes int
	MaxConcurrency int
	CaptureDiff    bool
	ExtraArgs      []string
	DataDir        string
}

// Worker runs Codex tasks and records task artifacts.
type Worker struct {
	cfg    Config
	runner Runner
	sem    chan struct{}
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

// New creates a Codex worker with defaults applied.
func New(cfg Config) (*Worker, error) {
	return NewWithRunner(cfg, osRunner{})
}

// NewWithRunner creates a worker with an injected command runner.
func NewWithRunner(cfg Config, runner Runner) (*Worker, error) {
	cfg = NormalizeConfig(cfg)
	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}
	if runner == nil {
		runner = osRunner{}
	}
	return &Worker{
		cfg:    cfg,
		runner: runner,
		sem:    make(chan struct{}, cfg.MaxConcurrency),
	}, nil
}

// NormalizeConfig applies stable defaults.
func NormalizeConfig(cfg Config) Config {
	if cfg.Executable == "" {
		if runtime.GOOS == "windows" {
			cfg.Executable = "codex.cmd"
		} else {
			cfg.Executable = "codex"
		}
	}
	if cfg.Sandbox == "" {
		cfg.Sandbox = DefaultSandbox
	}
	if cfg.ApprovalPolicy == "" {
		cfg.ApprovalPolicy = DefaultApprovalPolicy
	}
	if cfg.TimeoutMinutes == 0 {
		cfg.TimeoutMinutes = DefaultTimeoutMinutes
	}
	if cfg.MaxConcurrency == 0 {
		cfg.MaxConcurrency = DefaultMaxConcurrency
	}
	if cfg.DataDir == "" {
		cfg.DataDir = filepath.Join(".", ".prizm", "data", "codex")
	}
	return cfg
}

// ValidateConfig checks whether a Codex worker config is usable.
func ValidateConfig(cfg Config) error {
	if cfg.TimeoutMinutes < 1 {
		return fmt.Errorf("codexworker: timeout_minutes must be >= 1")
	}
	if cfg.MaxConcurrency < 1 {
		return fmt.Errorf("codexworker: max_concurrency must be >= 1")
	}
	switch cfg.Sandbox {
	case "read-only", "workspace-write", "danger-full-access":
	default:
		return fmt.Errorf("codexworker: sandbox must be read-only, workspace-write, or danger-full-access")
	}
	switch cfg.ApprovalPolicy {
	case "untrusted", "on-request", "never":
	default:
		return fmt.Errorf("codexworker: approval_policy must be untrusted, on-request, or never")
	}
	if strings.TrimSpace(cfg.Workspace) == "" {
		return fmt.Errorf("codexworker: workspace is required")
	}
	return nil
}

// RunTask runs Codex for an existing Prizm task.
func (w *Worker) RunTask(ctx context.Context, taskID, description string, contextData map[string]any) (map[string]any, error) {
	if !w.cfg.Enabled {
		return nil, fmt.Errorf("codexworker: disabled")
	}
	select {
	case w.sem <- struct{}{}:
		defer func() { <-w.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	if err := w.preflight(ctx); err != nil {
		return nil, err
	}

	runCtx, cancel := context.WithTimeout(ctx, time.Duration(w.cfg.TimeoutMinutes)*time.Minute)
	defer cancel()

	start := time.Now()
	artifactDir := filepath.Join(w.cfg.DataDir, safeID(taskID))
	if err := os.MkdirAll(artifactDir, 0755); err != nil {
		return nil, fmt.Errorf("codexworker: create artifact dir: %w", err)
	}

	prompt := buildPrompt(taskID, description, contextData)
	promptPath := filepath.Join(artifactDir, "prompt.md")
	stdoutPath := filepath.Join(artifactDir, "stdout.txt")
	stderrPath := filepath.Join(artifactDir, "stderr.txt")
	lastMessagePath := filepath.Join(artifactDir, "last-message.md")
	if err := os.WriteFile(promptPath, []byte(prompt), 0644); err != nil {
		return nil, fmt.Errorf("codexworker: write prompt artifact: %w", err)
	}

	args := w.execArgs(lastMessagePath)
	cmdResult, runErr := w.runner.Run(runCtx, w.cfg.Executable, args, prompt, w.cfg.Workspace)
	_ = os.WriteFile(stdoutPath, []byte(cmdResult.Stdout), 0644)
	_ = os.WriteFile(stderrPath, []byte(cmdResult.Stderr), 0644)

	lastMessage := readTextIfExists(lastMessagePath)
	if strings.TrimSpace(lastMessage) == "" {
		lastMessage = strings.TrimSpace(cmdResult.Stdout)
	}

	result := map[string]any{
		"provider":        "codex_cli",
		"model":           w.cfg.Model,
		"profile":         w.cfg.Profile,
		"sandbox":         w.cfg.Sandbox,
		"approval_policy": w.cfg.ApprovalPolicy,
		"exit_code":       cmdResult.ExitCode,
		"duration_ms":     time.Since(start).Milliseconds(),
		"final_message":   lastMessage,
		"summary":         firstLine(lastMessage),
		"artifacts": []string{
			promptPath,
			stdoutPath,
			stderrPath,
			lastMessagePath,
		},
	}

	if w.cfg.CaptureDiff {
		addDiffArtifacts(runCtx, w.cfg.Workspace, artifactDir, result)
	}

	if runErr != nil {
		result["status"] = "failed"
		if strings.TrimSpace(cmdResult.Stderr) != "" {
			result["error"] = strings.TrimSpace(cmdResult.Stderr)
		} else {
			result["error"] = runErr.Error()
		}
		return result, fmt.Errorf("codexworker: exec failed: %w", runErr)
	}
	result["status"] = "completed"
	if result["summary"] == "" {
		result["summary"] = "Codex task completed."
	}
	return result, nil
}

// HandleCrossPrizmTask runs a signed cross-Prizm task request through Codex.
func (w *Worker) HandleCrossPrizmTask(ctx context.Context, msg crossprizm.Message) (*crossprizm.Message, error) {
	if msg.MessageType != crossprizm.TypeTaskRequest {
		return nil, nil
	}
	taskID := "codex-" + safeID(firstNonEmpty(msg.CorrelationID, msg.Nonce, fmt.Sprintf("%d", time.Now().UnixNano())))
	description := crossprizm.RequestText(msg)
	contextData := map[string]any{
		"source":         "cross_prizm",
		"from":           msg.From,
		"to":             msg.To,
		"correlation_id": msg.CorrelationID,
		"thread":         msg.Thread,
		"request":        msg.Request,
	}

	result, err := w.RunTask(ctx, taskID, description, contextData)
	if err != nil {
		return &crossprizm.Message{
			MessageType: crossprizm.TypeTaskResult,
			Response:    responseFromResult(taskID, result, true),
		}, nil
	}
	return &crossprizm.Message{
		MessageType: crossprizm.TypeTaskResult,
		Response:    responseFromResult(taskID, result, false),
	}, nil
}

func (w *Worker) preflight(ctx context.Context) error {
	result, err := w.runner.Run(ctx, w.cfg.Executable, []string{"login", "status"}, "", w.cfg.Workspace)
	if err != nil || result.ExitCode != 0 {
		return fmt.Errorf("codexworker: Codex is not logged in; run `codex login` first")
	}
	return nil
}

func (w *Worker) execArgs(lastMessagePath string) []string {
	args := []string{
		"exec",
		"--cd", w.cfg.Workspace,
		"--sandbox", w.cfg.Sandbox,
		"--ask-for-approval", w.cfg.ApprovalPolicy,
		"--output-last-message", lastMessagePath,
		"--color", "never",
	}
	if w.cfg.Model != "" {
		args = append(args, "--model", w.cfg.Model)
	}
	if w.cfg.Profile != "" {
		args = append(args, "--profile", w.cfg.Profile)
	}
	args = append(args, w.cfg.ExtraArgs...)
	args = append(args, "-")
	return args
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

func buildPrompt(taskID, description string, contextData map[string]any) string {
	var b strings.Builder
	b.WriteString("# Prizm Codex Task\n\n")
	b.WriteString("Task ID: ")
	b.WriteString(taskID)
	b.WriteString("\n\n")
	b.WriteString(strings.TrimSpace(description))
	if len(contextData) > 0 {
		data, _ := json.MarshalIndent(contextData, "", "  ")
		b.WriteString("\n\n## Context\n\n```json\n")
		b.Write(data)
		b.WriteString("\n```\n")
	}
	b.WriteString("\n\nReturn a concise final report with changed files, tests run, blockers, and next recommended action.\n")
	return b.String()
}

func addDiffArtifacts(ctx context.Context, workspace, artifactDir string, result map[string]any) {
	stat, statErr := runGit(ctx, workspace, "diff", "--stat")
	diff, diffErr := runGit(ctx, workspace, "diff")
	if statErr != nil || diffErr != nil {
		result["diff_error"] = firstNonEmpty(errorString(statErr), errorString(diffErr))
		return
	}
	statPath := filepath.Join(artifactDir, "git-diff-stat.txt")
	diffPath := filepath.Join(artifactDir, "git-diff.patch")
	_ = os.WriteFile(statPath, []byte(stat), 0644)
	_ = os.WriteFile(diffPath, []byte(diff), 0644)
	result["diff_stat"] = strings.TrimSpace(stat)
	result["diff_path"] = diffPath
	if artifacts, ok := result["artifacts"].([]string); ok {
		result["artifacts"] = append(artifacts, statPath, diffPath)
	}
}

func runGit(ctx context.Context, workspace string, args ...string) (string, error) {
	cmdArgs := append([]string{"-C", workspace}, args...)
	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return "", err
	}
	return stdout.String(), nil
}

func responseFromResult(taskID string, result map[string]any, failed bool) map[string]any {
	resp := map[string]any{
		"task_id":                 taskID,
		"status":                  "completed",
		"summary":                 "",
		"confidence":              1.0,
		"artifacts":               []string{},
		"blockers":                []string{},
		"next_recommended_action": "review_result",
		"needs_human_input":       false,
	}
	if failed {
		resp["status"] = "failed"
		resp["needs_human_input"] = true
		resp["next_recommended_action"] = "inspect_codex_error"
	}
	for _, key := range []string{"summary", "final_message", "provider", "model", "sandbox", "approval_policy", "duration_ms", "exit_code", "diff_stat", "diff_path", "error"} {
		if value, ok := result[key]; ok {
			resp[key] = value
		}
	}
	if artifacts, ok := result["artifacts"]; ok {
		resp["artifacts"] = artifacts
	}
	if errText, ok := result["error"].(string); ok && strings.TrimSpace(errText) != "" {
		resp["blockers"] = []string{errText}
	}
	return resp
}

func readTextIfExists(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func firstLine(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	line := text
	if idx := strings.IndexAny(text, "\r\n"); idx >= 0 {
		line = text[:idx]
	}
	if len(line) > 240 {
		line = line[:240]
	}
	return strings.TrimSpace(line)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func safeID(value string) string {
	value = strings.ToLower(safeIDPattern.ReplaceAllString(value, "-"))
	value = strings.Trim(value, ".-_")
	if value == "" {
		return "task"
	}
	if len(value) > 100 {
		return value[:100]
	}
	return value
}

var safeIDPattern = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

var _ crossprizm.TaskAdapter = (*Worker)(nil)

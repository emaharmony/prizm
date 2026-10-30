package codexcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/provider"
)

// ConstrainedSelectorConfig is intentionally narrower than Config. Freewill
// selection must not inherit profiles, extra arguments, workspaces, fallback
// models, or mutation-capable sandbox settings from the general Codex provider.
type ConstrainedSelectorConfig struct {
	Executable     string
	Model          string
	TimeoutMinutes int
	OutputSchema   []byte
}

// ConstrainedSelector runs one strict, no-tools Codex selection request.
type ConstrainedSelector struct {
	cfg    ConstrainedSelectorConfig
	runner Runner
}

var constrainedSelectorDisabledFeatures = []string{
	"shell_tool",
	"apps",
	"plugins",
	"browser_use",
	"browser_use_external",
	"computer_use",
	"multi_agent",
	"code_mode_host",
	"hooks",
	"in_app_browser",
	"workspace_dependencies",
	"image_generation",
	"tool_call_mcp_elicitation",
}

// NormalizeConstrainedSelectorConfig applies only non-authority defaults.
func NormalizeConstrainedSelectorConfig(cfg ConstrainedSelectorConfig) ConstrainedSelectorConfig {
	if strings.TrimSpace(cfg.Executable) == "" {
		if runtime.GOOS == "windows" {
			cfg.Executable = "codex.cmd"
		} else {
			cfg.Executable = "codex"
		}
	}
	if cfg.TimeoutMinutes <= 0 {
		cfg.TimeoutMinutes = DefaultTimeoutMinutes
	}
	cfg.Model = strings.TrimSpace(cfg.Model)
	cfg.OutputSchema = append([]byte(nil), cfg.OutputSchema...)
	return cfg
}

// NewConstrainedSelector creates the selector-only Codex adapter.
func NewConstrainedSelector(cfg ConstrainedSelectorConfig) (*ConstrainedSelector, error) {
	return NewConstrainedSelectorWithRunner(cfg, osRunner{})
}

// NewConstrainedSelectorWithRunner creates a selector with an injected runner.
func NewConstrainedSelectorWithRunner(cfg ConstrainedSelectorConfig, runner Runner) (*ConstrainedSelector, error) {
	cfg = NormalizeConstrainedSelectorConfig(cfg)
	if cfg.Model == "" {
		return nil, errors.New("codex: constrained selector model is required")
	}
	var schema map[string]any
	if len(cfg.OutputSchema) == 0 || json.Unmarshal(cfg.OutputSchema, &schema) != nil || schema["type"] != "object" {
		return nil, errors.New("codex: constrained selector output schema is invalid")
	}
	if runner == nil {
		runner = osRunner{}
	}
	return &ConstrainedSelector{cfg: cfg, runner: runner}, nil
}

// ChatGenerate executes in a fresh empty working directory with user
// configuration ignored, session persistence disabled, read-only sandboxing,
// a strict output schema, and every native tool-bearing feature disabled.
func (p *ConstrainedSelector) ChatGenerate(ctx context.Context, req provider.ChatGenerateRequest) (provider.ChatGenerateResponse, error) {
	if p == nil {
		return provider.ChatGenerateResponse{}, errors.New("codex: constrained selector is unavailable")
	}
	if len(req.Tools) != 0 {
		return provider.ChatGenerateResponse{}, errors.New("codex: constrained selector does not accept tools")
	}
	if requested := strings.TrimSpace(req.Model); requested != "" && requested != p.cfg.Model {
		return provider.ChatGenerateResponse{}, errors.New("codex: constrained selector model override is not allowed")
	}

	workspace, err := os.MkdirTemp("", "prizm-freewill-selector-workspace-*")
	if err != nil {
		return provider.ChatGenerateResponse{}, fmt.Errorf("codex: create constrained selector workspace: %w", err)
	}
	defer os.RemoveAll(workspace)

	schemaFile, err := os.CreateTemp("", "prizm-freewill-selector-schema-*.json")
	if err != nil {
		return provider.ChatGenerateResponse{}, fmt.Errorf("codex: create constrained selector schema: %w", err)
	}
	schemaPath := schemaFile.Name()
	defer os.Remove(schemaPath)
	if _, err := schemaFile.Write(p.cfg.OutputSchema); err != nil {
		_ = schemaFile.Close()
		return provider.ChatGenerateResponse{}, fmt.Errorf("codex: write constrained selector schema: %w", err)
	}
	if err := schemaFile.Close(); err != nil {
		return provider.ChatGenerateResponse{}, fmt.Errorf("codex: close constrained selector schema: %w", err)
	}

	lastMessage, err := os.CreateTemp("", "prizm-freewill-selector-last-*.json")
	if err != nil {
		return provider.ChatGenerateResponse{}, fmt.Errorf("codex: create constrained selector output: %w", err)
	}
	lastPath := lastMessage.Name()
	_ = lastMessage.Close()
	defer os.Remove(lastPath)

	args := []string{
		"exec",
		"--ignore-user-config",
		"--ephemeral",
		"--cd", workspace,
		"--sandbox", "read-only",
		"--skip-git-repo-check",
		"--output-schema", schemaPath,
		"--output-last-message", lastPath,
		"--color", "never",
		"--model", p.cfg.Model,
	}
	for _, feature := range constrainedSelectorDisabledFeatures {
		args = append(args, "--disable", feature)
	}
	args = append(args, "-")

	cctx, cancel := context.WithTimeout(ctx, time.Duration(p.cfg.TimeoutMinutes)*time.Minute)
	defer cancel()
	start := time.Now()
	result, runErr := p.runner.Run(cctx, p.cfg.Executable, args, flattenSelectorMessages(req.Messages), workspace)
	latency := time.Since(start).Milliseconds()
	if runErr != nil {
		return provider.ChatGenerateResponse{}, fmt.Errorf("codex constrained selection failed (exit %d): %v: %s", result.ExitCode, runErr, strings.TrimSpace(result.Stderr))
	}
	content := strings.TrimSpace(readIfExists(lastPath))
	if content == "" {
		content = strings.TrimSpace(result.Stdout)
	}
	return provider.ChatGenerateResponse{
		Content: content, Model: p.cfg.Model, Provider: "codex", LatencyMS: latency,
		Raw: map[string]any{"exit_code": result.ExitCode, "sandbox": "read-only", "ephemeral": true},
	}, nil
}

func flattenSelectorMessages(messages []provider.ChatMessage) string {
	var prompt strings.Builder
	for _, message := range messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		if role == "" {
			role = "user"
		}
		prompt.WriteByte('[')
		prompt.WriteString(role)
		prompt.WriteByte(']')
		prompt.WriteByte(10)
		prompt.WriteString(message.Content)
		prompt.WriteByte(10)
	}
	return prompt.String()
}

// Package factory writes validation-only tasks into the Roblox Factory queue.
package factory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/crossprizm"
)

// Config controls Roblox Factory task handoff.
type Config struct {
	Enabled            bool
	Root               string
	Project            string
	ProjectPath        string
	ApprovalMode       string
	RunCodex           bool
	VisionReview       string
	PlaytestMode       string
	EnableUIGeneration bool
	UIGenerationDryRun bool
}

// Operator writes cross-Prizm task requests into the Factory inbox.
type Operator struct {
	cfg Config
}

// NewOperator creates a Factory operator.
func NewOperator(cfg Config) (*Operator, error) {
	if cfg.Root == "" {
		return nil, fmt.Errorf("factory: root is required")
	}
	if cfg.Project == "" {
		cfg.Project = "eggventura"
	}
	if cfg.ApprovalMode == "" {
		cfg.ApprovalMode = "report_only"
	}
	if cfg.VisionReview == "" {
		cfg.VisionReview = "none"
	}
	if cfg.PlaytestMode == "" {
		cfg.PlaytestMode = "none"
	}
	if cfg.RunCodex && cfg.ApprovalMode != "implementation" {
		return nil, fmt.Errorf("factory: run_codex=true requires approval_mode=implementation")
	}
	return &Operator{cfg: cfg}, nil
}

// HandleCrossPrizmTask handles a verified cross-Prizm task_request message.
func (o *Operator) HandleCrossPrizmTask(ctx context.Context, msg crossprizm.Message) (*crossprizm.Message, error) {
	_ = ctx
	if msg.MessageType != crossprizm.TypeTaskRequest {
		return nil, nil
	}
	taskID := taskIDFromMessage(msg)
	requestText := requestTextFromMessage(msg)
	if strings.TrimSpace(requestText) == "" {
		requestText = "Factory task request received without a request body."
	}

	requestPath, taskPath, err := o.writeTaskFiles(taskID, requestText, msg)
	if err != nil {
		return nil, err
	}

	return &crossprizm.Message{
		MessageType: crossprizm.TypeTaskResponse,
		Response: map[string]any{
			"status":       "accepted",
			"task_id":      taskID,
			"factory_root": o.cfg.Root,
			"request_path": requestPath,
			"task_path":    taskPath,
			"mode":         o.cfg.ApprovalMode,
			"run_codex":    o.cfg.RunCodex,
		},
	}, nil
}

func (o *Operator) writeTaskFiles(taskID, requestText string, msg crossprizm.Message) (string, string, error) {
	root, err := filepath.Abs(o.cfg.Root)
	if err != nil {
		return "", "", fmt.Errorf("factory: resolve root: %w", err)
	}
	inbox := filepath.Join(root, "inbox")
	prompts := filepath.Join(root, "prompts")
	for _, dir := range []string{inbox, prompts, filepath.Join(root, "outbox"), filepath.Join(root, "logs")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", "", fmt.Errorf("factory: create %s: %w", dir, err)
		}
	}

	requestPath := filepath.Join(prompts, taskID+".md")
	body := buildRequestMarkdown(msg, requestText)
	if err := os.WriteFile(requestPath, []byte(body), 0644); err != nil {
		return "", "", fmt.Errorf("factory: write request: %w", err)
	}

	payload := map[string]any{
		"task_id":               taskID,
		"project":               o.cfg.Project,
		"request_path":          relativeToRoot(root, requestPath),
		"approval_mode":         o.cfg.ApprovalMode,
		"run_codex":             o.cfg.RunCodex,
		"vision_review":         o.cfg.VisionReview,
		"playtest_mode":         o.cfg.PlaytestMode,
		"enable_ui_generation":  o.cfg.EnableUIGeneration,
		"ui_generation_dry_run": o.cfg.UIGenerationDryRun,
		"created_by":            msg.From,
		"created_at":            time.Now().UTC().Format(time.RFC3339Nano),
		"source":                "prizm-cross",
		"correlation_id":        msg.CorrelationID,
	}
	if o.cfg.ProjectPath != "" {
		payload["project_path"] = o.cfg.ProjectPath
	}
	taskJSON, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", "", fmt.Errorf("factory: marshal task: %w", err)
	}
	taskPath := filepath.Join(inbox, taskID+".json")
	if err := os.WriteFile(taskPath, append(taskJSON, '\n'), 0644); err != nil {
		return "", "", fmt.Errorf("factory: write task: %w", err)
	}
	return requestPath, taskPath, nil
}

func buildRequestMarkdown(msg crossprizm.Message, requestText string) string {
	var b strings.Builder
	b.WriteString("# Prizm Factory Request\n\n")
	b.WriteString(requestText)
	b.WriteString("\n\n## Cross-Prizm Metadata\n\n")
	b.WriteString("- From: " + msg.From + "\n")
	b.WriteString("- To: " + msg.To + "\n")
	if msg.CorrelationID != "" {
		b.WriteString("- Correlation: " + msg.CorrelationID + "\n")
	}
	return b.String()
}

func requestTextFromMessage(msg crossprizm.Message) string {
	for _, key := range []string{"prompt", "task", "content", "request", "summary"} {
		if value, ok := msg.Request[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	if len(msg.Request) > 0 {
		data, _ := json.MarshalIndent(msg.Request, "", "  ")
		return "```json\n" + string(data) + "\n```"
	}
	return ""
}

func taskIDFromMessage(msg crossprizm.Message) string {
	base := msg.CorrelationID
	if base == "" {
		base = msg.Nonce
	}
	if base == "" {
		base = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	base = strings.ToLower(safeTaskID.ReplaceAllString(base, "-"))
	base = strings.Trim(base, ".-_")
	if base == "" {
		base = "task"
	}
	if len(base) > 80 {
		base = base[:80]
	}
	return "prizm-" + base
}

func relativeToRoot(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return rel
}

var safeTaskID = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

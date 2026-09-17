package autopatch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/emaharmony/prizm/internal/codexworker"
	"github.com/emaharmony/prizm/internal/provider"
)

// CodexWorker runs the local Codex CLI inside the isolated worktree.
type CodexWorker struct {
	Config codexworker.Config
}

func NewCodexWorker(cfg codexworker.Config) *CodexWorker {
	return &CodexWorker{Config: cfg}
}

func (w *CodexWorker) Name() string {
	return "codex"
}

func (w *CodexWorker) Run(ctx context.Context, req WorkerRequest) (WorkerResult, error) {
	cfg := w.Config
	cfg.Enabled = true
	cfg.Workspace = req.Worktree
	cfg.DataDir = filepath.Join(req.ArtifactDir, "codex")
	cfg.CaptureDiff = true

	worker, err := codexworker.New(cfg)
	if err != nil {
		return WorkerResult{Worker: w.Name()}, err
	}

	description := buildWorkerPrompt(req, "Edit files directly in the current worktree. Do not commit. Keep the change minimal.")
	result, err := worker.RunTask(ctx, req.TaskID, description, map[string]any{
		"source":   "autopatch",
		"attempt":  req.Attempt,
		"worktree": req.Worktree,
	})
	res := WorkerResult{Worker: w.Name()}
	if final, ok := result["final_message"].(string); ok {
		res.Diagnosis = firstParagraph(final)
	}
	if artifacts, ok := result["artifacts"].([]string); ok {
		res.Artifacts = append(res.Artifacts, artifacts...)
	}
	return res, err
}

// LocalAgentWorker asks a configured Prizm model for a unified diff and applies it.
type LocalAgentWorker struct {
	Provider provider.Provider
	Model    string
	AgentID  string
}

func NewLocalAgentWorker(p provider.Provider, model, agentID string) *LocalAgentWorker {
	return &LocalAgentWorker{Provider: p, Model: model, AgentID: agentID}
}

func (w *LocalAgentWorker) Name() string {
	return "local_agent"
}

func (w *LocalAgentWorker) Run(ctx context.Context, req WorkerRequest) (WorkerResult, error) {
	if w.Provider == nil {
		return WorkerResult{Worker: w.Name()}, fmt.Errorf("local agent provider unavailable")
	}
	prompt := buildWorkerPrompt(req, "Return only a unified git diff patch. Do not include prose outside the diff.")
	resp, err := w.Provider.Generate(ctx, provider.GenerateRequest{
		Prompt:      prompt,
		Model:       w.Model,
		Agent:       w.AgentID,
		Project:     "prizm",
		Task:        req.Description,
		Temperature: 0.1,
		MaxTokens:   4096,
	})
	if err != nil {
		return WorkerResult{Worker: w.Name()}, err
	}
	rawPath := filepath.Join(req.ArtifactDir, "local-agent-response.txt")
	_ = os.WriteFile(rawPath, []byte(resp.Text), 0644)

	diff, err := extractDiff(resp.Text)
	if err != nil {
		return WorkerResult{Worker: w.Name(), Artifacts: []string{rawPath}}, err
	}
	diffPath := filepath.Join(req.ArtifactDir, "local-agent.patch")
	_ = os.WriteFile(diffPath, []byte(diff), 0644)

	if _, err := runCommand(ctx, req.Worktree, diff, "git", "apply", "--check", "-"); err != nil {
		return WorkerResult{Worker: w.Name(), Artifacts: []string{rawPath, diffPath}}, fmt.Errorf("generated patch failed git apply --check: %w", err)
	}
	if _, err := runCommand(ctx, req.Worktree, diff, "git", "apply", "-"); err != nil {
		return WorkerResult{Worker: w.Name(), Artifacts: []string{rawPath, diffPath}}, fmt.Errorf("generated patch failed git apply: %w", err)
	}
	return WorkerResult{
		Worker:    w.Name(),
		Diagnosis: "Local agent generated a patch diff.",
		Artifacts: []string{rawPath, diffPath},
	}, nil
}

func buildWorkerPrompt(req WorkerRequest, instruction string) string {
	var b strings.Builder
	b.WriteString("# Autopatch Task\n\n")
	b.WriteString("You are fixing a bug in an isolated git worktree.\n\n")
	b.WriteString("## Bug Report\n\n")
	b.WriteString(strings.TrimSpace(req.Description))
	b.WriteString("\n\n## Constraints\n\n")
	b.WriteString("- Diagnose the likely cause before editing.\n")
	b.WriteString("- Keep the patch minimal and focused.\n")
	b.WriteString("- Do not commit changes.\n")
	b.WriteString("- Do not modify generated, cache, or dependency directories unless the bug requires it.\n")
	b.WriteString("- ")
	b.WriteString(instruction)
	b.WriteString("\n")
	if strings.TrimSpace(req.Feedback) != "" {
		b.WriteString("\n## Previous Attempt Feedback\n\n")
		b.WriteString(req.Feedback)
		b.WriteString("\n")
	}
	return b.String()
}

func extractDiff(text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("empty patch response")
	}
	if strings.Contains(text, "```") {
		parts := strings.Split(text, "```")
		for i := 1; i < len(parts); i += 2 {
			block := strings.TrimSpace(parts[i])
			block = strings.TrimPrefix(block, "diff")
			block = strings.TrimPrefix(block, "patch")
			block = strings.TrimSpace(block)
			if strings.Contains(block, "diff --git ") || strings.HasPrefix(block, "--- ") {
				return block + "\n", nil
			}
		}
	}
	if idx := strings.Index(text, "diff --git "); idx >= 0 {
		return text[idx:] + "\n", nil
	}
	if strings.HasPrefix(text, "--- ") {
		return text + "\n", nil
	}
	return "", fmt.Errorf("response did not contain a unified diff")
}

func firstParagraph(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if idx := strings.Index(text, "\n\n"); idx >= 0 {
		text = text[:idx]
	}
	if len(text) > 600 {
		text = text[:600]
	}
	return strings.TrimSpace(text)
}

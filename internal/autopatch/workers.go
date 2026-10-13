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
		MaxTokens:   0, // no output-token ceiling unless the caller configures one
	})
	if err != nil {
		return WorkerResult{Worker: w.Name()}, err
	}
	diff, err := extractDiff(resp.Text)
	if err != nil {
		return WorkerResult{Worker: w.Name()}, err
	}
	rawPath, diffPath, err := writeLocalAgentArtifacts(req.ArtifactDir, diff)
	if err != nil {
		return WorkerResult{Worker: w.Name()}, err
	}

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

func writeLocalAgentArtifacts(artifactDir, diff string) (string, string, error) {
	rawPath := filepath.Join(artifactDir, "local-agent-response.txt")
	const publicArtifact = "Provider response omitted; the extracted patch is stored in local-agent.patch.\n"
	if err := os.WriteFile(rawPath, []byte(publicArtifact), 0644); err != nil {
		return "", "", fmt.Errorf("write public response artifact: %w", err)
	}
	diffPath := filepath.Join(artifactDir, "local-agent.patch")
	if err := os.WriteFile(diffPath, []byte(diff), 0644); err != nil {
		return "", "", fmt.Errorf("write patch artifact: %w", err)
	}
	return rawPath, diffPath, nil
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
	// Find the first diff while skipping private reasoning blocks. Stop scanning
	// as soon as the patch begins so literal <think> tags in source hunks remain
	// part of the patch. Unterminated reasoning fails closed.
	lower := strings.ToLower(text)
	inFence := false
	for i := 0; i < len(text); {
		if hasThinkTagPrefix(lower, i, "<think") {
			if !strings.HasPrefix(lower[i:], "<think>") {
				return "", fmt.Errorf("response contained a malformed <think> tag")
			}
			closeStart, closeEnd, malformed := findThinkClose(lower, i+len("<think>"))
			if malformed {
				return "", fmt.Errorf("response contained a malformed </think> tag")
			}
			if closeStart < 0 {
				return "", fmt.Errorf("response contained an unterminated <think> block")
			}
			i = closeEnd
			continue
		}
		if hasThinkTagPrefix(lower, i, "</think") {
			return "", fmt.Errorf("response contained an unmatched or malformed </think> tag")
		}
		if strings.HasPrefix(text[i:], "```") {
			inFence = !inFence
			i += len("```")
			continue
		}
		if strings.HasPrefix(text[i:], "diff --git ") {
			if inFence {
				if end := strings.Index(text[i:], "```"); end >= 0 {
					return strings.TrimSpace(text[i:i+end]) + "\n", nil
				}
			}
			return text[i:] + "\n", nil
		}
		if strings.HasPrefix(text[i:], "--- ") && (i == 0 || text[i-1] == '\n') {
			if inFence {
				if end := strings.Index(text[i:], "```"); end >= 0 {
					return strings.TrimSpace(text[i:i+end]) + "\n", nil
				}
			}
			return text[i:] + "\n", nil
		}
		i++
	}
	return "", fmt.Errorf("response did not contain a unified diff")
}

func hasThinkTagPrefix(lower string, i int, prefix string) bool {
	if !strings.HasPrefix(lower[i:], prefix) {
		return false
	}
	next := i + len(prefix)
	if next == len(lower) {
		return true
	}
	char := lower[next]
	return !(char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '_')
}

func findThinkClose(lower string, start int) (int, int, bool) {
	for i := start; i < len(lower); {
		openRel := strings.Index(lower[i:], "<think")
		closeRel := strings.Index(lower[i:], "</think")
		if closeRel < 0 {
			if openRel >= 0 {
				open := i + openRel
				if hasThinkTagPrefix(lower, open, "<think") {
					return open, -1, true
				}
			}
			return -1, -1, false
		}
		close := i + closeRel
		if openRel >= 0 {
			open := i + openRel
			if open < close && hasThinkTagPrefix(lower, open, "<think") {
				return open, -1, true
			}
		}
		i = close
		if !hasThinkTagPrefix(lower, close, "</think") {
			i += len("</think")
			continue
		}
		if !strings.HasPrefix(lower[close:], "</think>") {
			return close, -1, true
		}
		return close, close + len("</think>"), false
	}
	return -1, -1, false
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

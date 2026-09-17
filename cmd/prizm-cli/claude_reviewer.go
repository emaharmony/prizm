// Package main: Claude Code sub-agent reviewer service.
//
// When the gated loop pauses at a FEEDBACK_PRE or FEEDBACK_POST gate, the engine
// emits prizm.workflow.feedback.requested with the phase, the parties whose
// sign-off is required, and a formatted plan/review package. This service watches
// that subject and, when the gate requires the configured reviewer name (default
// "claude"), runs the Claude Code CLI to produce a verdict and publishes it back
// on prizm.workflow.feedback.response — the same subject a human approval uses —
// which releases the gate. Human reviewers (e.g. "ema") are left untouched.
package main

import (
	stdcontext "context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/emaharmony/prizm/internal/claudeworker"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/nats-io/nats.go"
)

// claudeReviewer fulfills gated-loop feedback gates using the Claude Code CLI.
type claudeReviewer struct {
	nc     *nats.Conn
	worker *claudeworker.Worker
	cfg    *orchestrator.Config
}

// startClaudeReviewer subscribes the reviewer to feedback requests.
func startClaudeReviewer(nc *nats.Conn, worker *claudeworker.Worker, cfg *orchestrator.Config) error {
	if nc == nil || worker == nil {
		return fmt.Errorf("claude reviewer requires NATS and a worker")
	}
	cr := &claudeReviewer{nc: nc, worker: worker, cfg: cfg}
	_, err := nc.Subscribe("prizm.workflow.feedback.requested", cr.handle)
	if err != nil {
		return fmt.Errorf("subscribe feedback.requested: %w", err)
	}
	log.Printf("[CLAUDE-REVIEW] watching feedback gates as reviewer %q", worker.ReviewerName())
	return nil
}

// feedbackEnvelope matches the NATSEmitter wrapper {type,payload,timestamp}.
type feedbackEnvelope struct {
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
}

func (cr *claudeReviewer) handle(msg *nats.Msg) {
	var env feedbackEnvelope
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		log.Printf("[CLAUDE-REVIEW] bad feedback.requested: %v", err)
		return
	}
	p := env.Payload
	phase, _ := p["phase"].(string)
	runID, _ := p["run_id"].(string)
	pkg, _ := p["package"].(string)
	repoPath, _ := p["repo_path"].(string)

	// Is this reviewer one of the required parties for the gate?
	parties := append(stringSliceOf(p["approvers"]), stringSliceOf(p["required_reviewers"])...)
	if !containsFold(parties, cr.worker.ReviewerName()) {
		return // not my job — leave it to humans / other reviewers
	}

	go cr.review(phase, runID, pkg, repoPath)
}

func (cr *claudeReviewer) review(phase, runID, pkg, repoPath string) {
	if repoPath == "" {
		if proj := cr.cfg.DefaultProject(); proj != nil {
			repoPath = proj.RepoPath
		}
	}
	prompt := buildReviewPrompt(phase, pkg)

	log.Printf("[CLAUDE-REVIEW] reviewing %s for run %s (repo=%s)", phase, runID, repoPath)
	out, err := cr.worker.Review(stdcontext.Background(), repoPath, prompt)
	if err != nil {
		// Do not auto-respond on hard failure: leave the gate to a human and the
		// engine's max-wait. Logged loudly so the operator can intervene.
		log.Printf("[CLAUDE-REVIEW] ERROR review failed for run %s: %v", runID, err)
		return
	}

	verdict := claudeworker.ParseVerdict(out)
	log.Printf("[CLAUDE-REVIEW] run %s %s verdict=%s notes=%q", runID, phase, verdict.Decision, verdict.Notes)
	cr.publish(phase, runID, verdict)
}

func (cr *claudeReviewer) publish(phase, runID string, v claudeworker.Verdict) {
	payload := map[string]any{
		"decision":    v.Decision,
		"notes":       v.Notes,
		"workflow_id": runID,
	}
	// FEEDBACK_PRE uses approval semantics (no reviewer field); FEEDBACK_POST
	// uses review semantics (reviewer + optional dimensions).
	if phase == "FEEDBACK_POST" {
		payload["type"] = "review_response"
		payload["reviewer"] = cr.worker.ReviewerName()
		if len(v.Dimensions) > 0 {
			payload["dimensions"] = v.Dimensions
		}
	} else {
		payload["type"] = "feedback_response"
	}
	data, _ := json.Marshal(payload)
	if err := cr.nc.Publish("prizm.workflow.feedback.response", data); err != nil {
		log.Printf("[CLAUDE-REVIEW] publish response failed: %v", err)
	}
}

// buildReviewPrompt assembles the print-mode prompt for the Claude Code CLI.
func buildReviewPrompt(phase, pkg string) string {
	var b strings.Builder
	if phase == "FEEDBACK_PRE" {
		b.WriteString("You are a senior engineering reviewer. Review the PLAN below BEFORE any code is written.\n")
		b.WriteString("Judge whether it is sound, complete, scoped correctly, and safe to execute.\n\n")
	} else {
		b.WriteString("You are a senior code reviewer. Review the COMPLETED WORK below.\n")
		b.WriteString("Inspect the repository changes (use git diff / git log against the base branch and read changed files) for correctness, regressions, test coverage, and whether the task was actually completed.\n\n")
	}
	b.WriteString(pkg)
	b.WriteString("\n\n## Required output\n")
	b.WriteString("Be concise. End your response with EXACTLY one of these lines:\n")
	b.WriteString("VERDICT: approved\n")
	b.WriteString("VERDICT: changes_requested\n")
	b.WriteString("Then optionally one line: NOTES: <one-line summary of the key issue or confirmation>\n")
	return b.String()
}

// stringSliceOf coerces an any (from JSON) into a []string.
func stringSliceOf(v any) []string {
	out := []string{}
	switch s := v.(type) {
	case []string:
		return s
	case []any:
		for _, item := range s {
			if str, ok := item.(string); ok {
				out = append(out, str)
			}
		}
	}
	return out
}

func containsFold(list []string, target string) bool {
	for _, s := range list {
		if strings.EqualFold(strings.TrimSpace(s), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}

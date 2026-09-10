// Package main provides the wake handler for V32 event-driven wake.
//
// The wake handler subscribes to NATS events from the scheduler and triggers
// LLM inference with context-appropriate prompts. This is how Prizm "wakes up"
// on schedule instead of relying on heartbeat babysitting.
//
// Supported actions:
//   - daily_review: Review state, check for stale tasks, summarize progress
//   - memory_consolidation: Trigger Remembrance dream cycle
//   - check_prs: Check for open PRs and notify
package main

import (
	stdcontext "context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/emaharmony/prizm/internal/adapter/builtin/discordbot"
	"github.com/emaharmony/prizm/internal/agent"
	"github.com/emaharmony/prizm/internal/api"
	contextpkg "github.com/emaharmony/prizm/internal/context"
	"github.com/emaharmony/prizm/internal/factorymonitor"
	"github.com/emaharmony/prizm/internal/gitx"
	"github.com/emaharmony/prizm/internal/improve"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/plan"
	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/session"
	"github.com/emaharmony/prizm/internal/skill"
	"github.com/emaharmony/prizm/internal/state"
	"github.com/emaharmony/prizm/internal/tool"
	"github.com/emaharmony/prizm/internal/validation"
	"github.com/emaharmony/prizm/internal/workflow/v2"
	"github.com/emaharmony/prizm/internal/workstart"
	"github.com/nats-io/nats.go"
)

// WakeHandler subscribes to scheduler events and triggers LLM inference.
// It also listens for inter-agent messages on NATS (e.g. from OpenClaw Lumi).
type WakeHandler struct {
	cfg        *orchestrator.Config
	providers  *provider.ProviderRegistry
	sessMgr    *session.Manager
	stateMgr   *state.Manager
	natsConn   *nats.Conn
	bot        discordBotClient
	ctxBuilder *contextpkg.Builder
	planMgr    *plan.Manager
	improveMgr *improve.Manager
	factoryMon *factorymonitor.Monitor

	toolExec   *tool.Executor  // V35: Tool executor for project_work action
	toolReg    *tool.Registry  // V35: Tool registry for listing tools in prompt
	skills     *skill.Registry // V54: skills advertised in the system prompt

	// agentMessages stores recent inter-agent messages received via NATS.
	// Accessed under agentMu for concurrent safety.
	agentMu       sync.Mutex
	agentMessages []agentMessage
}

// agentMessage is a message from another agent (e.g. OpenClaw Lumi) received via NATS.
type agentMessage struct {
	From      string    `json:"from"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// wakeAction defines a scheduled action prompt.
type wakeAction struct {
	Prompt    string // System prompt for the LLM
	ChannelID string // Discord channel to send results to
	MaxTokens int    // Max response tokens
	SkipLLM   bool   // If true, run the action directly without LLM (e.g., gh pr list)
}

// knownActions maps action names to their prompts and target channels.
var knownActions = map[string]wakeAction{
	"daily_review": {
		Prompt: `You are performing a daily review of your working state. Read your state files and:
1. Check if any tasks are stale (status hasn't changed in >24 hours)
2. Review blocked items — are any unblocked now?
3. Summarize what you accomplished yesterday and what's next
4. Clean up state files if needed (clear completed tasks, update context)
5. Be concise — this is a status report, not a novel

If everything looks good, say so briefly. If something needs attention, flag it clearly.`,
		ChannelID: "1491622863118008431", // scheduled-reports destination
		MaxTokens: 2048,
	},
	"memory_consolidation": {
		Prompt: `You are performing weekly memory consolidation. Review recent conversations and:
1. Identify recurring themes, decisions, and patterns
2. Flag anything that should be persisted to long-term memory
3. Note any contradictions or outdated information
4. Be concise — this is a consolidation report

Focus on what matters. Skip trivia.`,
		ChannelID: "1491622863118008431", // scheduled-reports destination
		MaxTokens: 1024,
	},
	"check_prs": {
		Prompt: `You are summarizing PR status for a Discord channel. Take the raw PR data below and format it concisely:
1. List each PR with number, title, author, review status, and CI status
2. Flag any PRs that need attention (stale reviews, failed CI, ready to merge)
3. If no PRs are open, say "No open PRs" and nothing else

Be concise — this is a status report, not a novel.`,
		ChannelID: "1491622863118008431", // scheduled-reports destination
		MaxTokens: 1024,
		SkipLLM:   true, // Run gh pr list directly, then optionally summarize
	},
	"factory_status_digest": {
		Prompt:    `Report the current Roblox Factory queue status.`,
		ChannelID: "1491622863118008431", // scheduled-reports destination
		MaxTokens: 512,
		SkipLLM:   true,
	},
	"auto_patch": {
		Prompt: `You are the auto-patch agent. You are an active developer, not just a reporter. Your job is to find and fix bugs, improve user experience, and increase effectiveness across the codebase.

## CRITICAL: JSON Only
You MUST respond with JSON tool calls, not prose. Every response must be either:
- {"type": "tool_request", "tool": "tool_name", "input": {"key": "value"}}
- {"type": "final", "content": "summary text"}

DO NOT write prose like "I'll start by checking..." or "Let me look at...". That text is discarded and wastes your turn. Instead, immediately emit a tool_request JSON to do the action.

## Your Capabilities
You can: create git branches, write code, commit, push, and open PRs. You have git_status, git_log, git_diff, file_read, file_write, and file_list tools. Use them.

## Workflow
1. First, call git_status to check the current state
2. Then call git_log to see recent commits
3. For each issue found: create a git branch, write the fix, commit, push, and open a PR
4. If a tool fails, mention <@1512994928769237002> (OpenClaw Lumi) with the error

## Active Bug Hunting
Look for:
- Bugs: uncommitted issues, failing tests, error messages
- UX issues: confusing text, missing error handling
- Effectiveness: redundant code, incomplete implementations
- Test gaps: add tests for untested functions

## Quality Standards
- Every fix should include or update a test if applicable
- Commit messages should be clear and descriptive
- If unsure about a change, propose it as an improvement rather than auto-PR

Be thorough, proactive, and fast. START WITH A TOOL CALL, not prose.`,
		ChannelID: "1491622863118008431", // scheduled-reports destination
		MaxTokens: 2048,
	},
	"status_report": {
		Prompt:    `Generate a 2-hour status report (project state, recent runs, git activity).`,
		ChannelID: "1491622863118008431", // scheduled-reports destination
		MaxTokens: 1024,
		SkipLLM:   true, // Reads run summaries + PROJECT_STATE.md directly
	},
	"mol_status": {
		Prompt:    `Report the MOL (Machine Orchestration Loop) self-status.`,
		ChannelID: "1491622863118008431", // scheduled-reports destination
		MaxTokens: 1024,
		SkipLLM:   true, // Pure introspection — no LLM needed
	},
	"review_improvements": {
		Prompt: `You are reviewing active improvement proposals.

1. Check the improvement proposals in your workspace state
2. For each proposed improvement:
   a. Assess whether it's still relevant
   b. Check if the underlying error pattern has resolved
   c. Determine the correct approval level
   d. If auto-PR eligible, flag it for the next auto_patch cycle
   e. If it needs Ema's approval, send a clear notification with the details
3. Dismiss duplicates or stale proposals
4. Be concise — list proposals with status and recommended action`,
		ChannelID: "1491622863118008431", // scheduled-reports destination
		MaxTokens: 1024,
	},
	"project_work": {
		// project_work runs the full gated loop via runNaturalGatesWorkflow →
		// RunGatedLoop. The actual phase prompt is built dynamically per project
		// in buildGatedLoopSystemPrompt, so this static prompt is only a seed used
		// when no project state file supplies a task.
		Prompt: `You are working on BassBook in an autonomous development loop. Your job has two phases:

1. DISCOVERY: Read PROJECT_STATE.md. If there are no unchecked tasks (or all remaining tasks are stale/vague), scan the codebase for issues, TODOs, broken features, or improvement opportunities. Add new tasks to PROJECT_STATE.md with clear descriptions and risk levels.

2. IMPLEMENTATION: Pick the topmost unchecked task. Create a feature branch (feature/bb-{task-slug}). Implement the change. Run self-review via git_diff. Fix obvious errors. Stage, commit, and push. Mark the task [x] in PROJECT_STATE.md only AFTER the review passes.

Work fast and decisively. Don't overthink — implement, review, push. If you get stuck, report the blocker and move on.`,
		ChannelID: "1491622863118008431", // scheduled-reports destination (fallback; project.channel preferred)
		MaxTokens: 4096,
	},
}

// schedulerActionList exposes the known wake actions as cron-job presets for
// the dashboard scheduler editor (action dropdown).
func schedulerActionList() []api.SchedulerAction {
	out := make([]api.SchedulerAction, 0, len(knownActions))
	for key, def := range knownActions {
		out = append(out, api.SchedulerAction{Key: key, SkipLLM: def.SkipLLM})
	}
	return out
}

// NewWakeHandler creates a wake handler with the given dependencies.
func NewWakeHandler(
	cfg *orchestrator.Config,
	providers *provider.ProviderRegistry,
	sessMgr *session.Manager,
	stateMgr *state.Manager,
	natsConn *nats.Conn,
	bot discordBotClient,
	ctxBuilder *contextpkg.Builder,
	planMgr *plan.Manager,
	improveMgr *improve.Manager,
	factoryMon *factorymonitor.Monitor,
	toolExec *tool.Executor,
	toolReg *tool.Registry,
) *WakeHandler {
	return &WakeHandler{
		cfg:        cfg,
		providers:  providers,
		sessMgr:    sessMgr,
		stateMgr:   stateMgr,
		natsConn:   natsConn,
		bot:        bot,
		ctxBuilder: ctxBuilder,
		planMgr:    planMgr,
		improveMgr: improveMgr,
		factoryMon: factoryMon,
		toolExec:   toolExec,
		toolReg:    toolReg,
	}
}

// SetSkills wires the discovered skill registry so the gated-loop system prompt
// advertises available skills (invokable via the use_skill tool).
func (wh *WakeHandler) SetSkills(skills *skill.Registry) { wh.skills = skills }

// Start subscribes to scheduler events and begins processing.
func (wh *WakeHandler) Start() error {
	if wh.natsConn == nil {
		return fmt.Errorf("NATS not connected")
	}

	_, err := wh.natsConn.Subscribe("prizm.task.scheduled", func(msg *nats.Msg) {
		wh.handleScheduledEvent(msg)
	})
	if err != nil {
		return fmt.Errorf("subscribe to prizm.task.scheduled: %w", err)
	}
	log.Printf("[WAKE] subscribed to prizm.task.scheduled")

	// Subscribe to inter-agent messages from OpenClaw Lumi
	_, err = wh.natsConn.Subscribe("prizm.agent.openclaw", func(msg *nats.Msg) {
		var am agentMessage
		if err := json.Unmarshal(msg.Data, &am); err != nil {
			log.Printf("[WAKE] failed to parse agent message: %v", err)
			return
		}
		am.Timestamp = time.Now()
		wh.agentMu.Lock()
		wh.agentMessages = append(wh.agentMessages, am)
		// Keep last 50 messages
		if len(wh.agentMessages) > 50 {
			wh.agentMessages = wh.agentMessages[len(wh.agentMessages)-50:]
		}
		wh.agentMu.Unlock()
		log.Printf("[WAKE] received agent message from %s: %s", am.From, truncate(am.Content, 80))
	})
	if err != nil {
		return fmt.Errorf("subscribe to prizm.agent.openclaw: %w", err)
	}
	log.Printf("[WAKE] subscribed to prizm.agent.openclaw")

	// Interactive gated-loop trigger: any client (CLI, API, Discord) can start a
	// gated loop for {project, prompt} by publishing here.
	_, err = wh.natsConn.Subscribe("prizm.workflow.start", func(msg *nats.Msg) {
		var req workstart.Request
		if err := json.Unmarshal(msg.Data, &req); err != nil {
			log.Printf("[WAKE] failed to parse workflow.start: %v", err)
			return
		}
		go wh.handleWorkflowStart(req)
	})
	if err != nil {
		return fmt.Errorf("subscribe to prizm.workflow.start: %w", err)
	}
	log.Printf("[WAKE] subscribed to prizm.workflow.start")

	return nil
}

// newAutoExec builds an auto-approving tool executor for autonomous loop
// execution. The human control points are the FEEDBACK_PRE/POST gates, not
// per-tool approval, so EXECUTION mutations auto-apply once the plan is approved.
func (wh *WakeHandler) newAutoExec() *tool.Executor {
	autoPolicy := *wh.toolExec.Policy
	autoPolicy.AutoApproveMutations = true
	autoExec := tool.NewExecutor(wh.toolReg, &autoPolicy)
	if wh.toolExec.Emit != nil {
		autoExec.SetEmitter(wh.toolExec.Emit)
	}
	if wh.toolExec.ApprovalStore != nil {
		autoExec.SetApprovalStore(wh.toolExec.ApprovalStore)
	}
	return autoExec
}

// handleWorkflowStart runs the gated loop for an interactively-triggered
// {project, prompt}. The project is resolved from config by ID (falling back to
// the default project); results post to the given channel or the project channel.
func (wh *WakeHandler) handleWorkflowStart(req workstart.Request) {
	if wh.toolExec == nil || wh.toolReg == nil {
		log.Printf("[WAKE] workflow.start ignored: tool executor not configured")
		return
	}

	resolved, err := workstart.Resolve(wh.cfg, req)
	if err != nil {
		log.Printf("[WAKE] workflow.start rejected: %v", err)
		wh.postWorkflowStartFailure(req.Channel, fmt.Sprintf("Workflow start rejected: %v", err))
		return
	}
	if resolved.NeedsLocation {
		log.Printf("[WAKE] workflow.start needs location: %s", resolved.Reason)
		msg := "Workflow start needs a confirmed project folder."
		if resolved.Recommendation != "" {
			msg = fmt.Sprintf("%s Recommended path: `%s`.", msg, resolved.Recommendation)
		}
		wh.postWorkflowStartFailure(req.Channel, msg)
		return
	}

	project := resolved.Project
	if project == nil {
		project = &orchestrator.ProjectConfig{
			ID:       resolved.ProjectID,
			RepoPath: resolved.RepoPath,
			Channel:  resolved.Channel,
		}
	} else {
		cp := *project
		cp.RepoPath = resolved.RepoPath
		if cp.Channel == "" {
			cp.Channel = resolved.Channel
		}
		project = &cp
	}

	if err := wh.bootstrapProjectRepo(resolved.RepoPath, req.Bootstrap || resolved.Project == nil); err != nil {
		log.Printf("[WAKE] workflow.start bootstrap failed: %v", err)
		wh.postWorkflowStartFailure(resolved.Channel, fmt.Sprintf("Workflow start failed before execution: %v", err))
		return
	}

	agentCfg := wh.orchestratorAgentFor(project)
	if agentCfg == nil {
		log.Printf("[WAKE] workflow.start: no agent configured")
		return
	}
	model := agentCfg.Model
	if model == "" {
		model = "glm-5.1:cloud"
	}

	// Resolve the result channel from the request or project config.
	channel := resolved.Channel
	if channel == "" && project != nil {
		channel = project.Channel
	}

	ctx, cancel := stdcontext.WithTimeout(stdcontext.Background(), 60*time.Minute)
	defer cancel()

	log.Printf("[WAKE] workflow.start: project=%s repo=%s prompt=%q", resolved.ProjectID, resolved.RepoPath, truncate(req.Prompt, 80))
	content, pt, ct := wh.RunGatedLoop(ctx, project, req.Prompt, model, agentCfg, wh.newAutoExec(), channel)

	if wh.bot != nil && channel != "" {
		wh.bot.Send(&discordbot.OutboundMessage{
			ChannelID: channel,
			Content:   fmt.Sprintf("🔁 **Gated Loop**\n\n%s", cleanForDiscord(content)),
		})
	}
	pid := "default"
	if project != nil && project.ID != "" {
		pid = project.ID
	}
	wh.writeRunSummary("gated_loop:"+pid, agentCfg.ID, content, pt, ct)
}

func (wh *WakeHandler) postWorkflowStartFailure(channelID, content string) {
	if wh.bot == nil || channelID == "" {
		return
	}
	if err := wh.bot.Send(&discordbot.OutboundMessage{ChannelID: channelID, Content: content}); err != nil {
		log.Printf("[WAKE] failed to post workflow start failure: %v", err)
	}
}

func (wh *WakeHandler) bootstrapProjectRepo(repoPath string, allowCreate bool) error {
	resolved, err := workstart.ResolveRepoPath(wh.cfg, repoPath)
	if err != nil {
		return err
	}

	info, statErr := os.Stat(resolved)
	if statErr != nil {
		if !os.IsNotExist(statErr) {
			return fmt.Errorf("stat repo path: %w", statErr)
		}
		if !allowCreate {
			return fmt.Errorf("repo path %q does not exist; confirm a path before creating it", resolved)
		}
		if err := os.MkdirAll(resolved, 0755); err != nil {
			return fmt.Errorf("create repo path: %w", err)
		}
	} else if !info.IsDir() {
		return fmt.Errorf("repo path %q is not a directory", resolved)
	}

	if _, err := os.Stat(filepath.Join(resolved, ".git")); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat .git: %w", err)
	}
	if !allowCreate {
		return fmt.Errorf("repo path %q is not a git repository; confirm bootstrap before starting work", resolved)
	}
	cmd := exec.Command("git", "-C", resolved, "init")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git init failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func repoHasRemote(repoPath string) bool {
	cmd := exec.Command("git", "-C", repoPath, "remote")
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// workflowHasVerification reports whether any phase declares a verification
// profile, so we only stand up the validation executor when it will be used.
func workflowHasVerification(config *v2.WorkflowConfig) bool {
	if config == nil {
		return false
	}
	for _, p := range config.Phases {
		if p.Verification != nil && strings.TrimSpace(p.Verification.Profile) != "" {
			return true
		}
	}
	return false
}

// verificationSummary builds a short, model-readable digest of a failed
// validation run by tailing the captured stdout/stderr artifacts (where `go test`
// and build errors land). Bounded so it never floods the transcript.
func verificationSummary(res *validation.Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "status=%s exit=%d", res.Status, res.ExitCode)
	if res.Error != "" {
		fmt.Fprintf(&b, "\nerror: %s", res.Error)
	}
	for label, path := range map[string]string{"stdout": res.StdoutPath, "stderr": res.StderrPath} {
		if path == "" {
			continue
		}
		if data, err := os.ReadFile(path); err == nil {
			if tail := tailBytes(strings.TrimSpace(string(data)), 1500); tail != "" {
				fmt.Fprintf(&b, "\n--- %s ---\n%s", label, tail)
			}
		}
	}
	return b.String()
}

// tailBytes returns the last max bytes of s, on a rune-safe boundary.
func tailBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	t := s[len(s)-max:]
	if i := strings.IndexByte(t, '\n'); i >= 0 && i < len(t)-1 {
		t = t[i+1:]
	}
	return "…\n" + t
}

// primaryAgent returns the primary agent config, or the first agent.
func (wh *WakeHandler) primaryAgent() *orchestrator.AgentConfig {
	for i := range wh.cfg.Agents {
		if wh.cfg.Agents[i].Primary {
			return &wh.cfg.Agents[i]
		}
	}
	if len(wh.cfg.Agents) > 0 {
		return &wh.cfg.Agents[0]
	}
	return nil
}

// orchestratorAgentFor returns the agent that should drive a project's gated
// loop: the agent named by project.Orchestrator if set and found, otherwise the
// primary agent. This is the seam that lets a project opt into a Claude Code
// (subscription) brain without changing the global default.
func (wh *WakeHandler) orchestratorAgentFor(project *orchestrator.ProjectConfig) *orchestrator.AgentConfig {
	if project != nil && project.Orchestrator != "" {
		for i := range wh.cfg.Agents {
			if wh.cfg.Agents[i].ID == project.Orchestrator {
				return &wh.cfg.Agents[i]
			}
		}
		log.Printf("[WAKE] project %q orchestrator %q not found in agents; using primary", project.ID, project.Orchestrator)
	}
	return wh.primaryAgent()
}

// handleScheduledEvent processes a scheduler event.
func (wh *WakeHandler) handleScheduledEvent(msg *nats.Msg) {
	var payload map[string]any
	if err := json.Unmarshal(msg.Data, &payload); err != nil {
		log.Printf("[WAKE] ERROR unmarshaling event: %v", err)
		return
	}

	action, _ := payload["action"].(string)
	jobName, _ := payload["job_name"].(string)
	firedAt, _ := payload["fired_at"].(string)

	if action == "" {
		log.Printf("[WAKE] ERROR event missing action field: %v", payload)
		return
	}

	log.Printf("[WAKE] processing scheduled event: job=%q action=%q fired_at=%s", jobName, action, firedAt)

	actionDef, ok := knownActions[action]
	if !ok {
		log.Printf("[WAKE] WARN unknown action %q, using generic prompt", action)
		actionDef = wakeAction{
			Prompt:    fmt.Sprintf("You received a scheduled event with action %q. Check your state and report anything that needs attention.", action),
			ChannelID: "1491622863118008431", // scheduled-reports destination
			MaxTokens: 1024,
		}
	}

	// Handle direct-execution actions (no LLM needed)
	if actionDef.SkipLLM {
		wh.handleDirectAction(action, actionDef, firedAt)
		return
	}

	// V37: Idle Guard — check locally (zero cloud tokens) whether there's actual work.
	// If nothing to do, skip the cloud LLM entirely.
	if action == "project_work" || action == "auto_patch" || action == "daily_review" || action == "review_improvements" {
		if !wh.hasWorkToDo(action) {
			log.Printf("[WAKE] idle guard: no work for %q — skipping cloud LLM (0 tokens spent)", action)
			return
		}
	}

	// Build the system prompt with state context
	systemPrompt := actionDef.Prompt

	// Inject scheduler context so the LLM knows about its own cron jobs.
	if wh.cfg != nil && wh.cfg.Prizm.Scheduler.Enabled {
		log.Printf("[WAKE] injecting scheduler context (%d jobs) into system prompt", len(wh.cfg.Prizm.Scheduler.Jobs))
		systemPrompt += "\n\n## Your Scheduled Jobs (Cron)\n"
		systemPrompt += "You run on an automated schedule. Here are your cron jobs:\n"
		for _, job := range wh.cfg.Prizm.Scheduler.Jobs {
			if !job.Enabled {
				continue
			}
			actionName := ""
			if a, ok := job.Payload["action"]; ok {
				actionName = fmt.Sprintf("%v", a)
			}
			systemPrompt += fmt.Sprintf("- **%s**: schedule=%s, action=%s", job.Name, job.Schedule, actionName)
			if actionName == action {
				systemPrompt += " ← (this is the current job)"
			}
			systemPrompt += "\n"
		}
		systemPrompt += "\nWhen Ema or another agent references these jobs by name, you now know what they are.\n"
	}

	// Inject working state if available
	if wh.stateMgr != nil {
		if statePrompt := wh.stateMgr.FormatStateForPrompt(); statePrompt != "" {
			systemPrompt += "\n\n## Current Working State\n" + statePrompt
		}
	}

	// Inject workspace context if available
	if wh.ctxBuilder != nil {
		if injected, err := wh.ctxBuilder.BuildCached(); err == nil && injected != nil && injected.FormattedString != "" {
			systemPrompt += "\n\n## Workspace Context\n" + injected.FormattedString
		}
	}

	// V32: Inject improvement proposals for auto_patch and review_improvements actions
	if wh.improveMgr != nil && (action == "auto_patch" || action == "review_improvements") {
		activeImpr := wh.improveMgr.GetActiveImprovements()
		if len(activeImpr) > 0 {
			systemPrompt += "\n\n" + improve.FormatImprovementsForPrompt(activeImpr)
		}
		errorPatterns := wh.improveMgr.GetErrorPatterns()
		if len(errorPatterns) > 0 {
			systemPrompt += "\n\n" + improve.FormatErrorPatternsForPrompt(errorPatterns)
		}
	}

	// V32: Inject active plans for auto_patch action
	if wh.planMgr != nil && action == "auto_patch" {
		if plans, err := wh.planMgr.LoadPlans(); err == nil && len(plans) > 0 {
			if activePlan := plan.ActivePlan(plans); activePlan != nil {
				systemPrompt += "\n\n" + plan.FormatPlanForPrompt(activePlan)
			}
		}
	}

	// V33: Inject recent Discord channel messages so the agent sees replies from other agents (e.g. OpenClaw Lumi)
	// before reporting. This prevents stale duplicate reports and lets agents respond to each other.
	if wh.bot != nil && actionDef.ChannelID != "" {
		recent := wh.bot.GetRecentMessages(actionDef.ChannelID, 10)
		if len(recent) > 0 {
			systemPrompt += "\n\n## Recent Channel Messages (last 10, oldest-first)\n"
			systemPrompt += "These are the most recent messages in the Discord channel. Read them BEFORE reporting. If another agent (e.g. OpenClaw Lumi, bot ID 1512994928769237002) has already addressed an issue or corrected information, acknowledge and update your report accordingly. Do NOT re-report issues that have been resolved.\n\n"
			for _, m := range recent {
				systemPrompt += fmt.Sprintf("**[%s] %s:** %s\n", m.Timestamp, m.AuthorName, m.Content)
			}
			systemPrompt += "\n--- End of recent messages ---\n"
		}
	}

	// V33: Inject inter-agent messages received via NATS
	wh.agentMu.Lock()
	if len(wh.agentMessages) > 0 {
		systemPrompt += "\n\n## Direct Messages from OpenClaw Lumi (via NATS)\n"
		systemPrompt += "These are direct messages from OpenClaw Lumi, not Discord channel messages. Treat them as authoritative updates from your partner agent.\n\n"
		for _, m := range wh.agentMessages {
			systemPrompt += fmt.Sprintf("**[%s] %s:** %s\n", m.Timestamp.Format("2006-01-02 15:04"), m.From, m.Content)
		}
		systemPrompt += "\n--- End of agent messages ---\n"
	}
	wh.agentMu.Unlock()

	// V34→V61: Inject Remembrance memory search results with dynamic queries.
	// Instead of hardcoded query strings, derive search queries from:
	//   1. The action being performed
	//   2. Recent Discord channel messages (conversation context)
	//   3. Recent inter-agent NATS messages
	//   4. The current date (for recency-aware recall)
	// Memory search is handled upstream by Smart Memory Injector.
	// Local MarkdownStore handles all memory retrieval now.

	// V34: Inject tool capability summary so Prizm knows what she can do
	systemPrompt += `

## Your Available Tools
You have access to the following tools. Use them proactively to investigate and fix issues:
- **git_status**: Check working tree status in any repo
- **git_log**: View recent commit history
- **git_diff**: See uncommitted changes
- **git_push**: Push branches to remote (with validation)
- **file_read**: Read file contents
- **file_write**: Write file contents
- **file_list**: List directory contents
- **plan_create**: Create structured plans with approval flow
- **plan_approve**: Approve or reject plans
- **improvement_create**: Propose code improvements
- **improvement_resolve**: Mark improvements as resolved

You can create branches, commit changes, push to remote, and open PRs. You are not just a reporter — you are an active developer. If you find a bug, fix it. If you see a UX issue, improve it. If you have an idea to make something more effective, propose it.
`

	// V37: Use scout (local qwen3:8b) to gather codebase context before cloud model runs.
	// This saves cloud tokens on the "read and understand" phase.
	if action == "auto_patch" || action == "project_work" {
		scoutContext := wh.gatherScoutContext(stdcontext.Background())
		if scoutContext != "" {
			systemPrompt += "\n\n## Local Context Summary (gathered by scout on qwen3:8b, zero cloud tokens)\n"
			systemPrompt += scoutContext
			systemPrompt += "\n--- End of local context ---\n"
		}
	}

	// Get the primary agent config
	var agentCfg *orchestrator.AgentConfig
	for i := range wh.cfg.Agents {
		if wh.cfg.Agents[i].Primary {
			agentCfg = &wh.cfg.Agents[i]
			break
		}
	}
	if agentCfg == nil && len(wh.cfg.Agents) > 0 {
		agentCfg = &wh.cfg.Agents[0]
	}
	if agentCfg == nil {
		log.Printf("[WAKE] ERROR no agent configured")
		return
	}

	model := agentCfg.Model
	if model == "" {
		model = "glm-5.1:cloud"
	}

	// Create or reuse a session for this action
	sessionKey := fmt.Sprintf("wake:%s:%s", action, time.Now().Format("2006-01-02"))
	sess, err := wh.sessMgr.FindActive("wake", sessionKey, "scheduler")
	if err != nil || sess == nil {
		sess, err = wh.sessMgr.Create(agentCfg.ID, "wake", sessionKey, "scheduler")
		if err != nil {
			log.Printf("[WAKE] ERROR creating session: %v", err)
			return
		}
	}

	// Build the prompt
	userPrompt := fmt.Sprintf("Perform the %s action now. This was triggered by the scheduler at %s.", action, firedAt)

	// Call the LLM — try ChatProvider first (supports tool calling), fall back to text
	log.Printf("[WAKE] calling LLM for action %q with model %q", action, model)

	ctx, cancel := stdcontext.WithTimeout(stdcontext.Background(), 10*time.Minute)
	defer cancel()

	var responseContent string
	var promptTokens, completionTokens int

	// V36: For project_work, use the V2 Natural Gates Workflow System
	if action == "project_work" && wh.toolExec != nil && wh.toolReg != nil {
		// Create an auto-approving executor for autonomous wake actions
		autoPolicy := *wh.toolExec.Policy
		autoPolicy.AutoApproveMutations = true
		autoExec := tool.NewExecutor(wh.toolReg, &autoPolicy)
		if wh.toolExec.Emit != nil {
			autoExec.SetEmitter(wh.toolExec.Emit)
		}
		if wh.toolExec.ApprovalStore != nil {
			autoExec.SetApprovalStore(wh.toolExec.ApprovalStore)
		}
		responseContent, promptTokens, completionTokens = wh.runNaturalGatesWorkflow(ctx, systemPrompt, userPrompt, model, agentCfg, autoExec)
	} else if action == "auto_patch" && wh.toolExec != nil && wh.toolReg != nil {
		// auto_patch still uses V36 tool loop
		autoPolicy := *wh.toolExec.Policy
		autoPolicy.AutoApproveMutations = true
		autoExec := tool.NewExecutor(wh.toolReg, &autoPolicy)
		if wh.toolExec.Emit != nil {
			autoExec.SetEmitter(wh.toolExec.Emit)
		}
		if wh.toolExec.ApprovalStore != nil {
			autoExec.SetApprovalStore(wh.toolExec.ApprovalStore)
		}
		responseContent, promptTokens, completionTokens = wh.runToolLoopWake(ctx, systemPrompt, userPrompt, model, agentCfg, autoExec)
	} else {
		chatProv, chatErr := wh.providers.GetChatProviderForAgent(agentCfg.ID, model)
		if chatErr == nil {
			// Use ChatProvider (preferred path — same as Discord serve)
			resp, err := chatProv.ChatGenerate(ctx, provider.ChatGenerateRequest{
				RunID: fmt.Sprintf("wake-%s-%d", action, time.Now().Unix()),
				Agent: agentCfg.ID,
				Model: model,
				Messages: []provider.ChatMessage{
					{Role: "system", Content: systemPrompt},
					{Role: "user", Content: userPrompt},
				},
				Temperature: 0.7,
				MaxTokens:   actionDef.MaxTokens,
			})
			if err != nil {
				log.Printf("[WAKE] ERROR chat LLM call failed for action %q: %v", action, err)
				if wh.bot != nil && action != "project_work" {
					wh.bot.Send(&discordbot.OutboundMessage{
						ChannelID: actionDef.ChannelID,
						Content:   fmt.Sprintf("⚠️ Scheduled task **%s** failed: %v", formatActionName(action), err),
					})
				}
				return
			}
			responseContent = resp.Content
			promptTokens = resp.PromptTokens
			completionTokens = resp.OutputTokens
		} else {
			// Fall back to text provider
			log.Printf("[WAKE] WARN no chat provider for %q: %v, falling back to text provider", model, chatErr)
			prov, err := wh.providers.GetForAgent(agentCfg.ID, model)
			if err != nil {
				log.Printf("[WAKE] ERROR no provider for model %q: %v", model, err)
				return
			}

			resp, err := prov.Generate(ctx, provider.GenerateRequest{
				RunID:       fmt.Sprintf("wake-%s-%d", action, time.Now().Unix()),
				Agent:       agentCfg.ID,
				Model:       model,
				Prompt:      systemPrompt + "\n\n" + userPrompt,
				Temperature: 0.7,
				MaxTokens:   actionDef.MaxTokens,
			})
			if err != nil {
				log.Printf("[WAKE] ERROR text LLM call failed for action %q: %v", action, err)
				if wh.bot != nil && action != "project_work" {
					wh.bot.Send(&discordbot.OutboundMessage{
						ChannelID: actionDef.ChannelID,
						Content:   fmt.Sprintf("⚠️ Scheduled task **%s** failed: %v", formatActionName(action), err),
					})
				}
				return
			}
			responseContent = resp.Text
			promptTokens = resp.PromptTokens
			completionTokens = resp.OutputTokens
		}
	} // end else (non-project_work path)

	// Save to session
	wh.sessMgr.AddMessage(sess.ID, "user", userPrompt, "scheduler")
	wh.sessMgr.AddMessage(sess.ID, "agent", responseContent, agentCfg.ID)

	// Send result to Discord
	// For project_work: suppress per-cycle Discord posts (post only run summary).
	// The status_report action (every 2h) sends the human-facing summary.
	// project_work still posts on errors/blockers.
	if wh.bot != nil && actionDef.ChannelID != "" {
		if action == "project_work" {
			// Only write run summary, don't post to Discord
			log.Printf("[WAKE] project_work completed (suppressed Discord post; will report on next status_report cycle)")
			// Clean JSON tool requests from the output before writing summary
			responseContent = cleanForDiscord(responseContent)
			wh.writeRunSummary(action, agentCfg.ID, responseContent, promptTokens, completionTokens)
		} else {
			// Send result to Discord (the bot adapter handles splitting long messages)
			content := fmt.Sprintf("🔔 **%s**\n\n%s", formatActionName(action), responseContent)
			wh.bot.Send(&discordbot.OutboundMessage{
				ChannelID: actionDef.ChannelID,
				Content:   content,
			})
			log.Printf("[WAKE] sent %s result to Discord channel %s", action, actionDef.ChannelID)

			// V35c: Write run summary to runs/ directory for dashboard visibility
			if action == "auto_patch" {
				// Clean JSON tool requests from the output before sending to Discord
				responseContent = cleanForDiscord(responseContent)
				wh.writeRunSummary(action, agentCfg.ID, responseContent, promptTokens, completionTokens)
			}
		}
	}

	// V32: After sending result, check for plans that need approval and notify Ema
	// Only check after improvement-related actions (auto_patch, review_improvements, daily_review)
	// to avoid unnecessary disk reads on every wake event.
	if wh.bot != nil && wh.planMgr != nil && (action == "auto_patch" || action == "review_improvements" || action == "daily_review") {
		wh.notifyPendingApprovals(actionDef.ChannelID)
	}

	log.Printf("[WAKE] completed action %q (tokens: prompt=%d, completion=%d)", action, promptTokens, completionTokens)
}

// handleDirectAction processes actions that don't need LLM inference.
// Currently only supports check_prs, which runs gh pr list directly.
func (wh *WakeHandler) handleDirectAction(action string, actionDef wakeAction, firedAt string) {
	var resultContent string

	switch action {
	case "check_prs":
		resultContent = wh.checkPRStatus()
	case "factory_status_digest":
		resultContent = wh.factoryStatusDigest()
	case "status_report":
		resultContent = wh.statusReport()
	case "mol_status":
		resultContent = wh.molStatusReport()
	default:
		log.Printf("[WAKE] WARN unknown direct action %q", action)
		return
	}

	if resultContent == "" {
		log.Printf("[WAKE] direct action %q produced no output", action)
		return
	}

	// Send result to Discord (the bot adapter handles splitting long messages)
	if wh.bot != nil && actionDef.ChannelID != "" {
		content := fmt.Sprintf("\U0001F514 **%s**\n\n%s", formatActionName(action), resultContent)
		wh.bot.Send(&discordbot.OutboundMessage{
			ChannelID: actionDef.ChannelID,
			Content:   content,
		})
	}
	log.Printf("[WAKE] completed direct action %q", action)
}

func (wh *WakeHandler) factoryStatusDigest() string {
	if wh.factoryMon == nil {
		return "Factory monitor is not enabled."
	}
	wh.factoryMon.PublishDigest()
	return ""
}

// statusReportRepoPath resolves the git repository the status report describes:
// the default project's repo_path when configured, otherwise the current working
// directory (the Prizm repo when serve runs from the repo root). Keeping this in
// config rather than hardcoded means the report works on any machine.
func (wh *WakeHandler) statusReportRepoPath() string {
	if wh.cfg != nil {
		if p := wh.cfg.DefaultProject(); p != nil && p.RepoPath != "" {
			return p.RepoPath
		}
	}
	return "."
}

// statusReport generates a 2-hour status report by reading recent run summaries,
// PROJECT_STATE.md, and git activity for the resolved repo. No LLM needed — pure
// file reading + formatting.
func (wh *WakeHandler) statusReport() string {
	repoPath := wh.statusReportRepoPath()

	var sb strings.Builder
	title := "Prizm"
	if wh.cfg != nil && wh.cfg.Prizm.InstanceID != "" {
		title = wh.cfg.Prizm.InstanceID
	}
	sb.WriteString(fmt.Sprintf("📊 **2-Hour Status Report — %s**\n\n", title))

	// Read PROJECT_STATE.md from the resolved repo (optional).
	statePath := filepath.Join(repoPath, "PROJECT_STATE.md")
	stateContent, err := os.ReadFile(statePath)
	if err != nil {
		sb.WriteString("_No PROJECT_STATE.md — skipping project state._\n\n")
	} else {
		sb.WriteString("### Project State\n")
		// Extract task lines (lines starting with - [ ] or - [x])
		// Stop at "Completed Work" section to avoid mentioning old tasks.
		lines := strings.Split(string(stateContent), "\n")
		done := 0
		pending := 0
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.Contains(trimmed, "Completed Work") {
				break // stop reading completed tasks
			}
			if strings.HasPrefix(trimmed, "- [x]") {
				done++
				sb.WriteString("✅ " + trimmed + "\n")
			} else if strings.HasPrefix(trimmed, "- [ ]") {
				pending++
			}
		}
		sb.WriteString(fmt.Sprintf("\n**Progress:** %d done, %d pending\n\n", done, pending))
	}

	// Read recent run summaries (last 2 hours) from the repo's runs/ dir.
	runsDir := filepath.Join(repoPath, "runs")
	cutoff := time.Now().Add(-2 * time.Hour)
	recentRuns := 0
	successRuns := 0
	failedRuns := 0

	if entries, err := os.ReadDir(runsDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			// Check if run is recent enough
			info, err := entry.Info()
			if err != nil {
				continue
			}
			if info.ModTime().Before(cutoff) {
				continue
			}
			if entry.Name() == "gated-loop" {
				continue // container for gated-loop runs, not a run itself
			}
			recentRuns++

			// Read summary.json
			summaryPath := filepath.Join(runsDir, entry.Name(), "summary.json")
			summaryData, err := os.ReadFile(summaryPath)
			if err != nil {
				continue
			}
			var summary map[string]any
			if err := json.Unmarshal(summaryData, &summary); err != nil {
				continue
			}
			status := fmt.Sprintf("%v", summary["status"])
			if status == "completed" {
				successRuns++
			} else {
				failedRuns++
			}
		}
	}

	sb.WriteString("### Recent Activity (last 2h)\n")
	sb.WriteString(fmt.Sprintf("- **Runs:** %d total, %d completed, %d failed/other\n", recentRuns, successRuns, failedRuns))

	// Show recent git commits (last 2 hours)
	reportRepo := repoPath
	cmd := exec.Command("git", "-C", reportRepo, "log", "--oneline", "--since=2 hours ago", "--all")
	if commitOut, err := cmd.Output(); err == nil {
		commitLines := strings.Split(strings.TrimSpace(string(commitOut)), "\n")
		if len(commitLines) > 0 && commitLines[0] != "" {
			sb.WriteString("\n### Recent Commits\n")
			for _, line := range commitLines {
				if line == "" {
					continue
				}
				sb.WriteString("• `" + line + "`\n")
			}
		}
	}
	// Show current branch
	branchCmd := exec.Command("git", "-C", reportRepo, "branch", "--show-current")
	if branchOut, err := branchCmd.Output(); err == nil {
		branch := strings.TrimSpace(string(branchOut))
		if branch != "" {
			sb.WriteString(fmt.Sprintf("\n**Branch:** %s\n", branch))
		}
	}
	// Show working tree status
	statusCmd := exec.Command("git", "-C", reportRepo, "status", "--short")
	if statusOut, err := statusCmd.Output(); err == nil {
		statusLines := strings.Split(strings.TrimSpace(string(statusOut)), "\n")
		clean := len(statusLines) == 1 && statusLines[0] == ""
		if clean {
			sb.WriteString("**Working tree:** clean ✅\n")
		} else {
			sb.WriteString(fmt.Sprintf("**Working tree:** %d uncommitted changes\n", len(statusLines)))
		}
	}

	// List run summaries
	if entries, err := os.ReadDir(runsDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			info, err := entry.Info()
			if err != nil || info.ModTime().Before(cutoff) {
				continue
			}
			summaryPath := filepath.Join(runsDir, entry.Name(), "summary.json")
			summaryData, err := os.ReadFile(summaryPath)
			if err != nil {
				continue
			}
			var summary map[string]any
			if err := json.Unmarshal(summaryData, &summary); err != nil {
				continue
			}
			action := fmt.Sprintf("%v", summary["task"])
			status := fmt.Sprintf("%v", summary["status"])
			output := fmt.Sprintf("%v", summary["output"])
			if len(output) > 150 {
				output = output[:150] + "..."
			}
			emoji := "✅"
			if status != "completed" {
				emoji = "❌"
			}
			sb.WriteString(fmt.Sprintf("%s **%s** — %s\n", emoji, action, output))
		}
	}

	if recentRuns == 0 {
		sb.WriteString("\n*No runs in the last 2 hours.*\n")
	}

	return sb.String()
}

// checkPRStatus runs gh pr list and formats the results.
func (wh *WakeHandler) checkPRStatus() string {
	ctx, cancel := stdcontext.WithTimeout(stdcontext.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "gh", "pr", "list",
		"--json", "number,title,author,updatedAt,reviewDecision,statusCheckRollup,labels",
		"--state", "open",
		"--limit", "20",
	)
	// Run in the Prizm repo directory so gh pr list checks Prizm's PRs specifically
	cmd.Dir = "/Users/ema/projects/repos/prizm"

	output, err := cmd.Output()
	if err != nil {
		// gh might not be installed or no PRs exist
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			// No open PRs — return empty so handleDirectAction skips sending
			return ""
		}
		return fmt.Sprintf("Could not check PRs: %v", err)
	}

	if len(strings.TrimSpace(string(output))) == 0 {
		// No open PRs — return empty so handleDirectAction skips sending
		return ""
	}

	return wh.formatPRList(string(output))
}

// formatPRList formats gh pr list JSON output into a readable Discord message.
func (wh *WakeHandler) formatPRList(jsonOutput string) string {
	type PRAuthor struct {
		Login string `json:"login"`
	}
	type PRLabel struct {
		Name string `json:"name"`
	}
	type PRStatusCheck struct {
		State string `json:"state"`
		Name  string `json:"name"`
	}
	type PR struct {
		Number            int             `json:"number"`
		Title             string          `json:"title"`
		Author            PRAuthor        `json:"author"`
		UpdatedAt         string          `json:"updatedAt"`
		ReviewDecision    string          `json:"reviewDecision"`
		StatusCheckRollup []PRStatusCheck `json:"statusCheckRollup"`
		Labels            []PRLabel       `json:"labels"`
	}

	var prs []PR
	if err := json.Unmarshal([]byte(jsonOutput), &prs); err != nil {
		// If parsing fails, return the raw output
		return "PR Status:\n" + jsonOutput
	}

	if len(prs) == 0 {
		// No open PRs — return empty so handleDirectAction skips sending
		return ""
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("PR Status Report — %d open PRs\n\n", len(prs)))

	for _, pr := range prs {
		// Format review decision
		review := pr.ReviewDecision
		if review == "" {
			review = "PENDING"
		}
		switch review {
		case "APPROVED":
			review = "\u2705 Approved"
		case "CHANGES_REQUESTED":
			review = "\u26a0\ufe0f Changes Requested"
		case "REVIEW_REQUIRED":
			review = "\U0001f50d Review Required"
		}

		// Format CI status
		ciStatus := "\u2753 Unknown"
		if len(pr.StatusCheckRollup) > 0 {
			allPass := true
			for _, check := range pr.StatusCheckRollup {
				if check.State != "success" {
					allPass = false
					break
				}
			}
			if allPass {
				ciStatus = "\u2705 All passing"
			} else {
				ciStatus = "\u274c Failing"
			}
		}

		// Format labels
		var labelStr string
		if len(pr.Labels) > 0 {
			labels := make([]string, len(pr.Labels))
			for i, l := range pr.Labels {
				labels[i] = l.Name
			}
			labelStr = " [" + strings.Join(labels, ", ") + "]"
		}

		sb.WriteString(fmt.Sprintf("**#%d** %s\n", pr.Number, pr.Title))
		sb.WriteString(fmt.Sprintf("   by @%s • %s • CI: %s%s\n\n", pr.Author.Login, review, ciStatus, labelStr))
	}

	return sb.String()
}
func formatActionName(action string) string {
	parts := strings.Split(action, "_")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}

// notifyPendingApprovals checks for plans that need Ema's approval and sends a Discord notification.
func (wh *WakeHandler) notifyPendingApprovals(channelID string) {
	plans, err := wh.planMgr.LoadPlans()
	if err != nil {
		log.Printf("[WAKE] WARN could not load plans for approval check: %v", err)
		return
	}

	var pendingApproval []*plan.Plan
	for i := range plans {
		if plans[i].Status == plan.StatusPendingApproval {
			pendingApproval = append(pendingApproval, &plans[i])
		}
	}

	if len(pendingApproval) == 0 {
		return
	}

	var msg strings.Builder
	msg.WriteString("\u26a0\ufe0f **Plans Needing Approval**\n\n")
	for _, p := range pendingApproval {
		msg.WriteString(fmt.Sprintf("**%s** (%s)\n", p.Title, p.ID))
		msg.WriteString(fmt.Sprintf("Approval level: %s\n", p.ApprovalLevel))
		if p.Description != "" {
			msg.WriteString(fmt.Sprintf("Description: %s\n", p.Description))
		}
		if p.Reasoning != "" {
			msg.WriteString(fmt.Sprintf("Reasoning: %s\n", p.Reasoning))
		}
		msg.WriteString(fmt.Sprintf("Created: %s\n\n", p.CreatedAt.Format("Jan 02 15:04")))
	}
	msg.WriteString("Reply with `approve P-XXX` or `reject P-XXX` to act on these plans.")

	if wh.bot != nil {
		wh.bot.Send(&discordbot.OutboundMessage{
			ChannelID: channelID,
			Content:   msg.String(),
		})
	}
	log.Printf("[WAKE] notified %d pending approval plans", len(pendingApproval))
}

// cleanForDiscord strips embedded JSON tool_request objects from LLM output
// before posting to Discord. The LLM sometimes generates natural language
// with embedded {"type":"tool_request",...} JSON — this removes that JSON
// so Discord readers see clean text.
func cleanForDiscord(text string) string {
	// Remove any {"type":"tool_request"...} blocks
	for {
		idx := strings.Index(text, `{"type":"tool_request"`)
		if idx == -1 {
			idx = strings.Index(text, `{"type": "tool_request"`)
			if idx == -1 {
				break
			}
		}
		// Find the matching closing brace
		depth := 0
		end := -1
		for i := idx; i < len(text); i++ {
			if text[i] == '{' {
				depth++
			} else if text[i] == '}' {
				depth--
				if depth == 0 {
					end = i
					break
				}
			}
		}
		if end == -1 {
			break
		}
		// Remove the JSON block and any trailing space
		text = text[:idx] + text[end+1:]
		text = strings.TrimSpace(text)
	}
	return strings.TrimSpace(text)
}

// postToDiscord posts a message to the manager-room channel.
//
//lint:ignore U1000 retained for manager-room notification integration
func (wh *WakeHandler) postToDiscord(message string) {
	if wh.bot == nil {
		log.Printf("[V2-NATURAL-GATES] no bot, cannot post to Discord")
		return
	}
	channelID := "1491622863118008431" // scheduled-reports destination
	wh.bot.Send(&discordbot.OutboundMessage{
		ChannelID: channelID,
		Content:   message,
	})
}

// writeRunSummary writes a run summary to the runs/ directory so the V11 dashboard
// can display wake handler actions. Creates a run directory with summary.json,
// events.jsonl, and projections/run_status.json.
func (wh *WakeHandler) writeRunSummary(action, agent, content string, promptTokens, completionTokens int) {
	runDir := wh.cfg.Prizm.Workspace
	if runDir == "" {
		runDir = "."
	}
	runsDir := filepath.Join(runDir, "runs")

	// Generate run ID from timestamp
	runID := fmt.Sprintf("wake_%s_%d", action, time.Now().Unix())
	runPath := filepath.Join(runsDir, runID)

	// Create directory structure
	if err := os.MkdirAll(filepath.Join(runPath, "projections"), 0755); err != nil {
		log.Printf("[WAKE] failed to create run dir: %v", err)
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// Write summary.json
	summary := map[string]any{
		"run_id":            runID,
		"status":            "completed",
		"agent":             agent,
		"project":           "bassbook",
		"task":              fmt.Sprintf("Scheduled action: %s", action),
		"started_at":        now,
		"completed_at":      now,
		"event_count":       1,
		"output":            content,
		"prompt_tokens":     promptTokens,
		"completion_tokens": completionTokens,
	}
	summaryData, _ := json.MarshalIndent(summary, "", "  ")
	if err := os.WriteFile(filepath.Join(runPath, "summary.json"), summaryData, 0644); err != nil {
		log.Printf("[WAKE] failed to write summary.json: %v", err)
		return
	}

	// Write projections/run_status.json
	projData := map[string]any{
		"agent":        agent,
		"completed_at": now,
		"started_at":   now,
		"status":       "completed",
		"project":      "bassbook",
		"task":         fmt.Sprintf("Scheduled action: %s", action),
		"error":        "",
		"duration_ms":  0,
	}
	projJSON, _ := json.MarshalIndent(projData, "", "  ")
	os.WriteFile(filepath.Join(runPath, "projections", "run_status.json"), projJSON, 0644)

	// Write events.jsonl with a single event
	event := map[string]any{
		"id":        fmt.Sprintf("evt_%d", time.Now().UnixNano()),
		"type":      "prizm.wake.completed",
		"source":    "wake-handler",
		"timestamp": now,
		"payload": map[string]any{
			"action":            action,
			"agent":             agent,
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"output_length":     len(content),
		},
		"metadata": map[string]any{
			"run_id": runID,
		},
	}
	eventJSON, _ := json.Marshal(event)
	eventJSON = append(eventJSON, '\n')
	os.WriteFile(filepath.Join(runPath, "events.jsonl"), eventJSON, 0644)

	// Write output.md
	os.WriteFile(filepath.Join(runPath, "output.md"), []byte(content), 0644)

	log.Printf("[WAKE] wrote run summary to %s", runPath)
}

// runToolLoopWake runs a text-based tool calling loop for project_work actions.
// The LLM responds with JSON: {"type": "tool_request", "tool": "...", "input": {...}}
// or {"type": "final", "content": "..."}.
// We execute tool requests and feed results back until we get a final response.
// runToolLoopWake V36 — Phased autonomous workflow with code-level enforcement.
// Replaces the flat 20-iteration loop with 7 strict phases, branch protection,
// commit-push enforcement, task assignment, and self-review.

func (wh *WakeHandler) runToolLoopWake(ctx stdcontext.Context, systemPrompt, userPrompt, model string, agentCfg *orchestrator.AgentConfig, exec *tool.Executor) (string, int, int) {
	const maxTotalIterations = 200

	// Build tool prompt suffix
	toolInfos := wh.toolReg.ListWithDescriptions()
	workspaceRoot := wh.cfg.Prizm.Workspace
	if workspaceRoot == "" {
		workspaceRoot = "."
	}
	toolSuffix := agent.BuildToolPromptSuffix(toolInfos, workspaceRoot, wh.defaultRepoPath())
	fullSystemPrompt := systemPrompt + toolSuffix

	// Build initial messages
	messages := []provider.ChatMessage{
		{Role: "system", Content: fullSystemPrompt},
		{Role: "user", Content: userPrompt},
	}

	totalPromptTokens := 0
	totalCompletionTokens := 0

	chatProv, chatErr := wh.providers.GetChatProviderForAgent(agentCfg.ID, model)

	// V36: Run state tracking
	type RunState struct {
		filesWritten    bool
		filesStaged     bool
		committed       bool
		pushed          bool
		readsInPhase    int
		proseCount      int
		branchName      string
		assignedTask    string
		lastToolHashes  []string
		lastRespHashes  []string
		orientFoundWork bool // V37: ORIENT phase must find actual work before BRANCH is allowed
	}
	state := RunState{}

	// V36: Check current branch at start
	state.branchName = wh.getCurrentBranch()

	// V36: Parse PROJECT_STATE.md to assign a task
	state.assignedTask = wh.parseProjectStateTask()

	// V37: If a task was assigned from PROJECT_STATE.md, ORIENT has work to do
	if state.assignedTask != "" {
		state.orientFoundWork = true
		log.Printf("[WAKE-TOOL] task assigned from PROJECT_STATE.md — ORIENT has work")
	}

	// V36: Inject task assignment into messages if found
	if state.assignedTask != "" {
		messages = append(messages, provider.ChatMessage{
			Role:    "system",
			Content: fmt.Sprintf("## SYSTEM-ASSIGNED TASK (do not deviate)\n%s\n\nThis is the ONLY task you should work on this cycle. Do not pick any other task.", state.assignedTask),
		})
	}

	// V36: Inject previous cycle state if available
	prevState := wh.readPreviousWorkState()
	if prevState != "" {
		messages = append(messages, provider.ChatMessage{
			Role:    "system",
			Content: fmt.Sprintf("## PREVIOUS CYCLE\n%s", prevState),
		})
	}

	// V36: Phase definitions
	type Phase struct {
		Name          string
		MaxIterations int
		AllowedTools  map[string]bool // empty = all allowed
		Description   string
	}
	phases := []Phase{
		{Name: "ORIENT", MaxIterations: 10, AllowedTools: map[string]bool{"read_file": true, "git_status": true, "git_log": true, "git_branch_list": true, "project_overview": true, "list_dir": true}, Description: "Read PROJECT_STATE.md and check git state. The system has already assigned your task — confirm it."},
		{Name: "BRANCH", MaxIterations: 10, AllowedTools: map[string]bool{"git_branch_list": true, "git_status": true, "read_file": true, "git_checkout": true}, Description: "If on main/master, you MUST create a feature branch (feature/bb-{task-slug}). The system blocks all writes on main. If already on a feature branch, proceed."},
		{Name: "IMPLEMENT", MaxIterations: 80, AllowedTools: map[string]bool{"read_file": true, "write_file": true, "create_directory": true, "list_dir": true, "search_files": true, "git_status": true, "git_diff": true}, Description: "Write code for your assigned task. After 3 read_file calls, the system forces you to mutate files or directories. You MUST produce at least one write_file or create_directory."},
		{Name: "SELF_REVIEW", MaxIterations: 20, AllowedTools: map[string]bool{"read_file": true, "write_file": true, "create_directory": true, "git_diff": true, "git_status": true}, Description: "Review your changes via git_diff. Fix obvious errors. Respond REVIEW_PASSED when clean."},
		{Name: "COMMIT_PUSH", MaxIterations: 10, AllowedTools: map[string]bool{"git_add": true, "git_commit": true, "git_push": true, "create_pr": true, "git_status": true}, Description: "Stage, commit, and push your changes. All three must complete."},
		{Name: "UPDATE_STATE", MaxIterations: 10, AllowedTools: map[string]bool{"write_file": true, "read_file": true}, Description: "Update PROJECT_STATE.md to mark your task complete with [x]."},
		{Name: "REPORT", MaxIterations: 5, AllowedTools: map[string]bool{}, Description: "Emit final summary. No tools allowed."},
	}

	phaseIdx := 0
	iterInPhase := 0
	tokenCeiling := wh.wakeToolTokenCeiling()

	for totalIter := 0; totalIter < maxTotalIterations; totalIter++ {
		// Bound token spend like the gated loop. This V36 tool loop was previously
		// capped only by iteration count; use the default run ceiling so a stuck or
		// expensive loop can't burn tokens without limit.
		if used := totalPromptTokens + totalCompletionTokens; wakeTokenBudgetExceeded(used, tokenCeiling) {
			log.Printf("[WAKE-TOOL] token budget exhausted (%d/%d) - stopping", used, tokenCeiling)
			return fmt.Sprintf("Project work stopped: token budget exhausted (%d/%d tokens)", used, tokenCeiling), totalPromptTokens, totalCompletionTokens
		}
		phase := phases[phaseIdx]
		log.Printf("[WAKE-TOOL] phase=%s iteration %d/%d (total %d/%d)", phase.Name, iterInPhase+1, phase.MaxIterations, totalIter+1, maxTotalIterations)

		// V36: Phase transition enforcement
		if iterInPhase >= phase.MaxIterations {
			phaseIdx++
			if phaseIdx >= len(phases) {
				log.Printf("[WAKE-TOOL] all phases complete, forcing final")
				break
			}
			iterInPhase = 0
			state.readsInPhase = 0
			nextPhase := phases[phaseIdx]
			messages = append(messages, provider.ChatMessage{
				Role:    "system",
				Content: fmt.Sprintf("PHASE %s COMPLETE. Moving to %s phase. You MUST now: %s", phase.Name, nextPhase.Name, nextPhase.Description),
			})
			log.Printf("[WAKE-TOOL] phase transition: %s → %s", phase.Name, nextPhase.Name)
			continue
		}

		// V37: Branch gate — when transitioning from ORIENT to BRANCH, check if ORIENT found work.
		// If no task was assigned AND ORIENT didn't find issues, skip BRANCH and go straight to REPORT.
		if phase.Name == "ORIENT" && phaseIdx+1 < len(phases) {
			if state.assignedTask == "" && !state.orientFoundWork {
				log.Printf("[WAKE-TOOL] ORIENT found no work and no task assigned — skipping to REPORT")
				// Skip to REPORT phase (last phase)
				phaseIdx = len(phases) - 1
				iterInPhase = 0
				state.readsInPhase = 0
				messages = append(messages, provider.ChatMessage{
					Role:    "system",
					Content: "No issues found and no task assigned. Skip to REPORT and emit final summary. No branch creation needed.",
				})
				continue
			}
		}

		// V36: Phase-specific nudges
		if phase.Name == "IMPLEMENT" && state.readsInPhase >= 3 && !state.filesWritten {
			messages = append(messages, provider.ChatMessage{
				Role:    "system",
				Content: "You have read enough files in IMPLEMENT phase. You MUST use write_file or create_directory now. Further read_file calls will be denied.",
			})
		}

		// V36: Self-review phase — auto-inject git diff
		if phase.Name == "SELF_REVIEW" && iterInPhase == 0 {
			diffOutput := wh.getGitDiff()
			if diffOutput != "" {
				messages = append(messages, provider.ChatMessage{
					Role:    "system",
					Content: fmt.Sprintf("## Your Changes (git diff)\n```diff\n%s\n```\n\nReview these changes for:\n1. Syntax errors or typos\n2. Missing imports or references\n3. Incomplete logic\n4. Does this complete the assigned task?\n\nIf issues found, fix with write_file. If clean, respond with REVIEW_PASSED.", diffOutput),
				})
			} else {
				// No changes — skip self-review
				phaseIdx++
				iterInPhase = 0
				continue
			}
		}

		// V36: Report phase — no tools allowed
		if phase.Name == "REPORT" {
			messages = append(messages, provider.ChatMessage{
				Role:    "system",
				Content: "You are in REPORT phase. Emit your final summary now as {\"type\":\"final\",\"content\":\"...\"}. No tool calls.",
			})
		}

		var responseText string

		if chatErr == nil {
			resp, err := chatProv.ChatGenerate(ctx, provider.ChatGenerateRequest{
				RunID:       fmt.Sprintf("wake-project_work-%d", time.Now().Unix()),
				Agent:       agentCfg.ID,
				Model:       model,
				Messages:    messages,
				Temperature: 0.7,
				MaxTokens:   4096,
			})
			if err != nil {
				log.Printf("[WAKE-TOOL] ERROR LLM call failed: %v", err)
				return fmt.Sprintf("Project work failed: %v", err), totalPromptTokens, totalCompletionTokens
			}
			responseText = resp.Content
			pt, ct := resp.PromptTokens, resp.OutputTokens
			if pt == 0 && ct == 0 && responseText != "" {
				pt, ct = estimateWakeChatPromptTokens(messages), estimateWakeTokens(responseText)
				log.Printf("[WAKE-TOOL] provider reported no token usage; estimated %d prompt + %d completion", pt, ct)
			}
			totalPromptTokens += pt
			totalCompletionTokens += ct
			if used := totalPromptTokens + totalCompletionTokens; wakeTokenBudgetExceeded(used, tokenCeiling) {
				log.Printf("[WAKE-TOOL] token budget exhausted (%d/%d) after model call", used, tokenCeiling)
				return fmt.Sprintf("Project work stopped: token budget exhausted (%d/%d tokens)", used, tokenCeiling), totalPromptTokens, totalCompletionTokens
			}
		} else {
			// Fall back to text provider
			prov, provErr := wh.providers.GetForAgent(agentCfg.ID, model)
			if provErr != nil {
				return fmt.Sprintf("Project work failed: no provider for %q", model), totalPromptTokens, totalCompletionTokens
			}
			flatPrompt := fullSystemPrompt + "\n\n"
			for _, m := range messages {
				if m.Role == "user" {
					flatPrompt += m.Content + "\n\n"
				} else if m.Role == "assistant" {
					flatPrompt += "[Assistant]: " + m.Content + "\n\n"
				}
			}
			resp, err := prov.Generate(ctx, provider.GenerateRequest{
				RunID:       fmt.Sprintf("wake-project_work-%d-iter%d", time.Now().Unix(), totalIter),
				Agent:       agentCfg.ID,
				Model:       model,
				Prompt:      flatPrompt,
				Temperature: 0.7,
				MaxTokens:   4096,
			})
			if err != nil {
				return fmt.Sprintf("Project work failed at iteration %d: %v", totalIter+1, err), totalPromptTokens, totalCompletionTokens
			}
			responseText = resp.Text
			pt, ct := resp.PromptTokens, resp.OutputTokens
			if pt == 0 && ct == 0 && responseText != "" {
				pt, ct = estimateWakeTokens(flatPrompt), estimateWakeTokens(responseText)
				log.Printf("[WAKE-TOOL] provider reported no token usage; estimated %d prompt + %d completion", pt, ct)
			}
			totalPromptTokens += pt
			totalCompletionTokens += ct
			if used := totalPromptTokens + totalCompletionTokens; wakeTokenBudgetExceeded(used, tokenCeiling) {
				log.Printf("[WAKE-TOOL] token budget exhausted (%d/%d) after model call", used, tokenCeiling)
				return fmt.Sprintf("Project work stopped: token budget exhausted (%d/%d tokens)", used, tokenCeiling), totalPromptTokens, totalCompletionTokens
			}
		}

		// V36: Prose detection
		trimmed := strings.TrimSpace(responseText)
		if len(trimmed) > 100 && (trimmed[0] != '{') {
			state.proseCount++
			log.Printf("[WAKE-TOOL] prose-before-JSON detected (count: %d)", state.proseCount)
			if state.proseCount >= 3 {
				messages = append(messages, provider.ChatMessage{
					Role:    "system",
					Content: "STOP writing prose before JSON. Your last 3 responses wasted iterations on text that was discarded. PURE JSON only: start with { and end with }.",
				})
			}
		}

		// V36: Stuck detection
		respHash := simpleHash(responseText)
		state.lastRespHashes = append(state.lastRespHashes, respHash)
		if len(state.lastRespHashes) >= 3 {
			if state.lastRespHashes[len(state.lastRespHashes)-1] == state.lastRespHashes[len(state.lastRespHashes)-2] &&
				state.lastRespHashes[len(state.lastRespHashes)-1] == state.lastRespHashes[len(state.lastRespHashes)-3] {
				log.Printf("[WAKE-TOOL] STUCK: 3 identical responses detected")
				messages = append(messages, provider.ChatMessage{
					Role:    "system",
					Content: "YOU ARE STUCK. You have produced the same response 3 times. Try a completely different approach or move to the next phase.",
				})
				state.lastRespHashes = nil // reset
			}
		}

		// Parse the response
		parsed := agent.ParseAgentOutputWithFallback(responseText)

		if parsed.Type == agent.ResponseFinal {
			// V36: Commit-Push Enforcement
			if state.filesWritten && !state.committed {
				log.Printf("[WAKE-TOOL] FINAL REJECTED: files written but not committed")
				messages = append(messages, provider.ChatMessage{
					Role:    "system",
					Content: "COMMIT REQUIRED: You wrote files but have not committed. You MUST use git_add then git_commit before finishing. Your final response is rejected.",
				})
				continue
			}
			if state.committed && !state.pushed {
				log.Printf("[WAKE-TOOL] FINAL REJECTED: committed but not pushed")
				messages = append(messages, provider.ChatMessage{
					Role:    "system",
					Content: "PUSH REQUIRED: You committed but have not pushed. You MUST use git_push before finishing. Your final response is rejected.",
				})
				continue
			}

			log.Printf("[WAKE-TOOL] final response accepted at phase %s", phase.Name)

			// V36: Write work state for next cycle
			wh.writeWorkState(state.branchName, state.assignedTask, state.committed, state.pushed)

			return parsed.Content, totalPromptTokens, totalCompletionTokens
		}

		if parsed.Type == agent.ResponseToolRequest {
			log.Printf("[WAKE-TOOL] tool request: %s (phase: %s)", parsed.ToolName, phase.Name)

			// V36: Phase tool enforcement — check if tool is allowed in current phase
			if len(phase.AllowedTools) > 0 && !phase.AllowedTools[parsed.ToolName] {
				log.Printf("[WAKE-TOOL] tool %s denied in phase %s", parsed.ToolName, phase.Name)
				messages = append(messages, provider.ChatMessage{Role: "assistant", Content: responseText})
				messages = append(messages, provider.ChatMessage{
					Role:    "user",
					Content: fmt.Sprintf("Tool %q is not allowed in the %s phase. You should be: %s", parsed.ToolName, phase.Name, phase.Description),
				})
				iterInPhase++
				continue
			}

			// V36: Read budget in IMPLEMENT phase
			if phase.Name == "IMPLEMENT" && (parsed.ToolName == "read_file" || parsed.ToolName == "list_dir" || parsed.ToolName == "search_files") {
				state.readsInPhase++
				if state.readsInPhase > 3 && !state.filesWritten {
					log.Printf("[WAKE-TOOL] read budget exceeded in IMPLEMENT, denying %s", parsed.ToolName)
					messages = append(messages, provider.ChatMessage{Role: "assistant", Content: responseText})
					messages = append(messages, provider.ChatMessage{
						Role:    "user",
						Content: "READ BUDGET EXCEEDED. You have read enough. Use write_file or create_directory to make your changes now.",
					})
					continue
				}
			}

			// V36: Branch Protection Gate
			writeTools := map[string]bool{"write_file": true, "create_directory": true, "git_add": true, "git_commit": true, "git_push": true}
			if writeTools[parsed.ToolName] {
				branch := wh.getCurrentBranch()
				if branch == "main" || branch == "master" {
					log.Printf("[WAKE-TOOL] BRANCH PROTECTION: %s denied on %s", parsed.ToolName, branch)
					messages = append(messages, provider.ChatMessage{Role: "assistant", Content: responseText})
					messages = append(messages, provider.ChatMessage{
						Role:    "user",
						Content: fmt.Sprintf("BRANCH PROTECTION: You are on '%s'. Mutations are blocked. Create a feature branch first. The system cannot proceed until you are on a feature branch.", branch),
					})
					iterInPhase++
					continue
				}
			}

			// Execute the tool
			result, err := exec.ExecuteWithPolicy(ctx, parsed.ToolName, agentCfg.ID, "bassbook", fmt.Sprintf("wake-project_work-%d", time.Now().Unix()), parsed.ToolInput)
			if err != nil {
				log.Printf("[WAKE-TOOL] tool %s failed: %v", parsed.ToolName, err)
				// V36: Edge case fix (Mango review) — if git_commit fails because nothing to commit,
				// treat as committed (files were already committed or no changes needed)
				if parsed.ToolName == "git_commit" && (strings.Contains(err.Error(), "nothing to commit") || strings.Contains(err.Error(), "no changes")) {
					state.committed = true
					log.Printf("[WAKE-TOOL] git_commit 'nothing to commit' — treating as committed")
					messages = append(messages, provider.ChatMessage{Role: "assistant", Content: responseText})
					messages = append(messages, provider.ChatMessage{
						Role:    "user",
						Content: "Commit succeeded (nothing to commit — changes were already staged). Proceed to git_push.",
					})
					continue
				}
				messages = append(messages, provider.ChatMessage{Role: "assistant", Content: responseText})
				messages = append(messages, provider.ChatMessage{
					Role:    "user",
					Content: fmt.Sprintf("Tool %q failed: %v. Try a different approach.", parsed.ToolName, err),
				})
				continue
			}

			// Format tool result
			resultStr := fmt.Sprintf("Tool %q result:\n%v", parsed.ToolName, result.Output)
			if result.Error != "" {
				resultStr = fmt.Sprintf("Tool %q error: %s", parsed.ToolName, result.Error)
			}
			log.Printf("[WAKE-TOOL] tool %s succeeded: %s", parsed.ToolName, truncateStr(resultStr, 100))

			// V36: Update run state
			switch parsed.ToolName {
			case "write_file", "create_directory":
				state.filesWritten = true
			case "git_add":
				state.filesStaged = true
			case "git_commit":
				state.committed = true
			case "git_push":
				state.pushed = true
			case "git_branch_list", "git_status":
				// re-check branch after git operations
				newBranch := wh.getCurrentBranch()
				if newBranch != state.branchName {
					state.branchName = newBranch
					log.Printf("[WAKE-TOOL] branch changed to %s", newBranch)
				}
				// V37: If git_status shows uncommitted changes, ORIENT found work
				if phase.Name == "ORIENT" && (strings.Contains(resultStr, "modified") || strings.Contains(resultStr, "untracked") || strings.Contains(resultStr, "Changes") || strings.Contains(resultStr, "ahead")) {
					state.orientFoundWork = true
					log.Printf("[WAKE-TOOL] ORIENT found work via git_status")
				}
			case "read_file":
				// V37: If read_file in ORIENT reads PROJECT_STATE.md and finds incomplete tasks
				if phase.Name == "ORIENT" && strings.Contains(resultStr, "[ ]") {
					state.orientFoundWork = true
					log.Printf("[WAKE-TOOL] ORIENT found incomplete tasks in PROJECT_STATE.md")
				}
			}

			// V36: Tool result summarization (truncate large outputs)
			resultStr = summarizeToolResult(parsed.ToolName, resultStr)

			// V36: Stuck detection for tool calls
			toolHash := simpleHash(parsed.ToolName + fmt.Sprintf("%v", parsed.ToolInput))
			state.lastToolHashes = append(state.lastToolHashes, toolHash)
			if len(state.lastToolHashes) >= 3 {
				if state.lastToolHashes[len(state.lastToolHashes)-1] == state.lastToolHashes[len(state.lastToolHashes)-2] &&
					state.lastToolHashes[len(state.lastToolHashes)-1] == state.lastToolHashes[len(state.lastToolHashes)-3] {
					log.Printf("[WAKE-TOOL] STUCK: 3 identical tool calls detected")
					messages = append(messages, provider.ChatMessage{
						Role:    "system",
						Content: fmt.Sprintf("YOU ARE STUCK. You have called %s with the same input 3 times. Try a different approach.", parsed.ToolName),
					})
					state.lastToolHashes = nil
				}
			}

			messages = append(messages, provider.ChatMessage{Role: "assistant", Content: responseText})
			messages = append(messages, provider.ChatMessage{
				Role:    "user",
				Content: resultStr,
			})
			iterInPhase++
			continue
		}

		// Unknown response type — treat as final
		return responseText, totalPromptTokens, totalCompletionTokens
	}

	// V36: Write work state even on incomplete runs
	wh.writeWorkState(state.branchName, state.assignedTask, state.committed, state.pushed)

	log.Printf("[WAKE-TOOL] max iterations reached. State: written=%v staged=%v committed=%v pushed=%v", state.filesWritten, state.filesStaged, state.committed, state.pushed)
	return "Project work cycle reached max iterations. See logs for details.", totalPromptTokens, totalCompletionTokens
}

// getCurrentBranch returns the current git branch in the BassBook repo.
// defaultRepoPath resolves the working repo from the default project, falling
// back to the workspace root. Replaces the hardcoded BassBook path.
func (wh *WakeHandler) defaultRepoPath() string {
	if p := wh.cfg.DefaultProject(); p != nil && p.RepoPath != "" {
		return p.RepoPath
	}
	if wh.cfg.Prizm.Workspace != "" {
		return wh.cfg.Prizm.Workspace
	}
	return "."
}

// projectStatePath resolves a project's state file to an absolute path.
func (wh *WakeHandler) projectStatePath(p *orchestrator.ProjectConfig) string {
	if p == nil || p.StateFile == "" {
		return ""
	}
	if filepath.IsAbs(p.StateFile) {
		return p.StateFile
	}
	return filepath.Join(p.RepoPath, p.StateFile)
}

func (wh *WakeHandler) getCurrentBranch() string {
	return wh.getCurrentBranchIn(wh.defaultRepoPath())
}

// getCurrentBranchIn returns the current git branch for the given repo.
func (wh *WakeHandler) getCurrentBranchIn(repoPath string) string {
	if repoPath == "" {
		repoPath = "."
	}
	return strings.TrimSpace(exec_command("git", []string{"branch", "--show-current"}, repoPath))
}

// parseProjectStateTask reads the default project's state file and returns the first incomplete task.
func (wh *WakeHandler) parseProjectStateTask() string {
	return wh.parseProjectStateTaskFrom(wh.projectStatePath(wh.cfg.DefaultProject()))
}

// parseProjectStateTaskFrom reads a project state file and returns the first incomplete task.
func (wh *WakeHandler) parseProjectStateTaskFrom(statePath string) string {
	if statePath == "" {
		return ""
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		log.Printf("[WAKE-TOOL] could not read state file %s: %v", statePath, err)
		return ""
	}
	lines := strings.Split(string(data), "\n")
	var tasks []string
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Look for "- [ ]" markers (incomplete tasks)
		if strings.HasPrefix(trimmed, "- [ ]") {
			// Grab this line plus any following description lines until next task or blank
			taskText := trimmed
			for j := i + 1; j < len(lines) && j < i+3; j++ {
				next := strings.TrimSpace(lines[j])
				if next == "" || strings.HasPrefix(next, "- [") || strings.HasPrefix(next, "###") {
					break
				}
				if !strings.HasPrefix(next, "-") && !strings.HasPrefix(next, "#") {
					taskText += " " + next
				}
			}
			tasks = append(tasks, taskText)
		}
		// Also look for "### Task N:" without ✅
		if strings.HasPrefix(trimmed, "### Task ") && !strings.Contains(trimmed, "✅") {
			tasks = append(tasks, trimmed)
		}
	}
	if len(tasks) == 0 {
		return ""
	}
	return tasks[0]
}

// workStatePath returns the path to the cross-cycle work state file under the workspace.
func (wh *WakeHandler) workStatePath() string {
	root := wh.cfg.Prizm.Workspace
	if root == "" {
		root = "."
	}
	return filepath.Join(root, "runs", "current_work_state.json")
}

// readPreviousWorkState reads the cross-cycle work state for continuity.
func (wh *WakeHandler) readPreviousWorkState() string {
	data, err := os.ReadFile(wh.workStatePath())
	if err != nil {
		return ""
	}
	return fmt.Sprintf("Last cycle state:\n%s", string(data))
}

// writeWorkState writes the current cycle state for the next cycle to read.
func (wh *WakeHandler) writeWorkState(branch, task string, committed, pushed bool) {
	state := fmt.Sprintf(`{
  "last_branch": %q,
  "last_task": %q,
  "committed": %t,
  "pushed": %t,
  "completed_at": %q
}`, branch, task, committed, pushed, time.Now().Format(time.RFC3339))
	path := wh.workStatePath()
	os.MkdirAll(filepath.Dir(path), 0755)
	os.WriteFile(path, []byte(state), 0644)
}

// getGitDiff returns the git diff for the default project's repo.
func (wh *WakeHandler) getGitDiff() string {
	return wh.getGitDiffIn(wh.defaultRepoPath())
}

// getGitDiffIn returns the git diff --stat for the given repo.
func (wh *WakeHandler) getGitDiffIn(repoPath string) string {
	if repoPath == "" {
		repoPath = "."
	}
	return exec_command("git", []string{"diff", "--stat"}, repoPath)
}

// summarizeToolResult truncates large tool results before feeding back to the LLM.
func summarizeToolResult(toolName, result string) string {
	const maxLen = 3000
	if len(result) <= maxLen {
		return result
	}
	// Keep first 1500 + last 500 with truncation marker
	first := result[:1500]
	last := result[len(result)-500:]
	return first + fmt.Sprintf("\n... [truncated: %d total chars] ...\n", len(result)) + last
}

// exec_command runs a command and returns its stdout as a string.
func exec_command(name string, args []string, dir string) string {
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(output)
}

// simpleHash returns a simple hash of a string for stuck detection.
func simpleHash(s string) string {
	h := 0
	for _, c := range s {
		h = h*31 + int(c)
	}
	return fmt.Sprintf("%d", h)
}

// hasWorkToDo checks locally (zero cloud tokens) whether there is actual work
// to do for the given action. This is the idle guard — it prevents the cloud
// LLM from being called when there's nothing to work on.
// buildDynamicMemoryQueries derives Remembrance search queries from the current
// context: the action being performed, recent channel messages, and inter-agent
// messages. This replaces the previous hardcoded query list with queries that
// adapt to what is actually happening in the conversation and system.
func (wh *WakeHandler) buildDynamicMemoryQueries(action string, actionDef wakeAction) []string {
	queries := make([]string, 0, 6)
	now := time.Now().Format("2006-01-02")

	// Query 1: Action + date — recency-aware recall of work related to this action
	queries = append(queries, fmt.Sprintf("%s %s", action, now))

	// Query 2: Action-specific context
	switch action {
	case "project_work":
		queries = append(queries, "project assignment task progress status")
	case "status_report":
		queries = append(queries, "recent progress commits changes updates")
	case "daily_review":
		queries = append(queries, "daily review decisions architecture state")
	case "auto_patch":
		queries = append(queries, "bug fix error patch failing test")
	case "check_prs":
		queries = append(queries, "pull request review feedback merge")
	case "review_improvements":
		queries = append(queries, "improvement proposal optimization quality")
	default:
		queries = append(queries, fmt.Sprintf("%s work status", action))
	}

	// Query 3: Derived from recent Discord channel messages
	if wh.bot != nil && actionDef.ChannelID != "" {
		recent := wh.bot.GetRecentMessages(actionDef.ChannelID, 10)
		if len(recent) > 0 {
			// Use the most recent non-empty message as a search query
			for i := len(recent) - 1; i >= 0; i-- {
				content := strings.TrimSpace(recent[i].Content)
				if content != "" && len(content) > 10 {
					// Truncate to keep query focused
					if len(content) > 120 {
						content = content[:120]
					}
					queries = append(queries, content)
					break
				}
			}
		}
	}

	// Query 4: Derived from inter-agent NATS messages
	wh.agentMu.Lock()
	if len(wh.agentMessages) > 0 {
		latest := wh.agentMessages[len(wh.agentMessages)-1]
		content := strings.TrimSpace(latest.Content)
		if content != "" && len(content) > 10 {
			if len(content) > 120 {
				content = content[:120]
			}
			queries = append(queries, content)
		}
	}
	wh.agentMu.Unlock()

	// Query 5: Cross-agent decisions and architecture (always useful for context)
	queries = append(queries, "OpenClaw Lumi Prizm decisions architecture")

	return queries
}

func (wh *WakeHandler) hasWorkToDo(action string) bool {
	repoPath := wh.defaultRepoPath()

	switch action {
	case "project_work":
		// Check for incomplete tasks in PROJECT_STATE.md
		statePath := wh.projectStatePath(wh.cfg.DefaultProject())
		if statePath != "" {
			if data, err := os.ReadFile(statePath); err == nil {
				content := string(data)
				// Look for incomplete task markers: "- [ ]" or "### Task N:" without ✅
				lines := strings.Split(content, "\n")
				for _, line := range lines {
					trimmed := strings.TrimSpace(line)
					if strings.HasPrefix(trimmed, "- [ ]") {
						return true // Found incomplete task
					}
					if strings.HasPrefix(trimmed, "### Task ") && !strings.Contains(trimmed, "✅") {
						return true // Found incomplete task header
					}
				}
			}
		}
		// Check for uncommitted changes
		gitStatus := exec_command("git", []string{"status", "--short"}, repoPath)
		if gitStatus != "" {
			return true // Uncommitted changes need attention
		}
		// No work found
		return false

	case "auto_patch":
		// Check for uncommitted changes (potential bugs to fix)
		gitStatus := exec_command("git", []string{"status", "--short"}, repoPath)
		if gitStatus != "" {
			return true
		}
		// Check for failing tests
		testOutput := exec_command("go", []string{"test", "./...", "-count=1"}, repoPath)
		if strings.Contains(testOutput, "FAIL") {
			return true // Failing tests need fixing
		}
		// Check for open PRs that might need attention
		prList := exec_command("gh", []string{"pr", "list", "--state", "open", "--repo", wh.cfg.DefaultProject().RepoPath}, "")
		if prList != "" && !strings.Contains(prList, "No pull requests") {
			return true // Open PRs to review
		}
		// Memory search handled by local MarkdownStore
		return false

	case "daily_review":
		// Always run daily review — it's once per day and checks state
		return true

	case "review_improvements":
		// Check if there are any active improvement proposals
		if wh.improveMgr != nil {
			active := wh.improveMgr.GetActiveImprovements()
			if len(active) > 0 {
				return true
			}
		}
		return false
	}

	// Default: allow the action to run
	return true
}

// gatherScoutContext uses the local scout agent (qwen3:8b on Ollama) to gather
// codebase context before the cloud model runs. This saves cloud tokens on the
// "read and understand" phase — scout reads git status, git log, and PROJECT_STATE.md
// locally and returns a concise summary that gets injected into the cloud model's prompt.
func (wh *WakeHandler) gatherScoutContext(ctx stdcontext.Context) string {
	repoPath := wh.defaultRepoPath()

	// Gather raw context using git commands (zero token cost)
	gitStatus := exec_command("git", []string{"status", "--short"}, repoPath)
	gitLog := exec_command("git", []string{"log", "--oneline", "-10"}, repoPath)

	// Read PROJECT_STATE.md
	statePath := wh.projectStatePath(wh.cfg.DefaultProject())
	stateContent := ""
	if statePath != "" {
		if data, err := os.ReadFile(statePath); err == nil {
			stateContent = string(data)
			if len(stateContent) > 2000 {
				stateContent = stateContent[:2000] + "..."
			}
		}
	}

	// If nothing to report, skip scout
	if gitStatus == "" && gitLog == "" && stateContent == "" {
		return ""
	}

	// Build a prompt for scout to summarize
	scoutPrompt := fmt.Sprintf("Summarize the current project state concisely. Focus on: what work is in progress, what files changed, and what the next incomplete task is. Be brief — 5 bullet points max.\n\n## Git Status\n%s\n\n## Recent Commits\n%s\n\n## PROJECT_STATE.md (excerpt)\n%s", gitStatus, gitLog, stateContent)

	// Call scout via the local Ollama provider (qwen3:8b — zero cloud tokens)
	prov, err := wh.providers.Get("qwen3:8b")
	if err != nil {
		log.Printf("[SCOUT] qwen3:8b not available: %v — skipping local context", err)
		return ""
	}

	resp, err := prov.Generate(ctx, provider.GenerateRequest{
		RunID:       fmt.Sprintf("scout-context-%d", time.Now().Unix()),
		Agent:       "scout",
		Model:       "qwen3:8b",
		Prompt:      scoutPrompt,
		Temperature: 0.3,
		MaxTokens:   512,
	})
	if err != nil {
		log.Printf("[SCOUT] local context gather failed: %v", err)
		return ""
	}

	log.Printf("[SCOUT] local context gathered (%d chars, 0 cloud tokens)", len(resp.Text))
	return resp.Text
}

// loadWorkflowConfig resolves the gated-loop workflow definition: a project
// override takes precedence, then the global prizm.workflow_config, then the
// built-in 7-phase DefaultConfig.
func (wh *WakeHandler) loadWorkflowConfig(project *orchestrator.ProjectConfig) *v2.WorkflowConfig {
	var paths []string
	if project != nil && project.WorkflowConfig != "" {
		paths = append(paths, project.WorkflowConfig)
	}
	if wh.cfg.Prizm.WorkflowConfig != "" {
		paths = append(paths, wh.cfg.Prizm.WorkflowConfig)
	}
	for _, p := range paths {
		if cfg, err := v2.LoadConfig(p); err == nil {
			cfg = applyProjectTokenBudget(cfg, project)
			log.Printf("[GATED-LOOP] loaded workflow config %s (%d phases)", p, len(cfg.Phases))
			return cfg
		} else {
			log.Printf("[GATED-LOOP] WARN could not load workflow config %s: %v", p, err)
		}
	}
	return applyProjectTokenBudget(v2.DefaultConfig(), project)
}

func applyProjectTokenBudget(cfg *v2.WorkflowConfig, project *orchestrator.ProjectConfig) *v2.WorkflowConfig {
	v2.NormalizeTokenBudgets(cfg)
	if cfg != nil && project != nil && project.TokenBudget != 0 && project.TokenBudget >= v2.UnlimitedTokens {
		cfg.Global.MaxTotalTokens = project.TokenBudget
	}
	return cfg
}

func (wh *WakeHandler) wakeToolTokenCeiling() int {
	cfg := applyProjectTokenBudget(v2.DefaultConfig(), wh.cfg.DefaultProject())
	if cfg == nil {
		return v2.DefaultRunTokenCeiling
	}
	return cfg.Global.MaxTotalTokens
}

func wakeTokenBudgetExceeded(used, max int) bool {
	return max > 0 && used >= max
}

func estimateWakeTokens(s string) int {
	return (len(s) + 3) / 4
}

func estimateWakeChatPromptTokens(messages []provider.ChatMessage) int {
	n := 0
	for _, m := range messages {
		n += len(m.Content)
	}
	return (n + 3) / 4
}

// runNaturalGatesWorkflow is the scheduled entry point: resolve the default
// project, seed a task from its state file (or the prompt), then run the loop.
func (wh *WakeHandler) runNaturalGatesWorkflow(ctx stdcontext.Context, systemPrompt, userPrompt, model string, agentCfg *orchestrator.AgentConfig, exec *tool.Executor) (string, int, int) {
	project := wh.cfg.DefaultProject()
	// Honor a per-project orchestrator brain (e.g. claude_code) over the
	// primary agent the scheduler selected before the project was known.
	if a := wh.orchestratorAgentFor(project); a != nil {
		agentCfg = a
		if a.Model != "" {
			model = a.Model
		}
	}
	task := wh.parseProjectStateTaskFrom(wh.projectStatePath(project))
	if task == "" {
		task = userPrompt
	}
	channel := ""
	if project != nil {
		channel = project.Channel
	}
	return wh.RunGatedLoop(ctx, project, task, model, agentCfg, exec, channel)
}

// RunGatedLoop runs the full 7-phase gated loop for {project, prompt}. It is the
// single implementation shared by the scheduled wake action and the interactive
// trigger (CLI/API), driving the real v2.Engine via Engine.Drive.
func (wh *WakeHandler) RunGatedLoop(ctx stdcontext.Context, project *orchestrator.ProjectConfig, taskPrompt, model string, agentCfg *orchestrator.AgentConfig, exec *tool.Executor, channel string) (string, int, int) {
	config := wh.loadWorkflowConfig(project)

	repoPath := "."
	projectID := "default"
	if project != nil {
		if project.RepoPath != "" {
			repoPath = project.RepoPath
		}
		if project.ID != "" {
			projectID = project.ID
		}
		if channel == "" {
			channel = project.Channel
		}
	} else if wh.cfg.Prizm.Workspace != "" {
		repoPath = wh.cfg.Prizm.Workspace
	}

	stateDir := config.Global.StatePersistenceDir
	if stateDir == "" {
		stateDir = "runs/gated-loop"
	}
	runID := fmt.Sprintf("gl-%d", time.Now().Unix())

	// V56: per-run worktree isolation. The run gets its own directory under
	// <repo>/.prizm/worktrees/<runID> on a fresh prizm/<runID> branch, so
	// parallel runs on one repo cannot collide and the main worktree is never
	// touched. workRoot is what tools, verification, branch detection, and the
	// system prompt operate on; stateDir stays outside the worktree. Fail
	// closed: if the user asked for isolation and we can't provide it, don't
	// run un-isolated.
	workRoot := repoPath
	runBranch := ""
	runRolledBack := false // set by the V57 rollback runner; read by worktree cleanup
	if project != nil && project.WorktreeIsolation {
		wtPath := filepath.Join(repoPath, ".prizm", "worktrees", runID)
		runBranch = "prizm/" + runID
		if err := gitx.EnsureExcluded(ctx, repoPath, ".prizm/"); err != nil {
			log.Printf("[GATED-LOOP] worktree exclude not written (continuing): %v", err)
		}
		if err := gitx.CreateBranchWorktree(ctx, repoPath, wtPath, runBranch, ""); err != nil {
			log.Printf("[GATED-LOOP] worktree isolation failed, refusing to run un-isolated: %v", err)
			return fmt.Sprintf("Gated loop aborted: worktree isolation is enabled for project %s but the worktree could not be created: %v", projectID, err), 0, 0
		}
		workRoot = wtPath
		// Defers run LIFO: the branch deletion is registered FIRST so it runs
		// AFTER the worktree is removed (a checked-out branch can't be deleted).
		// A rolled-back run's branch holds nothing worth keeping locally.
		defer func() {
			if runRolledBack && runBranch != "" {
				gitx.DeleteBranch(stdcontext.Background(), repoPath, runBranch)
				log.Printf("[GATED-LOOP] rolled-back run branch %s deleted", runBranch)
			}
		}()
		defer gitx.RemoveWorktree(stdcontext.Background(), repoPath, wtPath)
		log.Printf("[GATED-LOOP] run isolated in worktree %s (branch %s)", wtPath, runBranch)
	}

	// Engine: NATS emitter for dashboard observability + delegation for optional
	// sub-agent reviewers. Falls back to a no-op log emitter without NATS.
	var emitter v2.EventEmitter = &v2.LogEmitter{}
	if wh.natsConn != nil {
		emitter = v2.NewNATSEmitter(v2.NewNATSPublisherFromConn(wh.natsConn), "prizm.workflow")
	}
	delegation := v2.NewDelegationManager("prizm.agent.openclaw", "prizm.workflow.task.complete")
	delegation.ApplyTimeoutConfig(config.Global.DelegationTimeouts)
	engine := v2.NewEngine(config, emitter, delegation)
	engine.GetState().RunID = runID
	engine.GetState().ProjectID = projectID
	engine.GetState().RepoPath = workRoot
	engine.GetState().Channel = channel
	requirePush := repoHasRemote(repoPath)
	engine.GetState().RequirePush = requirePush

	// Objective verification: bind phases that declare a verification profile to
	// the V5 validation executor (allowlisted commands only — the model never
	// controls what runs). This makes EXECUTION prove the code builds and its
	// tests pass before the phase can complete, instead of committing unverified code.
	if workflowHasVerification(config) {
		vexec := validation.NewExecutor(validation.NewRegistry(), workRoot, filepath.Join(stateDir, runID))
		vexec.SetEmitter(func(eventType, source string, payload map[string]any) {
			emitter.Emit(eventType, payload)
		})
		engine.SetVerificationRunner(func(vctx stdcontext.Context, profile string) v2.VerificationOutcome {
			res, err := vexec.Run(vctx, profile, runID)
			if res == nil || res.Status == "error" {
				if err != nil {
					log.Printf("[wake] verification profile %q unavailable: %v", profile, err)
				}
				return v2.VerificationOutcome{Ran: false}
			}
			passed := res.Status == "passed"
			summary := res.Status
			if !passed {
				summary = verificationSummary(res)
			}
			return v2.VerificationOutcome{Ran: true, Passed: passed, ExitCode: res.ExitCode, Summary: summary}
		})
	}

	// V57: auto-rollback runner. Captures the run's start SHA; when the engine
	// declares the run failing (blocking verification exhausted, blocking
	// fallback, or ending with red verification), the run's work is discarded:
	// reset --hard to the start SHA and, under worktree isolation, the run
	// branch is deleted after cleanup. Pushed commits are NOT force-removed —
	// the remote branch remains for forensics.
	if config.Global.AutoRollback {
		startSHA, shaErr := gitx.CurrentSHA(ctx, workRoot)
		origBranch, _ := gitx.CurrentBranch(ctx, workRoot)
		if shaErr != nil {
			log.Printf("[GATED-LOOP] auto_rollback disabled for this run (cannot resolve start SHA): %v", shaErr)
		} else {
			engine.SetRollbackRunner(func(rctx stdcontext.Context, reason string) error {
				if err := gitx.ResetHard(rctx, workRoot, startSHA); err != nil {
					return err
				}
				// Outside worktree isolation the model may have checked out its
				// own feature branch — return to the branch the run started on.
				if origBranch != "" {
					if cur, _ := gitx.CurrentBranch(rctx, workRoot); cur != origBranch {
						if _, err := gitx.RunCommand(rctx, workRoot, "", "git", "checkout", origBranch); err != nil {
							return err
						}
					}
				}
				runRolledBack = true
				log.Printf("[GATED-LOOP] rolled back to %.12s: %s", startSHA, reason)
				return nil
			})
		}
	}

	// Delegation transport: publish delegated task packets onto the agent subject so
	// other agents/Prizms can pick them up. Without NATS, delegations are still
	// recorded in state but not dispatched.
	if wh.natsConn != nil {
		engine.SetTaskPublisher(func(packet v2.TaskPacket) error {
			data, err := json.Marshal(packet)
			if err != nil {
				return err
			}
			return wh.natsConn.Publish(delegation.Subject(), data)
		})
	}

	// Feedback resume: forward approval/review responses from NATS into the engine
	// so FEEDBACK_PRE/FEEDBACK_POST pauses can be released from Discord/dashboard/API.
	if wh.natsConn != nil {
		eventCh := engine.GetExternalEventChannel()
		sub, subErr := wh.natsConn.Subscribe("prizm.workflow.feedback.response", func(msg *nats.Msg) {
			var payload map[string]any
			if err := json.Unmarshal(msg.Data, &payload); err != nil {
				return
			}
			workflowID, _ := payload["workflow_id"].(string)
			if workflowID != "" && workflowID != runID {
				return
			}
			decision, _ := payload["decision"].(string)
			reviewer, _ := payload["reviewer"].(string)
			extType := "approval"
			if reviewer != "" || payload["type"] == "review_response" {
				extType = "review"
			}
			evt := v2.ExternalEvent{
				Type:          extType,
				CorrelationID: runID,
				Source:        "nats",
				Data: map[string]any{
					"decision":   decision,
					"reviewer":   reviewer,
					"notes":      payload["notes"],
					"dimensions": payload["dimensions"],
				},
			}
			select {
			case eventCh <- evt:
			default:
			}
		})
		if subErr == nil {
			defer sub.Unsubscribe()
		}

		// Delegated task completions: forward into the engine so the delegation
		// manager closes out the record and the task_completion gate sees it.
		csub, csubErr := wh.natsConn.Subscribe(delegation.Subject()+".complete", func(msg *nats.Msg) {
			var payload map[string]any
			if err := json.Unmarshal(msg.Data, &payload); err != nil {
				return
			}
			if wfID, _ := payload["workflow_id"].(string); wfID != "" && wfID != runID {
				return
			}
			evt := v2.ExternalEvent{
				Type:          "task_complete",
				CorrelationID: runID,
				Source:        "nats",
				Data: map[string]any{
					"task_id":        payload["task_id"],
					"status":         payload["status"],
					"output_summary": payload["output_summary"],
				},
			}
			select {
			case eventCh <- evt:
			default:
			}
		})
		if csubErr == nil {
			defer csub.Unsubscribe()
		}
	}

	// System prompt (project-aware, no hardcoded paths) + tool schemas.
	// Under worktree isolation the model is pointed at workRoot and told the
	// run branch already exists.
	systemPromptFull := wh.buildGatedLoopSystemPrompt(project, taskPrompt, workRoot, requirePush)
	if runBranch != "" {
		systemPromptFull += fmt.Sprintf("\n## Worktree isolation\nThis run is isolated in its own git worktree, already checked out on branch `%s`. Do NOT create a new branch (skip git_checkout with create=true) — commit and push on the current branch.\n", runBranch)
	}
	if wh.toolReg != nil {
		toolInfos := wh.toolReg.ListWithDescriptions()
		systemPromptFull += agent.BuildToolPromptSuffix(toolInfos, workRoot, workRoot)
	}
	if wh.skills != nil {
		systemPromptFull += skill.PromptSuffix(wh.skills.List())
	}
	userPromptFull := fmt.Sprintf("Begin the gated loop. Task: %s", taskPrompt)

	// LLM callback — prefer the tool-calling chat provider, fall back to text.
	chatProv, chatErr := wh.providers.GetChatProviderForAgent(agentCfg.ID, model)
	llm := func(c stdcontext.Context, msgs []v2.Message) (string, int, int, error) {
		if chatErr == nil {
			cm := make([]provider.ChatMessage, len(msgs))
			for i, m := range msgs {
				cm[i] = provider.ChatMessage{Role: m.Role, Content: m.Content}
			}
			resp, err := chatProv.ChatGenerate(c, provider.ChatGenerateRequest{
				RunID: runID, Agent: agentCfg.ID, Model: model, Messages: cm,
				Temperature: 0.7, MaxTokens: 4096,
			})
			if err != nil {
				return "", 0, 0, err
			}
			if local, _ := resp.Raw["local_fallback"].(bool); local {
				pt, ct := v2.MarkLocalUsage(resp.PromptTokens, resp.OutputTokens)
				return resp.Content, pt, ct, nil
			}
			return resp.Content, resp.PromptTokens, resp.OutputTokens, nil
		}
		prov, provErr := wh.providers.GetForAgent(agentCfg.ID, model)
		if provErr != nil {
			return "", 0, 0, provErr
		}
		var flat strings.Builder
		for _, m := range msgs {
			flat.WriteString(m.Content)
			flat.WriteString("\n\n")
		}
		resp, err := prov.Generate(c, provider.GenerateRequest{
			RunID: runID, Agent: agentCfg.ID, Model: model, Prompt: flat.String(),
			Temperature: 0.7, MaxTokens: 4096,
		})
		if err != nil {
			return "", 0, 0, err
		}
		if local, _ := resp.Raw["local_fallback"].(bool); local {
			pt, ct := v2.MarkLocalUsage(resp.PromptTokens, resp.OutputTokens)
			return resp.Text, pt, ct, nil
		}
		return resp.Text, resp.PromptTokens, resp.OutputTokens, nil
	}

	// Tool callback — inject the project's repo_path for git tools, run under policy.
	gitTools := map[string]bool{"git_checkout": true, "git_status": true, "git_log": true, "git_diff": true, "git_branch_list": true, "git_add": true, "git_commit": true, "git_push": true}
	toolFn := func(c stdcontext.Context, phase string, req *v2.ToolRequest) (string, error) {
		input := req.Input
		if input == nil {
			input = map[string]any{}
		}
		if gitTools[req.Tool] {
			if _, ok := input["repo_path"]; !ok {
				input["repo_path"] = workRoot
			}
		}
		result, err := exec.ExecuteWithPolicy(c, req.Tool, agentCfg.ID, projectID, runID, input)
		if err != nil {
			return "", err
		}
		if !result.Success {
			if result.Error != "" {
				return "", fmt.Errorf("%s", result.Error)
			}
			return "", fmt.Errorf("tool %s failed", req.Tool)
		}
		return summarizeToolResult(req.Tool, fmt.Sprintf("%v", result.Output)), nil
	}

	getBranch := func() string { return wh.getCurrentBranchIn(workRoot) }

	log.Printf("[GATED-LOOP] starting %q project=%s repo=%s (%d phases)", config.Name, projectID, workRoot, len(config.Phases))
	wfState, driveErr := engine.Drive(ctx, llm, toolFn, v2.DriveOptions{
		SystemPrompt:        systemPromptFull,
		UserPrompt:          userPromptFull,
		StateDir:            stateDir,
		RepoPath:            workRoot,
		ProjectID:           projectID,
		Channel:             channel,
		GetBranch:           getBranch,
		SkipPushRequirement: !requirePush,
	})
	if driveErr != nil {
		log.Printf("[GATED-LOOP] ended: %v", driveErr)
	}

	// Persist a durable proof-of-work report artifact (non-fatal on failure).
	if path, werr := v2.WriteReportArtifact(wfState, stateDir); werr != nil {
		log.Printf("[GATED-LOOP] report artifact not written: %v", werr)
	} else {
		log.Printf("[GATED-LOOP] report written: %s", path)
	}

	pt := wfState.GetTotalPromptTokens()
	ct := wfState.GetTotalCompletionTokens()

	content := ""
	if wfState.Report != nil && wfState.Report.Content != "" {
		content = cleanForDiscord(wfState.Report.Content)
	} else {
		content = formatWorkflowReport(wfState)
	}
	return content, pt, ct
}

// buildGatedLoopSystemPrompt builds the project-aware system prompt for the loop.
// No hardcoded repo paths or agent IDs — everything comes from the project config.
func (wh *WakeHandler) buildGatedLoopSystemPrompt(project *orchestrator.ProjectConfig, taskPrompt, repoPath string, requirePush bool) string {
	var b strings.Builder
	b.WriteString("You are an autonomous engineering agent running a gated dev loop. You build production-quality code.\n\n")
	b.WriteString("## Task (assigned by the system — do not deviate)\n")
	b.WriteString(taskPrompt + "\n\n")
	b.WriteString("## Project\n")
	fmt.Fprintf(&b, "- Repo: %s\n", repoPath)
	if project != nil && project.StateFile != "" {
		fmt.Fprintf(&b, "- State file: %s\n", project.StateFile)
	}
	b.WriteString("- Relative file paths resolve against the repo root above.\n\n")
	b.WriteString(`## How the loop works
You move through phases: PROBE -> RESEARCH -> PLAN -> FEEDBACK_PRE -> EXECUTION -> FEEDBACK_POST -> REPORT.
The system tells you the current phase, the allowed tools, and the completion signal. Follow them.
- PROBE: surface assumptions and reduce them with web_search / memory_search / file reads.
- RESEARCH: raise confidence across domains using the web, memory, and the codebase.
- PLAN: break the work into tasks with success criteria.
- FEEDBACK_PRE: the loop PAUSES for human approval of your plan. Do NOT write code yet.
`)
	if requirePush {
		b.WriteString("- EXECUTION: create a feature branch (writes are blocked on the protected branch), write code, commit, and push. You cannot finish until you have committed AND pushed.\n")
	} else {
		b.WriteString("- EXECUTION: create a feature branch (writes are blocked on the protected branch), write code, and commit. This repo has no configured git remote, so do not push unless a remote is added.\n")
	}
	b.WriteString(`- FEEDBACK_POST: the loop PAUSES for review. If changes are requested it loops back to EXECUTION.
- REPORT: summarize with proof of work (files, branch, commit hashes).

## Response format
Respond with PURE JSON: {"type":"tool_request","tool":"...","input":{...}} or {"type":"final","content":"..."}.
You may include declarations (ASSUMPTION:, CONFIDENCE:, TASK:) and the phase completion signal in your text before the JSON.
`)
	return b.String()
}

// formatWorkflowReport creates a human-readable report from workflow state.
func formatWorkflowReport(state *v2.WorkflowState) string {
	report := "## Natural Gates Workflow Report\n\n"
	report += fmt.Sprintf("**Status:** %s\n", state.Status)
	report += fmt.Sprintf("**Run ID:** %s\n\n", state.RunID)

	// Phase summary
	report += "### Phase Summary\n"
	for _, phaseCfg := range v2.DefaultConfig().Phases {
		if ps, ok := state.PhaseStates[phaseCfg.Name]; ok {
			status := string(ps.Status)
			if ps.GateResult != nil && ps.GateResult.Passed {
				status += fmt.Sprintf(" (score: %.2f)", ps.GateResult.Score)
			}
			report += fmt.Sprintf("- %s: %s (%d iterations)\n", phaseCfg.Name, status, ps.Iterations)
		}
	}

	// Assumptions
	if len(state.Assumptions) > 0 {
		report += "\n### Assumptions\n"
		for _, a := range state.Assumptions {
			status := a.Status
			if status == "addressed" {
				status = "✅ resolved"
			}
			report += fmt.Sprintf("- %s [%s, conf: %.1f]: %s — %s\n", a.ID, a.Criticality, a.Confidence, a.Statement, status)
		}
	}

	// Confidence
	report += "\n### Confidence Matrix\n"
	for domain, cd := range state.ConfidenceMatrix {
		report += fmt.Sprintf("- %s: %.2f\n", domain, cd.Score)
	}

	// Plan
	if state.Plan != nil && len(state.Plan.Tasks) > 0 {
		report += "\n### Plan\n"
		for _, task := range state.Plan.Tasks {
			report += fmt.Sprintf("- %s [%s] → %s (%s)\n", task.ID, task.Agent, task.Description, task.Status)
		}
	}

	// Feedback
	if state.Feedback != nil {
		if state.Feedback.PreExecution != nil {
			report += fmt.Sprintf("\n### Pre-Execution Feedback: %s\n", state.Feedback.PreExecution.Status)
		}
		if state.Feedback.PostExecution != nil {
			report += "\n### Post-Execution Review\n"
			for reviewer, rs := range state.Feedback.PostExecution.Reviewers {
				report += fmt.Sprintf("- %s: %s\n", reviewer, rs.Status)
			}
		}
	}

	return report
}

// molStatusReport generates a self-status report for the Machine Orchestration Loop.
// It reports Prizm's own health: uptime, sessions, agents, recent runs, and observations.
// This is the simplest meaningful MOL action: schedule → execute → emit event.

package main

import (
	stdcontext "context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/emaharmony/prizm/internal/agent"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/subagent"
	"github.com/emaharmony/prizm/internal/tool"
	v2 "github.com/emaharmony/prizm/internal/workflow/v2"
	"github.com/nats-io/nats.go"
)

// subagent_worker.go wires the generic sub-agent worker (internal/subagent) into
// serve: it subscribes to the delegation subject, runs each TaskPacket through a
// bounded tool-loop backed by the real provider registry + tool executor, and
// publishes the TaskCompletion back. This is what makes delegated sub-agents
// genuinely autonomous (V58). Feature-flagged via PRIZM_SUBAGENT_WORKER so it is
// inert — zero behavior change — until explicitly enabled.

const (
	subAgentDelegationSubject = "prizm.agent.openclaw"
	subAgentCompletionSubject = "prizm.workflow.task.complete"
	// subAgentMaxConcurrency bounds simultaneous sub-agent task runs.
	subAgentMaxConcurrency = 4
)

// subAgentResolver resolves an agent id to its runtime from the loaded config.
type subAgentResolver struct {
	agents map[string]subagent.AgentRuntime
}

func newSubAgentResolver(cfg *orchestrator.Config) *subAgentResolver {
	m := make(map[string]subagent.AgentRuntime, len(cfg.Agents))
	for _, a := range cfg.Agents {
		m[a.ID] = subagent.AgentRuntime{
			AgentID:      a.ID,
			Provider:     a.Provider,
			Model:        a.Model,
			Capabilities: append([]string(nil), a.Capabilities...),
		}
	}
	return &subAgentResolver{agents: m}
}

func (r *subAgentResolver) Resolve(id string) (subagent.AgentRuntime, bool) {
	rt, ok := r.agents[id]
	return rt, ok
}

// subAgentRepoRoot picks the git repo sub-agent worktrees are cut from: the
// default project's repo, else the workspace. Empty disables isolation.
func subAgentRepoRoot(cfg *orchestrator.Config) string {
	if p := cfg.DefaultProject(); p != nil && p.RepoPath != "" {
		return p.RepoPath
	}
	return cfg.Prizm.Workspace
}

// subAgentBackend binds an agent runtime to live provider + tool execution
// callbacks, reusing the same text-generation + JSON-contract parsing the gated
// loop uses.
type subAgentBackend struct {
	providers       *provider.ProviderRegistry
	exec            *tool.Executor // shared executor (no worktree isolation)
	toolReg         *tool.Registry // shared registry, source for non-root tools
	protectedBranch string         // branch git_commit/git_push in worktree executors must refuse to write to
}

// subAgentWorktreeMaxFileSize matches serve's builtin file-size cap.
const subAgentWorktreeMaxFileSize = 10 * 1024 * 1024

// executorFor returns the tool executor for a run. With no worktree it's the
// shared executor; with a worktree it builds a per-run executor whose file and
// git tools are rooted at the worktree (read/write roots = the worktree), so
// write_file/create_directory are isolated too — not just git (V58 4d). Shared,
// root-agnostic tools (research, image, skill, MCP, state, plan) are copied from
// the main registry so the sub-agent keeps its full toolset. Same policy as the
// shared executor.
func (b *subAgentBackend) executorFor(workDir string) *tool.Executor {
	if workDir == "" || b.toolReg == nil {
		return b.exec
	}
	reg := tool.NewRegistry()
	roots := []string{workDir}
	tool.RegisterBuiltinsWithRoots(reg, workDir, subAgentWorktreeMaxFileSize, roots, roots)
	_ = reg.Register(&tool.WriteFileProposal{WorkspaceRoot: workDir, AllowedPaths: roots})
	_ = reg.Register(&tool.CreateDirectoryProposal{WorkspaceRoot: workDir, AllowedPaths: roots})
	_ = reg.Register(&tool.WriteFileDirect{WorkspaceRoot: workDir, AllowedPaths: roots})
	_ = reg.Register(&tool.CreateDirectoryDirect{WorkspaceRoot: workDir, AllowedPaths: roots})
	_ = reg.Register(&tool.GitAddTool{ToolPaths: tool.ToolPaths{WorkspaceRoot: workDir, AllowedPaths: roots}})
	_ = reg.Register(&tool.GitCommitTool{ToolPaths: tool.ToolPaths{WorkspaceRoot: workDir, AllowedPaths: roots}, ProtectedBranch: b.protectedBranch})
	_ = reg.Register(&tool.GitPushTool{ToolPaths: tool.ToolPaths{WorkspaceRoot: workDir, AllowedPaths: roots}, ProtectedBranch: b.protectedBranch})
	// Copy every shared tool not already provided as a worktree-rooted version
	// (research/image/skill/MCP/state/plan/read-only git). Same instance reuse
	// keeps live MCP client connections intact.
	for _, name := range b.toolReg.List() {
		if _, err := reg.Resolve(name); err == nil {
			continue // worktree-rooted version already registered
		}
		if t, err := b.toolReg.Resolve(name); err == nil {
			_ = reg.Register(t)
		}
	}
	return tool.NewExecutor(reg, b.exec.Policy) // b.exec.Policy is already *PolicyConfig
}

func (b *subAgentBackend) Bind(rt subagent.AgentRuntime) (subagent.LLMFunc, subagent.Parser, subagent.ToolExec, error) {
	prov, err := b.providers.Get(rt.Model)
	if err != nil {
		return nil, nil, nil, err
	}

	// Per-run executor: rooted at the task's worktree when isolated, so all
	// file + git tools (not just git) operate inside it.
	ex := b.executorFor(rt.WorkDir)

	llm := func(ctx stdcontext.Context, msgs []v2.Message) (subagent.Turn, error) {
		var sb strings.Builder
		for _, m := range msgs {
			sb.WriteString(m.Content)
			sb.WriteString("\n\n")
		}
		resp, gerr := prov.Generate(ctx, provider.GenerateRequest{
			Agent: rt.AgentID, Model: rt.Model, Prompt: sb.String(),
			Temperature: 0.7, MaxTokens: 4096,
		})
		if gerr != nil {
			return subagent.Turn{}, gerr
		}
		return subagent.Turn{Text: resp.Text, PromptTokens: resp.PromptTokens, CompletionTokens: resp.OutputTokens}, nil
	}

	parse := func(text string) subagent.Action {
		if content, ok := v2.ParseFinalText(text); ok {
			return subagent.Action{Final: true, Content: content}
		}
		if toolName, input, ok := v2.ParseToolRequestText(text); ok {
			return subagent.Action{Tool: toolName, Input: input}
		}
		return subagent.Action{}
	}

	execFn := func(ctx stdcontext.Context, toolName string, input map[string]any) (string, error) {
		// The executor's tools are already rooted at the worktree (executorFor),
		// so no per-call repo_path injection is needed.
		execInput := make(map[string]any, len(input)+1)
		for key, value := range input {
			execInput[key] = value
		}
		project := "subagent"
		correlationID := rt.AgentID
		if rt.RunID != "" {
			execInput["_run_id"] = rt.RunID
			project = "multiagent"
			correlationID = rt.RunID
		}
		if rt.ExecutionKey != "" {
			execInput["_execution_key"] = rt.ExecutionKey
			correlationID = rt.ExecutionKey
		}
		res, xerr := ex.ExecuteWithPolicy(
			ctx, toolName, rt.AgentID, project, correlationID, execInput)
		if xerr != nil {
			return "", xerr
		}
		if !res.Success {
			if res.Error != "" {
				return "", fmt.Errorf("%s", res.Error)
			}
			return "", fmt.Errorf("tool %s failed", toolName)
		}
		return fmt.Sprintf("%v", res.Output), nil
	}

	return llm, parse, execFn, nil
}

// subAgentPublisher publishes completions back onto the completion subject.
type subAgentPublisher struct {
	nc      *nats.Conn
	subject string
}

func (p *subAgentPublisher) PublishCompletion(c v2.TaskCompletion) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return p.nc.Publish(p.subject, data)
}

// startSubAgentWorker subscribes the generic sub-agent worker to the delegation
// subject. Feature-flagged via PRIZM_SUBAGENT_WORKER (empty = off) so it is a
// no-op until explicitly enabled — no behavior change to existing deployments.
func startSubAgentWorker(nc *nats.Conn, providers *provider.ProviderRegistry, exec *tool.Executor, toolReg *tool.Registry, cfg *orchestrator.Config) {
	if os.Getenv("PRIZM_SUBAGENT_WORKER") == "" {
		return
	}
	if nc == nil || exec == nil || cfg == nil {
		log.Printf("[SUBAGENT] worker not started: missing NATS, executor, or config")
		return
	}

	var toolInfos []tool.ToolInfo
	if toolReg != nil {
		toolInfos = toolReg.ListWithDescriptions()
	}

	resolver := newSubAgentResolver(cfg)
	backend := &subAgentBackend{providers: providers, exec: exec, toolReg: toolReg, protectedBranch: cfg.ProtectedBranch()}
	runner := subagent.NewLoopRunner(subagent.LoopRunnerConfig{
		Backend: backend,
		// Per-agent tool scoping: keep each sub-agent in its role lane (only
		// "code"-capable agents may mutate/git-write/use MCP build tools).
		Scope: subagent.DefaultToolScope(),
		SystemPrompt: func(_ v2.TaskPacket, rt subagent.AgentRuntime) string {
			// Charter + the tool JSON contract so the model emits the
			// {"type":"tool_request"|"final"} format the parser expects.
			charter := fmt.Sprintf("You are agent %q running a single delegated task autonomously. Complete the task with your tools, then return a final answer.", rt.AgentID)
			workspace := cfg.Prizm.Workspace
			if workspace == "" {
				workspace = "."
			}
			return charter + agent.BuildToolPromptSuffix(toolInfos, workspace, workspace)
		},
	})
	worker := subagent.NewWorker(resolver, runner, 0)
	// Per-task worktree isolation for code-capable agents, rooted at the default
	// project repo (falls back to the workspace). Non-mutating agents skip it.
	if root := subAgentRepoRoot(cfg); root != "" {
		worker.SetWorktrees(subagent.GitWorktreeProvider{Root: root})
	}
	pub := &subAgentPublisher{nc: nc, subject: subAgentCompletionSubject}

	// Bound concurrent sub-agent runs so a burst of delegations can't exhaust
	// resources. Parallel runs stay isolated at the git layer via V56 worktrees
	// when a task mutates files.
	sem := make(chan struct{}, subAgentMaxConcurrency)

	_, err := nc.Subscribe(subAgentDelegationSubject, func(msg *nats.Msg) {
		var packet v2.TaskPacket
		if err := json.Unmarshal(msg.Data, &packet); err != nil {
			return
		}
		// The subject also carries inter-agent chat; only act on task packets.
		if packet.Type != "task_delegation" || packet.TargetAgent == "" || packet.TaskID == "" {
			return
		}
		go func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			completion, herr := worker.HandleAndPublish(stdcontext.Background(), packet, pub)
			if herr != nil {
				log.Printf("[SUBAGENT] task %s publish error: %v", packet.TaskID, herr)
			} else {
				log.Printf("[SUBAGENT] task %s → %s (agent %s)", packet.TaskID, completion.Status, packet.TargetAgent)
			}
		}()
	})
	if err != nil {
		log.Printf("[SUBAGENT] subscribe to %s failed: %v", subAgentDelegationSubject, err)
		return
	}
	log.Printf("[SUBAGENT] worker started: %s → %s", subAgentDelegationSubject, subAgentCompletionSubject)
}

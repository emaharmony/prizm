package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/emaharmony/prizm/internal/api"
	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/freewill"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/policy"
	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/provider/codexcli"
	"github.com/emaharmony/prizm/internal/sandbox"
	multiagent "github.com/emaharmony/prizm/internal/workflow/multiagent"
	"github.com/nats-io/nats.go"
)

const freewillScheduleSubject = "prizm.freewill.scheduled"

type freewillServeRuntime struct {
	wake              *multiagent.FreewillWakeService
	approvals         *multiagent.FreewillApprovalService
	store             *multiagent.SQLiteDurableRunStore
	control           *multiagent.FreewillOperatorControl
	childApprovals    *approval.Store
	sandboxConfig     sandbox.Config
	events            *event.SQLiteEventStore
	eventsPath        string
	runDir            string
	configPath        string
	schedulingEnabled bool
	executionEnabled  bool
	readOnly          bool
	mu                sync.Mutex
	started           bool
	closed            bool
	sub               *nats.Subscription
	cancel            context.CancelFunc
	done              chan struct{}
	beforeSubscribe   func()
	resumeMu          sync.Mutex
	resumingChildren  map[string]bool
	childApprovalMu   sync.Mutex
}

type freewillReferenceExecutionDispatcher struct {
	runDir        string
	configPath    string
	approvals     *multiagent.FreewillApprovalService
	control       *multiagent.FreewillOperatorControl
	sandboxConfig sandbox.Config
}

func (d freewillReferenceExecutionDispatcher) Start(ctx context.Context, request multiagent.FreewillExecutionRequest) (multiagent.FreewillExecutionResult, error) {
	reconcile := func(ctx context.Context, childRunID string) error {
		return d.reconcileChild(ctx, request.ParentRunID, childRunID)
	}
	if d.sandboxConfig.Image == "" {
		return startFreewillReferenceRunWithExecutionControl(ctx, d.runDir, d.configPath, request, reconcile, d.control)
	}
	return startFreewillReferenceRunWithExecutionControl(ctx, d.runDir, d.configPath, request, reconcile, d.control, d.sandboxConfig)
}

func (d freewillReferenceExecutionDispatcher) reconcileChild(ctx context.Context, parentRunID, childRunID string) error {
	if d.approvals == nil {
		return errors.New("freewill: terminal child reconciler is unavailable")
	}
	result, err := freewillTerminalResultFromReferenceRun(ctx, d.runDir, childRunID)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 40; attempt++ {
		_, err = d.approvals.ReconcileExecutionTerminal(ctx, parentRunID, result)
		if !errors.Is(err, multiagent.ErrFreewillDispatchNotAccepted) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	return err
}

// openFreewillStatusRuntime exposes historical status only when Freewill is
// disabled. It never creates a database or constructs a provider/wake service.
func openFreewillStatusRuntime(cfg *orchestrator.Config, configPath string) (*freewillServeRuntime, error) {
	if cfg == nil {
		return nil, errors.New("freewill: host configuration is required")
	}
	dataDir := cfg.Prizm.DataDir
	if !filepath.IsAbs(dataDir) {
		dataDir = filepath.Join(filepath.Dir(configPath), dataDir)
	}
	dbPath := filepath.Join(dataDir, "freewill.db")
	if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("freewill: inspect existing status database: %w", err)
	}
	store, err := multiagent.OpenExistingSQLiteDurableRunStoreReadOnly(dbPath)
	if err != nil {
		return nil, err
	}
	statusReader, err := multiagent.NewFreewillApprovalStatusReader(store,
		approval.NewStore(resolveConfigPath(configPath, cfg.Prizm.RunsDir)))
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	return &freewillServeRuntime{approvals: statusReader, store: store, readOnly: true}, nil
}

func openFreewillServeRuntime(cfg *orchestrator.Config, providers *provider.ProviderRegistry, _ string, configPath string) (*freewillServeRuntime, error) {
	if cfg == nil || providers == nil || !cfg.Freewill.Enabled {
		return nil, errors.New("freewill: enabled host configuration and provider registry are required")
	}
	freewillCfg := cfg.Freewill
	if err := freewillCfg.ValidateHostBinding(cfg.Agents, cfg.Projects); err != nil {
		return nil, err
	}
	var projectPath string
	for _, project := range cfg.Projects {
		if project.ID == freewillCfg.ProjectID {
			projectPath = project.RepoPath
			break
		}
	}
	if !filepath.IsAbs(projectPath) {
		return nil, errors.New("freewill: configured project repository path must be absolute")
	}
	projectPath, err := filepath.EvalSymlinks(projectPath)
	if err != nil {
		return nil, errors.New("freewill: configured project repository is unavailable")
	}
	projectInfo, err := os.Stat(projectPath)
	if err != nil || !projectInfo.IsDir() {
		return nil, errors.New("freewill: configured project repository is unavailable")
	}
	var sandboxConfig sandbox.Config
	if freewillCfg.Execution.Enabled {
		sandboxConfig, err = freewillSandboxConfigFromEnvironment(projectPath)
		if err != nil {
			return nil, err
		}
	}
	var selectorAgent *orchestrator.AgentConfig
	for i := range cfg.Agents {
		if cfg.Agents[i].ID == freewillCfg.AgentID {
			selectorAgent = &cfg.Agents[i]
			break
		}
	}
	if selectorAgent == nil {
		return nil, errors.New("freewill: configured selector agent is unavailable")
	}
	model := strings.TrimSpace(freewillCfg.Model)
	if model == "" {
		model = selectorAgent.Model
	}
	chatProvider, err := resolveFreewillSelectorProvider(cfg, providers, *selectorAgent, model)
	if err != nil {
		return nil, fmt.Errorf("freewill: configured selector provider is unavailable: %w", err)
	}

	policyDir := freewillCfg.PolicyDir
	if policyDir == "" {
		policyDir = "policies"
	}
	if !filepath.IsAbs(policyDir) {
		policyDir = filepath.Join(filepath.Dir(configPath), policyDir)
	}
	policyRegistry := policy.NewRegistry()
	if _, err := policyRegistry.LoadFromDir(policyDir); err != nil {
		return nil, fmt.Errorf("freewill: configured policy directory is unavailable: %w", err)
	}

	dataDir := cfg.Prizm.DataDir
	if !filepath.IsAbs(dataDir) {
		dataDir = filepath.Join(filepath.Dir(configPath), dataDir)
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("freewill: create durable data directory: %w", err)
	}
	dbPath := filepath.Join(dataDir, "freewill.db")
	store, err := multiagent.NewSQLiteDurableRunStore(dbPath)
	if err != nil {
		return nil, fmt.Errorf("freewill: open durable run store: %w", err)
	}
	events, err := event.NewSQLiteEventStore(dbPath)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("freewill: open durable event store: %w", err)
	}
	graph, err := multiagent.CompatAdaptDefinition(multiagent.DefaultReferenceDefinition())
	if err != nil {
		_ = events.Close()
		_ = store.Close()
		return nil, fmt.Errorf("freewill: compile non-dispatch graph guard: %w", err)
	}
	if strings.TrimSpace(cfg.Prizm.RunsDir) == "" {
		_ = events.Close()
		_ = store.Close()
		return nil, errors.New("freewill: durable claim root is empty")
	}
	runtime, err := multiagent.NewDurableRuntime(graph, unavailableRoleRunner{}, store,
		multiagent.FileRunClaimer{Root: resolveConfigPath(configPath, cfg.Prizm.RunsDir)}, events, multiagent.DurableRuntimeOptions{})
	if err != nil {
		_ = events.Close()
		_ = store.Close()
		return nil, fmt.Errorf("freewill: compose durable runtime: %w", err)
	}
	approvalStore := approval.NewStore(resolveConfigPath(configPath, cfg.Prizm.RunsDir))
	policyApproval, err := multiagent.NewFreewillApprovalService(runtime, store,
		approvalStore, policy.NewEvaluator(policyRegistry), "freewill-policy")
	if err != nil {
		_ = events.Close()
		_ = store.Close()
		return nil, err
	}
	control, err := multiagent.NewFreewillOperatorControl(store, nil)
	if err != nil {
		_ = events.Close()
		_ = store.Close()
		return nil, fmt.Errorf("freewill: compose durable operator control: %w", err)
	}
	if freewillCfg.Execution.Enabled {
		policyApproval.SetExecutionDispatcher(freewillReferenceExecutionDispatcher{
			runDir: resolveConfigPath(configPath, cfg.Prizm.RunsDir), configPath: configPath,
			approvals: policyApproval, control: control, sandboxConfig: sandboxConfig,
		})
	}
	wake, err := multiagent.NewFreewillWakeService(runtime, store,
		multiagent.ChatFreewillSelector{Provider: chatProvider, AgentID: selectorAgent.ID, Model: model,
			ReasoningEffort: freewillFirstNonEmpty(freewillCfg.ReasoningEffort, selectorAgent.ReasoningEffort)},
		multiagent.FreewillWakeServiceConfig{Enabled: true, SchedulingEnabled: freewillCfg.Scheduling.Enabled,
			ExecutionEnabled: freewillCfg.Execution.Enabled,
			AgentID:          selectorAgent.ID, Scope: freewill.Scope{ProjectID: freewillCfg.ProjectID, Workspace: projectPath},
			Objective: freewillCfg.Objective,
			Owner:     "prizm-freewill-scheduler", LeaseFor: 2 * time.Minute, OperatorControl: control}, nil)
	if err != nil {
		_ = events.Close()
		_ = store.Close()
		return nil, err
	}
	return &freewillServeRuntime{wake: wake, approvals: policyApproval, store: store, control: control, events: events, eventsPath: dbPath,
		childApprovals: approvalStore, sandboxConfig: sandboxConfig,
		runDir: resolveConfigPath(configPath, cfg.Prizm.RunsDir), configPath: configPath, resumingChildren: make(map[string]bool),
		schedulingEnabled: freewillCfg.Scheduling.Enabled, executionEnabled: freewillCfg.Execution.Enabled}, nil
}

func freewillSandboxConfigFromEnvironment(sourceRoot string) (sandbox.Config, error) {
	dockerPath := strings.TrimSpace(os.Getenv("PRIZM_SUBAGENT_SANDBOX_DOCKER"))
	if dockerPath == "" {
		dockerPath = "docker"
	}
	if _, err := exec.LookPath(dockerPath); err != nil {
		return sandbox.Config{}, errors.New("freewill: execution requires an available local OCI engine")
	}
	image := strings.TrimSpace(os.Getenv("PRIZM_SUBAGENT_SANDBOX_IMAGE"))
	if !pinnedSandboxImage(image) {
		return sandbox.Config{}, errors.New("freewill: execution requires a sandbox image pinned by sha256 digest")
	}
	workRoot := strings.TrimSpace(os.Getenv("PRIZM_SUBAGENT_SANDBOX_WORK_ROOT"))
	artifactRoot := strings.TrimSpace(os.Getenv("PRIZM_SUBAGENT_SANDBOX_ARTIFACT_ROOT"))
	if !filepath.IsAbs(workRoot) || !filepath.IsAbs(artifactRoot) || freewillSandboxPathsOverlap(sourceRoot, workRoot) || freewillSandboxPathsOverlap(sourceRoot, artifactRoot) {
		return sandbox.Config{}, errors.New("freewill: execution requires absolute sandbox storage roots outside the project")
	}
	return sandbox.Config{DockerPath: dockerPath, Image: image, WorkRoot: workRoot, ArtifactRoot: artifactRoot}, nil
}

func pinnedSandboxImage(image string) bool {
	name, digest, ok := strings.Cut(image, "@sha256:")
	if !ok || name == "" || len(digest) != 64 {
		return false
	}
	for _, char := range digest {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func freewillSandboxPathsOverlap(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(filepath.Clean(left))
	rightAbs, rightErr := filepath.Abs(filepath.Clean(right))
	if leftErr != nil || rightErr != nil {
		return true
	}
	leftRel, leftRelErr := filepath.Rel(leftAbs, rightAbs)
	rightRel, rightRelErr := filepath.Rel(rightAbs, leftAbs)
	return leftRelErr != nil || rightRelErr != nil || leftRel == "." || rightRel == "." ||
		(leftRel != ".." && !strings.HasPrefix(leftRel, ".."+string(filepath.Separator))) ||
		(rightRel != ".." && !strings.HasPrefix(rightRel, ".."+string(filepath.Separator)))
}

func sameFreewillSandboxConfig(left, right *sandbox.Config) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (r *freewillServeRuntime) ListChildApprovals(ctx context.Context, parentRunID, childRunID string) ([]api.FreewillChildApproval, error) {
	if _, _, err := r.validateFreewillChild(ctx, parentRunID, childRunID, "", false); err != nil {
		return nil, err
	}
	items, err := r.childApprovals.List(childRunID)
	if err != nil {
		return nil, err
	}
	result := make([]api.FreewillChildApproval, 0, len(items))
	for _, item := range items {
		if item == nil || item.Status != approval.StatusPending || item.MutationType != approval.MutationApplyPatch ||
			item.PatchPlan == nil || item.SandboxBinding == nil {
			continue
		}
		projected, err := freewillChildApprovalProjection(parentRunID, childRunID, item)
		if err != nil {
			return nil, err
		}
		result = append(result, projected)
	}
	return result, nil
}

func (r *freewillServeRuntime) DecideChildApproval(ctx context.Context, parentRunID, childRunID, approvalID, decision, actor, reason string) (api.FreewillChildApproval, error) {
	if !r.executionEnabled || r.readOnly || r.childApprovals == nil || strings.TrimSpace(actor) == "" ||
		(decision != "grant" && decision != "deny") {
		return api.FreewillChildApproval{}, errors.New("freewill: child approval is not enabled")
	}
	r.childApprovalMu.Lock()
	defer r.childApprovalMu.Unlock()
	item, _, err := r.validateFreewillChild(ctx, parentRunID, childRunID, approvalID, true)
	if err != nil {
		return api.FreewillChildApproval{}, err
	}
	if item.Status != approval.StatusPending {
		return api.FreewillChildApproval{}, errors.New("freewill: child approval is no longer pending")
	}
	if decision == "grant" {
		if err := item.Approve(actor); err != nil {
			return api.FreewillChildApproval{}, err
		}
	} else if err := item.Deny(actor, safeFreewillApprovalReason(reason)); err != nil {
		return api.FreewillChildApproval{}, err
	}
	if err := r.childApprovals.Save(item); err != nil {
		return api.FreewillChildApproval{}, err
	}
	if err := r.resumeAcceptedChild(ctx, parentRunID, childRunID); err != nil {
		return api.FreewillChildApproval{}, err
	}
	return freewillChildApprovalProjection(parentRunID, childRunID, item)
}

func (r *freewillServeRuntime) validateFreewillChild(ctx context.Context, parentRunID, childRunID, approvalID string, requireApproval bool) (*approval.Approval, referenceWorkflowManifest, error) {
	if r == nil || r.readOnly || !r.executionEnabled || r.store == nil || r.childApprovals == nil ||
		strings.TrimSpace(parentRunID) == "" || strings.TrimSpace(childRunID) == "" {
		return nil, referenceWorkflowManifest{}, errors.New("freewill: child approval service is unavailable")
	}
	parent, err := r.store.Load(ctx, parentRunID)
	if err != nil {
		return nil, referenceWorkflowManifest{}, err
	}
	if parent.Freewill == nil || parent.Freewill.ApprovalReceipt == nil || parent.Freewill.ApprovalReceipt.Decision != multiagent.FreewillApprovalGranted ||
		parent.Freewill.ExecutionDispatch == nil || parent.Freewill.ExecutionDispatch.ChildRunID != childRunID ||
		parent.Freewill.ExecutionDispatch.Status != "accepted" {
		return nil, referenceWorkflowManifest{}, multiagent.ErrFreewillApprovalStale
	}
	manifest, err := loadReferenceManifest(r.runDir, childRunID)
	if err != nil || manifest.RunID != childRunID || !manifest.SandboxApprovalMode || manifest.FreewillSandboxConfig == nil ||
		manifest.SourceWorkspacePath == "" || manifest.WorkspacePath == "" || filepath.Clean(manifest.SourceWorkspacePath) != filepath.Clean(parent.Freewill.Scope.Workspace) {
		return nil, referenceWorkflowManifest{}, multiagent.ErrFreewillApprovalStale
	}
	inspection, err := openInspectionReferenceRuntime(r.runDir, manifest)
	if err != nil {
		return nil, referenceWorkflowManifest{}, err
	}
	child, inspectErr := inspection.runtime.Inspect(ctx, childRunID)
	closeErr := inspection.close()
	if inspectErr != nil || closeErr != nil {
		return nil, referenceWorkflowManifest{}, errors.Join(inspectErr, closeErr)
	}
	if child.State.Status.Terminal() || requireApproval && child.State.Status != multiagent.RunStatusPaused {
		return nil, referenceWorkflowManifest{}, multiagent.ErrFreewillApprovalStale
	}
	if approvalID == "" {
		return nil, manifest, nil
	}
	item, err := r.childApprovals.Load(childRunID, approvalID)
	if err != nil {
		return nil, referenceWorkflowManifest{}, err
	}
	if item.RunID != childRunID || item.ApprovalID != approvalID || item.Status != approval.StatusPending ||
		item.MutationType != approval.MutationApplyPatch || item.PatchPlan == nil || item.SandboxBinding == nil ||
		item.SandboxBinding.AgentID != item.RequestedBy || item.SandboxBinding.ExecutionKey != item.CorrelationID ||
		item.SandboxBinding.WorkspacePath != manifest.WorkspacePath || item.SandboxBinding.WorkspaceID != manifest.WorkspaceID ||
		item.SandboxBinding.ArtifactDigest == "" {
		return nil, referenceWorkflowManifest{}, multiagent.ErrFreewillApprovalStale
	}
	return item, manifest, nil
}

func freewillChildApprovalProjection(parentRunID, childRunID string, item *approval.Approval) (api.FreewillChildApproval, error) {
	if item == nil || item.MutationType != approval.MutationApplyPatch || item.PatchPlan == nil || len(item.Content) > 1<<20 {
		return api.FreewillChildApproval{}, errors.New("freewill: child patch is unavailable or exceeds the review limit")
	}
	return api.FreewillChildApproval{ParentRunID: parentRunID, ChildRunID: childRunID, ApprovalID: item.ApprovalID,
		ProposalID: item.ProposalID, Status: string(item.Status), RequestedBy: item.RequestedBy,
		TargetPath: strings.Join(item.PatchPlan.Paths, ", "), Patch: item.Content,
		SandboxBinding: api.FreewillChildSandboxBinding{TaskID: item.SandboxBinding.TaskID,
			ExecutionKey: item.SandboxBinding.ExecutionKey, AgentID: item.SandboxBinding.AgentID,
			WorkspaceID: item.SandboxBinding.WorkspaceID, WorkspacePath: item.SandboxBinding.WorkspacePath,
			BaseSHA: item.SandboxBinding.BaseSHA, BaseTree: item.SandboxBinding.BaseTree,
			ArtifactDigest: item.SandboxBinding.ArtifactDigest},
		PatchPlan: api.FreewillChildPatchPlan{BaseSHA: item.PatchPlan.BaseSHA, BaseTree: item.PatchPlan.BaseTree,
			ExpectedTree: item.PatchPlan.ExpectedTree, PatchSHA256: item.PatchPlan.PatchSHA256,
			Paths: append([]string(nil), item.PatchPlan.Paths...)}}, nil
}

func safeFreewillApprovalReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if len(reason) > 256 {
		return reason[:256]
	}
	return reason
}

func resolveFreewillSelectorProvider(cfg *orchestrator.Config, providers *provider.ProviderRegistry, agent orchestrator.AgentConfig, model string) (provider.ChatProvider, error) {
	if cfg == nil || providers == nil {
		return nil, errors.New("selector configuration is incomplete")
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, errors.New("selector model is required")
	}
	if len(agent.Fallbacks) != 0 {
		return nil, errors.New("selector fallback targets are not allowed")
	}
	switch agent.Provider {
	case "codex":
		if strings.TrimSpace(cfg.Codex.Profile) != "" {
			return nil, errors.New("codex selector profiles are not allowed")
		}
		if len(cfg.Codex.ExtraArgs) != 0 {
			return nil, errors.New("codex selector extra arguments are not allowed")
		}
		configuredModel := strings.TrimSpace(cfg.Codex.Model)
		if configuredModel == "" {
			configuredModel = model
		}
		if configuredModel != model {
			return nil, errors.New("codex selector model must match the configured Freewill model")
		}
		selectorCfg := codexcli.NormalizeConstrainedSelectorConfig(codexcli.ConstrainedSelectorConfig{
			Executable: cfg.Codex.Executable, Model: configuredModel,
			TimeoutMinutes: cfg.Codex.TimeoutMinutes, OutputSchema: multiagent.FreewillSelectionOutputSchema(),
		})
		if _, err := exec.LookPath(selectorCfg.Executable); err != nil {
			return nil, fmt.Errorf("codex CLI executable %q not found: %w", selectorCfg.Executable, err)
		}
		return codexcli.NewConstrainedSelector(selectorCfg)
	case "ollama":
		info, err := providers.ModelInfo(model)
		if err != nil {
			return nil, err
		}
		if info.ID != model || info.ProviderName != "ollama" {
			return nil, errors.New("selector model registry binding does not match Ollama")
		}
		direct, err := providers.Get(model)
		if err != nil {
			return nil, err
		}
		chat, ok := direct.(provider.ChatProvider)
		if !ok {
			return nil, errors.New("configured Ollama selector does not support chat generation")
		}
		return chat, nil
	default:
		return nil, errors.New("selector provider must be codex or ollama")
	}
}

func (r *freewillServeRuntime) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	sub, cancel, done := r.sub, r.cancel, r.done
	r.sub, r.cancel, r.done = nil, nil, nil
	r.mu.Unlock()
	if sub != nil {
		_ = sub.Unsubscribe()
	}
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	if r.events != nil {
		_ = r.events.Close()
	}
	if r.store != nil {
		_ = r.store.Close()
	}
}

func (r *freewillServeRuntime) Start(ctx context.Context, nc *nats.Conn, schedulingEnabled bool) error {
	if r == nil {
		return errors.New("freewill: serve runtime is incomplete")
	}
	if r.readOnly {
		return nil
	}
	if r.wake == nil || r.approvals == nil || (schedulingEnabled && nc == nil) {
		return errors.New("freewill: serve runtime is incomplete")
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return errors.New("freewill: serve runtime is closed")
	}
	if r.started {
		r.mu.Unlock()
		return nil
	}
	// A scheduled wake must finish its durable decision even if the caller's
	// request context is canceled after the subscription has been installed.
	workerCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	r.started, r.cancel, r.done = true, cancel, make(chan struct{})
	done := r.done
	r.mu.Unlock()
	fail := func(err error) error {
		cancel()
		r.mu.Lock()
		r.started, r.cancel, r.done = false, nil, nil
		r.mu.Unlock()
		close(done)
		return err
	}
	if schedulingEnabled {
		if err := nc.Flush(); err != nil {
			return fail(fmt.Errorf("freewill: NATS is not ready: %w", err))
		}
		if r.beforeSubscribe != nil {
			r.beforeSubscribe()
		}
		sub, err := nc.Subscribe(freewillScheduleSubject, func(msg *nats.Msg) {
			select {
			case <-workerCtx.Done():
				return
			default:
			}
			if err := runFreewillScheduledTick(workerCtx, r.recoverPending, func(ctx context.Context) error {
				// The scheduler message carries no scope, identity, candidate, or policy
				// authority. Those values come only from the validated host config.
				run, runErr := r.wake.HandleScheduledTick(ctx, time.Now().UTC())
				if runErr != nil {
					return runErr
				}
				if run.Freewill != nil && run.Freewill.Lifecycle == freewill.StateProposed {
					if _, approvalErr := r.approvals.RequestApproval(ctx, run.State.RunID); approvalErr != nil {
						log.Printf("[FREEWILL] approval proposal could not be recorded: %v", approvalErr)
					}
				}
				return nil
			}); err != nil {
				log.Printf("[FREEWILL] scheduled tick deferred: %v", err)
			}
		})
		if err != nil {
			return fail(fmt.Errorf("freewill: subscribe to scheduled wake: %w", err))
		}
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			_ = sub.Unsubscribe()
			cancel()
			close(done)
			return errors.New("freewill: serve runtime closed during start")
		}
		r.sub = sub
		r.mu.Unlock()
		if err := nc.Flush(); err != nil {
			_ = sub.Unsubscribe()
			return fail(fmt.Errorf("freewill: scheduled wake subscription did not become ready: %w", err))
		}
	}
	go func() {
		defer close(done)
		if err := r.recoverPending(workerCtx); err != nil && workerCtx.Err() == nil {
			log.Printf("[FREEWILL] recovery scan failed: %v", err)
		}
	}()
	return nil
}

// runFreewillScheduledTick resumes durable accepted work before admitting a
// new intention. A recovery error defers this tick so a later message retries
// the same parent/child instead of overlapping it with new work.
func runFreewillScheduledTick(ctx context.Context, recoverPending func(context.Context) error, handleTick func(context.Context) error) error {
	if err := recoverPending(ctx); err != nil {
		return fmt.Errorf("recover accepted Freewill work before scheduled wake: %w", err)
	}
	return handleTick(ctx)
}

func (r *freewillServeRuntime) recoverPending(ctx context.Context) error {
	if r.schedulingEnabled {
		if err := r.wake.RecoverPending(ctx, time.Now().UTC(), 500); err != nil {
			return err
		}
	}
	lister := multiagent.FreewillRunLister(r.store)
	after := ""
	for {
		page, err := lister.ListFreewillRuns(ctx, after, 500)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return nil
		}
		for _, record := range page {
			if record.Freewill != nil && record.Freewill.Lifecycle == freewill.StateProposed {
				if _, err := r.approvals.RequestApproval(ctx, record.State.RunID); err != nil {
					return err
				}
			} else if record.Freewill != nil && (record.Freewill.Lifecycle == freewill.StateApprovalPending || (record.Freewill.Lifecycle == freewill.StatePaused && record.Freewill.PauseReason == "execution_disabled" && record.Freewill.ApprovalRef != "" && record.Freewill.ApprovalReceipt == nil)) {
				if _, err := r.approvals.RecoverApprovalDecision(ctx, record.State.RunID); err != nil {
					return err
				}
			}
			if r.executionEnabled {
				recovered, err := r.store.Load(ctx, record.State.RunID)
				if err != nil {
					return err
				}
				if recovered.Freewill != nil && recovered.Freewill.ApprovalReceipt != nil &&
					recovered.Freewill.ApprovalReceipt.Decision == multiagent.FreewillApprovalGranted {
					dispatchStatus := ""
					if recovered.Freewill.ExecutionDispatch != nil {
						dispatchStatus = recovered.Freewill.ExecutionDispatch.Status
					}
					switch dispatchStatus {
					case "accepted":
						if err := r.resumeAcceptedChild(ctx, recovered.State.RunID, recovered.Freewill.ExecutionDispatch.ChildRunID); err != nil {
							return err
						}
					case "completed", "execution_failed":
						// The terminal child outcome is already durable on the parent.
					case "execution_cancelled":
						// Operator halt permanently canceled this approved child.
					case "dispatching", "failed":
						if _, err := r.approvals.DispatchApproved(ctx, record.State.RunID); err != nil {
							return err
						}
					default:
						// A granted record without a durable dispatch marker was
						// decision-only; enabling execution later cannot dispatch it.
					}
				}
			}
		}
		after = page[len(page)-1].State.RunID
		if len(page) < 500 {
			return nil
		}
	}
}

func (r *freewillServeRuntime) resumeAcceptedChild(ctx context.Context, parentRunID, childRunID string) error {
	if strings.TrimSpace(r.runDir) == "" || strings.TrimSpace(r.configPath) == "" || strings.TrimSpace(childRunID) == "" {
		return errors.New("freewill: accepted child recovery paths are unavailable")
	}
	parent, err := r.store.Load(ctx, parentRunID)
	if err != nil {
		return fmt.Errorf("freewill: load accepted parent %s: %w", parentRunID, err)
	}
	if parent.Freewill == nil || parent.State.RequestIdentity == nil || parent.Freewill.ExecutionDispatch == nil ||
		parent.Freewill.ExecutionDispatch.Status != "accepted" || parent.Freewill.ExecutionDispatch.ChildRunID != childRunID {
		return multiagent.ErrFreewillApprovalStale
	}
	agentID, scope, generation := parent.State.RequestIdentity.RequesterID, parent.Freewill.Scope, parent.Freewill.ApprovalGeneration
	terminal, err := freewillReferenceChildIsTerminal(ctx, r.runDir, childRunID)
	if err != nil {
		return fmt.Errorf("freewill: inspect accepted child %s before recovery: %w", childRunID, err)
	}
	if !terminal {
		if r.approvals == nil {
			return errors.New("freewill: approval service is unavailable for child recovery")
		}
		if err := r.approvals.ValidateAcceptedExecutionResume(ctx, parentRunID); err != nil {
			if !errors.Is(err, multiagent.ErrFreewillHalted) && !errors.Is(err, multiagent.ErrFreewillApprovalRevoked) {
				return fmt.Errorf("freewill: operator controls block resuming accepted child %s: %w", childRunID, err)
			}
			reason := "freewill approval generation revoked"
			if errors.Is(err, multiagent.ErrFreewillHalted) {
				reason = "freewill operator emergency halt"
			}
			_, cancelErr := cancelFreewillReferenceChild(ctx, r.runDir, childRunID, reason)
			if cancelErr != nil {
				return fmt.Errorf("freewill: durably cancel accepted child %s after control change: %w", childRunID, cancelErr)
			}
			// Resume consumes an interrupted child cancellation request through the
			// workflow's existing reconciliation path. If another process still
			// owns the claim, its registered context callback is already canceled.
			terminal = true
		}
	}
	r.resumeMu.Lock()
	if r.resumingChildren == nil {
		r.resumingChildren = make(map[string]bool)
	}
	if r.resumingChildren[childRunID] {
		r.resumeMu.Unlock()
		return nil
	}
	r.resumingChildren[childRunID] = true
	r.resumeMu.Unlock()
	if err := resumeFreewillReferenceChildWithExecutionControl(r.runDir, r.configPath, childRunID,
		func(ctx context.Context, actualChildRunID string) error {
			return (freewillReferenceExecutionDispatcher{runDir: r.runDir, configPath: r.configPath, approvals: r.approvals,
				control: r.control}).reconcileChild(ctx, parentRunID, actualChildRunID)
		}, func() {
			r.resumeMu.Lock()
			delete(r.resumingChildren, childRunID)
			r.resumeMu.Unlock()
		}, r.control, agentID, scope, generation); err != nil {
		r.resumeMu.Lock()
		delete(r.resumingChildren, childRunID)
		r.resumeMu.Unlock()
		return fmt.Errorf("freewill: recover accepted child for parent %s: %w", parentRunID, err)
	}
	return nil
}

func cancelFreewillReferenceChild(ctx context.Context, runDir, childRunID, reason string) (multiagent.RunState, error) {
	manifest, err := loadReferenceManifest(runDir, childRunID)
	if err != nil {
		return multiagent.RunState{}, err
	}
	inspection, err := openInspectionReferenceRuntime(runDir, manifest)
	if err != nil {
		return multiagent.RunState{}, err
	}
	state, cancelErr := inspection.runtime.Cancel(ctx, childRunID, reason)
	return state, errors.Join(cancelErr, inspection.close())
}

func freewillFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func resolveConfigPath(configPath, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(filepath.Dir(configPath), path))
}

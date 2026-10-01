package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/emaharmony/prizm/internal/agent"
	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/gitx"
	"github.com/emaharmony/prizm/internal/memory"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/safety"
	"github.com/emaharmony/prizm/internal/subagent"
	"github.com/emaharmony/prizm/internal/tool"
	"github.com/emaharmony/prizm/internal/validation"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
	v2 "github.com/emaharmony/prizm/internal/workflow/v2"
	"runtime"
)

const referenceManifestSchemaVersion = 2

// referenceWorkflowManifest is the on-disk shape of every durable multiagent
// run this package starts, whether via the legacy `prism workflow run`
// reference-workflow path or PR6's `prism graph run` registry-backed path.
// WorkflowVersion/DefinitionDBPath are PR6 additions (additive, omitempty):
// WorkflowVersion == 0 (the pre-PR6 zero value) means "legacy run, build the
// graph from the embedded Definition via CompatAdaptDefinition"; > 0 means
// "registry-backed run, resolve (WorkflowID, WorkflowVersion) through the
// DefinitionStore at DefinitionDBPath instead" (Definition is left at its
// zero value for that case). This is what lets loadReferenceManifest/
// openReferenceRuntime — and therefore the EXISTING `prism workflow
// status/cancel/resume` commands and MultiAgentController's API-facing
// Resume/Cancel/Pause — work unmodified against a `graph run`-started run:
// they already only ever go through these two functions, which now branch
// internally on WorkflowVersion instead of assuming a legacy Definition is
// always present. See run_locator.go's parallel runManifest extension for
// the same discriminator applied to the RunLocator-based read paths
// (ListRuns/OpenInspection).
type referenceWorkflowManifest struct {
	SchemaVersion       int                               `json:"schema_version"`
	RunID               string                            `json:"run_id"`
	WorkflowID          string                            `json:"workflow_id"`
	Input               multiagent.ReferenceWorkflowInput `json:"input"`
	Definition          multiagent.Definition             `json:"definition"`
	WorkspaceID         string                            `json:"workspace_id"`
	WorkspacePath       string                            `json:"workspace_path"`
	SourceWorkspacePath string                            `json:"source_workspace_path,omitempty"`
	WorkspaceCleaned    bool                              `json:"workspace_cleaned,omitempty"`
	WorkflowVersion     int64                             `json:"workflow_version,omitempty"`
	DefinitionDBPath    string                            `json:"definition_db_path,omitempty"`
}

// registryBacked reports whether m describes a PR6 `graph run`-started run
// rather than a legacy reference-workflow run.
func (m referenceWorkflowManifest) registryBacked() bool {
	return m.WorkflowVersion > 0
}

type referenceRuntime struct {
	runtime *multiagent.DurableRuntime
	store   *multiagent.SQLiteDurableRunStore
	events  *event.SQLiteEventStore
}

func (r *referenceRuntime) close() error {
	return errors.Join(r.store.Close(), r.events.Close())
}

func executeReferenceWorkflowRun(inputFile, runDir, configPath string) error {
	input, err := loadReferenceWorkflowInput(inputFile)
	if err != nil {
		return err
	}
	cfg, err := orchestrator.LoadConfig(configPath)
	if err != nil {
		return fmt.Errorf("load Prizm config: %w", err)
	}
	definition, err := multiagent.ApplyReferenceOverrides(
		multiagent.DefaultReferenceDefinition(), input,
	)
	if err != nil {
		return err
	}
	bindConfiguredReferenceProfiles(&definition, input.RoleProfiles, cfg.Agents)
	runID := event.NewRunID()
	workspacePath, workspaceID, sourceWorkspacePath, err := isolatedReferenceWorkspace(context.Background(), input.Workspace, runID)
	if err != nil {
		return err
	}
	manifest := referenceWorkflowManifest{
		SchemaVersion: referenceManifestSchemaVersion,
		RunID:         runID, WorkflowID: multiagent.ReferenceWorkflowID,
		Input: input, Definition: definition,
		WorkspaceID: workspaceID, WorkspacePath: workspacePath, SourceWorkspacePath: sourceWorkspacePath,
	}
	if err := writeReferenceManifest(runDir, manifest); err != nil {
		return err
	}

	// The identifier is durable before any provider call so operators can
	// inspect, cancel, or resume an interrupted invocation.
	fmt.Printf("Run ID: %s\n", runID)
	fmt.Printf("Artifacts: %s\n", filepath.Join(runDir, runID))

	runtime, err := openLiveReferenceRuntime(runDir, configPath, manifest)
	if err != nil {
		return err
	}
	defer runtime.close()
	state, runErr := runtime.runtime.Run(context.Background(), multiagent.RunRequest{
		RunID: runID,
		Task: multiagent.TaskReference{
			ID: "task_" + runID, Description: multiagent.ReferenceTaskDescription(input),
		},
	})
	if state.RunID != "" && state.Status.Terminal() {
		if err := finalizeTerminalReferenceRun(context.Background(), runDir, &manifest, runtime, state); err != nil {
			return errors.Join(runErr, err)
		}
	}
	fmt.Printf("Status: %s\n", state.Status)
	return runErr
}

// bindConfiguredReferenceProfiles adapts the fixed reference graph's
// illustrative profile names to the configured local agent roster. Explicit
// user role-profile overrides remain authoritative. This is composition, not a
// workflow transition rule: the graph retains its bounded role capabilities.
func bindConfiguredReferenceProfiles(definition *multiagent.Definition, explicit map[multiagent.Role]string, agents []orchestrator.AgentConfig) {
	if definition == nil {
		return
	}
	for index := range definition.Roles {
		role := definition.Roles[index].Role
		if _, overridden := explicit[role]; overridden {
			continue
		}
		if profile := configuredProfileForRole(role, definition.Roles[index].Capabilities, agents); profile != "" {
			definition.Roles[index].AgentRef = profile
		}
	}
}

func configuredProfileForRole(role multiagent.Role, required []string, agents []orchestrator.AgentConfig) string {
	bestID, bestScore := "", -1
	for _, candidate := range agents {
		if strings.TrimSpace(candidate.ID) == "" || !agentHasCapabilities(candidate, required) {
			continue
		}
		score := 0
		candidateRole := strings.ToLower(strings.TrimSpace(candidate.Role))
		if candidateRole == string(role) {
			score += 100
		}
		if role == multiagent.RoleDeveloper && candidateRole == "coder" {
			score += 90
		}
		if candidate.Primary {
			score++
		}
		if score > bestScore {
			bestID, bestScore = candidate.ID, score
		}
	}
	return bestID
}

func agentHasCapabilities(candidate orchestrator.AgentConfig, required []string) bool {
	available := make(map[string]struct{}, len(candidate.Capabilities))
	for _, capability := range candidate.Capabilities {
		available[capability] = struct{}{}
	}
	for _, capability := range required {
		if _, ok := available[capability]; !ok {
			return false
		}
	}
	return true
}

func executeReferenceWorkflowStatus(runID, runDir string, jsonOutput bool) error {
	manifest, err := loadReferenceManifest(runDir, runID)
	if err != nil {
		return err
	}
	runtime, err := openInspectionReferenceRuntime(runDir, manifest)
	if err != nil {
		return err
	}
	defer runtime.close()
	record, err := runtime.runtime.Inspect(context.Background(), runID)
	if err != nil {
		return err
	}
	events, err := runtime.events.Query(context.Background(), event.EventFilter{RunID: runID, Limit: 10_000})
	if err != nil {
		return err
	}
	if len(events) > 20 {
		events = events[len(events)-20:]
	}
	snapshot := multiagent.BuildReferenceRunSnapshot(record.State, events)
	if jsonOutput {
		return writeJSON(os.Stdout, snapshot)
	}
	fmt.Printf("Workflow: %s\nRun ID: %s\nStatus: %s\n", snapshot.WorkflowID, runID, snapshot.Status)
	if snapshot.CurrentRole != "" && !snapshot.Status.Terminal() {
		fmt.Printf("Current role: %s\n", snapshot.CurrentRole)
	}
	fmt.Printf("Transitions: %d\nTokens: %d\n", snapshot.TransitionCount, snapshot.BudgetUsage.Tokens.TotalTokens)
	if snapshot.TerminalOutcome != nil {
		fmt.Printf("Outcome: %s (%s)\n", snapshot.TerminalOutcome.Condition, snapshot.TerminalOutcome.Reason)
	}
	return nil
}

func executeReferenceWorkflowCancel(runID, runDir, reason string) error {
	manifest, err := loadReferenceManifest(runDir, runID)
	if err != nil {
		return err
	}
	runtime, err := openInspectionReferenceRuntime(runDir, manifest)
	if err != nil {
		return err
	}
	defer runtime.close()
	state, err := runtime.runtime.Cancel(context.Background(), runID, reason)
	if err != nil {
		return err
	}
	if state.Status.Terminal() {
		if err := finalizeTerminalReferenceRun(context.Background(), runDir, &manifest, runtime, state); err != nil {
			return err
		}
		fmt.Printf("Run %s cancelled.\n", runID)
	} else {
		fmt.Printf("Cancellation requested for active run %s; it will stop at the next role boundary.\n", runID)
	}
	return nil
}

func executeReferenceWorkflowResume(runID, runDir, configPath string) error {
	manifest, err := loadReferenceManifest(runDir, runID)
	if err != nil {
		return err
	}
	runtime, err := openLiveReferenceRuntime(runDir, configPath, manifest)
	if err != nil {
		return err
	}
	defer runtime.close()
	fmt.Printf("Run ID: %s\n", runID)
	state, resumeErr := runtime.runtime.Resume(context.Background(), runID)
	if state.RunID != "" && state.Status.Terminal() {
		if err := finalizeTerminalReferenceRun(context.Background(), runDir, &manifest, runtime, state); err != nil {
			return errors.Join(resumeErr, err)
		}
	}
	fmt.Printf("Status: %s\n", state.Status)
	return resumeErr
}

func executeReferenceWorkflowReport(runID, runDir string, jsonOutput bool) error {
	manifest, err := loadReferenceManifest(runDir, runID)
	if err != nil {
		return err
	}
	runtime, err := openInspectionReferenceRuntime(runDir, manifest)
	if err != nil {
		return err
	}
	defer runtime.close()
	record, err := runtime.runtime.Inspect(context.Background(), runID)
	if err != nil {
		return err
	}
	if !record.State.Status.Terminal() {
		return fmt.Errorf("multi-agent report is available only for terminal runs (current status: %s)", record.State.Status)
	}
	events, err := runtime.events.Query(context.Background(), event.EventFilter{RunID: runID, Limit: 10_000})
	if err != nil {
		return err
	}
	report := multiagent.BuildReferenceRunReport(manifest.Input, record.State, events)
	if err := finalizeTerminalReferenceRun(context.Background(), runDir, &manifest, runtime, record.State); err != nil {
		return err
	}
	if jsonOutput {
		return writeJSON(os.Stdout, report)
	}
	fmt.Printf("Run ID: %s\nStatus: %s\nObjective: %s\n", report.RunID, report.FinalStatus, report.OriginalObjective)
	fmt.Printf("Plan: %s\nImplementation: %s\nTests: %s\nReview: %s\n", report.PlanSummary, report.ImplementationSummary, report.TestOutcome, report.ReviewOutcome)
	fmt.Printf("Artifacts: %d\nCorrection loops: %d\nTokens: %d\n", len(report.Artifacts), len(report.LoopHistory), report.BudgetUsage.Tokens.TotalTokens)
	return nil
}

func openLiveReferenceRuntime(runDir, configPath string, manifest referenceWorkflowManifest) (*referenceRuntime, error) {
	cfg, err := orchestrator.LoadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("load Prizm config: %w", err)
	}
	agentRegistry := agent.NewRegistry()
	if err := cfg.RegisterAgents(agentRegistry); err != nil {
		return nil, err
	}
	providerRegistry := provider.NewProviderRegistry()
	if err := registerProviders(cfg, providerRegistry); err != nil {
		return nil, err
	}

	toolRegistry := tool.NewRegistry()
	readRoots := []string{manifest.WorkspacePath}
	writeRoots := []string{manifest.WorkspacePath}
	tool.RegisterBuiltinsWithRoots(toolRegistry, manifest.WorkspacePath, 10*1024*1024, readRoots, writeRoots)
	if err := toolRegistry.Register(&tool.WriteFileProposal{WorkspaceRoot: manifest.WorkspacePath, AllowedPaths: writeRoots}); err != nil {
		return nil, err
	}
	if err := toolRegistry.Register(&tool.CreateDirectoryProposal{WorkspaceRoot: manifest.WorkspacePath, AllowedPaths: writeRoots}); err != nil {
		return nil, err
	}
	policyConfig := tool.DefaultPolicyConfig()
	policyConfig.WorkspaceRoot = manifest.WorkspacePath
	policyConfig.ReadRoots = readRoots
	policyConfig.WriteRoots = writeRoots
	policyConfig.AllowedPaths = []string{manifest.WorkspacePath}
	policyConfig.OrchestratorAgentID = configuredOrchestratorAgentID(cfg)
	policyConfig.WriteAgents = make(map[string]bool)
	for _, configuredAgent := range cfg.Agents {
		for _, capability := range configuredAgent.Capabilities {
			if capability == "code" {
				policyConfig.WriteAgents[configuredAgent.ID] = true
				break
			}
		}
	}
	toolExecutor := tool.NewExecutor(toolRegistry, &policyConfig)
	approvalStore := approval.NewStore(runDir)
	toolExecutor.SetApprovalStore(approvalStore)
	backend := &subAgentBackend{
		providers: providerRegistry, exec: toolExecutor, toolReg: toolRegistry,
		protectedBranch: cfg.ProtectedBranch(), approvalStore: approvalStore,
	}
	toolInfos := toolRegistry.ListWithDescriptions()
	loop := subagent.NewLoopRunner(subagent.LoopRunnerConfig{
		Backend: backend,
		Scope:   subagent.DefaultToolScope(),
		SystemPrompt: func(_ v2.TaskPacket, runtime subagent.AgentRuntime) string {
			charter := fmt.Sprintf("You are the distinct %q authority in a bounded multi-agent software workflow. Follow the role contract in the task. When it requires a tool, emit one tool_request JSON, wait for its result, then return exactly one final role JSON object.", runtime.AgentID)
			return charter + agent.BuildToolPromptSuffix(toolInfos, manifest.WorkspacePath, manifest.WorkspacePath)
		},
	})
	validationRunner := workspaceValidationRunner{
		registry: validation.NewRegistry(), fallbackRoot: manifest.WorkspacePath, artifactRoot: runDir,
	}
	roleRunner, err := multiagent.NewAgentRoleRunner(multiagent.AgentRoleRunnerOptions{
		Profiles: multiagent.RegistryProfileResolver{Registry: agentRegistry},
		Executor: multiagent.SubagentExecutor{Runner: loop},
		Workspaces: multiagent.WorkspaceResolverFunc(func(context.Context, string) (multiagent.Workspace, error) {
			return multiagent.Workspace{ID: manifest.WorkspaceID, Path: manifest.WorkspacePath}, nil
		}),
		Validation: validationRunner,
		Proposals:  approvalProposalResolver{store: approvalStore},
	})
	if err != nil {
		return nil, err
	}
	var interaction *multiagent.InteractionScheduler
	if adapterName := strings.TrimSpace(os.Getenv("PRIZM_INTERACTION_ADAPTER")); adapterName != "" {
		adapterRegistry := newAdapterRegistry()
		interactionAdapter, resolveErr := adapterRegistry.Resolve(adapterName)
		if resolveErr != nil {
			return nil, fmt.Errorf("resolve interaction adapter %q: %w", adapterName, resolveErr)
		}
		interaction, err = multiagent.NewInteractionScheduler(
			interactionAdapter,
			multiagent.FirstLegalInteractionSelector{},
			multiagent.InteractionSchedulerOptions{
				Store: multiagent.JSONInteractionRunStore{
					Directory: filepath.Join(runDir, manifest.RunID, "interaction"),
				},
			},
		)
		if err != nil {
			return nil, fmt.Errorf("configure interaction adapter %q: %w", adapterName, err)
		}
	}
	lifecycle := newApprovalProposalLifecycle(approvalStore, manifest.WorkspacePath, runDir)
	return openReferenceRuntimeWithInteraction(runDir, manifest, roleRunner, interaction, lifecycle)
}

func openInspectionReferenceRuntime(runDir string, manifest referenceWorkflowManifest) (*referenceRuntime, error) {
	return openReferenceRuntimeWithInteraction(runDir, manifest, unavailableRoleRunner{}, nil, nil)
}

func openReferenceRuntimeWithInteraction(runDir string, manifest referenceWorkflowManifest, runner multiagent.RoleRunner, interaction *multiagent.InteractionScheduler, proposals multiagent.ProposalLifecycle) (*referenceRuntime, error) {
	dbPath := filepath.Join(runDir, manifest.RunID, "multiagent.db")
	store, err := multiagent.NewSQLiteDurableRunStore(dbPath)
	if err != nil {
		return nil, err
	}
	eventStore, err := event.NewSQLiteEventStore(dbPath)
	if err != nil {
		store.Close()
		return nil, err
	}
	// Two convergence paths onto the same *multiagent.CompiledGraph: a legacy
	// reference-workflow manifest builds it from the embedded Definition via
	// CompatAdaptDefinition (unchanged since PR4 — a signature retarget, not
	// a logic rewrite; loadReferenceManifest already calls
	// manifest.Definition.Validate() for this case, so this does not
	// re-validate). A PR6 registry-backed manifest (graph run) instead
	// resolves its exact pinned (WorkflowID, WorkflowVersion) through a
	// DefinitionStore opened at DefinitionDBPath — deliberately Get, never
	// Latest, so this run keeps executing/resuming against the version it
	// was started with even after a newer version is registered. This is
	// what lets `prism workflow status/cancel/resume <run_id>` (and the API's
	// MultiAgentController, which calls these same functions) work
	// unmodified for a graph-run-started run.
	var graph *multiagent.CompiledGraph
	if manifest.registryBacked() {
		defStore, defErr := multiagent.NewDefinitionStore(manifest.DefinitionDBPath)
		if defErr != nil {
			store.Close()
			eventStore.Close()
			return nil, fmt.Errorf("open definition registry for run %s: %w", manifest.RunID, defErr)
		}
		reg, getErr := defStore.Get(context.Background(), manifest.WorkflowID, manifest.WorkflowVersion)
		closeErr := defStore.Close()
		if getErr != nil {
			store.Close()
			eventStore.Close()
			return nil, fmt.Errorf("resolve registry definition for run %s: %w", manifest.RunID, getErr)
		}
		if closeErr != nil {
			store.Close()
			eventStore.Close()
			return nil, fmt.Errorf("close definition registry for run %s: %w", manifest.RunID, closeErr)
		}
		graph = reg.Graph
	} else {
		var err error
		graph, err = multiagent.CompatAdaptDefinition(manifest.Definition)
		if err != nil {
			store.Close()
			eventStore.Close()
			return nil, err
		}
	}
	var reflectionMemory multiagent.ReflectionMemorySink
	if os.Getenv("PRIZM_REFLECTION_MEMORY") == "1" {
		reflectionMemory = &memory.ReflectionSink{
			Gate:  memory.NewGateExtractor([]string{"qwen3.5:9b"}, os.Getenv("OLLAMA_URL"), ""),
			Store: memory.NewMarkdownStore(filepath.Join(manifest.WorkspacePath, "memory")),
		}
	}
	var reflection multiagent.ReflectionRunner
	if candidate, ok := runner.(multiagent.ReflectionRunner); ok {
		reflection = candidate
	}
	runtime, err := multiagent.NewDurableRuntime(
		graph, runner, store,
		multiagent.FileRunClaimer{Root: runDir}, eventStore,
		multiagent.DurableRuntimeOptions{Interaction: interaction, Reflection: reflection, Memory: reflectionMemory, Proposals: proposals},
	)
	if err != nil {
		store.Close()
		eventStore.Close()
		return nil, err
	}
	return &referenceRuntime{runtime: runtime, store: store, events: eventStore}, nil
}

type unavailableRoleRunner struct{}

func (unavailableRoleRunner) RunRole(context.Context, multiagent.RoleRunRequest) (multiagent.RoleRunResult, error) {
	return multiagent.RoleRunResult{}, errors.New("multiagent: execution is unavailable in inspection mode")
}

func loadReferenceWorkflowInput(path string) (multiagent.ReferenceWorkflowInput, error) {
	if strings.TrimSpace(path) == "" {
		return multiagent.ReferenceWorkflowInput{}, errors.New("multi-agent workflow requires --input <file.json>")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return multiagent.ReferenceWorkflowInput{}, fmt.Errorf("read workflow input: %w", err)
	}
	var input multiagent.ReferenceWorkflowInput
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return input, fmt.Errorf("decode workflow input: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return input, errors.New("decode workflow input: expected exactly one JSON object")
	}
	if err := input.Validate(); err != nil {
		return input, err
	}
	return input, nil
}

func referenceWorkspace(path string) (string, string, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return "", "", fmt.Errorf("resolve workspace: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", "", fmt.Errorf("inspect workspace: %w", err)
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("workspace is not a directory: %s", absolute)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", "", fmt.Errorf("canonicalize workspace: %w", err)
	}
	identityPath := filepath.Clean(canonical)
	if runtime.GOOS == "windows" {
		identityPath = strings.ToLower(identityPath)
	}
	sum := sha256.Sum256([]byte(identityPath))
	return canonical, "workspace_" + hex.EncodeToString(sum[:12]), nil
}

func isolatedReferenceWorkspace(ctx context.Context, source, runID string) (string, string, string, error) {
	sourcePath, _, err := referenceWorkspace(source)
	if err != nil {
		return "", "", "", err
	}
	if err := gitx.EnsureExcluded(ctx, sourcePath, ".prizm/"); err != nil {
		return "", "", "", fmt.Errorf("prepare isolated workspace: %w", err)
	}
	worktreePath := filepath.Join(sourcePath, ".prizm", "worktrees", gitx.SafeID(runID, "run"))
	if err := gitx.CreateDetachedWorktree(ctx, sourcePath, worktreePath); err != nil {
		return "", "", "", err
	}
	workspacePath, workspaceID, err := referenceWorkspace(worktreePath)
	if err != nil {
		gitx.RemoveWorktree(context.Background(), sourcePath, worktreePath)
		return "", "", "", err
	}
	return workspacePath, workspaceID, sourcePath, nil
}

func referenceManifestPath(runDir, runID string) string {
	return filepath.Join(runDir, runID, "multiagent_manifest.json")
}

func writeReferenceManifest(runDir string, manifest referenceWorkflowManifest) error {
	if err := os.MkdirAll(filepath.Join(runDir, manifest.RunID), 0o755); err != nil {
		return err
	}
	return writeJSONFile(referenceManifestPath(runDir, manifest.RunID), manifest)
}

func loadReferenceManifest(runDir, runID string) (referenceWorkflowManifest, error) {
	data, err := os.ReadFile(referenceManifestPath(runDir, runID))
	if err != nil {
		return referenceWorkflowManifest{}, fmt.Errorf("multi-agent manifest not found for run %s: %w", runID, err)
	}
	var manifest referenceWorkflowManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return manifest, fmt.Errorf("decode multi-agent manifest: %w", err)
	}
	if (manifest.SchemaVersion != 1 && manifest.SchemaVersion != referenceManifestSchemaVersion) || manifest.RunID != runID {
		return manifest, fmt.Errorf("unsupported or inconsistent multi-agent manifest for run %s", runID)
	}
	// A PR6 registry-backed run (graph run) is not the fixed reference
	// workflow and carries no embedded legacy Definition/ReferenceWorkflowInput
	// to validate — openReferenceRuntime resolves its graph through the
	// definition registry instead (see this file's registryBacked doc).
	if !manifest.registryBacked() {
		if manifest.WorkflowID != multiagent.ReferenceWorkflowID {
			return manifest, fmt.Errorf("unsupported or inconsistent multi-agent manifest for run %s", runID)
		}
		if err := manifest.Input.Validate(); err != nil {
			return manifest, err
		}
		if err := manifest.Definition.Validate(); err != nil {
			return manifest, err
		}
	}
	if !manifest.WorkspaceCleaned {
		workspacePath, workspaceID, err := referenceWorkspace(manifest.WorkspacePath)
		if err != nil {
			return manifest, err
		}
		if workspacePath != manifest.WorkspacePath || workspaceID != manifest.WorkspaceID {
			return manifest, errors.New("workspace continuity check failed: persisted workspace identity changed")
		}
	}
	if manifest.SourceWorkspacePath != "" {
		sourcePath, _, sourceErr := referenceWorkspace(manifest.SourceWorkspacePath)
		if sourceErr != nil || sourcePath != manifest.SourceWorkspacePath {
			return manifest, errors.New("source workspace continuity check failed")
		}
	}
	return manifest, nil
}

func writeReferenceReport(ctx context.Context, runDir string, manifest referenceWorkflowManifest, runtime *referenceRuntime, state multiagent.RunState) error {
	events, err := runtime.events.Query(ctx, event.EventFilter{RunID: state.RunID, Limit: 10_000})
	if err != nil {
		return err
	}
	return persistReferenceReport(runDir, state.RunID, multiagent.BuildReferenceRunReport(manifest.Input, state, events))
}

// finalizeTerminalReferenceRun persists the complete report before reclaiming
// the detached worktree. Diff artifacts already live under runDir, so cleanup
// cannot erase the evidence needed for later inspection.
func finalizeTerminalReferenceRun(ctx context.Context, runDir string, manifest *referenceWorkflowManifest, runtime *referenceRuntime, state multiagent.RunState) error {
	if !state.Status.Terminal() {
		return nil
	}
	if err := writeReferenceReport(ctx, runDir, *manifest, runtime, state); err != nil {
		return err
	}
	return cleanupTerminalReferenceWorkspace(ctx, runDir, manifest)
}

func cleanupTerminalReferenceWorkspace(ctx context.Context, runDir string, manifest *referenceWorkflowManifest) error {
	if manifest.WorkspaceCleaned || manifest.SourceWorkspacePath == "" || manifest.WorkspacePath == "" {
		return nil
	}
	worktreeRoot := filepath.Join(manifest.SourceWorkspacePath, ".prizm", "worktrees")
	if !safety.IsWithinRoot(manifest.WorkspacePath, worktreeRoot) {
		return fmt.Errorf("refuse to clean unexpected run workspace %q", manifest.WorkspacePath)
	}
	if err := gitx.RemoveWorktreeChecked(ctx, manifest.SourceWorkspacePath, manifest.WorkspacePath); err != nil {
		return err
	}
	if _, err := os.Stat(manifest.WorkspacePath); !os.IsNotExist(err) {
		return fmt.Errorf("remove terminal run worktree: path still exists")
	}
	manifest.WorkspaceCleaned = true
	return writeReferenceManifest(runDir, *manifest)
}

func persistReferenceReport(runDir, runID string, report multiagent.ReferenceRunReport) error {
	return writeJSONFile(filepath.Join(runDir, runID, "multiagent_report.json"), report)
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("publish %s: %w", filepath.Base(path), err)
	}
	return nil
}

func writeJSON(file *os.File, value any) error {
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func isReferenceWorkflowRun(runDir, runID string) bool {
	_, err := os.Stat(referenceManifestPath(runDir, runID))
	return err == nil
}

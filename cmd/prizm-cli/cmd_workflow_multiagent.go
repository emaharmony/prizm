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
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/subagent"
	"github.com/emaharmony/prizm/internal/tool"
	"github.com/emaharmony/prizm/internal/validation"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
	v2 "github.com/emaharmony/prizm/internal/workflow/v2"
	"runtime"
)

const referenceManifestSchemaVersion = 1

type referenceWorkflowManifest struct {
	SchemaVersion int                               `json:"schema_version"`
	RunID         string                            `json:"run_id"`
	WorkflowID    string                            `json:"workflow_id"`
	Input         multiagent.ReferenceWorkflowInput `json:"input"`
	Definition    multiagent.Definition             `json:"definition"`
	WorkspaceID   string                            `json:"workspace_id"`
	WorkspacePath string                            `json:"workspace_path"`
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
	definition, err := multiagent.ApplyReferenceOverrides(
		multiagent.DefaultReferenceDefinition(), input,
	)
	if err != nil {
		return err
	}
	workspacePath, workspaceID, err := referenceWorkspace(input.Workspace)
	if err != nil {
		return err
	}
	runID := event.NewRunID()
	manifest := referenceWorkflowManifest{
		SchemaVersion: referenceManifestSchemaVersion,
		RunID:         runID, WorkflowID: multiagent.ReferenceWorkflowID,
		Input: input, Definition: definition,
		WorkspaceID: workspaceID, WorkspacePath: workspacePath,
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
		if err := writeReferenceReport(context.Background(), runDir, manifest, runtime, state); err != nil {
			return errors.Join(runErr, err)
		}
	}
	fmt.Printf("Status: %s\n", state.Status)
	return runErr
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
		if err := writeReferenceReport(context.Background(), runDir, manifest, runtime, state); err != nil {
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
		if err := writeReferenceReport(context.Background(), runDir, manifest, runtime, state); err != nil {
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
	if err := persistReferenceReport(runDir, runID, report); err != nil {
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
	toolExecutor := tool.NewExecutor(toolRegistry, &policyConfig)
	toolExecutor.SetApprovalStore(approval.NewStore(runDir))
	backend := &subAgentBackend{
		providers: providerRegistry, exec: toolExecutor, toolReg: toolRegistry,
		protectedBranch: cfg.ProtectedBranch(),
	}
	toolInfos := toolRegistry.ListWithDescriptions()
	loop := subagent.NewLoopRunner(subagent.LoopRunnerConfig{
		Backend: backend,
		Scope:   subagent.DefaultToolScope(),
		SystemPrompt: func(_ v2.TaskPacket, runtime subagent.AgentRuntime) string {
			charter := fmt.Sprintf("You are the distinct %q authority in a bounded multi-agent software workflow. Follow the role contract in the task and return exactly one final JSON object.", runtime.AgentID)
			return charter + agent.BuildToolPromptSuffix(toolInfos, manifest.WorkspacePath, manifest.WorkspacePath)
		},
	})
	validationExecutor := validation.NewExecutor(
		validation.NewRegistry(), manifest.WorkspacePath, filepath.Join(runDir, manifest.RunID),
	)
	roleRunner, err := multiagent.NewAgentRoleRunner(multiagent.AgentRoleRunnerOptions{
		Profiles: multiagent.RegistryProfileResolver{Registry: agentRegistry},
		Executor: multiagent.SubagentExecutor{Runner: loop},
		Workspaces: multiagent.WorkspaceResolverFunc(func(context.Context, string) (multiagent.Workspace, error) {
			return multiagent.Workspace{ID: manifest.WorkspaceID, Path: manifest.WorkspacePath}, nil
		}),
		Validation: multiagent.ValidationRunnerFunc(validationExecutor.Run),
	})
	if err != nil {
		return nil, err
	}
	return openReferenceRuntime(runDir, manifest, roleRunner)
}

func openInspectionReferenceRuntime(runDir string, manifest referenceWorkflowManifest) (*referenceRuntime, error) {
	return openReferenceRuntime(runDir, manifest, unavailableRoleRunner{})
}

func openReferenceRuntime(runDir string, manifest referenceWorkflowManifest, runner multiagent.RoleRunner) (*referenceRuntime, error) {
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
	runtime, err := multiagent.NewDurableRuntime(
		manifest.Definition, runner, store,
		multiagent.FileRunClaimer{Root: runDir}, eventStore,
		multiagent.DurableRuntimeOptions{},
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
	if manifest.SchemaVersion != referenceManifestSchemaVersion || manifest.RunID != runID || manifest.WorkflowID != multiagent.ReferenceWorkflowID {
		return manifest, fmt.Errorf("unsupported or inconsistent multi-agent manifest for run %s", runID)
	}
	if err := manifest.Input.Validate(); err != nil {
		return manifest, err
	}
	if err := manifest.Definition.Validate(); err != nil {
		return manifest, err
	}
	workspacePath, workspaceID, err := referenceWorkspace(manifest.WorkspacePath)
	if err != nil {
		return manifest, err
	}
	if workspacePath != manifest.WorkspacePath || workspaceID != manifest.WorkspaceID {
		return manifest, errors.New("workspace continuity check failed: persisted workspace identity changed")
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

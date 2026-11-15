package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/emaharmony/prizm/internal/agent"
	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/freewill"
	"github.com/emaharmony/prizm/internal/gitx"
	"github.com/emaharmony/prizm/internal/memory"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/safety"
	"github.com/emaharmony/prizm/internal/sandbox"
	"github.com/emaharmony/prizm/internal/subagent"
	"github.com/emaharmony/prizm/internal/tool"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
	v2 "github.com/emaharmony/prizm/internal/workflow/v2"
	"runtime"
)

const referenceManifestSchemaVersion = 2
const multiAgentSandboxApprovalEnv = "PRIZM_MULTIAGENT_SANDBOX"

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
	SchemaVersion                 int                                   `json:"schema_version"`
	RunID                         string                                `json:"run_id"`
	WorkflowID                    string                                `json:"workflow_id"`
	Input                         multiagent.ReferenceWorkflowInput     `json:"input"`
	Definition                    multiagent.Definition                 `json:"definition"`
	WorkspaceID                   string                                `json:"workspace_id"`
	WorkspacePath                 string                                `json:"workspace_path"`
	SourceWorkspacePath           string                                `json:"source_workspace_path,omitempty"`
	WorkspaceCleaned              bool                                  `json:"workspace_cleaned,omitempty"`
	WorkflowVersion               int64                                 `json:"workflow_version,omitempty"`
	DefinitionDBPath              string                                `json:"definition_db_path,omitempty"`
	ValidationProfileFingerprints map[string]string                     `json:"validation_profile_fingerprints,omitempty"`
	SandboxApprovalMode           bool                                  `json:"sandbox_approval_mode,omitempty"`
	FreewillSandboxConfig         *sandbox.Config                       `json:"freewill_sandbox_config,omitempty"`
	ResourceNegotiation           *multiagent.ResourceNegotiationConfig `json:"resource_negotiation,omitempty"`
}

// registryBacked reports whether m describes a PR6 `graph run`-started run
// rather than a legacy reference-workflow run.
func (m referenceWorkflowManifest) registryBacked() bool {
	return m.WorkflowVersion > 0
}

type referenceRuntime struct {
	runtime     *multiagent.DurableRuntime
	store       *multiagent.SQLiteDurableRunStore
	events      *event.SQLiteEventStore
	delegation  graphDelegationOutbox
	workspaceID string
}

func (r *referenceRuntime) close() error {
	return errors.Join(r.store.Close(), r.events.Close(), r.delegation.Close())
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
	if err := bindManifestValidationProfiles(configPath, &manifest); err != nil {
		return err
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
		RunID:       runID,
		WorkspaceID: runtime.workspaceID,
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

// startFreewillReferenceRun starts the fixed, embedded reference workflow for
// one host-approved intention. Its child run identity is supplied by the
// durable Freewill approval service, and its sandbox approval mode is bound
// into the manifest so retries cannot change execution mode.
type freewillChildTerminalReconciler func(context.Context, string) error

func startFreewillReferenceRun(ctx context.Context, runDir, configPath string, request multiagent.FreewillExecutionRequest, reconcile freewillChildTerminalReconciler) (multiagent.FreewillExecutionResult, error) {
	return startFreewillReferenceRunWithExecutionControl(ctx, runDir, configPath, request, reconcile, nil)
}

func startFreewillReferenceRunWithExecutionControl(ctx context.Context, runDir, configPath string, request multiagent.FreewillExecutionRequest, reconcile freewillChildTerminalReconciler, control *multiagent.FreewillOperatorControl, sandboxConfigs ...sandbox.Config) (multiagent.FreewillExecutionResult, error) {
	if request.Identity == nil || request.RunID == "" || stableRequestRunID(request.Identity) != request.RunID {
		return multiagent.FreewillExecutionResult{}, errors.New("freewill: stable child identity is invalid")
	}
	intention := request.Candidate.Request
	if err := request.Candidate.Validate(); err != nil {
		return multiagent.FreewillExecutionResult{}, errors.New("freewill: approved intention is invalid")
	}
	cfg, err := orchestrator.LoadConfig(configPath)
	if err != nil {
		return multiagent.FreewillExecutionResult{}, fmt.Errorf("load Prizm config: %w", err)
	}
	input, err := freewillReferenceInput(intention)
	if err != nil {
		return multiagent.FreewillExecutionResult{}, err
	}
	var resourceNegotiation *multiagent.ResourceNegotiationConfig
	resourceConfig := cfg.Freewill.Execution.ResourceNegotiation
	if resourceConfig.Enabled {
		hardCeiling := multiagent.Limit(resourceConfig.HardCeiling)
		input.Budgets.MaxTransitions = &hardCeiling
		resourceNegotiation = &multiagent.ResourceNegotiationConfig{
			Enabled: true, InitialAllowance: resourceConfig.InitialAllowance, Increment: resourceConfig.Increment,
			HardCeiling: resourceConfig.HardCeiling, OwnerID: resourceConfig.OwnerID,
			ParentRunID: request.ParentRunID, AgentID: intention.AgentID, ProjectID: intention.Scope.ProjectID,
			Workspace: intention.Scope.Workspace, GrantGeneration: request.ApprovalGeneration,
		}
	}
	var sandboxConfig *sandbox.Config
	if len(sandboxConfigs) > 0 {
		configured := sandboxConfigs[0]
		if configured.DockerPath == "" || !pinnedSandboxImage(configured.Image) || !filepath.IsAbs(configured.WorkRoot) || !filepath.IsAbs(configured.ArtifactRoot) || freewillSandboxPathsOverlap(configured.WorkRoot, configured.ArtifactRoot) || freewillSandboxPathsOverlap(configured.WorkRoot, input.Workspace) || freewillSandboxPathsOverlap(configured.ArtifactRoot, input.Workspace) {
			return multiagent.FreewillExecutionResult{}, errors.New("freewill: OCI sandbox configuration is invalid or overlaps the project")
		}
		sandboxConfig = &configured
	}
	manifestPath := referenceManifestPath(runDir, request.RunID)
	if _, err := os.Stat(manifestPath); err == nil {
		manifest, err := loadReferenceManifest(runDir, request.RunID)
		if err != nil {
			return multiagent.FreewillExecutionResult{}, err
		}
		if manifest.WorkflowID != multiagent.ReferenceWorkflowID || manifest.registryBacked() ||
			manifest.Input.Objective != input.Objective || manifest.Input.Workspace != input.Workspace || !manifest.SandboxApprovalMode || !sameFreewillSandboxConfig(manifest.FreewillSandboxConfig, sandboxConfig) ||
			!sameResourceNegotiationConfig(manifest.ResourceNegotiation, resourceNegotiation) {
			return multiagent.FreewillExecutionResult{}, multiagent.ErrRequestIdentityConflict
		}
		runtime, err := openReferenceRuntimeWithInteraction(runDir, manifest, unavailableRoleRunner{}, nil, nil)
		if err != nil {
			return multiagent.FreewillExecutionResult{}, err
		}
		record, acceptErr := runtime.runtime.Accept(ctx, multiagent.RunRequest{
			RunID: request.RunID, Task: multiagent.TaskReference{ID: "task_" + request.RunID, Description: multiagent.ReferenceTaskDescription(manifest.Input)},
			WorkspaceID: manifest.WorkspaceID, Identity: request.Identity,
		})
		closeErr := runtime.close()
		if acceptErr != nil || closeErr != nil {
			return multiagent.FreewillExecutionResult{}, errors.Join(acceptErr, closeErr)
		}
		if record.Status == multiagent.RunStatusCreated {
			go driveAcceptedFreewillReferenceRun(runDir, request.RunID, configPath, reconcile, control,
				intention.AgentID, intention.Scope, request.ApprovalGeneration)
		} else {
			// Start runs while DispatchApproved holds the parent claim. Resuming a
			// terminal child may reconcile that same parent, so do it asynchronously
			// and let the durable claim serialize reconciliation after admission saves.
			go func() {
				if err := resumeFreewillReferenceChildWithExecutionControl(runDir, configPath, request.RunID,
					reconcile, nil, control, intention.AgentID, intention.Scope, request.ApprovalGeneration); err != nil {
					log.Printf("[WARN] Freewill reference child %s retry resume failed: %v", request.RunID, err)
				}
			}()
		}
		return multiagent.FreewillExecutionResult{RunID: request.RunID, ReportRef: "multiagent_report.json"}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return multiagent.FreewillExecutionResult{}, err
	}
	definition, err := multiagent.ApplyReferenceOverrides(multiagent.DefaultReferenceDefinition(), input)
	if err != nil {
		return multiagent.FreewillExecutionResult{}, err
	}
	bindConfiguredReferenceProfiles(&definition, nil, cfg.Agents)
	workspacePath, workspaceID, sourceWorkspacePath, worktreePath, err := recoverOrCreateFreewillReferenceWorkspace(ctx, input.Workspace, request.RunID)
	if err != nil {
		return multiagent.FreewillExecutionResult{}, err
	}
	cleanupUnadmitted := func(cause error) error {
		return errors.Join(cause, cleanupUnadmittedFreewillWorkspace(ctx, sourceWorkspacePath, worktreePath, manifestPath))
	}
	manifest := referenceWorkflowManifest{
		SchemaVersion: referenceManifestSchemaVersion, RunID: request.RunID,
		WorkflowID: multiagent.ReferenceWorkflowID, Input: input, Definition: definition,
		WorkspaceID: workspaceID, WorkspacePath: workspacePath, SourceWorkspacePath: sourceWorkspacePath,
		SandboxApprovalMode:   true,
		FreewillSandboxConfig: sandboxConfig,
		ResourceNegotiation:   resourceNegotiation,
	}
	if err := bindManifestValidationProfiles(configPath, &manifest); err != nil {
		return multiagent.FreewillExecutionResult{}, cleanupUnadmitted(err)
	}
	if err := writeReferenceManifest(runDir, manifest); err != nil {
		return multiagent.FreewillExecutionResult{}, cleanupUnadmitted(err)
	}
	runtime, err := openReferenceRuntimeWithInteraction(runDir, manifest, unavailableRoleRunner{}, nil, nil)
	if err != nil {
		return multiagent.FreewillExecutionResult{}, cleanupUnadmitted(err)
	}
	record, acceptErr := runtime.runtime.Accept(ctx, multiagent.RunRequest{
		RunID: request.RunID, Task: multiagent.TaskReference{ID: "task_" + request.RunID, Description: multiagent.ReferenceTaskDescription(input)},
		WorkspaceID: workspaceID, Identity: request.Identity,
	})
	if acceptErr != nil {
		admitted, inspectErr := runtime.runtime.Inspect(ctx, request.RunID)
		closeErr := runtime.close()
		if inspectErr != nil {
			// Admission is uncertain; preserve the deterministic manifest and
			// workspace so a retry can reconcile the durable runtime record.
			return multiagent.FreewillExecutionResult{}, errors.Join(acceptErr, inspectErr, closeErr)
		}
		if freewillAdmissionConfirmedAbsent(inspectErr, admitted.State.RunID) {
			return multiagent.FreewillExecutionResult{}, errors.Join(acceptErr, closeErr, cleanupUnadmitted(nil))
		}
		return multiagent.FreewillExecutionResult{}, errors.Join(acceptErr, closeErr)
	}
	closeErr := runtime.close()
	if closeErr != nil {
		return multiagent.FreewillExecutionResult{}, closeErr
	}
	if !record.Status.Terminal() {
		go driveAcceptedFreewillReferenceRun(runDir, request.RunID, configPath, reconcile, control,
			intention.AgentID, intention.Scope, request.ApprovalGeneration)
	}
	return multiagent.FreewillExecutionResult{RunID: request.RunID, ReportRef: "multiagent_report.json"}, nil
}

func freewillAdmissionConfirmedAbsent(inspectErr error, inspectedRunID string) bool {
	return inspectErr == nil && inspectedRunID == ""
}

func resumeFreewillReferenceChildWithExecutionControl(runDir, configPath, runID string, reconcile freewillChildTerminalReconciler, finished func(),
	control *multiagent.FreewillOperatorControl, agentID string, scope freewill.Scope, generation uint64) error {
	ctx := context.Background()
	manifest, err := loadReferenceManifest(runDir, runID)
	if err != nil {
		return err
	}
	if !manifest.SandboxApprovalMode || manifest.registryBacked() {
		return multiagent.ErrRequestIdentityConflict
	}
	inspection, err := openInspectionReferenceRuntime(runDir, manifest)
	if err != nil {
		return err
	}
	record, inspectErr := inspection.runtime.Inspect(ctx, runID)
	if inspectErr != nil {
		return errors.Join(inspectErr, inspection.close())
	}
	if record.State.Status.Terminal() {
		reportPath := filepath.Join(runDir, runID, "multiagent_report.json")
		if _, statErr := os.Stat(reportPath); errors.Is(statErr, os.ErrNotExist) {
			if err := finalizeTerminalReferenceRun(ctx, runDir, &manifest, inspection, record.State); err != nil {
				return errors.Join(err, inspection.close())
			}
		}
		closeErr := inspection.close()
		if closeErr != nil {
			return closeErr
		}
		if reconcile != nil {
			if finished != nil {
				defer finished()
			}
			return reconcile(ctx, runID)
		}
		if finished != nil {
			finished()
		}
		return nil
	}
	// Resource negotiation is a dedicated owner-decision wait. Generic child
	// recovery must never resume it; the resource decision path clears this
	// exact wait before explicitly invoking recovery.
	if record.Waiting != nil && record.Waiting.Kind == "resource_extension" {
		return finishResourceWaitRecovery(inspection.close, finished)
	}
	if err := inspection.close(); err != nil {
		return err
	}
	live, err := openLiveReferenceRuntime(runDir, configPath, manifest)
	if err != nil {
		return err
	}
	go func() {
		defer live.close()
		if finished != nil {
			defer finished()
		}
		runCtx, cleanup, registrationErr := registerFreewillChildExecution(live, runID, control, agentID, scope, generation)
		if registrationErr != nil && !errors.Is(registrationErr, multiagent.ErrFreewillHalted) &&
			!errors.Is(registrationErr, multiagent.ErrFreewillApprovalRevoked) {
			log.Printf("[WARN] Freewill reference child %s execution control unavailable: %v", runID, registrationErr)
			return
		}
		if cleanup != nil {
			defer cleanup()
		}
		if registrationErr != nil {
			// Registration already durably requested cancellation and canceled the
			// context. Resume only to persist that terminal request and its report.
			runCtx = context.Background()
		}
		state, runErr := live.runtime.Resume(runCtx, runID)
		if state.Status.Terminal() {
			latest, manifestErr := loadReferenceManifest(runDir, runID)
			if manifestErr == nil {
				manifestErr = finalizeTerminalReferenceRun(context.Background(), runDir, &latest, live, state)
			}
			runErr = errors.Join(runErr, manifestErr)
			if manifestErr == nil && reconcile != nil {
				runErr = errors.Join(runErr, reconcile(context.Background(), runID))
			}
		}
		if runErr != nil {
			log.Printf("[WARN] Freewill reference child %s resume ended: %v", runID, runErr)
		}
	}()
	return nil
}

func finishResourceWaitRecovery(closeInspection func() error, finished func()) error {
	closeErr := closeInspection()
	if finished != nil {
		finished()
	}
	return closeErr
}

func driveAcceptedFreewillReferenceRun(runDir, runID, configPath string, reconcile freewillChildTerminalReconciler,
	control *multiagent.FreewillOperatorControl, agentID string, scope freewill.Scope, generation uint64) {
	manifest, err := loadReferenceManifest(runDir, runID)
	if err != nil {
		log.Printf("[WARN] Freewill reference child %s manifest unavailable: %v", runID, err)
		return
	}
	live, err := openLiveReferenceRuntime(runDir, configPath, manifest)
	if err != nil {
		live, err = openReferenceRuntimeWithInteraction(runDir, manifest, unavailableRoleRunner{}, nil, nil)
		if err != nil {
			log.Printf("[WARN] Freewill reference child %s runtime unavailable: %v", runID, err)
			return
		}
	}
	defer live.close()
	runCtx, cleanup, registrationErr := registerFreewillChildExecution(live, runID, control, agentID, scope, generation)
	if registrationErr != nil && !errors.Is(registrationErr, multiagent.ErrFreewillHalted) &&
		!errors.Is(registrationErr, multiagent.ErrFreewillApprovalRevoked) {
		log.Printf("[WARN] Freewill reference child %s execution control unavailable: %v", runID, registrationErr)
		return
	}
	if cleanup != nil {
		defer cleanup()
	}
	if registrationErr != nil {
		// The callback durably canceled this accepted child before it entered a role.
		runCtx = context.Background()
	}
	_, runErr := live.runtime.RunAccepted(runCtx, runID)
	record, inspectErr := live.runtime.Inspect(context.Background(), runID)
	state := record.State
	if inspectErr != nil {
		runErr = errors.Join(runErr, inspectErr)
	}
	if state.RunID != "" && state.Status.Terminal() {
		latest, manifestErr := loadReferenceManifest(runDir, runID)
		if manifestErr == nil {
			manifestErr = finalizeTerminalReferenceRun(context.Background(), runDir, &latest, live, state)
		}
		runErr = errors.Join(runErr, manifestErr)
	}
	if reconcile != nil && state.RunID != "" && state.Status.Terminal() {
		if err := reconcile(context.Background(), runID); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}
	if runErr != nil {
		log.Printf("[WARN] Freewill reference child %s run ended: %v", runID, runErr)
	}
}

func registerFreewillChildExecution(live *referenceRuntime, runID string, control *multiagent.FreewillOperatorControl,
	agentID string, scope freewill.Scope, generation uint64) (context.Context, func(), error) {
	runCtx, cancel := context.WithCancel(context.Background())
	cleanup := func() { cancel() }
	if control == nil {
		return runCtx, cleanup, nil
	}
	cancelErrors := make(chan error, 1)
	unregister, err := control.RegisterActiveExecution(agentID, scope, generation, func() {
		if _, runtimeCancelErr := live.runtime.Cancel(context.Background(), runID, "freewill operator halt or approval revocation"); runtimeCancelErr != nil {
			select {
			case cancelErrors <- runtimeCancelErr:
			default:
			}
			log.Printf("[WARN] Freewill reference child %s durable cancellation request failed: %v", runID, runtimeCancelErr)
		}
		cancel()
	})
	if err != nil {
		if errors.Is(err, multiagent.ErrFreewillHalted) || errors.Is(err, multiagent.ErrFreewillApprovalRevoked) {
			select {
			case runtimeCancelErr := <-cancelErrors:
				cancel()
				return nil, nil, fmt.Errorf("freewill: durable child cancellation request failed: %w", runtimeCancelErr)
			default:
			}
			return runCtx, cleanup, err
		}
		cancel()
		return nil, nil, err
	}
	return runCtx, func() { unregister(); cancel() }, nil
}

func freewillTerminalResultFromReferenceRun(ctx context.Context, runDir, childRunID string) (multiagent.FreewillExecutionTerminalResult, error) {
	manifest, err := loadReferenceManifest(runDir, childRunID)
	if err != nil {
		return multiagent.FreewillExecutionTerminalResult{}, err
	}
	if !manifest.SandboxApprovalMode || manifest.registryBacked() {
		return multiagent.FreewillExecutionTerminalResult{}, multiagent.ErrRequestIdentityConflict
	}
	inspection, err := openInspectionReferenceRuntime(runDir, manifest)
	if err != nil {
		return multiagent.FreewillExecutionTerminalResult{}, err
	}
	defer inspection.close()
	record, err := inspection.runtime.Inspect(ctx, childRunID)
	if err != nil {
		return multiagent.FreewillExecutionTerminalResult{}, err
	}
	if !record.State.Status.Terminal() {
		return multiagent.FreewillExecutionTerminalResult{}, errors.New("freewill: child run has not reached a terminal state")
	}
	reportPath := filepath.Join(runDir, childRunID, "multiagent_report.json")
	data, err := os.ReadFile(reportPath)
	if errors.Is(err, os.ErrNotExist) {
		if err := finalizeTerminalReferenceRun(ctx, runDir, &manifest, inspection, record.State); err != nil {
			return multiagent.FreewillExecutionTerminalResult{}, err
		}
		data, err = os.ReadFile(reportPath)
	}
	if err != nil {
		return multiagent.FreewillExecutionTerminalResult{}, err
	}
	if len(data) == 0 || len(data) > 1<<20 {
		return multiagent.FreewillExecutionTerminalResult{}, errors.New("freewill: child report size is invalid")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	var report multiagent.ReferenceRunReport
	if err := decoder.Decode(&report); err != nil {
		return multiagent.FreewillExecutionTerminalResult{}, errors.New("freewill: child report is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return multiagent.FreewillExecutionTerminalResult{}, errors.New("freewill: child report has trailing data")
	}
	if report.RunID != childRunID || report.FinalStatus != record.State.Status || !report.FinalStatus.Terminal() {
		return multiagent.FreewillExecutionTerminalResult{}, errors.New("freewill: child report does not match durable terminal run")
	}
	validationSummary, err := freewillValidationSummary(report.ValidationResults)
	if err != nil {
		return multiagent.FreewillExecutionTerminalResult{}, err
	}
	failureCode := strings.TrimSpace(report.FailureCode)
	if report.FinalStatus != multiagent.RunStatusCompleted && !validFreewillReportFailureCode(failureCode) {
		failureCode = "child_" + string(report.FinalStatus)
	}
	if report.FinalStatus == multiagent.RunStatusCompleted {
		failureCode = ""
	}
	digest := sha256.Sum256(data)
	return multiagent.FreewillExecutionTerminalResult{ChildRunID: childRunID, ChildStatus: report.FinalStatus,
		ReportRef: "multiagent_report.json", ReportSHA256: hex.EncodeToString(digest[:]),
		ValidationSummary: validationSummary, FailureCode: failureCode}, nil
}

func freewillValidationSummary(results []multiagent.ValidationEvidence) (string, error) {
	if len(results) == 0 {
		return "not_run", nil
	}
	passed, failed := false, false
	for _, result := range results {
		switch result.Status {
		case "passed":
			passed = true
		case "failed", "timeout", "error":
			failed = true
		default:
			return "", errors.New("freewill: child report contains an unknown validation status")
		}
	}
	if passed && failed {
		return "mixed", nil
	}
	if failed {
		return "failed", nil
	}
	return "passed", nil
}

func validFreewillReportFailureCode(code string) bool {
	if code == "" || len(code) > 128 {
		return false
	}
	for _, r := range code {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '_' && r != '-' && r != '.' {
			return false
		}
	}
	return true
}

func freewillReferenceChildIsTerminal(ctx context.Context, runDir, childRunID string) (bool, error) {
	manifest, err := loadReferenceManifest(runDir, childRunID)
	if err != nil {
		return false, err
	}
	inspection, err := openInspectionReferenceRuntime(runDir, manifest)
	if err != nil {
		return false, err
	}
	defer inspection.close()
	record, err := inspection.runtime.Inspect(ctx, childRunID)
	if err != nil {
		return false, err
	}
	return record.State.Status.Terminal(), nil
}

func freewillReferenceInput(intention freewill.IntentionRequest) (multiagent.ReferenceWorkflowInput, error) {
	if err := intention.Validate(); err != nil {
		return multiagent.ReferenceWorkflowInput{}, errors.New("freewill: approved intention is invalid")
	}
	if len(intention.RequestedCapabilities) != 1 || intention.RequestedCapabilities[0] != "code" {
		return multiagent.ReferenceWorkflowInput{}, errors.New("freewill: approved intention requests an unsupported capability envelope")
	}
	if intention.Budget.MaxModelCalls != nil || intention.Budget.DiskMB != nil {
		return multiagent.ReferenceWorkflowInput{}, errors.New("freewill: approved intention requests an unsupported resource budget")
	}
	maxTokens := multiagent.Limit(8000)
	if intention.Budget.MaxTokens != nil {
		if *intention.Budget.MaxTokens <= 0 {
			return multiagent.ReferenceWorkflowInput{}, errors.New("freewill: approved intention has an invalid token budget")
		}
		if *intention.Budget.MaxTokens < int(maxTokens) {
			maxTokens = multiagent.Limit(*intention.Budget.MaxTokens)
		}
	}
	// The fixed reference graph has four required roles, but a real code-capable
	// developer visit uses multiple local turns for sandbox execution, proposal,
	// and final output. The production OCI acceptance measured five iterations
	// before reviewer; eight leaves modest bounded recovery headroom while the
	// retry, transition, token, and runtime ceilings remain independently fixed.
	maxIterations := multiagent.Limit(8)
	maxRetries := multiagent.Limit(1)
	maxTransitions := multiagent.Limit(32)
	maxDuration := "15m"
	if intention.Budget.RuntimeMinutes != nil {
		if *intention.Budget.RuntimeMinutes <= 0 {
			return multiagent.ReferenceWorkflowInput{}, errors.New("freewill: approved intention has an invalid runtime budget")
		}
		if *intention.Budget.RuntimeMinutes < 15 {
			maxDuration = fmt.Sprintf("%dm", *intention.Budget.RuntimeMinutes)
		}
	}
	input := multiagent.ReferenceWorkflowInput{
		Objective:          "Freewill approved intention: " + intention.Title + "\n\nGoal: " + intention.Goal,
		Workspace:          intention.Scope.Workspace,
		Constraints:        []string{"Complete only this owner-approved intention.", "Use the configured Prizm workflow and its independent approval rules for changes."},
		AcceptanceCriteria: []string{"Return an evidence-backed report describing the completed work or the blocker."},
		Budgets: multiagent.ReferenceBudgetOverrides{
			MaxTransitions: &maxTransitions, MaxLocalIterations: &maxIterations, MaxRetries: &maxRetries,
			MaxTokens: &maxTokens, MaxDuration: maxDuration,
		},
	}
	if err := input.Validate(); err != nil {
		return multiagent.ReferenceWorkflowInput{}, errors.New("freewill: approved intention cannot form a reference workflow input")
	}
	return input, nil
}

func sameResourceNegotiationConfig(left, right *multiagent.ResourceNegotiationConfig) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
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

type liveReferenceComponents struct {
	runner      multiagent.RoleRunner
	interaction *multiagent.InteractionScheduler
	lifecycle   multiagent.ProposalLifecycle
}

func openLiveReferenceRuntime(runDir, configPath string, manifest referenceWorkflowManifest) (*referenceRuntime, error) {
	if err := checkAcceptedValidationProfiles(configPath, manifest); err != nil {
		inspection, openErr := openInspectionReferenceRuntime(runDir, manifest)
		if openErr != nil {
			return nil, errors.Join(err, openErr)
		}
		_, failErr := inspection.runtime.FailAcceptedRun(context.Background(), manifest.RunID, "validation_profile_configuration_changed")
		closeErr := inspection.close()
		return nil, errors.Join(err, failErr, closeErr)
	}
	components, err := buildLiveReferenceComponents(runDir, configPath, manifest)
	if err != nil {
		return nil, err
	}
	return openReferenceRuntimeWithInteraction(runDir, manifest, components.runner, components.interaction, components.lifecycle)
}

func buildLiveReferenceComponents(runDir, configPath string, manifest referenceWorkflowManifest) (*liveReferenceComponents, error) {
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
	if err := toolRegistry.Register(&tool.ApplyPatchProposal{WorkspaceRoot: manifest.WorkspacePath, AllowedPaths: writeRoots}); err != nil {
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
		worktreeRoot:        filepath.Join(manifest.SourceWorkspacePath, ".prizm", "worktrees"),
		sandboxApprovalMode: manifest.SandboxApprovalMode || os.Getenv(multiAgentSandboxApprovalEnv) == "oci-approval",
		sandboxConfig:       manifest.FreewillSandboxConfig,
	}
	toolInfos := toolRegistry.ListWithDescriptions()
	if backend.sandboxApprovalMode {
		toolInfos = sandboxApprovalPromptToolInfos(toolInfos)
		toolInfos = append(toolInfos, sandboxApprovalToolInfos()...)
	}
	loop := subagent.NewLoopRunner(subagent.LoopRunnerConfig{
		Backend:                 backend,
		Scope:                   subagent.DefaultToolScope(),
		FinalReplyReserveTokens: 0,
		SystemPrompt: func(_ v2.TaskPacket, runtime subagent.AgentRuntime) string {
			charter := fmt.Sprintf("You are the distinct %q authority in a bounded multi-agent software workflow. Follow the role contract in the task. When it requires a tool, emit one tool_request JSON, wait for its result, then return exactly one final role JSON object.", runtime.AgentID)
			return charter + agent.BuildToolPromptSuffix(toolInfos, manifest.WorkspacePath, manifest.WorkspacePath)
		},
	})
	validationProfiles, err := configuredValidationRegistry(cfg)
	if err != nil {
		return nil, err
	}
	validationRunner := workspaceValidationRunner{
		registry: validationProfiles, fallbackRoot: manifest.WorkspacePath, artifactRoot: runDir,
	}
	roleRunner, err := multiagent.NewAgentRoleRunner(multiagent.AgentRoleRunnerOptions{
		Profiles: multiagent.RegistryProfileResolver{Registry: agentRegistry},
		Executor: multiagent.SubagentExecutor{Runner: loop, SandboxApprovalBridge: backend.sandboxApprovalMode},
		Workspaces: multiagent.WorkspaceResolverFunc(func(context.Context, string) (multiagent.Workspace, error) {
			return multiagent.Workspace{ID: manifest.WorkspaceID, Path: manifest.WorkspacePath}, nil
		}),
		Validation:            validationRunner,
		Proposals:             approvalProposalResolver{store: approvalStore, workspace: manifest.WorkspacePath},
		SandboxApprovalBridge: backend.sandboxApprovalMode,
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
	lifecycle := newApprovalProposalLifecycle(approvalStore, manifest.WorkspacePath, runDir, manifest.SourceWorkspacePath)
	return &liveReferenceComponents{runner: roleRunner, interaction: interaction, lifecycle: lifecycle}, nil
}

func openInspectionReferenceRuntime(runDir string, manifest referenceWorkflowManifest) (*referenceRuntime, error) {
	return openReferenceRuntimeWithInteraction(runDir, manifest, unavailableRoleRunner{}, nil, nil)
}

func openReferenceRuntimeWithInteraction(runDir string, manifest referenceWorkflowManifest, runner multiagent.RoleRunner, interaction *multiagent.InteractionScheduler, proposals multiagent.ProposalLifecycle) (*referenceRuntime, error) {
	return openReferenceRuntimeWithInteractionForComposition(runDir, manifest, runner, interaction, proposals, false)
}

// openReferenceRuntimeWithInteractionForDelegationTest is reserved for package
// tests that inject graph delegation outcomes. Production composition must keep
// graph delegation disabled until a durable worker is available.
func openReferenceRuntimeWithInteractionForDelegationTest(runDir string, manifest referenceWorkflowManifest, runner multiagent.RoleRunner, interaction *multiagent.InteractionScheduler, proposals multiagent.ProposalLifecycle) (*referenceRuntime, error) {
	return openReferenceRuntimeWithInteractionForComposition(runDir, manifest, runner, interaction, proposals, true)
}

func openReferenceRuntimeWithInteractionForComposition(runDir string, manifest referenceWorkflowManifest, runner multiagent.RoleRunner, interaction *multiagent.InteractionScheduler, proposals multiagent.ProposalLifecycle, enableGraphDelegation bool) (*referenceRuntime, error) {
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
	newDelegationOutbox := newGraphDelegationOutbox
	if enableGraphDelegation {
		newDelegationOutbox = newGraphDelegationOutboxForTest
	}
	delegation, delegationOptions, err := newDelegationOutbox(dbPath)
	if err != nil {
		store.Close()
		eventStore.Close()
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
			delegation.Close()
			return nil, fmt.Errorf("open definition registry for run %s: %w", manifest.RunID, defErr)
		}
		reg, getErr := defStore.Get(context.Background(), manifest.WorkflowID, manifest.WorkflowVersion)
		closeErr := defStore.Close()
		if getErr != nil {
			store.Close()
			eventStore.Close()
			delegation.Close()
			return nil, fmt.Errorf("resolve registry definition for run %s: %w", manifest.RunID, getErr)
		}
		if closeErr != nil {
			store.Close()
			eventStore.Close()
			delegation.Close()
			return nil, fmt.Errorf("close definition registry for run %s: %w", manifest.RunID, closeErr)
		}
		graph = reg.Graph
	} else {
		var err error
		graph, err = multiagent.CompatAdaptDefinition(manifest.Definition)
		if err != nil {
			store.Close()
			eventStore.Close()
			delegation.Close()
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
		multiagent.DurableRuntimeOptions{Interaction: interaction, Reflection: reflection, Memory: reflectionMemory, Proposals: proposals, Delegation: delegationOptions, ResourceNegotiation: manifest.ResourceNegotiation},
	)
	if err != nil {
		store.Close()
		eventStore.Close()
		delegation.Close()
		return nil, err
	}
	return &referenceRuntime{runtime: runtime, store: store, events: eventStore, delegation: delegation, workspaceID: manifest.WorkspaceID}, nil
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

func recoverOrCreateFreewillReferenceWorkspace(ctx context.Context, source, runID string) (workspacePath, workspaceID, sourcePath, worktreePath string, err error) {
	sourcePath, _, err = referenceWorkspace(source)
	if err != nil {
		return "", "", "", "", err
	}
	worktreeRoot := filepath.Join(sourcePath, ".prizm", "worktrees")
	worktreePath = filepath.Join(worktreeRoot, gitx.SafeID(runID, "run"))
	if info, statErr := os.Stat(worktreePath); statErr == nil {
		if !info.IsDir() {
			return "", "", "", worktreePath, errors.New("freewill: deterministic workspace path is not a directory")
		}
		list, listErr := gitx.RunCommand(ctx, sourcePath, "", "git", "worktree", "list", "--porcelain")
		if listErr != nil {
			return "", "", "", worktreePath, listErr
		}
		registered := false
		for _, line := range strings.Split(list, "\n") {
			if !strings.HasPrefix(line, "worktree ") {
				continue
			}
			listedPath := filepath.Clean(strings.TrimPrefix(line, "worktree "))
			candidatePath := filepath.Clean(worktreePath)
			if runtime.GOOS == "windows" {
				registered = strings.EqualFold(listedPath, candidatePath)
			} else {
				registered = listedPath == candidatePath
			}
			if registered {
				break
			}
		}
		if !registered {
			return "", "", "", worktreePath, errors.New("freewill: refusing to reuse an unregistered deterministic workspace path")
		}
		workspacePath, workspaceID, err = referenceWorkspace(worktreePath)
		if err != nil {
			return "", "", "", worktreePath, err
		}
		if !safety.IsWithinRoot(workspacePath, worktreeRoot) {
			return "", "", "", worktreePath, errors.New("freewill: recovered workspace is outside its owned worktree root")
		}
		return workspacePath, workspaceID, sourcePath, worktreePath, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", "", "", worktreePath, statErr
	}
	workspacePath, workspaceID, sourcePath, err = isolatedReferenceWorkspace(ctx, sourcePath, runID)
	return workspacePath, workspaceID, sourcePath, worktreePath, err
}

func cleanupUnadmittedFreewillWorkspace(ctx context.Context, sourcePath, worktreePath, manifestPath string) error {
	worktreeRoot := filepath.Join(sourcePath, ".prizm", "worktrees")
	if !safety.IsWithinRoot(worktreePath, worktreeRoot) {
		return errors.New("freewill: refusing to clean an unexpected child workspace")
	}
	removeErr := gitx.RemoveWorktreeChecked(ctx, sourcePath, worktreePath)
	var manifestErr error
	for _, path := range []string{manifestPath, manifestPath + ".tmp"} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			manifestErr = errors.Join(manifestErr, err)
		}
	}
	return errors.Join(removeErr, manifestErr)
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

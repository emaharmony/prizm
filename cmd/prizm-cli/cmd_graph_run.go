package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/emaharmony/prism/internal/event"
	"github.com/emaharmony/prism/internal/workflow/multiagent"
)

// executeGraphRun is PR6's `prism graph run` command: it resolves a target
// WorkflowDefinition — either registry-backed (--workflow[/--version]) or a
// local file that gets validated+compiled+registered first as a local-dev
// convenience (--file) — then starts a real, durably-persisted run against
// it, wiring the SAME live agent/provider/tool/validation composition
// openLiveReferenceRuntime already builds for the Phase 1 reference
// workflow (prepareGraphRun below reuses openLiveReferenceRuntime directly:
// since graph-run-started runs share the reference workflow's manifest
// format — see cmd_workflow_multiagent.go's referenceWorkflowManifest
// WorkflowVersion/DefinitionDBPath fields — openReferenceRuntime already
// knows how to build a *multiagent.CompiledGraph for a registry-backed
// manifest, so there is no separate composition path to build or keep in
// sync).
func executeGraphRun(args []string) error {
	fs := flag.NewFlagSet("graph run", flag.ExitOnError)
	workflowID := fs.String("workflow", "", "Registered workflow ID to run (registry-backed)")
	version := fs.Int64("version", 0, "Registered workflow version to run (0 = latest)")
	file := fs.String("file", "", "Local workflow definition file to validate, compile, register, then run")
	sourceRef := fs.String("source-ref", "", "Optional provenance label recorded when --file registers a new version")
	inputPath := fs.String("input", "", `Path to a JSON task input file, e.g. {"id":"...","description":"..."}`)
	workspace := fs.String("workspace", ".", "Workspace directory the run's tools operate within")
	runDir := fs.String("run-dir", "./runs", "Directory for run outputs")
	dbPath := fs.String("db", "./runs/multiagent_definitions.db", "Path to the definition registry SQLite database")
	configPath := fs.String("config", "prism.yaml", "Path to prism.yaml configuration file")
	fs.Parse(args)

	if strings.TrimSpace(*file) == "" && strings.TrimSpace(*workflowID) == "" {
		return errors.New("graph run requires either --workflow (registry-backed) or --file (local definition)")
	}
	if strings.TrimSpace(*file) != "" && strings.TrimSpace(*workflowID) != "" {
		return errors.New("graph run: --workflow and --file are mutually exclusive")
	}

	store, err := multiagent.NewDefinitionStore(*dbPath)
	if err != nil {
		return err
	}
	defer store.Close()

	reg, err := resolveGraphRunTarget(store, *file, *workflowID, *version, *sourceRef)
	if err != nil {
		return err
	}
	fmt.Printf("Workflow: %s v%d (fingerprint %s)\n", reg.WorkflowID, reg.Version, reg.Fingerprint)

	task, err := loadGraphRunTaskInput(*inputPath)
	if err != nil {
		return err
	}

	runID, rt, err := prepareGraphRun(*runDir, *dbPath, *configPath, *workspace, reg, task)
	if err != nil {
		return err
	}
	defer rt.close()

	fmt.Printf("Run ID: %s\n", runID)
	fmt.Printf("Artifacts: %s\n", filepath.Join(*runDir, runID))

	state, runErr := rt.runtime.Run(context.Background(), multiagent.RunRequest{RunID: runID, Task: task})
	fmt.Printf("Status: %s\n", state.Status)
	fmt.Println()
	fmt.Println("To manage this run later (these existing commands work against any durable multi-agent run):")
	fmt.Printf("  prism workflow status %s --run-dir %s\n", runID, *runDir)
	fmt.Printf("  prism workflow resume %s --run-dir %s --config %s\n", runID, *runDir, *configPath)
	fmt.Printf("  prism workflow cancel %s --run-dir %s\n", runID, *runDir)
	return runErr
}

// resolveGraphRunTarget resolves the RegisteredDefinition graph run should
// execute: either --file (load+compile via the exact loader/compiler PR5's
// `graph validate`/`graph compile` already use, then register it — printing
// diagnostics the same way `graph validate` does) or --workflow[/--version]
// (a plain registry lookup, Latest when version is 0).
//
// This deliberately calls multiagent.LoadDefinition/multiagent.Compile
// directly rather than cmd_graph_common.go's loadAndCompileGraph: that
// shared helper returns only (*CompiledGraph, Diagnostics, error), not the
// WorkflowDefinition value Register also needs, so reusing it here would
// require re-deriving the definition another way. Calling the two
// underlying package functions directly is one line more than the helper,
// not a reimplementation of its error handling.
func resolveGraphRunTarget(
	store *multiagent.DefinitionStore,
	file, workflowID string, version int64, sourceRef string,
) (multiagent.RegisteredDefinition, error) {
	if strings.TrimSpace(file) == "" {
		if version > 0 {
			return store.Get(context.Background(), workflowID, version)
		}
		return store.Latest(context.Background(), workflowID)
	}

	def, idx, diags, err := multiagent.LoadDefinition(file)
	if err != nil {
		return multiagent.RegisteredDefinition{}, fmt.Errorf("read %s: %w", file, err)
	}
	if diags.HasErrors() {
		printDiagnostics(os.Stderr, diags)
		return multiagent.RegisteredDefinition{}, fmt.Errorf("%s failed to load: %d error diagnostic(s)", file, countErrorDiagnostics(diags))
	}
	graph, compileDiags, err := multiagent.Compile(def, idx, multiagent.CompileOptions{})
	allDiags := append(diags, compileDiags...)
	if len(allDiags) > 0 {
		printDiagnostics(os.Stderr, allDiags)
	}
	if err != nil {
		return multiagent.RegisteredDefinition{}, err
	}

	reg, err := store.Register(context.Background(), def, graph, sourceRef)
	if err != nil && !errors.Is(err, multiagent.ErrDefinitionUnchanged) {
		return multiagent.RegisteredDefinition{}, fmt.Errorf("register %s: %w", file, err)
	}
	if errors.Is(err, multiagent.ErrDefinitionUnchanged) {
		fmt.Printf("Definition unchanged since v%d; running that version.\n", reg.Version)
	} else {
		fmt.Printf("Registered %s as v%d.\n", reg.WorkflowID, reg.Version)
	}
	return reg, nil
}

// loadGraphRunTaskInput reads the optional --input JSON file into a
// multiagent.TaskReference. A missing --input is valid (an empty
// description); a missing/blank task ID is filled in with a fresh one so
// Run's "task id is required" check always passes.
func loadGraphRunTaskInput(inputPath string) (multiagent.TaskReference, error) {
	var task multiagent.TaskReference
	if strings.TrimSpace(inputPath) != "" {
		data, err := os.ReadFile(inputPath)
		if err != nil {
			return task, fmt.Errorf("read input: %w", err)
		}
		if err := json.Unmarshal(data, &task); err != nil {
			return task, fmt.Errorf("decode input: %w", err)
		}
	}
	if strings.TrimSpace(task.ID) == "" {
		task.ID = "task_" + event.NewRunID()
	}
	return task, nil
}

// prepareGraphRun performs graph run's synchronous setup — resolving the
// run's workspace identity, writing its manifest (the SAME
// referenceWorkflowManifest shape and file location
// cmd_workflow_multiagent.go's reference-workflow path already uses, with
// WorkflowVersion/DefinitionDBPath set instead of an embedded Definition —
// see that file's registryBacked doc), and opening a fully-wired live
// runtime via openLiveReferenceRuntime — and returns before actually driving
// the run. This split (mirroring referenceMultiAgentController.Resume's
// sync-setup/async-execution split) lets a caller choose to drive Run()
// synchronously (the CLI, so it can print the final status) or in a detached
// goroutine (the API's WorkflowRunStarter, so a POST .../run request doesn't
// block for the run's full duration).
func prepareGraphRun(
	runDir, dbPath, configPath, workspace string,
	reg multiagent.RegisteredDefinition,
	task multiagent.TaskReference,
) (runID string, rt *referenceRuntime, err error) {
	workspacePath, workspaceID, err := referenceWorkspace(workspace)
	if err != nil {
		return "", nil, err
	}
	runID = event.NewRunID()
	manifest := referenceWorkflowManifest{
		SchemaVersion:    referenceManifestSchemaVersion,
		RunID:            runID,
		WorkflowID:       reg.WorkflowID,
		WorkflowVersion:  reg.Version,
		DefinitionDBPath: dbPath,
		Input:            multiagent.ReferenceWorkflowInput{Workspace: workspace},
		WorkspaceID:      workspaceID,
		WorkspacePath:    workspacePath,
	}
	if err := writeReferenceManifest(runDir, manifest); err != nil {
		return "", nil, err
	}
	rt, err = openLiveReferenceRuntime(runDir, configPath, manifest)
	if err != nil {
		return "", nil, err
	}
	return runID, rt, nil
}

// graphRunStarter implements the API's narrow WorkflowRunStarter seam
// (internal/api/multiagent_definitions.go) by reusing the exact same
// resolve/prepare machinery executeGraphRun uses, factored out above so this
// type does not duplicate it. Constructed in cmd_serve.go alongside the
// existing referenceMultiAgentController.
type graphRunStarter struct {
	runDir, dbPath, configPath, workspace string
}

// newGraphRunStarter builds the starter `prism serve` wires into
// api.Config.WorkflowRunStarter.
func newGraphRunStarter(runDir, dbPath, configPath, workspace string) *graphRunStarter {
	return &graphRunStarter{runDir: runDir, dbPath: dbPath, configPath: configPath, workspace: workspace}
}

// StartRun resolves (workflowID, version|latest) against the definition
// registry, decodes input (a raw JSON multiagent.TaskReference, or empty for
// an auto-generated task id), and starts the run — validating synchronously
// (an unknown workflow/version, malformed input, or broken live-runtime
// wiring surfaces immediately) and then driving Run() in a detached
// goroutine, mirroring referenceMultiAgentController.Resume's exact
// sync-setup/async-execution split so a POST .../run request returns as soon
// as the run has been durably created rather than blocking for its full
// duration.
func (g *graphRunStarter) StartRun(
	ctx context.Context,
	workflowID string,
	version int64,
	input json.RawMessage,
) (string, error) {
	store, err := multiagent.NewDefinitionStore(g.dbPath)
	if err != nil {
		return "", err
	}
	defer store.Close()

	var reg multiagent.RegisteredDefinition
	if version > 0 {
		reg, err = store.Get(ctx, workflowID, version)
	} else {
		reg, err = store.Latest(ctx, workflowID)
	}
	if err != nil {
		return "", err
	}

	var task multiagent.TaskReference
	if len(input) > 0 {
		if err := json.Unmarshal(input, &task); err != nil {
			return "", fmt.Errorf("decode run input: %w", err)
		}
	}
	if strings.TrimSpace(task.ID) == "" {
		task.ID = "task_" + event.NewRunID()
	}

	runID, rt, err := prepareGraphRun(g.runDir, g.dbPath, g.configPath, g.workspace, reg, task)
	if err != nil {
		return "", err
	}
	go func() {
		defer rt.close()
		// context.Background(): this run must outlive the HTTP
		// request/response cycle that started it — the caller observes
		// progress via the existing SSE stream, matching how
		// referenceMultiAgentController.Resume already detaches Resume().
		if _, runErr := rt.runtime.Run(context.Background(), multiagent.RunRequest{RunID: runID, Task: task}); runErr != nil {
			log.Printf("[WARN] graph run %s ended: %v", runID, runErr)
		}
	}()
	return runID, nil
}

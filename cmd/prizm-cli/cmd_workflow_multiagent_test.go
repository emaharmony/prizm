package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

func TestLoadReferenceWorkflowInputStrictContract(t *testing.T) {
	dir := t.TempDir()
	validPath := filepath.Join(dir, "valid.json")
	if err := os.WriteFile(validPath, []byte(`{
  "objective": "Implement the accepted design",
  "workspace": ".",
  "constraints": ["preserve compatibility"],
  "acceptance_criteria": ["tests pass"]
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	input, err := loadReferenceWorkflowInput(validPath)
	if err != nil || input.Objective != "Implement the accepted design" {
		t.Fatalf("valid input = %#v, error %v", input, err)
	}

	unknownPath := filepath.Join(dir, "unknown.json")
	if err := os.WriteFile(unknownPath, []byte(`{"objective":"x","workspace":".","graph":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadReferenceWorkflowInput(unknownPath); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field error = %v", err)
	}
	trailingPath := filepath.Join(dir, "trailing.json")
	if err := os.WriteFile(trailingPath, []byte(`{"objective":"x","workspace":"."} {}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadReferenceWorkflowInput(trailingPath); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("trailing JSON error = %v", err)
	}

	if _, err := loadReferenceWorkflowInput(""); err == nil {
		t.Fatal("missing --input was accepted")
	}
}

func TestReferenceManifestPersistsEffectiveDefinitionAndWorkspace(t *testing.T) {
	runDir := t.TempDir()
	workspace := t.TempDir()
	workspacePath, workspaceID, err := referenceWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	input := multiagent.ReferenceWorkflowInput{Objective: "test manifest", Workspace: workspace}
	definition, err := multiagent.ApplyReferenceOverrides(multiagent.DefaultReferenceDefinition(), input)
	if err != nil {
		t.Fatal(err)
	}
	manifest := referenceWorkflowManifest{
		SchemaVersion: referenceManifestSchemaVersion,
		RunID:         "run-manifest-test",
		WorkflowID:    multiagent.ReferenceWorkflowID,
		Input:         input,
		Definition:    definition,
		WorkspaceID:   workspaceID,
		WorkspacePath: workspacePath,
	}
	if err := writeReferenceManifest(runDir, manifest); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadReferenceManifest(runDir, manifest.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.WorkspaceID != workspaceID || loaded.Definition.Budgets.MaxTokens != multiagent.Unlimited {
		t.Fatalf("loaded manifest = %#v", loaded)
	}
	if !isReferenceWorkflowRun(runDir, manifest.RunID) {
		t.Fatal("reference run was not detected")
	}
}

func TestApprovalWorkspaceForRunUsesPersistedWorkspace(t *testing.T) {
	runDir := t.TempDir()
	workspace := t.TempDir()
	workspacePath, workspaceID, err := referenceWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	input := multiagent.ReferenceWorkflowInput{Objective: "approval workspace", Workspace: workspace}
	definition, err := multiagent.ApplyReferenceOverrides(multiagent.DefaultReferenceDefinition(), input)
	if err != nil {
		t.Fatal(err)
	}
	manifest := referenceWorkflowManifest{
		SchemaVersion: referenceManifestSchemaVersion,
		RunID:         "run-approval-workspace",
		WorkflowID:    multiagent.ReferenceWorkflowID,
		Input:         input,
		Definition:    definition,
		WorkspaceID:   workspaceID,
		WorkspacePath: workspacePath,
	}
	if err := writeReferenceManifest(runDir, manifest); err != nil {
		t.Fatal(err)
	}

	resolved, hasManifest, err := approvalWorkspaceForRun(runDir, manifest.RunID, "source-checkout")
	if err != nil || !hasManifest || resolved != workspacePath {
		t.Fatalf("resolved=%q hasManifest=%t err=%v", resolved, hasManifest, err)
	}
}

func TestApprovalWorkspaceForRunFailsClosedOnInvalidManifest(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version int
		flowID  string
	}{
		{name: "unknown workflow with missing pinned version", version: referenceManifestSchemaVersion, flowID: "future-workflow"},
		{name: "unsupported manifest schema", version: referenceManifestSchemaVersion + 1, flowID: multiagent.ReferenceWorkflowID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runDir := t.TempDir()
			manifest := referenceWorkflowManifest{
				SchemaVersion: tc.version,
				RunID:         "run-invalid-manifest",
				WorkflowID:    tc.flowID,
				WorkspacePath: filepath.Join(t.TempDir(), "isolated"),
			}
			if err := writeReferenceManifest(runDir, manifest); err != nil {
				t.Fatal(err)
			}

			resolved, hasManifest, err := approvalWorkspaceForRun(runDir, manifest.RunID, "source-checkout")
			if err == nil || !hasManifest || resolved != "" || !strings.Contains(err.Error(), "refusing approval") {
				t.Fatalf("resolved=%q hasManifest=%t err=%v", resolved, hasManifest, err)
			}
		})
	}
}

func TestApprovalWorkspaceForRunAllowsLegacyFallbackWithoutManifest(t *testing.T) {
	workspace := t.TempDir()
	resolved, hasManifest, err := approvalWorkspaceForRun(t.TempDir(), "run-without-manifest", workspace)
	if err != nil || hasManifest || resolved != workspace {
		t.Fatalf("resolved=%q hasManifest=%t err=%v", resolved, hasManifest, err)
	}
}

func TestLoadReferenceManifestAllowsCleanedTerminalWorkspace(t *testing.T) {
	runDir := t.TempDir()
	workspace := t.TempDir()
	workspacePath, workspaceID, err := referenceWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	input := multiagent.ReferenceWorkflowInput{Objective: "terminal recovery", Workspace: workspace}
	definition, err := multiagent.ApplyReferenceOverrides(multiagent.DefaultReferenceDefinition(), input)
	if err != nil {
		t.Fatal(err)
	}
	manifest := referenceWorkflowManifest{
		SchemaVersion: referenceManifestSchemaVersion,
		RunID:         "run-cleaned-workspace", WorkflowID: multiagent.ReferenceWorkflowID,
		Input: input, Definition: definition, WorkspaceID: workspaceID,
		WorkspacePath: workspacePath, WorkspaceCleaned: true,
	}
	if err := writeReferenceManifest(runDir, manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(workspace); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadReferenceManifest(runDir, manifest.RunID)
	if err != nil || !loaded.WorkspaceCleaned {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
}

func TestBindConfiguredReferenceProfilesUsesCapabilitiesAndPreservesOverrides(t *testing.T) {
	explicit := map[multiagent.Role]string{
		multiagent.RoleReviewer: "explicit-reviewer",
	}
	definition, err := multiagent.ApplyReferenceOverrides(multiagent.DefaultReferenceDefinition(), multiagent.ReferenceWorkflowInput{
		Objective: "profile composition", Workspace: ".", RoleProfiles: explicit,
	})
	if err != nil {
		t.Fatal(err)
	}
	bindConfiguredReferenceProfiles(&definition, explicit, []orchestrator.AgentConfig{
		{ID: "planner-profile", Role: "planner", Capabilities: []string{"plan"}},
		{ID: "coder-profile", Role: "coder", Capabilities: []string{"code", "test"}},
		{ID: "reviewer-profile", Role: "reviewer", Capabilities: []string{"review"}},
	})
	profiles := map[multiagent.Role]string{}
	for _, role := range definition.Roles {
		profiles[role.Role] = role.AgentRef
	}
	if profiles[multiagent.RolePlanner] != "planner-profile" ||
		profiles[multiagent.RoleDeveloper] != "coder-profile" ||
		profiles[multiagent.RoleTester] != "coder-profile" ||
		profiles[multiagent.RoleReviewer] != "explicit-reviewer" {
		t.Fatalf("profiles=%#v", profiles)
	}
}

func TestReferenceWorkspaceIdentityIsStableAndScoped(t *testing.T) {
	workspace := t.TempDir()
	firstPath, firstID, err := referenceWorkspace(workspace)
	if err != nil {
		t.Fatal(err)
	}
	secondPath, secondID, err := referenceWorkspace(filepath.Join(workspace, "."))
	if err != nil {
		t.Fatal(err)
	}
	if firstPath != secondPath || firstID != secondID || !strings.HasPrefix(firstID, "workspace_") {
		t.Fatalf("workspace identity changed: %q/%q then %q/%q", firstPath, firstID, secondPath, secondID)
	}
	file := filepath.Join(workspace, "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := referenceWorkspace(file); err == nil {
		t.Fatal("file was accepted as a workspace")
	}
}

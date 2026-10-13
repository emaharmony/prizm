package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/validation"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

func writeValidationProfileConfig(t *testing.T, path, args string) {
	t.Helper()
	data := "validation_profiles:\n  - name: factory_pytest_selected_v1\n    description: selected project tests\n    command: go\n    args: [" + args + "]\n    working_dir: .\n    timeout_seconds: 600\n    allowed_exit_codes: [0]\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestChangedAcceptedValidationProfileFailsRunThroughDurableOutbox(t *testing.T) {
	workspace, runDir := t.TempDir(), t.TempDir()
	config := filepath.Join(t.TempDir(), "prizm.yaml")
	writeValidationProfileConfig(t, config, `"version"`)
	manifest := validationProfileTestManifest(workspace)
	manifest.RunID = "run-profile-changed"
	if err := bindManifestValidationProfiles(config, &manifest); err != nil {
		t.Fatal(err)
	}
	runtime, err := openInspectionReferenceRuntime(runDir, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.runtime.Accept(context.Background(), multiagent.RunRequest{
		RunID: manifest.RunID, WorkspaceID: manifest.WorkspaceID,
		Task: multiagent.TaskReference{ID: "task-profile-change", Description: "verify profile drift is terminal"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.close(); err != nil {
		t.Fatal(err)
	}
	writeValidationProfileConfig(t, config, `"env"`)
	if _, err := openLiveReferenceRuntime(runDir, config, manifest); err == nil {
		t.Fatal("changed profile was accepted for resume")
	}
	inspection, err := openInspectionReferenceRuntime(runDir, manifest)
	if err != nil {
		t.Fatal(err)
	}
	record, err := inspection.runtime.Inspect(context.Background(), manifest.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if record.State.Status != multiagent.RunStatusFailed || record.Failure == nil || record.Failure.Message != "validation_profile_configuration_changed" {
		t.Fatalf("durable mismatch outcome=%#v failure=%#v", record.State, record.Failure)
	}
	failures, err := inspection.events.Query(context.Background(), event.EventFilter{RunID: manifest.RunID, Type: event.EventMultiAgentRunFailed})
	if err != nil || len(failures) != 1 {
		t.Fatalf("terminal outbox events=%d err=%v", len(failures), err)
	}
	if err := inspection.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := openLiveReferenceRuntime(runDir, config, manifest); err == nil {
		t.Fatal("changed profile became accepted on repeated open")
	}
	inspection, err = openInspectionReferenceRuntime(runDir, manifest)
	if err != nil {
		t.Fatal(err)
	}
	defer inspection.close()
	failures, err = inspection.events.Query(context.Background(), event.EventFilter{RunID: manifest.RunID, Type: event.EventMultiAgentRunFailed})
	if err != nil || len(failures) != 1 {
		t.Fatalf("duplicate terminal notices=%d err=%v", len(failures), err)
	}
}

func validationProfileTestManifest(workspace string) referenceWorkflowManifest {
	definition := multiagent.DefaultReferenceDefinition()
	for index := range definition.Roles {
		if definition.Roles[index].Role == multiagent.RoleTester {
			definition.Roles[index].ValidationProfiles = []string{"factory_pytest_selected_v1"}
		}
	}
	return referenceWorkflowManifest{RunID: "run-profile-binding", Definition: definition, WorkspacePath: workspace}
}

func TestValidationProfileFingerprintBindsEffectiveHostSpec(t *testing.T) {
	workspace := t.TempDir()
	config := filepath.Join(t.TempDir(), "prizm.yaml")
	writeValidationProfileConfig(t, config, "\"version\"")
	manifest := validationProfileTestManifest(workspace)
	if err := bindManifestValidationProfiles(config, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.ValidationProfileFingerprints) != 1 || manifest.ValidationProfileFingerprints["factory_pytest_selected_v1"] == "" {
		t.Fatalf("fingerprints=%v", manifest.ValidationProfileFingerprints)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"args":["version"]`) || strings.Contains(string(encoded), `"command":"go"`) {
		t.Fatalf("manifest leaked profile spec: %s", encoded)
	}
	if err := checkAcceptedValidationProfiles(config, manifest); err != nil {
		t.Fatalf("unchanged config: %v", err)
	}
	writeValidationProfileConfig(t, config, "\"env\"")
	if err := checkAcceptedValidationProfiles(config, manifest); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("changed profile error=%v", err)
	}
	os.Remove(config)
	if err := checkAcceptedValidationProfiles(config, manifest); err == nil {
		t.Fatal("missing profile config accepted")
	}
}

func TestValidationProfileRegistryRejectsBuiltInCollision(t *testing.T) {
	cfg := &orchestrator.Config{ValidationProfiles: []validation.Profile{{
		Name: "go_test_all", Command: "go", Args: []string{"test", "./..."}, WorkingDir: ".", TimeoutSeconds: 10,
	}}}
	if _, err := configuredValidationRegistry(cfg); err == nil {
		t.Fatal("duplicate built-in profile accepted")
	}
}

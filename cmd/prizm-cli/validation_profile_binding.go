package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/validation"
	"github.com/emaharmony/prizm/internal/workflow/multiagent"
)

func configuredValidationRegistry(cfg *orchestrator.Config) (*validation.Registry, error) {
	registry := validation.NewRegistry()
	for _, profile := range cfg.ValidationProfiles {
		if err := registry.Register(profile); err != nil {
			return nil, fmt.Errorf("register host validation profile %q: %w", profile.Name, err)
		}
	}
	return registry, nil
}

func profileFingerprints(configPath string, manifest referenceWorkflowManifest) (map[string]string, error) {
	cfg, err := orchestrator.LoadConfig(configPath)
	if err != nil {
		return nil, errors.New("validation profile configuration unavailable")
	}
	registry, err := configuredValidationRegistry(cfg)
	if err != nil {
		return nil, errors.New("validation profile configuration invalid")
	}
	graph, err := graphForManifest(manifest)
	if err != nil {
		return nil, errors.New("accepted workflow definition unavailable")
	}
	selected := map[string]struct{}{}
	for _, node := range graph.Nodes() {
		for _, name := range node.RoleConfig.ValidationProfiles {
			if strings.TrimSpace(name) != "" {
				selected[name] = struct{}{}
			}
		}
	}
	names := make([]string, 0, len(selected))
	for name := range selected {
		names = append(names, name)
	}
	sort.Strings(names)
	fingerprints := make(map[string]string, len(names))
	for _, name := range names {
		profile, resolveErr := registry.Resolve(name)
		if resolveErr != nil {
			return nil, fmt.Errorf("validation profile %s unavailable", name)
		}
		if err := validation.ValidateProfileSafety(profile, manifest.WorkspacePath); err != nil {
			return nil, fmt.Errorf("validation profile %s is unsafe for this workspace", name)
		}
		canonical, _ := json.Marshal(struct {
			Name             string   `json:"name"`
			Command          string   `json:"command"`
			Args             []string `json:"args"`
			WorkingDir       string   `json:"working_dir"`
			TimeoutSeconds   int      `json:"timeout_seconds"`
			AllowedExitCodes []int    `json:"allowed_exit_codes"`
		}{profile.Name, profile.Command, append([]string(nil), profile.Args...), profile.WorkingDir, profile.TimeoutSeconds, append([]int(nil), profile.AllowedExitCodes...)})
		digest := sha256.Sum256(canonical)
		fingerprints[name] = hex.EncodeToString(digest[:])
	}
	return fingerprints, nil
}

func graphForManifest(manifest referenceWorkflowManifest) (*multiagent.CompiledGraph, error) {
	if manifest.registryBacked() {
		store, err := multiagent.NewDefinitionStore(manifest.DefinitionDBPath)
		if err != nil {
			return nil, err
		}
		defer store.Close()
		definition, err := store.Get(context.Background(), manifest.WorkflowID, manifest.WorkflowVersion)
		if err != nil {
			return nil, err
		}
		return definition.Graph, nil
	}
	return multiagent.CompatAdaptDefinition(manifest.Definition)
}

func checkAcceptedValidationProfiles(configPath string, manifest referenceWorkflowManifest) error {
	current, err := profileFingerprints(configPath, manifest)
	if err != nil {
		return err
	}
	if len(manifest.ValidationProfileFingerprints) == 0 {
		for name := range current {
			if name != "echo_test" && name != "go_test_all" {
				return fmt.Errorf("validation profile %s was not bound at acceptance", name)
			}
		}
		return nil
	}
	if len(current) != len(manifest.ValidationProfileFingerprints) {
		return errors.New("validation profile configuration changed")
	}
	for name, fingerprint := range current {
		if manifest.ValidationProfileFingerprints[name] != fingerprint {
			return fmt.Errorf("validation profile %s configuration changed", name)
		}
	}
	return nil
}

func bindManifestValidationProfiles(configPath string, manifest *referenceWorkflowManifest) error {
	if manifest == nil {
		return errors.New("manifest is required")
	}
	fingerprints, err := profileFingerprints(configPath, *manifest)
	if err != nil {
		return err
	}
	manifest.ValidationProfileFingerprints = fingerprints
	return nil
}

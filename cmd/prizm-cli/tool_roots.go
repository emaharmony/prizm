package main

import (
	"log"
	"path/filepath"

	"github.com/emaharmony/prizm/internal/orchestrator"
)

func resolveConfiguredRoots(paths []string, label string) []string {
	roots := make([]string, 0, len(paths))
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			log.Printf("[WARN] invalid %s %q: %v", label, p, err)
			continue
		}
		roots = append(roots, abs)
		log.Printf("[TOOL] %s: %s", label, abs)
	}
	return roots
}

func configuredReadRoots(cfg *orchestrator.Config) []string {
	if cfg == nil {
		return nil
	}
	return resolveConfiguredRoots(cfg.EffectiveReadRoots(), "read root")
}

func configuredWriteRoots(cfg *orchestrator.Config) []string {
	if cfg == nil {
		return nil
	}
	return resolveConfiguredRoots(cfg.EffectiveWriteRoots(), "write root")
}

func configuredOrchestratorAgentID(cfg *orchestrator.Config) string {
	if cfg == nil {
		return ""
	}
	if p := cfg.PrimaryAgent(); p != nil {
		return p.ID
	}
	return ""
}

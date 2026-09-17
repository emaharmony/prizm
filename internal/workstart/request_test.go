package workstart

import (
	"path/filepath"
	"testing"

	"github.com/emaharmony/prizm/internal/orchestrator"
)

func TestResolveUnknownProjectNeedsLocationWithRecommendation(t *testing.T) {
	root := t.TempDir()
	cfg := &orchestrator.Config{
		Prizm: orchestrator.PrizmConfig{WriteRoots: []string{root}},
	}

	res, err := Resolve(cfg, Request{Project: "GudEats", Prompt: "start a web app"})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if !res.NeedsLocation {
		t.Fatal("expected location clarification")
	}
	want := filepath.Join(root, "GudEats")
	if res.Recommendation != want {
		t.Fatalf("recommendation = %q, want %q", res.Recommendation, want)
	}
}

func TestResolveExplicitRepoPathInsideWriteRoot(t *testing.T) {
	root := t.TempDir()
	cfg := &orchestrator.Config{
		Prizm: orchestrator.PrizmConfig{WriteRoots: []string{root}},
	}
	repoPath := filepath.Join(root, "GudEats")

	res, err := Resolve(cfg, Request{Project: "GudEats", RepoPath: repoPath, Prompt: "start a web app"})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if res.NeedsLocation {
		t.Fatal("did not expect location clarification")
	}
	if res.RepoPath != repoPath {
		t.Fatalf("repo path = %q, want %q", res.RepoPath, repoPath)
	}
}

func TestResolveExplicitRepoPathOutsideWriteRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	cfg := &orchestrator.Config{
		Prizm: orchestrator.PrizmConfig{WriteRoots: []string{root}},
	}

	_, err := Resolve(cfg, Request{Project: "GudEats", RepoPath: filepath.Join(outside, "GudEats"), Prompt: "start a web app"})
	if err == nil {
		t.Fatal("expected outside-root error")
	}
}

func TestResolveConfiguredProjectFirst(t *testing.T) {
	root := t.TempDir()
	repoPath := filepath.Join(root, "Configured")
	cfg := &orchestrator.Config{
		Prizm: orchestrator.PrizmConfig{WriteRoots: []string{root}},
		Projects: []orchestrator.ProjectConfig{
			{ID: "configured", RepoPath: repoPath, Channel: "channel-1"},
		},
	}

	res, err := Resolve(cfg, Request{Project: "configured", Prompt: "fix bug"})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if res.Project == nil || res.Project.ID != "configured" {
		t.Fatalf("project not resolved: %+v", res.Project)
	}
	if res.RepoPath != repoPath {
		t.Fatalf("repo path = %q, want %q", res.RepoPath, repoPath)
	}
	if res.Channel != "channel-1" {
		t.Fatalf("channel = %q", res.Channel)
	}
}

func TestResolveRequireLocationDoesNotUseDefaultProject(t *testing.T) {
	root := t.TempDir()
	cfg := &orchestrator.Config{
		Prizm: orchestrator.PrizmConfig{WriteRoots: []string{root}},
		Projects: []orchestrator.ProjectConfig{
			{ID: "default", RepoPath: filepath.Join(root, "Default"), Default: true},
		},
	}

	res, err := Resolve(cfg, Request{Prompt: "start a web app", RequireLocation: true})
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if !res.NeedsLocation {
		t.Fatal("expected location clarification instead of default project")
	}
	if res.ProjectID != "" {
		t.Fatalf("project id = %q", res.ProjectID)
	}
}

// Package codesummary provides bounded, artifact-backed codebase summaries.
package codesummary

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/provider"
)

const (
	maxKeyFileBytes = 12000
	maxDocs         = 12
	maxPackages     = 80
)

// RequestMatches reports whether a message is asking for a broad codebase summary.
func RequestMatches(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return false
	}

	// Universal veto, checked first so it also applies to strong-phrase matches.
	if strings.Contains(lower, "workflow editor") && !containsAny(lower, "codebase", "repo", "repository") {
		return false
	}

	// Strong phrases: unambiguous asks for a codebase-wide analysis/summary.
	// Mirrors internal/autopatch/classify.go's RequestMatches pattern.
	strong := []string{
		"run an analysis", "run a full analysis", "run a codebase analysis",
		"do an analysis", "give me an analysis", "give me a full analysis",
		"give me a summary", "analyse this", "analyze this", "analyse it",
		"analyze it", "can you analyze it", "can you analyse it",
		"can you analyze this", "can you analyse this", "summarize this",
		"summarise this", "codebase summary", "codebase overview",
		"repo summary", "repo overview", "repository summary",
		"repository overview", "architecture overview", "architecture summary",
	}
	for _, phrase := range strong {
		if strings.Contains(lower, phrase) {
			return true
		}
	}

	// Fallback: action word + target noun + prizm/code hint (existing
	// behavior, extended with "analysis" and "summarise").
	action := containsAny(lower, "summarize", "summarise", "summary", "overview", "architecture", "explain", "analyze", "analyse", "analysis", "read")
	target := containsAny(lower, "codebase", "repo", "repository", "source", "project")
	prizmHint := strings.Contains(lower, "prizm")
	return action && target && (prizmHint || strings.Contains(lower, "code"))
}

// Config controls one summary run.
type Config struct {
	Root        string
	ArtifactDir string
	TaskID      string
	AgentID     string
	Project     string
	Model       string
	Provider    provider.Provider
	Timeout     time.Duration
}

// Result is persisted into the task result map.
type Result struct {
	ReportPath   string `json:"report_path"`
	EvidencePath string `json:"evidence_path"`
	FilesScanned int    `json:"files_scanned"`
	PackagesSeen int    `json:"packages_seen"`
	DurationMS   int64  `json:"duration_ms"`
	Excerpt      string `json:"summary_excerpt"`
}

// Evidence is the bounded source material used to build the report.
type Evidence struct {
	Root          string            `json:"root"`
	GeneratedAt   string            `json:"generated_at"`
	KeyFiles      map[string]string `json:"key_files"`
	Entrypoints   []string          `json:"entrypoints"`
	Packages      []PackageSummary  `json:"packages"`
	Docs          []string          `json:"docs"`
	TopLevelDirs  []string          `json:"top_level_dirs"`
	RecentCommits []string          `json:"recent_commits"`
	FilesScanned  int               `json:"files_scanned"`
}

// PackageSummary captures a Go package directory at architecture-map granularity.
type PackageSummary struct {
	Path       string   `json:"path"`
	Name       string   `json:"name"`
	GoFiles    int      `json:"go_files"`
	TestFiles  int      `json:"test_files"`
	FileNames  []string `json:"file_names"`
	FirstLines []string `json:"first_lines,omitempty"`
}

// Run collects evidence, writes artifacts, optionally asks the model for synthesis, and returns result metadata.
func Run(ctx context.Context, cfg Config) (Result, error) {
	start := time.Now()
	if cfg.Root == "" {
		cfg.Root = "."
	}
	root, err := filepath.Abs(cfg.Root)
	if err != nil {
		return Result{}, err
	}
	if cfg.ArtifactDir == "" {
		cfg.ArtifactDir = filepath.Join(root, ".prizm", "data", "codebase-summary", cfg.TaskID)
	}
	if err := os.MkdirAll(cfg.ArtifactDir, 0755); err != nil {
		return Result{}, err
	}

	evidence, err := Collect(ctx, root)
	if err != nil {
		return Result{}, err
	}

	report := BuildReport(evidence, "")
	if cfg.Provider != nil {
		timeout := cfg.Timeout
		if timeout <= 0 {
			timeout = 10 * time.Minute
		}
		llmCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if synthesis, synthErr := synthesize(llmCtx, cfg, evidence); synthErr == nil && strings.TrimSpace(synthesis) != "" {
			report = BuildReport(evidence, synthesis)
		}
	}

	evidencePath := filepath.Join(cfg.ArtifactDir, "evidence.json")
	reportPath := filepath.Join(cfg.ArtifactDir, "report.md")
	if err := writeJSON(evidencePath, evidence); err != nil {
		return Result{}, err
	}
	if err := os.WriteFile(reportPath, []byte(report), 0644); err != nil {
		return Result{}, err
	}

	return Result{
		ReportPath:   reportPath,
		EvidencePath: evidencePath,
		FilesScanned: evidence.FilesScanned,
		PackagesSeen: len(evidence.Packages),
		DurationMS:   time.Since(start).Milliseconds(),
		Excerpt:      excerpt(report, 900),
	}, nil
}

// Collect gathers bounded evidence from the repository.
func Collect(ctx context.Context, root string) (Evidence, error) {
	ev := Evidence{
		Root:        root,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		KeyFiles:    make(map[string]string),
	}
	ev.TopLevelDirs = topLevelDirs(root)
	ev.KeyFiles = readKeyFiles(root)
	ev.Entrypoints = globRel(root, "cmd/*/main.go")
	ev.Docs = selectedDocs(root)
	ev.RecentCommits = recentCommits(root)

	pkgs, scanned, err := collectPackages(ctx, root)
	if err != nil {
		return ev, err
	}
	ev.Packages = pkgs
	ev.FilesScanned = scanned
	return ev, nil
}

// BuildReport creates the deterministic architecture-map report.
func BuildReport(ev Evidence, synthesis string) string {
	var b strings.Builder
	b.WriteString("# Prizm Codebase Architecture Summary\n\n")
	b.WriteString(fmt.Sprintf("Generated: %s\n\n", ev.GeneratedAt))

	if strings.TrimSpace(synthesis) != "" {
		b.WriteString("## Model Synthesis\n")
		b.WriteString(strings.TrimSpace(synthesis))
		b.WriteString("\n\n")
	}

	b.WriteString("## Architecture Map\n")
	b.WriteString("Prizm is organized as a Go service/CLI with command entrypoints under `cmd/`, runtime subsystems under `internal/`, Python semantic memory under `remembrance/`, and local dashboard assets under `internal/dashboard/static/`.\n\n")

	b.WriteString("## Entrypoints\n")
	for _, e := range ev.Entrypoints {
		b.WriteString("- `" + e + "`\n")
	}
	if len(ev.Entrypoints) == 0 {
		b.WriteString("- No Go command entrypoints found.\n")
	}
	b.WriteString("\n")

	b.WriteString("## Major Package Areas\n")
	for _, p := range ev.Packages {
		if strings.HasPrefix(p.Path, "internal/") || strings.HasPrefix(p.Path, "cmd/") {
			b.WriteString(fmt.Sprintf("- `%s` (%s): %d Go files, %d tests", p.Path, p.Name, p.GoFiles, p.TestFiles))
			if len(p.FirstLines) > 0 {
				b.WriteString(" - " + strings.Join(p.FirstLines, " "))
			}
			b.WriteString("\n")
		}
	}
	b.WriteString("\n")

	b.WriteString("## Runtime Flow\n")
	b.WriteString("- `prizm serve` loads `prizm.yaml`, connects to NATS, registers agents/providers, starts sessions/tasks, wires Discord, exposes health/API endpoints, and injects Remembrance/context/tool guidance before LLM calls.\n")
	b.WriteString("- Codebase-level questions should use the async summary task because normal Discord turns have a short handler timeout and tool loops are intentionally bounded.\n")
	b.WriteString("- Remembrance is a separate Python/FastAPI memory service that supplies semantic context over HTTP.\n\n")

	b.WriteString("## Configuration And UI\n")
	b.WriteString("- `prizm.yaml` is the live service config for agents, channels, bridge, memory, paths, sessions, and timeouts.\n")
	b.WriteString("- The workflow editor converts agent config into graph nodes/edges, exposes CRUD through `/api/v1/editor`, and previews YAML through `/api/v1/editor/save`.\n\n")

	b.WriteString("## Evidence\n")
	b.WriteString(fmt.Sprintf("- Files scanned: %d\n", ev.FilesScanned))
	b.WriteString(fmt.Sprintf("- Packages seen: %d\n", len(ev.Packages)))
	if len(ev.Docs) > 0 {
		b.WriteString("- Key docs: `" + strings.Join(ev.Docs, "`, `") + "`\n")
	}
	if len(ev.RecentCommits) > 0 {
		b.WriteString("- Recent commits:\n")
		for _, c := range ev.RecentCommits {
			b.WriteString("  - " + c + "\n")
		}
	}
	return b.String()
}

func synthesize(ctx context.Context, cfg Config, ev Evidence) (string, error) {
	payload, _ := json.MarshalIndent(ev, "", "  ")
	prompt := "Create a concise architecture-map summary of this Prizm codebase evidence. Focus on entrypoints, major subsystems, runtime flow, config, memory/RagCAG, UI/editor, and risks. Do not invent facts.\n\n" + string(payload)
	resp, err := cfg.Provider.Generate(ctx, provider.GenerateRequest{
		RunID:       cfg.TaskID,
		Agent:       cfg.AgentID,
		Project:     cfg.Project,
		Task:        "Summarize Prizm codebase architecture",
		Prompt:      prompt,
		Model:       cfg.Model,
		Temperature: 0.2,
		MaxTokens:   1600,
	})
	if err != nil {
		return "", err
	}
	return resp.Text, nil
}

func readKeyFiles(root string) map[string]string {
	names := []string{"README.md", "go.mod", "Makefile", "Dockerfile", "docker-compose.yaml", "prizm.yaml", "prizm.yaml.example", "docs/design/TASKS.md", "docs/ROADMAP.md", "remembrance/README.md", "remembrance/pyproject.toml"}
	out := make(map[string]string)
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			continue
		}
		content := string(data)
		if len(content) > maxKeyFileBytes {
			content = content[:maxKeyFileBytes] + "\n... (truncated)"
		}
		out[name] = content
	}
	return out
}

func collectPackages(ctx context.Context, root string) ([]PackageSummary, int, error) {
	byDir := make(map[string]*PackageSummary)
	scanned := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if d.IsDir() {
			if shouldSkipDir(d.Name()) && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		scanned++
		if filepath.Ext(path) != ".go" {
			return nil
		}
		relDir, _ := filepath.Rel(root, filepath.Dir(path))
		relDir = filepath.ToSlash(relDir)
		ps := byDir[relDir]
		if ps == nil {
			ps = &PackageSummary{Path: relDir}
			byDir[relDir] = ps
		}
		name := filepath.Base(path)
		ps.FileNames = append(ps.FileNames, name)
		if strings.HasSuffix(name, "_test.go") {
			ps.TestFiles++
		} else {
			ps.GoFiles++
		}
		if ps.Name == "" || len(ps.FirstLines) < 2 {
			readPackageHints(path, ps)
		}
		return nil
	})
	if err != nil {
		return nil, scanned, err
	}
	var pkgs []PackageSummary
	for _, ps := range byDir {
		sort.Strings(ps.FileNames)
		pkgs = append(pkgs, *ps)
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].Path < pkgs[j].Path })
	if len(pkgs) > maxPackages {
		pkgs = pkgs[:maxPackages]
	}
	return pkgs, scanned, nil
}

func readPackageHints(path string, ps *PackageSummary) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "package ") && ps.Name == "" {
			ps.Name = strings.TrimSpace(strings.TrimPrefix(line, "package "))
		}
		if strings.HasPrefix(line, "// Package ") && len(ps.FirstLines) < 2 {
			ps.FirstLines = append(ps.FirstLines, line)
		}
		if ps.Name != "" && len(ps.FirstLines) >= 2 {
			return
		}
	}
}

func topLevelDirs(root string) []string {
	items, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var dirs []string
	for _, item := range items {
		if item.IsDir() && !shouldSkipDir(item.Name()) {
			dirs = append(dirs, item.Name())
		}
	}
	sort.Strings(dirs)
	return dirs
}

func selectedDocs(root string) []string {
	matches, _ := filepath.Glob(filepath.Join(root, "docs", "*.md"))
	var docs []string
	for _, m := range matches {
		rel, _ := filepath.Rel(root, m)
		docs = append(docs, filepath.ToSlash(rel))
	}
	sort.Strings(docs)
	if len(docs) > maxDocs {
		return docs[:maxDocs]
	}
	return docs
}

func globRel(root, pattern string) []string {
	matches, _ := filepath.Glob(filepath.Join(root, filepath.FromSlash(pattern)))
	var out []string
	for _, m := range matches {
		rel, _ := filepath.Rel(root, m)
		out = append(out, filepath.ToSlash(rel))
	}
	sort.Strings(out)
	return out
}

func recentCommits(root string) []string {
	cmd := exec.Command("git", "log", "--oneline", "-5")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var commits []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			commits = append(commits, line)
		}
	}
	return commits
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", ".idea", ".prizm", "runs", "node_modules", "vendor", "__pycache__", ".pytest_cache", ".cache", "dist", "build", "out", "bin", ".venv", "remembrance-data":
		return true
	default:
		return false
	}
}

func containsAny(s string, needles ...string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

func excerpt(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n]) + "\n..."
}

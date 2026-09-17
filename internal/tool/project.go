// Package tool provides project-level tools for reading and searching codebases.
package tool

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// SearchFilesTool searches for a text pattern across project files, like grep.
// Returns matching lines with file paths and line numbers.
// Policy: always allowed (read-only, no modifications).
type SearchFilesTool struct {
	WorkspaceRoot string
	AllowedPaths  []string
}

func (t *SearchFilesTool) Name() string { return "search_files" }
func (t *SearchFilesTool) Description() string {
	return "Searches for text patterns across project files (like grep). All space-separated keywords in the pattern must be present in matching lines (AND logic, case-insensitive). Returns matching lines with file paths and line numbers. Use this to find where functions, types, or patterns are defined or used, or to search memory files for past decisions."
}
func (t *SearchFilesTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"pattern":     {Type: "string", Description: "The text pattern to search for", Required: true},
			"path":        {Type: "string", Description: "Directory to search in. Use an absolute path for projects outside the workspace, or a path relative to the workspace root (default: '.')", Required: false},
			"max_results": {Type: "integer", Description: "Maximum number of matching lines to return (default: 30)", Required: false},
		},
		Output: ParamSpec{Type: "array", Description: "List of matches with file, line number, and text"},
	}
}

func (t *SearchFilesTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	pattern, ok := input["pattern"].(string)
	if !ok {
		return ToolResult{Success: false, Error: "required parameter 'pattern' must be a string"}, nil
	}
	if pattern == "" {
		return ToolResult{Success: false, Error: "pattern must not be empty"}, nil
	}

	// Split pattern into keywords for AND logic (case-insensitive)
	keywords := strings.Fields(strings.ToLower(pattern))
	if len(keywords) == 0 {
		return ToolResult{Success: false, Error: "no keywords in pattern"}, nil
	}

	searchDir := "."
	if dir, ok := input["path"].(string); ok && dir != "" {
		searchDir = dir
	}

	maxResults := 30
	if mr, ok := input["max_results"].(float64); ok && mr > 0 {
		maxResults = int(mr)
	}

	// Resolve search directory against allowed paths
	searchDirResolved, resolveErr := FuzzyResolvePath(ToolPaths{WorkspaceRoot: t.WorkspaceRoot, AllowedPaths: t.AllowedPaths}, searchDir)
	if resolveErr != nil {
		return ToolResult{Success: false, Error: resolveErr.Error()}, nil
	}

	var matches []map[string]any
	var totalMatches int

	err := filepath.Walk(searchDirResolved, func(walkPath string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if info.IsDir() {
			if skipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		ext := strings.ToLower(filepath.Ext(walkPath))
		if skipExtensions[ext] {
			return nil
		}
		if info.Size() > 10*1024*1024 { // skip files > 10MB
			return nil
		}

		f, err := os.Open(walkPath)
		if err != nil {
			return nil
		}
		defer f.Close()

		relPath, _ := filepath.Rel(searchDirResolved, walkPath)
		scanner := bufio.NewScanner(f)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			// Multi-keyword AND: all keywords must be present (case-insensitive)
			lineLower := strings.ToLower(scanner.Text())
			allMatch := true
			for _, kw := range keywords {
				if !strings.Contains(lineLower, kw) {
					allMatch = false
					break
				}
			}
			if allMatch {
				totalMatches++
				if len(matches) < maxResults {
					lineText := scanner.Text()
					if len(lineText) > 200 {
						lineText = lineText[:200] + "..."
					}
					matches = append(matches, map[string]any{
						"file": relPath,
						"line": lineNum,
						"text": lineText,
					})
				}
			}
		}
		return nil
	})
	if err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("search failed: %v", err)}, nil
	}

	return ToolResult{
		Success: true,
		Output: map[string]any{
			"pattern":       pattern,
			"matches":       matches,
			"match_count":   len(matches),
			"total_matches": totalMatches,
			"truncated":     totalMatches > maxResults,
		},
	}, nil
}

// ProjectOverviewTool reads key project metadata files and builds a directory
// tree summary. This gives a quick understanding of a project without reading
// every file — it targets README, package manifests, config files, and the
// directory structure.
// Policy: always allowed (read-only, no modifications).
type ProjectOverviewTool struct {
	WorkspaceRoot string
	AllowedPaths  []string
}

func (t *ProjectOverviewTool) Name() string { return "project_overview" }
func (t *ProjectOverviewTool) Description() string {
	return "Provides an overview of a project: reads README, package manifest, config files, and builds a directory tree. Set deep_dive=true to also read key architecture files (Prizma schema, API modules, Program.cs, configs, etc.) and get recent git history for deeper understanding. Always use deep_dive=true when you need to understand a project's architecture and current direction."
}
func (t *ProjectOverviewTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"path":      {Type: "string", Description: "Project root path. Use an absolute path for projects outside the workspace, or a path relative to the workspace root (default: '.')", Required: false},
			"deep_dive": {Type: "boolean", Description: "If true, also read key architecture files from subdirectories (Prizma schema, API modules, config files, etc.) for deeper understanding", Required: false},
		},
		Output: ParamSpec{Type: "object", Description: "Project overview with key files, directory tree, and optionally architecture files"},
	}
}

// keyFiles are filenames that are especially useful for understanding a project.
var keyFiles = map[string]bool{
	"README.md": true, "README.rst": true, "README.txt": true, "README": true,
	"go.mod": true, "go.sum": true, "package.json": true, "Cargo.toml": true,
	"pyproject.toml": true, "requirements.txt": true, "Gemfile": true,
	"pom.xml": true, "build.gradle": true, "CMakeLists.txt": true,
	"Makefile": true, "Dockerfile": true, "docker-compose.yaml": true, "docker-compose.yml": true,
	".env.example": true, "prizm.yaml": true, "prizm.yaml.example": true,
	"tsconfig.json": true, "next.config.js": true, "next.config.mjs": true,
	"vite.config.ts": true, "webpack.config.js": true,
}

func (t *ProjectOverviewTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	projectPath := "."
	if p, ok := input["path"].(string); ok && p != "" {
		projectPath = p
	}

	deepDive := false
	if dd, ok := input["deep_dive"].(bool); ok {
		deepDive = dd
	}

	// Resolve project directory against allowed paths
	absProjectDir, err := FuzzyResolvePath(ToolPaths{WorkspaceRoot: t.WorkspaceRoot, AllowedPaths: t.AllowedPaths}, projectPath)
	if err != nil {
		return ToolResult{Success: false, Error: err.Error()}, nil
	}

	// Read key files
	var keyFileContents []map[string]any
	for filename := range keyFiles {
		fpath := filepath.Join(absProjectDir, filename)
		data, err := os.ReadFile(fpath)
		if err != nil {
			continue // file doesn't exist, skip
		}
		content := string(data)
		if len(content) > 2000 { // cap key file reads at 2KB for context efficiency
			content = content[:2000] + "\n... (truncated)"
		}
		keyFileContents = append(keyFileContents, map[string]any{
			"file":    filename,
			"content": content,
			"size":    len(data),
		})
	}
	sort.Slice(keyFileContents, func(i, j int) bool {
		return keyFileContents[i]["file"].(string) < keyFileContents[j]["file"].(string)
	})

	// Build directory tree (2 levels deep for overview, 4 for deep dive)
	treeDepth := 2
	if deepDive {
		treeDepth = 4
	}
	tree := buildDirectoryTree(absProjectDir, absProjectDir, treeDepth)

	result := map[string]any{
		"path":           projectPath,
		"key_files":      keyFileContents,
		"directory_tree": tree,
	}

	// Deep dive: read architecture-defining files from subdirectories
	if deepDive {
		archFiles, err := findArchitectureFiles(absProjectDir)
		if err != nil {
			result["architecture_scan_error"] = err.Error()
		} else {
			var archContents []map[string]any
			for _, relPath := range archFiles {
				fpath := filepath.Join(absProjectDir, relPath)
				data, err := os.ReadFile(fpath)
				if err != nil {
					continue
				}
				content := string(data)
				if len(content) > 3000 { // cap architecture files at 3KB for context efficiency
					content = content[:3000] + "\n... (truncated)"
				}
				archContents = append(archContents, map[string]any{
					"file":    relPath,
					"content": content,
					"size":    len(data),
				})
			}
			sort.Slice(archContents, func(i, j int) bool {
				return archContents[i]["file"].(string) < archContents[j]["file"].(string)
			})
			result["architecture_files"] = archContents
		}

		// Also read git recent commits for project velocity/direction
		gitLog, _, err := runGitCommand(absProjectDir, "log", "--oneline", "-20")
		if err == nil && gitLog != "" {
			result["recent_commits"] = gitLog
		}

		// Read current branch
		gitBranch, _, err := runGitCommand(absProjectDir, "branch", "--show-current")
		if err == nil && gitBranch != "" {
			result["current_branch"] = gitBranch
		}

		// Count git branches
		gitBranches, _, err := runGitCommand(absProjectDir, "branch", "-a", "--list")
		if err == nil && gitBranches != "" {
			result["branch_count"] = len(strings.Split(strings.TrimSpace(gitBranches), "\n"))
		}
	}

	return ToolResult{
		Success: true,
		Output:  result,
	}, nil
}

// buildDirectoryTree creates a compact directory tree representation.
func buildDirectoryTree(root, workspaceRoot string, maxDepth int) []map[string]any {
	var entries []map[string]any

	items, err := os.ReadDir(root)
	if err != nil {
		return entries
	}

	sort.Slice(items, func(i, j int) bool {
		// Directories first, then files
		if items[i].IsDir() != items[j].IsDir() {
			return items[i].IsDir()
		}
		return items[i].Name() < items[j].Name()
	})

	for _, item := range items {
		name := item.Name()
		if skipDirs[name] {
			continue
		}

		entry := map[string]any{
			"name": name,
			"type": "file",
		}
		if item.IsDir() {
			entry["type"] = "dir"
			if maxDepth > 1 {
				subPath := filepath.Join(root, name)
				entry["children"] = buildDirectoryTree(subPath, workspaceRoot, maxDepth-1)
			}
		}
		entries = append(entries, entry)
	}

	return entries
}

// architectureFilePatterns lists subdirectory files that define a project's architecture.
// These are files that reveal the tech stack, data model, routing, service structure,
// and key configuration — not source code implementation details.
var architectureFilePatterns = []string{
	// Prizma / ORM
	"packages/db/prizma/schema.prizma",
	"prizma/schema.prizma",
	// .NET API
	"apps/api/Program.cs",
	"apps/api/BassBook.Api.csproj",
	"apps/api/appsettings.json",
	"apps/api/appsettings.Development.json",
	// API routes / modules
	"apps/api/Modules/**/*.cs",
	// Next.js / Frontend
	"apps/web/package.json",
	"apps/web/next.config.js",
	"apps/web/next.config.mjs",
	"apps/web/next.config.ts",
	// Worker / Background jobs
	"apps/worker/package.json",
	// Shared packages
	"packages/types/src/index.ts",
	"packages/config/src/index.ts",
	// Monorepo config
	"turbo.json",
	// Go projects
	"cmd/*/main.go",
	"internal/**/*_types.go",
	"internal/**/config.go",
	// Docker / Deploy
	"Dockerfile",
	"apps/api/Dockerfile",
	"apps/web/Dockerfile",
}

// findArchitectureFiles discovers architecture-relevant files in the project.
// It checks known patterns and also scans for common architecture markers.
func findArchitectureFiles(root string) ([]string, error) {
	var found []string
	seen := make(map[string]bool)

	// Check known patterns
	for _, pattern := range architectureFilePatterns {
		matches, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			continue
		}
		for _, match := range matches {
			rel, err := filepath.Rel(root, match)
			if err != nil {
				continue
			}
			if !seen[rel] {
				found = append(found, rel)
				seen[rel] = true
			}
		}
	}

	// Also scan for common architecture markers that might not be in the pattern list
	architectureMarkers := []string{
		"schema.prizma", "Program.cs", "appsettings.json",
		"next.config.js", "next.config.mjs", "next.config.ts",
	}

	filepath.Walk(root, func(walkPath string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if info.IsDir() {
			// Skip common non-architecture directories
			name := info.Name()
			if name == "node_modules" || name == ".git" || name == "bin" || name == "obj" || name == ".next" || name == "dist" || name == "build" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, walkPath)
		if err != nil {
			return nil
		}
		for _, marker := range architectureMarkers {
			if filepath.Base(rel) == marker && !seen[rel] {
				found = append(found, rel)
				seen[rel] = true
			}
		}
		return nil
	})

	sort.Strings(found)
	return found, nil
}

// runGitCommand is a helper for git tools to execute git commands safely.
func runGitCommand(workspaceRoot string, args ...string) (string, int, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = workspaceRoot
	out, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return "", -1, fmt.Errorf("git command failed: %v", err)
		}
	}
	return strings.TrimSpace(string(out)), exitCode, nil
}

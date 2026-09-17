// Package context provides workspace context injection for Prizm runs.
// It reads workspace files, applies selection rules, respects token budgets,
// and produces formatted context strings for LLM prompts.
//
// V19 introduces smart context injection as a pipeline stage:
//
//	Connection → Context → Remembrance → LLM → Tool → Approval → Validation → Review
//
// Context is read-only — Prizm never writes back to the workspace.
//
// File organization within this package:
//
//   named.go    — named source maps, priorities, directory constants
//   types.go    — ContextFile, InjectedContext, formatting, token estimation
//   budget.go   — token budget truncation logic
//   sources.go  — directory loading (memory/*.md, correspondence/*.md)
//   discover.go — docs/ keyword search and file discovery
//   builder.go  — Builder struct, Build(), BuildCached(), single-file reading
package context

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Builder constructs injected context from workspace files.
type Builder struct {
	WorkspaceRoot string
	NamedContexts []string // e.g., ["soul", "agents"]
	AutoFiles     []string // Files discovered by keyword matching
	ExplicitFiles []string // Files specified via --context-file
	TokenBudget   int      // Max tokens to inject (0 = no limit)

	// Cache for BuildCached(). Invalidated when workspace files change.
	cache   *cachedContext
	cacheMu sync.Mutex
}

// cachedContext holds a previously-built InjectedContext along with file
// mtimes and sizes. BuildCached() checks these to determine if a rebuild
// is needed.
type cachedContext struct {
	result   *InjectedContext
	fileInfo map[string]os.FileInfo
}

// NewBuilder creates a context builder with the given workspace root.
func NewBuilder(workspaceRoot string) *Builder {
	return &Builder{
		WorkspaceRoot: workspaceRoot,
		TokenBudget:   0, // No limit by default
	}
}

// WithNamedContexts sets which named context sources to include.
func (b *Builder) WithNamedContexts(names []string) *Builder {
	b.NamedContexts = names
	return b
}

// WithAutoFiles sets keyword-matched files.
func (b *Builder) WithAutoFiles(files []string) *Builder {
	b.AutoFiles = files
	return b
}

// WithExplicitFiles sets explicitly-specified files.
func (b *Builder) WithExplicitFiles(files []string) *Builder {
	b.ExplicitFiles = files
	return b
}

// WithTokenBudget sets the maximum number of tokens to inject.
func (b *Builder) WithTokenBudget(budget int) *Builder {
	b.TokenBudget = budget
	return b
}

// Build reads workspace files and constructs the injected context.
func (b *Builder) Build() (*InjectedContext, error) {
	var files []ContextFile

	// 1. Named sources (highest priority)
	for _, name := range b.NamedContexts {
		filename, ok := NamedSources[name]
		if !ok {
			continue
		}
		cf, err := b.readFile(name, filename, "named", SourcePriority[name])
		if err != nil {
			continue
		}
		files = append(files, cf)
	}

	// 1b. Memory directory (memory/*.md) — relationship history, sorted newest-first
	files = append(files, b.readMemoryDir()...)

	// 1c. Correspondence directory (correspondence/*.md) — inter-agent letters
	files = append(files, b.readCorrespondenceDir()...)

	// 2. Auto-discovered files (lowest priority)
	for _, path := range b.AutoFiles {
		name := filepath.Base(path)
		cf, err := b.readFile(name, path, "auto", 30)
		if err != nil {
			continue
		}
		files = append(files, cf)
	}

	// 3. Explicit files (medium priority)
	for _, path := range b.ExplicitFiles {
		name := filepath.Base(path)
		cf, err := b.readFile(name, path, "file", 60)
		if err != nil {
			continue
		}
		files = append(files, cf)
	}

	// 4. Content hash (before truncation)
	rawContent := ""
	for _, f := range files {
		rawContent += f.Content
	}
	hash := sha256.Sum256([]byte(rawContent))

	// 5. Apply token budget
	if b.TokenBudget > 0 {
		files = b.applyBudget(files)
	}

	// 6. Format
	formatted := formatContext(files)

	totalTokens := 0
	truncated := false
	for _, f := range files {
		totalTokens += f.EstimatedTokens
		if f.Truncated {
			truncated = true
		}
	}

	return &InjectedContext{
		Files:           files,
		TotalTokens:     totalTokens,
		Truncated:       truncated,
		ContentHash:     fmt.Sprintf("%x", hash),
		FormattedString: formatted,
	}, nil
}

// BuildCached returns the cached result if no workspace files have changed
// (checked via os.Stat mtime+size). This is the preferred method for
// hot-path calls like per-message context injection.
func (b *Builder) BuildCached() (*InjectedContext, error) {
	b.cacheMu.Lock()
	defer b.cacheMu.Unlock()

	filePath := func(name, filename, source string) string {
		if !filepath.IsAbs(filename) && source == "named" {
			return filepath.Join(b.WorkspaceRoot, filename)
		}
		return filename
	}

	needRebuild := b.cache == nil

	// Build the set of expected file paths
	expectedPaths := make(map[string]bool)
	for _, name := range b.NamedContexts {
		if filename, ok := NamedSources[name]; ok {
			expectedPaths[filePath(name, filename, "named")] = true
		}
	}
	for _, path := range b.AutoFiles {
		expectedPaths[path] = true
	}
	for _, path := range b.ExplicitFiles {
		expectedPaths[path] = true
	}
	// Include directory-based context files in cache validation
	for p := range dirMDFiles(filepath.Join(b.WorkspaceRoot, memoryDir)) {
		expectedPaths[p] = true
	}
	for p := range dirMDFiles(filepath.Join(b.WorkspaceRoot, correspondenceDir)) {
		expectedPaths[p] = true
	}

	if b.cache != nil {
		for p, oldInfo := range b.cache.fileInfo {
			if !expectedPaths[p] {
				needRebuild = true
				break
			}
			newInfo, err := os.Stat(p)
			if err != nil {
				needRebuild = true
				break
			}
			if newInfo.Size() != oldInfo.Size() || !newInfo.ModTime().Equal(oldInfo.ModTime()) {
				needRebuild = true
				break
			}
		}
		if !needRebuild {
			for p := range expectedPaths {
				if _, ok := b.cache.fileInfo[p]; !ok {
					needRebuild = true
					break
				}
			}
		}
	}

	if needRebuild {
		result, err := b.Build()
		if err != nil {
			return nil, err
		}
		fileInfo := make(map[string]os.FileInfo)
		for p := range expectedPaths {
			if info, err := os.Stat(p); err == nil {
				fileInfo[p] = info
			}
		}
		b.cache = &cachedContext{result: result, fileInfo: fileInfo}
	}

	return b.cache.result, nil
}

// readFile reads a single file from the workspace.
func (b *Builder) readFile(name, filename, source string, priority int) (ContextFile, error) {
	fullPath := filename
	if !filepath.IsAbs(filename) && source == "named" {
		fullPath = filepath.Join(b.WorkspaceRoot, filename)
	}

	data, err := os.ReadFile(fullPath)
	if err != nil {
		return ContextFile{}, fmt.Errorf("read %s: %w", fullPath, err)
	}

	content := string(data)
	return ContextFile{
		Name:            name,
		Path:            fullPath,
		Content:         content,
		SizeBytes:       len(data),
		EstimatedTokens: estimateTokens(content),
		Priority:        priority,
		Source:          source,
	}, nil
}

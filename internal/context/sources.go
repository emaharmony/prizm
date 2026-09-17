package context

import (
	"os"
	"path/filepath"
	"strings"
)

// maxMemoryFiles caps how many memory/*.md files are loaded into context.
// Files are sorted newest-first, so this keeps only the most recent entries.
// Set to 0 for no limit — with 128k token budget, all memory files fit.
const maxMemoryFiles = 0

// readDirAsContext is the shared implementation for loading a directory of
// .md files as context entries. Files are sorted newest-first (by filename
// descending, since memory and correspondence files start with dates).
//
// sourceLabel is used as the ContextFile.Source value and as a prefix for
// the Name (e.g., "memory/2026-07-15.md"). priority controls truncation
// order — lower priority files are truncated first under token pressure.
// maxFiles caps the number of files loaded (0 = no cap, keep all).
func (b *Builder) readDirAsContext(dir, sourceLabel string, priority, maxFiles int) []ContextFile {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // Directory doesn't exist or can't be read
	}

	// Collect .md files
	var mdFiles []os.DirEntry
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			mdFiles = append(mdFiles, e)
		}
	}
	if len(mdFiles) == 0 {
		return nil
	}

	// Sort by name descending (newest first — files start with dates)
	for i := 1; i < len(mdFiles); i++ {
		for j := i; j > 0 && mdFiles[j].Name() > mdFiles[j-1].Name(); j-- {
			mdFiles[j], mdFiles[j-1] = mdFiles[j-1], mdFiles[j]
		}
	}

	// Cap to maxFiles (keep newest)
	if maxFiles > 0 && len(mdFiles) > maxFiles {
		mdFiles = mdFiles[:maxFiles]
	}

	var files []ContextFile
	for _, entry := range mdFiles {
		fullPath := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(fullPath)
		if err != nil {
			continue
		}
		content := string(data)
		files = append(files, ContextFile{
			Name:            sourceLabel + "/" + entry.Name(),
			Path:            fullPath,
			Content:         content,
			SizeBytes:       len(data),
			EstimatedTokens: estimateTokens(content),
			Priority:        priority,
			Source:          sourceLabel,
		})
	}
	return files
}

// readMemoryDir reads memory/*.md files from the workspace's memory directory.
// These hold session summaries, decisions, and patterns — the agent's full
// relationship history. Priority 40: below named sources, truncated first.
// Capped to the 30 most recent files to keep context window manageable.
func (b *Builder) readMemoryDir() []ContextFile {
	return b.readDirAsContext(
		filepath.Join(b.WorkspaceRoot, memoryDir),
		"memory",
		40,
		maxMemoryFiles,
	)
}

// readCorrespondenceDir reads correspondence/*.md files from the workspace's
// correspondence directory. These are inter-agent letters between OpenClaw
// Lumi and Prizm Lumi. Priority 65: above memory files but below named
// sources — letters are more time-sensitive than historical memory but
// less foundational than identity docs. No cap (letters are rare).
func (b *Builder) readCorrespondenceDir() []ContextFile {
	return b.readDirAsContext(
		filepath.Join(b.WorkspaceRoot, correspondenceDir),
		"correspondence",
		65,
		0, // no cap — letters are infrequent
	)
}

// dirMDFiles returns the set of .md file paths in a directory, used by
// BuildCached to include directory files in cache validation.
func dirMDFiles(dir string) map[string]bool {
	paths := make(map[string]bool)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return paths
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			paths[filepath.Join(dir, e.Name())] = true
		}
	}
	return paths
}
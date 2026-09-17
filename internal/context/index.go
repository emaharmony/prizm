package context

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BuildIndex produces a compact context index instead of full file contents.
// In "open book" mode, the system prompt contains just the index, and the model
// uses read_file to load specific files on demand. This reduces the initial
// prompt from ~150K tokens to ~2K tokens, leaving room for tool calls.
func (b *Builder) BuildIndex() (*InjectedContext, error) {
	var files []ContextFile

	// 1. Named sources — read just the first 20 lines for a summary
	for _, name := range b.NamedContexts {
		filename, ok := NamedSources[name]
		if !ok {
			continue
		}
		cf, err := b.readFileIndex(name, filename, "named", SourcePriority[name])
		if err != nil {
			continue
		}
		files = append(files, cf)
	}

	// 2. Memory directory — list file names only
	files = append(files, b.readMemoryDirIndex()...)

	// 3. Correspondence directory — list file names only
	files = append(files, b.readCorrespondenceDirIndex()...)

	totalTokens := 0
	for _, f := range files {
		totalTokens += f.EstimatedTokens
	}

	return &InjectedContext{
		Files:           files,
		TotalTokens:     totalTokens,
		Truncated:       false,
		FormattedString: formatIndex(files, b.WorkspaceRoot),
	}, nil
}

// readFileIndex reads just the first 20 lines of a file to produce a summary.
// This gives the model enough context to know what the file contains without
// loading the entire file.
func (b *Builder) readFileIndex(name, filename, source string, priority int) (ContextFile, error) {
	fullPath := filename
	if !filepath.IsAbs(filename) && source == "named" {
		fullPath = filepath.Join(b.WorkspaceRoot, filename)
	}

	data, err := os.ReadFile(fullPath)
	if err != nil {
		return ContextFile{}, fmt.Errorf("read %s: %w", fullPath, err)
	}

	content := string(data)
	lines := strings.Split(content, "\n")

	// Take first 20 lines as summary
	summaryLines := 20
	if len(lines) < summaryLines {
		summaryLines = len(lines)
	}
	summary := strings.Join(lines[:summaryLines], "\n")
	truncated := len(lines) > summaryLines
	if truncated {
		summary += fmt.Sprintf("\n... (%d more lines, use read_file to access)", len(lines)-summaryLines)
	}

	fullTokens := estimateTokens(content)
	summaryTokens := estimateTokens(summary)

	return ContextFile{
		Name:            name,
		Path:            fullPath,
		Content:         summary,
		SizeBytes:       len(data),
		EstimatedTokens: summaryTokens,
		Priority:        priority,
		Source:          source,
		Truncated:       truncated,
		TruncatedBy:     fullTokens - summaryTokens,
	}, nil
}

// readMemoryDirIndex produces index entries for memory/*.md files — just filenames.
func (b *Builder) readMemoryDirIndex() []ContextFile {
	dir := filepath.Join(b.WorkspaceRoot, memoryDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var files []ContextFile
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		fullPath := filepath.Join(dir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, ContextFile{
			Name:            entry.Name(),
			Path:            fullPath,
			Content:         "", // Empty — model uses read_file to access
			SizeBytes:       int(info.Size()),
			EstimatedTokens: 0,
			Priority:        50,
			Source:          "memory",
			Truncated:       true,
			TruncatedBy:     estimateTokens(fmt.Sprintf("memory file: %s (%d bytes)", entry.Name(), info.Size())),
		})
	}
	return files
}

// readCorrespondenceDirIndex produces index entries for correspondence/*.md files.
func (b *Builder) readCorrespondenceDirIndex() []ContextFile {
	dir := filepath.Join(b.WorkspaceRoot, correspondenceDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var files []ContextFile
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		fullPath := filepath.Join(dir, entry.Name())
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, ContextFile{
			Name:            entry.Name(),
			Path:            fullPath,
			Content:         "",
			SizeBytes:       int(info.Size()),
			EstimatedTokens: 0,
			Priority:        70,
			Source:          "correspondence",
			Truncated:       true,
			TruncatedBy:     estimateTokens(fmt.Sprintf("correspondence file: %s (%d bytes)", entry.Name(), info.Size())),
		})
	}
	return files
}

// formatIndex produces the open-book index string for LLM injection.
// Instead of full file contents, it provides a structured index with instructions
// for the model to use read_file to access specific files on demand.
func formatIndex(files []ContextFile, workspaceRoot string) string {
	var sb strings.Builder

	sb.WriteString("# Open Book Context\n\n")
	sb.WriteString("You have access to workspace context files listed below. ")
	sb.WriteString("Instead of loading all files into your prompt, only their summaries are shown. ")
	sb.WriteString("Use the `read_file` tool to load specific files when you need details.\n\n")

	// Group by source
	groups := map[string][]ContextFile{}
	for _, f := range files {
		groups[f.Source] = append(groups[f.Source], f)
	}

	// Named sources (with summaries)
	if named, ok := groups["named"]; ok {
		sb.WriteString("## Core Context Files\n\n")
		for _, f := range named {
			// Make path relative to workspace root for cleaner display
			relPath := f.Path
			if strings.HasPrefix(f.Path, workspaceRoot) {
				relPath = strings.TrimPrefix(f.Path, workspaceRoot+"/")
			}
			sb.WriteString(fmt.Sprintf("### %s (`%s`)\n", f.Name, relPath))
			if f.Content != "" {
				sb.WriteString(f.Content)
				if f.Truncated {
					sb.WriteString(fmt.Sprintf("\n\n⚠️ *This is a summary. Use `read_file(\"%s\")` for the full content.*", f.Path))
				}
				sb.WriteString("\n\n")
			} else {
				sb.WriteString(fmt.Sprintf("*Available via `read_file(\"%s\")`*\n\n", f.Path))
			}
		}
	}

	// Memory files (filenames only)
	if memory, ok := groups["memory"]; ok {
		sb.WriteString("## Memory Files\n\n")
		sb.WriteString("Recent session memories. Use `read_file` to access specific files.\n\n")
		for _, f := range memory {
			sb.WriteString(fmt.Sprintf("- `%s` (%d bytes)\n", f.Name, f.SizeBytes))
		}
		sb.WriteString("\n")
	}

	// Correspondence files (filenames only)
	if corr, ok := groups["correspondence"]; ok {
		sb.WriteString("## Correspondence\n\n")
		sb.WriteString("Inter-agent letters. Use `read_file` to access.\n\n")
		for _, f := range corr {
			sb.WriteString(fmt.Sprintf("- `%s` (%d bytes)\n", f.Name, f.SizeBytes))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("---\n\n")
	sb.WriteString("**Remember:** Load files with `read_file` only when you need their content. ")
	sb.WriteString("Use `max_lines` to load just the section you need (e.g., `read_file(\"path\", max_lines=50)`). ")
	sb.WriteString("This keeps your context window clear for tool calls and conversation.\n")

	return sb.String()
}
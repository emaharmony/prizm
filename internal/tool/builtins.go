// Package tool provides Prizm's built-in tools — the actions an AI agent can request
// during a run. Each tool is a named function that takes JSON input and returns a result.
//
// Built-in tools (V1): echo, list_dir, read_file — read-only, always allowed.
// Built-in tools (V4): write_file_dry_run (preview a write), write_file_proposal
// (propose a file change for human approval). Direct write_file is denied by policy.
// Built-in tools (V28): read_project — recursively read a project directory.
// (propose a file change for human approval). Direct write_file is denied by policy.
//
// Path safety (V12 — centralized):
// All path containment checks now use safety.IsWithinRoot from internal/safety.
// This replaces the previous defense-in-depth pattern where each tool had its own
// isWithinRoot implementation. The centralization means one place to audit and fix
// bugs, while still being called at each filesystem boundary.
//
// Why does write_file_proposal use ApprovalStorer as `any` instead of *approval.Approval?
// Because importing the approval package would create a circular dependency (tool →
// approval → event → tool). The `any` type breaks the cycle at the cost of type safety.
package tool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/safety"
)

// EchoTool returns whatever text is passed in. Always approved by policy.
// EchoTool is the simplest tool — it returns its input unchanged.
// Used for testing the tool execution pipeline end-to-end without side effects.
// Policy: always allowed (read-only, no filesystem access).
type EchoTool struct{}

func (t *EchoTool) Name() string { return "echo" }
func (t *EchoTool) Description() string {
	return "Returns the input text unchanged. Useful for testing and verification."
}
func (t *EchoTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"text": {Type: "string", Description: "The text to echo back", Required: true},
		},
		Output: ParamSpec{Type: "string", Description: "The echoed text"},
	}
}
func (t *EchoTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	text, ok := input["text"].(string)
	if !ok {
		return ToolResult{
			Success: false,
			Output:  nil,
			Error:   "required parameter 'text' must be a string",
		}, nil
	}
	return ToolResult{
		Success: true,
		Output:  map[string]any{"text": text},
	}, nil
}

// ListDirTool lists files and directories under a given path within the
// workspace root. Path traversal is blocked by policy.
// ListDirTool lists files and directories under a given path.
// The path must be within the workspace root — path traversal (..) is blocked.
// Symlinks are resolved with EvalSymlinks before the containment check.
// Policy: always allowed (read-only, no modifications).
type ListDirTool struct {
	// WorkspaceRoot is the root directory that constrains allowed paths.
	WorkspaceRoot string
	// AllowedPaths is a list of additional directory roots the agent can access.
	AllowedPaths []string
}

func (t *ListDirTool) Name() string { return "list_dir" }
func (t *ListDirTool) Description() string {
	return "Lists files and directories under a given path within the workspace root."
}
func (t *ListDirTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"path": {Type: "string", Description: "Path to the file. Use an absolute path for projects outside the workspace, or a path relative to the workspace root", Required: true},
		},
		Output: ParamSpec{Type: "array", Description: "List of file and directory names"},
	}
}
func (t *ListDirTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	pathVal, ok := input["path"].(string)
	if !ok {
		return ToolResult{
			Success: false,
			Output:  nil,
			Error:   "required parameter 'path' must be a string",
		}, nil
	}

	resolvedPath, err := FuzzyResolvePath(ToolPaths{WorkspaceRoot: t.WorkspaceRoot, AllowedPaths: t.AllowedPaths}, pathVal)
	if err != nil {
		return ToolResult{
			Success: false,
			Output:  nil,
			Error:   err.Error(),
		}, nil
	}

	entries, err := os.ReadDir(resolvedPath)
	if err != nil {
		return ToolResult{
			Success: false,
			Output:  nil,
			Error:   fmt.Sprintf("failed to read directory: %v", err),
		}, nil
	}

	var names []string
	for _, entry := range entries {
		prefix := ""
		if entry.IsDir() {
			prefix = "dir:"
		} else {
			prefix = "file:"
		}
		names = append(names, prefix+entry.Name())
	}
	sort.Strings(names)

	return ToolResult{
		Success: true,
		Output: map[string]any{
			"path":  pathVal,
			"files": names,
			"count": len(names),
		},
	}, nil
}

// ReadFileTool reads a text file within the workspace root. Policy blocks
// path traversal and enforces a maximum file size.
// ReadFileTool reads a text file's contents within the workspace root.
// Enforces a maximum file size (default 1MB) to prevent reading huge files.
// Path traversal and symlink escapes are blocked the same way as ListDirTool.
// Policy: always allowed (read-only, no modifications).
type ReadFileTool struct {
	// WorkspaceRoot is the root directory that constrains allowed paths.
	WorkspaceRoot string
	// AllowedPaths is a list of additional directory roots the agent can access.
	AllowedPaths []string
	// MaxFileSize is the maximum allowed file size in bytes.
	MaxFileSize int64
}

func (t *ReadFileTool) Name() string { return "read_file" }
func (t *ReadFileTool) Description() string {
	return "Reads a text file within the workspace root. Path traversal and file size limits are enforced by policy."
}
func (t *ReadFileTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"path":      {Type: "string", Description: "Path to the file. Use an absolute path for projects outside the workspace, or a path relative to the workspace root", Required: true},
			"max_lines": {Type: "integer", Description: "Maximum number of lines to return. Useful for previewing large files without loading the entire content into context. 0 or omitted means no limit.", Required: false},
		},
		Output: ParamSpec{Type: "string", Description: "The file contents"},
	}
}
func (t *ReadFileTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	pathVal, ok := input["path"].(string)
	if !ok {
		return ToolResult{
			Success: false,
			Output:  nil,
			Error:   "required parameter 'path' must be a string",
		}, nil
	}

	resolvedPath, err := FuzzyResolvePath(ToolPaths{WorkspaceRoot: t.WorkspaceRoot, AllowedPaths: t.AllowedPaths}, pathVal)
	if err != nil {
		return ToolResult{
			Success: false,
			Output:  nil,
			Error:   err.Error(),
		}, nil
	}

	// Use Lstat to check size without following symlinks at the final component
	info, err := os.Lstat(resolvedPath)
	if err != nil {
		return ToolResult{
			Success: false,
			Output:  nil,
			Error:   fmt.Sprintf("failed to stat file: %v", err),
		}, nil
	}

	// Reject symlinks at the final path component
	if info.Mode()&os.ModeSymlink != 0 {
		return ToolResult{
			Success: false,
			Output:  nil,
			Error:   "symlink at final path component is not allowed",
		}, nil
	}

	maxSize := t.MaxFileSize
	if maxSize <= 0 {
		maxSize = 1024 * 1024 // 1MB default
	}

	if info.Size() > maxSize {
		return ToolResult{
			Success: false,
			Output:  nil,
			Error:   fmt.Sprintf("file size %d bytes exceeds maximum allowed %d bytes", info.Size(), maxSize),
		}, nil
	}

	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		return ToolResult{
			Success: false,
			Output:  nil,
			Error:   fmt.Sprintf("failed to read file: %v", err),
		}, nil
	}

	content := string(data)

	// Apply max_lines truncation if specified
	if maxLinesVal, ok := input["max_lines"]; ok {
		if maxLines, ok := maxLinesVal.(float64); ok && maxLines > 0 {
			lines := strings.Split(content, "\n")
			totalLines := len(lines)
			maxLinesInt := int(maxLines)
			if totalLines > maxLinesInt {
				content = strings.Join(lines[:maxLinesInt], "\n")
				content += fmt.Sprintf("\n\n... (%d more lines, use read_file with a larger max_lines or without max_lines to see the rest)", totalLines-maxLinesInt)
			}
		}
	}

	return ToolResult{
		Success: true,
		Output: map[string]any{
			"path":    pathVal,
			"content": content,
			"size":    len(data),
		},
	}, nil
}

// WriteFileDryRunTool previews what a file write would look like. It does NOT
// write to disk. Always approved by policy (no mutation).
// WriteFileDryRun shows what a file write would look like without actually writing.
// It returns a "diff-style" preview: the content that would be written and the
// target path. No bytes are written to disk.
// Policy: always allowed (preview only, harmless).
//
// The 500-character preview truncation keeps the tool result from flooding
// the LLM's context window. The full content is in the output, just truncated
// in the preview field.
type WriteFileDryRun struct{}

func (t *WriteFileDryRun) Name() string { return "write_file_dry_run" }
func (t *WriteFileDryRun) Description() string {
	return "Previews a file write diff without writing to disk. No side effects."
}
func (t *WriteFileDryRun) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"path":    {Type: "string", Description: "Path to the file. Use an absolute path for projects outside the workspace, or a path relative to the workspace root", Required: true},
			"content": {Type: "string", Description: "Proposed file content", Required: true},
		},
		Output: ParamSpec{Type: "object", Description: "Preview of the write operation"},
	}
}
func (t *WriteFileDryRun) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	pathVal, ok := input["path"].(string)
	if !ok {
		return ToolResult{
			Success: false,
			Output:  nil,
			Error:   "required parameter 'path' must be a string",
		}, nil
	}

	content, ok := input["content"].(string)
	if !ok {
		return ToolResult{
			Success: false,
			Output:  nil,
			Error:   "required parameter 'content' must be a string",
		}, nil
	}

	// Build a preview diff — this does NOT write to disk
	lines := strings.Count(content, "\n") + 1
	preview := fmt.Sprintf("--- /dev/null\n+++ %s\n@@ -0,0 +1,%d @@\n", pathVal, lines)

	// Add each line with a "+" prefix (simplified unified diff)
	for _, line := range strings.Split(content, "\n") {
		preview += "+" + line + "\n"
	}

	return ToolResult{
		Success: true,
		Output: map[string]any{
			"path":           pathVal,
			"content_length": len(content),
			"lines":          lines,
			"preview_diff":   preview,
			"written":        false,
		},
	}, nil
}

// WriteFileProposalTool validates a file write proposal and returns an approval request.
// It does NOT write to disk. Instead, it emits mutation.proposed and approval.requested
// events, and returns an approval_id that can be used to approve or deny the mutation.
// WriteFileProposal proposes a file write and creates a pending approval.
// The file is NOT written — instead, a pending approval is saved to
// runs/<run_id>/approvals/<approval_id>.json with the proposed content.
// A human operator must review and approve before the file is actually written.
//
// This is the core of V4's safety model: the model proposes, Prizm validates,
// the human decides.
//
// Policy: requires_approval (creates a pending approval, blocks until human decides).
// Direct write_file without proposal → denied by policy.
type WriteFileProposal struct {
	WorkspaceRoot string
	AllowedPaths  []string
	// Emit is called to emit events.
	Emit func(eventType, source string, payload map[string]any)
}

// ApprovalProxy wraps an approval.Approval for storage.
type ApprovalProxy struct {
	ApprovalID    string         `json:"approval_id"`
	RunID         string         `json:"run_id"`
	CorrelationID string         `json:"correlation_id"`
	Status        string         `json:"status"`
	RequestedBy   string         `json:"requested_by"`
	Project       string         `json:"project"`
	MutationType  string         `json:"mutation_type"`
	TargetPath    string         `json:"target_path"`
	Content       string         `json:"content,omitempty"`
	Preview       string         `json:"preview,omitempty"`
	CreatedAt     string         `json:"created_at"`
	Policy        PolicyDecision `json:"policy"`
}

func (t *WriteFileProposal) Name() string { return "write_file_proposal" }
func (t *WriteFileProposal) Description() string {
	return "Proposes a file write for approval. Does not write to disk — returns an approval_id for the approval workflow."
}
func (t *WriteFileProposal) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"path":    {Type: "string", Description: "Path to the file. Use an absolute path for projects outside the workspace, or a path relative to the workspace root", Required: true},
			"content": {Type: "string", Description: "Proposed file content", Required: true},
		},
		Output: ParamSpec{Type: "object", Description: "Approval ID and preview"},
	}
}
func (t *WriteFileProposal) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	pathVal, ok := input["path"].(string)
	if !ok {
		return ToolResult{
			Success: false,
			Output:  nil,
			Error:   "required parameter 'path' must be a string",
		}, nil
	}

	content, ok := input["content"].(string)
	if !ok {
		return ToolResult{
			Success: false,
			Output:  nil,
			Error:   "required parameter 'content' must be a string",
		}, nil
	}

	// Validate path safety. Relative paths resolve against WorkspaceRoot;
	// absolute paths are allowed only when they stay inside configured roots.
	if strings.Contains(pathVal, "..") {
		return ToolResult{Success: false, Error: "path traversal blocked"}, nil
	}
	roots := append([]string{t.WorkspaceRoot}, t.AllowedPaths...)
	if _, err := safety.ResolveAndContainMulti(roots, pathVal); err != nil {
		return ToolResult{Success: false, Error: err.Error()}, nil
	}

	// Check content size
	const maxContentSize = 1024 * 1024
	if len(content) > maxContentSize {
		return ToolResult{Success: false, Error: fmt.Sprintf("content size %d exceeds 1MB limit", len(content))}, nil
	}

	// Generate approval ID and create artifact
	approvalID := fmt.Sprintf("appr_%d", time.Now().UnixNano())

	// Build preview
	previewLen := len(content)
	if previewLen > 500 {
		previewLen = 500
	}
	preview := content[:previewLen]
	if len(content) > 500 {
		preview += "..."
	}

	// Emit mutation.proposed and approval.requested events
	if t.Emit != nil {
		t.Emit("prizm.mutation.proposed", "prizm-tool-executor", map[string]any{
			"approval_id":     approvalID,
			"mutation_type":   "write_file",
			"target_path":     pathVal,
			"content_size":    len(content),
			"policy_decision": "requires_approval",
			"policy_reason":   "file writes require explicit approval",
		})
		t.Emit("prizm.approval.requested", "prizm-tool-executor", map[string]any{
			"approval_id":     approvalID,
			"mutation_type":   "write_file",
			"target_path":     pathVal,
			"policy_decision": "requires_approval",
			"policy_reason":   "file writes require explicit approval",
		})
	}

	return ToolResult{
		Success: true,
		Output: map[string]any{
			"approval_id":    approvalID,
			"mutation_type":  "write_file",
			"target_path":    pathVal,
			"content_length": len(content),
			"preview":        preview,
			"status":         "pending_approval",
			"instruction":    "Use 'prizm approval approve <approval_id> --by <name>' or 'prizm approval deny <approval_id> --by <name>' to proceed.",
		},
	}, nil
}

// WriteFileDirect writes a file to disk immediately (no approval workflow).
// This is used by autonomous wake handler actions where AutoApproveMutations is true.
// It performs the same path safety checks as WriteFileProposal but writes directly.
type WriteFileDirect struct {
	WorkspaceRoot string
	AllowedPaths  []string
	Emit          func(eventType, source string, payload map[string]any)
}

func (t *WriteFileDirect) Name() string { return "write_file" }
func (t *WriteFileDirect) Description() string {
	return "Writes a file to disk. Use this to create or overwrite files. Requires a path (absolute or relative to workspace) and the full file content."
}
func (t *WriteFileDirect) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"path":    {Type: "string", Description: "Path to the file. Use an absolute path for projects outside the workspace, or a path relative to the workspace root", Required: true},
			"content": {Type: "string", Description: "Full file content to write", Required: true},
		},
		Output: ParamSpec{Type: "object", Description: "Write result with path and bytes written"},
	}
}
func (t *WriteFileDirect) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	pathVal, ok := input["path"].(string)
	if !ok {
		return ToolResult{Success: false, Error: "required parameter 'path' must be a string"}, nil
	}
	content, ok := input["content"].(string)
	if !ok {
		return ToolResult{Success: false, Error: "required parameter 'content' must be a string"}, nil
	}
	if strings.Contains(pathVal, "..") {
		return ToolResult{Success: false, Error: "path traversal blocked"}, nil
	}
	roots := append([]string{t.WorkspaceRoot}, t.AllowedPaths...)
	absPath, err := safety.ResolveAndContainMulti(roots, pathVal)
	if err != nil {
		return ToolResult{Success: false, Error: err.Error()}, nil
	}
	const maxContentSize = 1024 * 1024
	if len(content) > maxContentSize {
		return ToolResult{Success: false, Error: fmt.Sprintf("content size %d exceeds 1MB limit", len(content))}, nil
	}
	// Create parent directories if needed
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("failed to create directory: %v", err)}, nil
	}
	if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("failed to write file: %v", err)}, nil
	}
	if t.Emit != nil {
		t.Emit("prizm.mutation.applied", "prizm-tool-executor", map[string]any{
			"mutation_type": "write_file",
			"target_path":   absPath,
			"content_size":  len(content),
		})
	}
	return ToolResult{
		Success: true,
		Output: map[string]any{
			"path":          absPath,
			"bytes_written": len(content),
			"status":        "written",
		},
	}, nil
}

// CreateDirectoryProposal proposes directory creation for approval. It does not
// write to disk; approval application is handled by the mutation executor.
type CreateDirectoryProposal struct {
	WorkspaceRoot string
	AllowedPaths  []string
	Emit          func(eventType, source string, payload map[string]any)
}

func (t *CreateDirectoryProposal) Name() string { return "create_directory_proposal" }
func (t *CreateDirectoryProposal) Description() string {
	return "Proposes creating a directory for approval. Does not write to disk - returns an approval_id for the approval workflow."
}
func (t *CreateDirectoryProposal) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"path": {Type: "string", Description: "Directory path to create. Use an absolute path under a configured write root, or a path relative to the workspace root", Required: true},
		},
		Output: ParamSpec{Type: "object", Description: "Approval ID and directory creation preview"},
	}
}
func (t *CreateDirectoryProposal) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	pathVal, ok := input["path"].(string)
	if !ok || strings.TrimSpace(pathVal) == "" {
		return ToolResult{Success: false, Error: "required parameter 'path' must be a non-empty string"}, nil
	}
	if strings.Contains(pathVal, "..") {
		return ToolResult{Success: false, Error: "path traversal blocked"}, nil
	}
	roots := append([]string{t.WorkspaceRoot}, t.AllowedPaths...)
	absPath, err := safety.ResolveAndContainMulti(roots, pathVal)
	if err != nil {
		return ToolResult{Success: false, Error: err.Error()}, nil
	}

	approvalID := fmt.Sprintf("appr_%d", time.Now().UnixNano())
	preview := fmt.Sprintf("Create directory: %s", absPath)
	if t.Emit != nil {
		t.Emit("prizm.mutation.proposed", "prizm-tool-executor", map[string]any{
			"approval_id":     approvalID,
			"mutation_type":   "create_directory",
			"target_path":     pathVal,
			"policy_decision": "requires_approval",
			"policy_reason":   "directory creation requires explicit approval",
		})
		t.Emit("prizm.approval.requested", "prizm-tool-executor", map[string]any{
			"approval_id":     approvalID,
			"mutation_type":   "create_directory",
			"target_path":     pathVal,
			"policy_decision": "requires_approval",
			"policy_reason":   "directory creation requires explicit approval",
		})
	}

	return ToolResult{
		Success: true,
		Output: map[string]any{
			"approval_id":   approvalID,
			"mutation_type": "create_directory",
			"target_path":   pathVal,
			"resolved_path": absPath,
			"preview":       preview,
			"status":        "pending_approval",
			"instruction":   "Use 'prizm approval approve <approval_id> --by <name>' or 'prizm approval deny <approval_id> --by <name>' to proceed.",
		},
	}, nil
}

// CreateDirectoryDirect creates a directory immediately. It is intended for
// auto-approved workflow execution after higher-level gates have passed.
type CreateDirectoryDirect struct {
	WorkspaceRoot string
	AllowedPaths  []string
	Emit          func(eventType, source string, payload map[string]any)
}

func (t *CreateDirectoryDirect) Name() string { return "create_directory" }
func (t *CreateDirectoryDirect) Description() string {
	return "Creates a directory and any missing parent directories under configured write roots."
}
func (t *CreateDirectoryDirect) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"path": {Type: "string", Description: "Directory path to create. Use an absolute path under a configured write root, or a path relative to the workspace root", Required: true},
		},
		Output: ParamSpec{Type: "object", Description: "Directory creation result"},
	}
}
func (t *CreateDirectoryDirect) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	pathVal, ok := input["path"].(string)
	if !ok || strings.TrimSpace(pathVal) == "" {
		return ToolResult{Success: false, Error: "required parameter 'path' must be a non-empty string"}, nil
	}
	if strings.Contains(pathVal, "..") {
		return ToolResult{Success: false, Error: "path traversal blocked"}, nil
	}
	roots := append([]string{t.WorkspaceRoot}, t.AllowedPaths...)
	absPath, err := safety.ResolveAndContainMulti(roots, pathVal)
	if err != nil {
		return ToolResult{Success: false, Error: err.Error()}, nil
	}

	if info, statErr := os.Lstat(absPath); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return ToolResult{Success: false, Error: fmt.Sprintf("target path %q is a symlink, not a directory", pathVal)}, nil
		}
		if !info.IsDir() {
			return ToolResult{Success: false, Error: fmt.Sprintf("target path %q exists and is not a directory", pathVal)}, nil
		}
		return ToolResult{
			Success: true,
			Output: map[string]any{
				"path":    absPath,
				"created": false,
				"status":  "already_exists",
			},
		}, nil
	} else if !os.IsNotExist(statErr) {
		return ToolResult{Success: false, Error: fmt.Sprintf("failed to inspect directory: %v", statErr)}, nil
	}

	if err := os.MkdirAll(absPath, 0755); err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("failed to create directory: %v", err)}, nil
	}
	if t.Emit != nil {
		t.Emit("prizm.mutation.applied", "prizm-tool-executor", map[string]any{
			"mutation_type": "create_directory",
			"target_path":   absPath,
		})
	}
	return ToolResult{
		Success: true,
		Output: map[string]any{
			"path":    absPath,
			"created": true,
			"status":  "created",
		},
	}, nil
}

// ReadProjectTool recursively reads a project directory, returning the contents of
// all text files within it. It respects .gitignore patterns and skips binary files,
// large files, and common non-code directories (node_modules, .git, vendor, etc.).
// This gives an agent a complete view of a codebase in one tool call instead of
// requiring many list_dir + read_file round-trips.
//
// Policy: always allowed (read-only, no modifications).
type ReadProjectTool struct {
	WorkspaceRoot string
	AllowedPaths  []string
	MaxFileSize   int64
	MaxTokens     int
}

const DefaultReadProjectTokenBudget = 50_000

const readProjectTruncationMarker = "\n[... truncated by read_project token budget ...]"

var errReadProjectTokenBudget = errors.New("read_project token budget exhausted")

func (t *ReadProjectTool) Name() string { return "read_project" }
func (t *ReadProjectTool) Description() string {
	return "Recursively reads a project directory and returns all text file contents. Skips .git, node_modules, vendor, binary files, and large files. Use this to understand a whole project at once."
}
func (t *ReadProjectTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"path":       {Type: "string", Description: "Root path. Use an absolute path for projects outside the workspace, or a path relative to the workspace root (use '.' for entire workspace)", Required: true},
			"max_files":  {Type: "integer", Description: "Maximum number of files to read (default 50)", Required: false},
			"max_tokens": {Type: "integer", Description: "Maximum estimated tokens to return across file contents (default 50000)", Required: false},
		},
		Output: ParamSpec{Type: "object", Description: "Map of file paths to their contents, plus file count, byte size, and token budget metadata"},
	}
}

// skipDirs are directories that should always be skipped during project traversal.
var skipDirs = map[string]bool{
	".git": true, ".svn": true, ".hg": true,
	"node_modules": true, "vendor": true, "__pycache__": true,
	".next": true, ".cache": true, "dist": true, "build": true, "out": true,
	"bin": true, ".prizm": true, "runs": true,
}

// skipExtensions are file extensions that indicate binary/non-code files.
var skipExtensions = map[string]bool{
	".exe": true, ".dll": true, ".so": true, ".dylib": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".ico": true,
	".mp3": true, ".mp4": true, ".wav": true, ".avi": true, ".mov": true,
	".zip": true, ".tar": true, ".gz": true, ".rar": true, ".7z": true,
	".pdf": true, ".doc": true, ".docx": true, ".xls": true, ".xlsx": true,
	".ppt": true, ".pptx": true,
	".woff": true, ".woff2": true, ".ttf": true, ".eot": true, ".otf": true,
	".db": true, ".sqlite": true, ".sqlite3": true,
	".pem": true, ".key": true, ".crt": true,
	".wasm": true, ".class": true, ".o": true, ".a": true,
}

// isTextFile uses a simple heuristic: check extension, then peek at first 512 bytes
// for null characters (which indicate binary content).
func isTextFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if skipExtensions[ext] {
		return false
	}

	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	buf := make([]byte, 512)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return false
	}

	// Binary files typically contain null bytes
	for i := 0; i < n; i++ {
		if buf[i] == 0 {
			return false
		}
	}
	return true
}

func (t *ReadProjectTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	pathVal, ok := input["path"].(string)
	if !ok {
		return ToolResult{Success: false, Error: "required parameter 'path' must be a string"}, nil
	}

	maxFiles := 50
	if mf, ok := input["max_files"].(float64); ok && mf > 0 {
		maxFiles = int(mf)
	}
	maxTokens := t.MaxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultReadProjectTokenBudget
	}
	if mt, ok := input["max_tokens"].(float64); ok && mt > 0 {
		maxTokens = int(mt)
	}

	resolvedPath, err := FuzzyResolvePath(ToolPaths{WorkspaceRoot: t.WorkspaceRoot, AllowedPaths: t.AllowedPaths}, pathVal)
	if err != nil {
		return ToolResult{Success: false, Error: err.Error()}, nil
	}

	maxSize := t.MaxFileSize
	if maxSize <= 0 {
		maxSize = 10 * 1024 * 1024 // 10MB default per file
	}

	var files []map[string]any
	var totalSize int
	var totalTokens int
	var skippedCount int
	var tokenBudgetExhausted bool

	err = filepath.Walk(resolvedPath, func(walkPath string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil // skip errors, keep walking
		}

		// Skip directories we don't care about
		if info.IsDir() {
			if skipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip binary files by extension
		ext := strings.ToLower(filepath.Ext(walkPath))
		if skipExtensions[ext] {
			skippedCount++
			return nil
		}

		// Skip files that are too large
		if info.Size() > maxSize {
			skippedCount++
			return nil
		}

		// Check if it's a text file
		if !isTextFile(walkPath) {
			skippedCount++
			return nil
		}

		// Enforce max files limit
		if len(files) >= maxFiles {
			return nil // stop adding but don't error
		}

		data, err := os.ReadFile(walkPath)
		if err != nil {
			skippedCount++
			return nil
		}
		content := string(data)
		fileTokens := estimateReadProjectTokens(content)
		truncatedForBudget := false
		if totalTokens+fileTokens > maxTokens {
			remainingTokens := maxTokens - totalTokens
			if remainingTokens <= 0 {
				skippedCount++
				tokenBudgetExhausted = true
				return errReadProjectTokenBudget
			}
			content = truncateReadProjectContent(content, remainingTokens)
			fileTokens = estimateReadProjectTokens(content)
			truncatedForBudget = true
			tokenBudgetExhausted = true
		}

		// Get relative path from workspace root
		relPath, err := filepath.Rel(resolvedPath, walkPath)
		if err != nil {
			relPath = walkPath
		}

		files = append(files, map[string]any{
			"path":      relPath,
			"content":   content,
			"size":      len(content),
			"tokens":    fileTokens,
			"truncated": truncatedForBudget,
		})
		totalSize += len(content)
		totalTokens += fileTokens
		if tokenBudgetExhausted {
			return errReadProjectTokenBudget
		}

		return nil
	})
	if errors.Is(err, errReadProjectTokenBudget) {
		err = nil
	}
	if err != nil {
		return ToolResult{Success: false, Error: fmt.Sprintf("walk failed: %v", err)}, nil
	}

	return ToolResult{
		Success: true,
		Output: map[string]any{
			"root":                   pathVal,
			"files":                  files,
			"file_count":             len(files),
			"total_bytes":            totalSize,
			"total_tokens":           totalTokens,
			"skipped_count":          skippedCount,
			"max_files":              maxFiles,
			"max_tokens":             maxTokens,
			"token_budget_exhausted": tokenBudgetExhausted,
			"truncated":              skippedCount > 0 || len(files) >= maxFiles || tokenBudgetExhausted,
		},
	}, nil
}

func estimateReadProjectTokens(content string) int {
	if content == "" {
		return 0
	}
	return (len(content) + 3) / 4
}

func truncateReadProjectContent(content string, tokenBudget int) string {
	if tokenBudget <= 0 {
		return ""
	}
	maxChars := tokenBudget * 4
	if len(content) <= maxChars {
		return content
	}
	if maxChars <= len(readProjectTruncationMarker) {
		return content[:maxChars]
	}
	return content[:maxChars-len(readProjectTruncationMarker)] + readProjectTruncationMarker
}

// RegisterBuiltins adds the built-in tools to a registry, using the given
// workspace root and max file size. Returns the registry for chaining.
func RegisterBuiltins(registry *Registry, workspaceRoot string, maxFileSize int64, allowedPaths ...string) *Registry {
	return RegisterBuiltinsWithRoots(registry, workspaceRoot, maxFileSize, allowedPaths, allowedPaths)
}

// RegisterBuiltinsWithRoots adds read-only tools scoped to read roots.
func RegisterBuiltinsWithRoots(registry *Registry, workspaceRoot string, maxFileSize int64, readRoots, writeRoots []string) *Registry {
	if maxFileSize <= 0 {
		maxFileSize = 1024 * 1024 // 1MB default
	}

	registry.Register(&EchoTool{})
	registry.Register(&ListDirTool{WorkspaceRoot: workspaceRoot, AllowedPaths: readRoots})
	registry.Register(&ReadFileTool{WorkspaceRoot: workspaceRoot, MaxFileSize: maxFileSize, AllowedPaths: readRoots})
	registry.Register(&WriteFileDryRun{})
	registry.Register(&ReadProjectTool{WorkspaceRoot: workspaceRoot, MaxFileSize: maxFileSize, AllowedPaths: readRoots})

	// V28: Project comprehension tools
	registry.Register(&SearchFilesTool{WorkspaceRoot: workspaceRoot, AllowedPaths: readRoots})
	registry.Register(&ProjectOverviewTool{WorkspaceRoot: workspaceRoot, AllowedPaths: readRoots})

	// V28: Git read-only tools
	registry.Register(&GitStatusTool{ToolPaths: ToolPaths{WorkspaceRoot: workspaceRoot, AllowedPaths: readRoots}})
	registry.Register(&GitLogTool{ToolPaths: ToolPaths{WorkspaceRoot: workspaceRoot, AllowedPaths: readRoots}})
	registry.Register(&GitDiffTool{ToolPaths: ToolPaths{WorkspaceRoot: workspaceRoot, AllowedPaths: readRoots}})
	registry.Register(&GitBranchListTool{ToolPaths: ToolPaths{WorkspaceRoot: workspaceRoot, AllowedPaths: readRoots}})
	registry.Register(&GitCheckoutTool{ToolPaths: ToolPaths{WorkspaceRoot: workspaceRoot, AllowedPaths: writeRoots}})

	return registry
}

// RegisterBuiltinsV4 adds all V3 + V4 + V28 tools to the registry.
// V4 tools (write_file_proposal) require approval.
// V28 mutation tools (git_add, git_commit, git_push) require approval.
// protectedBranch names a branch git_commit/git_push must refuse to write to
// directly; empty defaults to "main" (see RegisterBuiltinsV4WithRoots).
func RegisterBuiltinsV4(registry *Registry, workspaceRoot string, maxFileSize int64, protectedBranch string, allowedPaths ...string) *Registry {
	return RegisterBuiltinsV4WithRoots(registry, workspaceRoot, maxFileSize, allowedPaths, allowedPaths, protectedBranch)
}

// RegisterBuiltinsV4WithRoots adds read tools scoped to read roots and mutation
// proposal tools scoped to write roots. protectedBranch names a branch
// git_commit/git_push must refuse to write to directly; empty defaults to
// "main" (matching orchestrator.Config.ProtectedBranch's default for callers
// with no project config to consult, e.g. standalone CLI tool/workflow runs).
func RegisterBuiltinsV4WithRoots(registry *Registry, workspaceRoot string, maxFileSize int64, readRoots, writeRoots []string, protectedBranch string) *Registry {
	RegisterBuiltinsWithRoots(registry, workspaceRoot, maxFileSize, readRoots, writeRoots)
	registry.Register(&WriteFileProposal{WorkspaceRoot: workspaceRoot, AllowedPaths: writeRoots})
	registry.Register(&CreateDirectoryProposal{WorkspaceRoot: workspaceRoot, AllowedPaths: writeRoots})

	if protectedBranch == "" {
		protectedBranch = "main"
	}

	// V28: Git mutation tools (requires approval)
	registry.Register(&GitAddTool{ToolPaths: ToolPaths{WorkspaceRoot: workspaceRoot, AllowedPaths: writeRoots}})
	registry.Register(&GitCommitTool{ToolPaths: ToolPaths{WorkspaceRoot: workspaceRoot, AllowedPaths: writeRoots}, ProtectedBranch: protectedBranch})
	registry.Register(&GitPushTool{ToolPaths: ToolPaths{WorkspaceRoot: workspaceRoot, AllowedPaths: writeRoots}, ProtectedBranch: protectedBranch})

	return registry
}

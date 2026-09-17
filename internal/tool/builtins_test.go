package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEchoTool(t *testing.T) {
	tool := &EchoTool{}
	result, err := tool.Execute(context.Background(), map[string]any{"text": "hello world"})
	if err != nil {
		t.Fatalf("echo should not error: %v", err)
	}
	if !result.Success {
		t.Errorf("echo should succeed, got: %s", result.Error)
	}
	if result.Output["text"] != "hello world" {
		t.Errorf("expected 'hello world', got %v", result.Output["text"])
	}
}

func TestEchoToolMissingText(t *testing.T) {
	tool := &EchoTool{}
	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("echo should not return Go error: %v", err)
	}
	if result.Success {
		t.Error("echo without text should fail")
	}
}

func TestEchoToolSchema(t *testing.T) {
	tool := &EchoTool{}
	if tool.Name() != "echo" {
		t.Errorf("expected name 'echo', got %s", tool.Name())
	}
	schema := tool.Schema()
	if _, ok := schema.Input["text"]; !ok {
		t.Error("echo schema missing 'text' input parameter")
	}
}

func TestListDirTool(t *testing.T) {
	tmpDir := t.TempDir()
	// Create test structure
	os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("content1"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "file2.txt"), []byte("content2"), 0644)
	os.MkdirAll(filepath.Join(tmpDir, "subdir"), 0755)

	tool := &ListDirTool{WorkspaceRoot: tmpDir}
	result, err := tool.Execute(context.Background(), map[string]any{"path": "."})
	if err != nil {
		t.Fatalf("list_dir should not error: %v", err)
	}
	if !result.Success {
		t.Errorf("list_dir should succeed, got: %s", result.Error)
	}

	count, ok := result.Output["count"].(int)
	if !ok {
		t.Errorf("expected count to be int, got %T", result.Output["count"])
	}
	if count != 3 {
		t.Errorf("expected 3 entries (2 files + 1 dir), got %d", count)
	}

	// Verify file/dir prefixes
	files, ok := result.Output["files"].([]string)
	if !ok {
		t.Fatalf("expected files to be []string, got %T", result.Output["files"])
	}
	hasFile := false
	hasDir := false
	for _, f := range files {
		if len(f) > 5 && f[:5] == "file:" {
			hasFile = true
		}
		if len(f) > 4 && f[:4] == "dir:" {
			hasDir = true
		}
	}
	if !hasFile {
		t.Error("list_dir should include file: prefix entries")
	}
	if !hasDir {
		t.Error("list_dir should include dir: prefix entries")
	}
}

func TestListDirToolMissingPath(t *testing.T) {
	tool := &ListDirTool{WorkspaceRoot: "."}
	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("list_dir should not return Go error: %v", err)
	}
	if result.Success {
		t.Error("list_dir without path should fail")
	}
}

func TestReadFileTool(t *testing.T) {
	tmpDir := t.TempDir()
	testContent := "hello world\nline 2\nline 3"
	os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte(testContent), 0644)

	tool := &ReadFileTool{WorkspaceRoot: tmpDir, MaxFileSize: 1024 * 1024}
	result, err := tool.Execute(context.Background(), map[string]any{"path": "test.txt"})
	if err != nil {
		t.Fatalf("read_file should not error: %v", err)
	}
	if !result.Success {
		t.Errorf("read_file should succeed, got: %s", result.Error)
	}
	if result.Output["content"] != testContent {
		t.Errorf("content mismatch: expected %q, got %q", testContent, result.Output["content"])
	}
	if result.Output["path"] != "test.txt" {
		t.Errorf("path mismatch: expected 'test.txt', got %v", result.Output["path"])
	}
}

func TestReadFileToolMissingPath(t *testing.T) {
	tool := &ReadFileTool{WorkspaceRoot: ".", MaxFileSize: 1024 * 1024}
	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("read_file should not return Go error: %v", err)
	}
	if result.Success {
		t.Error("read_file without path should fail")
	}
}

func TestReadFileToolMaxLines(t *testing.T) {
	tmpDir := t.TempDir()
	lines := make([]string, 50)
	for i := 0; i < 50; i++ {
		lines[i] = fmt.Sprintf("line %d", i+1)
	}
	testContent := strings.Join(lines, "\n")
	os.WriteFile(filepath.Join(tmpDir, "long.txt"), []byte(testContent), 0644)

	tool := &ReadFileTool{WorkspaceRoot: tmpDir, MaxFileSize: 1024 * 1024}

	// With max_lines=10, should get first 10 lines + truncation notice
	result, err := tool.Execute(context.Background(), map[string]any{"path": "long.txt", "max_lines": float64(10)})
	if err != nil {
		t.Fatalf("read_file with max_lines should not error: %v", err)
	}
	if !result.Success {
		t.Fatalf("read_file with max_lines should succeed, got: %s", result.Error)
	}
	content := result.Output["content"].(string)
	if !strings.Contains(content, "line 10") {
		t.Error("max_lines result should contain line 10")
	}
	if strings.Contains(content, "line 11") {
		t.Error("max_lines result should NOT contain line 11")
	}
	if !strings.Contains(content, "40 more lines") {
		t.Error("max_lines result should contain truncation notice")
	}

	// Without max_lines, should get full content
	result2, _ := tool.Execute(context.Background(), map[string]any{"path": "long.txt"})
	fullContent := result2.Output["content"].(string)
	if !strings.Contains(fullContent, "line 50") {
		t.Error("full read should contain line 50")
	}
}

func TestReadFileToolSizeLimit(t *testing.T) {
	tmpDir := t.TempDir()
	// Create a file larger than the size limit
	largeContent := make([]byte, 2000) // 2KB file, 1KB limit
	for i := range largeContent {
		largeContent[i] = 'A'
	}
	os.WriteFile(filepath.Join(tmpDir, "large.txt"), largeContent, 0644)

	tool := &ReadFileTool{WorkspaceRoot: tmpDir, MaxFileSize: 1024} // 1KB limit
	result, err := tool.Execute(context.Background(), map[string]any{"path": "large.txt"})
	if err != nil {
		t.Fatalf("read_file should not error: %v", err)
	}
	if result.Success {
		t.Error("read_file should fail when file exceeds size limit")
	}
	if result.Error == "" {
		t.Error("expected error message for size limit exceeded")
	}
}

func TestReadFileToolNonexistent(t *testing.T) {
	tmpDir := t.TempDir()
	tool := &ReadFileTool{WorkspaceRoot: tmpDir, MaxFileSize: 1024 * 1024}
	result, err := tool.Execute(context.Background(), map[string]any{"path": "nonexistent.txt"})
	if err != nil {
		t.Fatalf("read_file should not return Go error for missing file: %v", err)
	}
	if result.Success {
		t.Error("read_file should fail for nonexistent file")
	}
}
func TestReadProjectToolTokenBudget(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte(strings.Repeat("A", 120)), 0644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "b.txt"), []byte(strings.Repeat("B", 120)), 0644); err != nil {
		t.Fatalf("write b.txt: %v", err)
	}

	tool := &ReadProjectTool{WorkspaceRoot: tmpDir, MaxFileSize: 1024 * 1024}
	result, err := tool.Execute(context.Background(), map[string]any{"path": ".", "max_tokens": float64(20)})
	if err != nil {
		t.Fatalf("read_project should not return Go error: %v", err)
	}
	if !result.Success {
		t.Fatalf("read_project should succeed, got: %s", result.Error)
	}
	if exhausted, _ := result.Output["token_budget_exhausted"].(bool); !exhausted {
		t.Fatalf("expected token_budget_exhausted=true, got %+v", result.Output)
	}
	if truncated, _ := result.Output["truncated"].(bool); !truncated {
		t.Fatalf("expected truncated=true, got %+v", result.Output)
	}
	totalTokens, ok := result.Output["total_tokens"].(int)
	if !ok {
		t.Fatalf("total_tokens type = %T", result.Output["total_tokens"])
	}
	if totalTokens > 20 {
		t.Fatalf("total_tokens = %d, want <= 20", totalTokens)
	}
	files, ok := result.Output["files"].([]map[string]any)
	if !ok || len(files) != 1 {
		t.Fatalf("files = %#v", result.Output["files"])
	}
	if files[0]["truncated"] != true {
		t.Fatalf("first file should be marked truncated: %#v", files[0])
	}
	content, ok := files[0]["content"].(string)
	if !ok {
		t.Fatalf("content type = %T", files[0]["content"])
	}
	if len(content) > 20*4 {
		t.Fatalf("truncated content length = %d, want <= 80", len(content))
	}
}

func TestWriteFileDryRunNoDiskWrite(t *testing.T) {
	tool := &WriteFileDryRun{}
	result, err := tool.Execute(context.Background(), map[string]any{
		"path":    "test.txt",
		"content": "hello world",
	})
	if err != nil {
		t.Fatalf("write_file_dry_run should not error: %v", err)
	}
	if !result.Success {
		t.Errorf("write_file_dry_run should succeed, got: %s", result.Error)
	}

	// Verify it returns a preview diff
	preview, ok := result.Output["preview_diff"].(string)
	if !ok || preview == "" {
		t.Error("write_file_dry_run should return preview_diff")
	}
	if !contains(preview, "test.txt") {
		t.Error("preview diff should contain the file path")
	}

	// Verify it reports not written
	if result.Output["written"] != false {
		t.Error("write_file_dry_run should have written=false")
	}
}

func TestWriteFileDryRunMissingParams(t *testing.T) {
	tool := &WriteFileDryRun{}

	// Missing path
	result, err := tool.Execute(context.Background(), map[string]any{"content": "hello"})
	if err != nil {
		t.Fatalf("should not return Go error: %v", err)
	}
	if result.Success {
		t.Error("missing path should cause failure")
	}

	// Missing content
	result, err = tool.Execute(context.Background(), map[string]any{"path": "test.txt"})
	if err != nil {
		t.Fatalf("should not return Go error: %v", err)
	}
	if result.Success {
		t.Error("missing content should cause failure")
	}
}

func TestCreateDirectoryDirectCreatesUnderWriteRoot(t *testing.T) {
	workspace := t.TempDir()
	writeRoot := t.TempDir()
	target := filepath.Join(writeRoot, "new", "empty")

	tool := &CreateDirectoryDirect{WorkspaceRoot: workspace, AllowedPaths: []string{writeRoot}}
	result, err := tool.Execute(context.Background(), map[string]any{"path": target})
	if err != nil {
		t.Fatalf("create_directory should not return Go error: %v", err)
	}
	if !result.Success {
		t.Fatalf("create_directory should succeed, got: %s", result.Error)
	}
	if result.Output["created"] != true {
		t.Fatalf("create_directory should report created=true, got %v", result.Output["created"])
	}
	info, statErr := os.Stat(target)
	if statErr != nil {
		t.Fatalf("created directory missing: %v", statErr)
	}
	if !info.IsDir() {
		t.Fatalf("target should be a directory")
	}
}

func TestCreateDirectoryDirectExistingDirectoryNoop(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "already")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}

	tool := &CreateDirectoryDirect{WorkspaceRoot: workspace}
	result, err := tool.Execute(context.Background(), map[string]any{"path": "already"})
	if err != nil {
		t.Fatalf("create_directory should not return Go error: %v", err)
	}
	if !result.Success {
		t.Fatalf("existing directory should succeed, got: %s", result.Error)
	}
	if result.Output["created"] != false {
		t.Fatalf("existing directory should report created=false, got %v", result.Output["created"])
	}
}

func TestCreateDirectoryDirectRejectsExistingFile(t *testing.T) {
	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "file.txt"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &CreateDirectoryDirect{WorkspaceRoot: workspace}
	result, err := tool.Execute(context.Background(), map[string]any{"path": "file.txt"})
	if err != nil {
		t.Fatalf("create_directory should not return Go error: %v", err)
	}
	if result.Success {
		t.Fatalf("create_directory should reject existing file")
	}
}

func TestCreateDirectoryDirectRejectsOutsideWriteRoots(t *testing.T) {
	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")

	tool := &CreateDirectoryDirect{WorkspaceRoot: workspace}
	result, err := tool.Execute(context.Background(), map[string]any{"path": outside})
	if err != nil {
		t.Fatalf("create_directory should not return Go error: %v", err)
	}
	if result.Success {
		t.Fatalf("create_directory should reject outside write roots")
	}
}

func TestCreateDirectoryProposalDoesNotCreateDirectory(t *testing.T) {
	workspace := t.TempDir()
	writeRoot := t.TempDir()
	target := filepath.Join(writeRoot, "pending")

	tool := &CreateDirectoryProposal{WorkspaceRoot: workspace, AllowedPaths: []string{writeRoot}}
	result, err := tool.Execute(context.Background(), map[string]any{"path": target})
	if err != nil {
		t.Fatalf("create_directory_proposal should not return Go error: %v", err)
	}
	if !result.Success {
		t.Fatalf("create_directory_proposal should succeed, got: %s", result.Error)
	}
	if result.Output["mutation_type"] != "create_directory" {
		t.Fatalf("mutation_type = %v, want create_directory", result.Output["mutation_type"])
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("proposal should not create directory, stat error = %v", statErr)
	}
}

func TestRegisterBuiltins(t *testing.T) {
	reg := NewRegistry()
	RegisterBuiltins(reg, ".", 1024*1024)

	names := reg.List()
	expectedTools := []string{"echo", "list_dir", "read_file", "write_file_dry_run", "read_project", "search_files", "project_overview", "git_status", "git_log", "git_diff", "git_branch_list", "git_checkout"}
	for _, name := range expectedTools {
		found := false
		for _, n := range names {
			if n == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected tool %q in registry, not found", name)
		}
	}
	if len(names) != len(expectedTools) {
		t.Errorf("expected %d builtins, got %d: %v", len(expectedTools), len(names), names)
	}
}

func TestRegisterBuiltinsV4IncludesCreateDirectoryProposal(t *testing.T) {
	reg := NewRegistry()
	RegisterBuiltinsV4(reg, ".", 1024*1024, "")

	if _, err := reg.Resolve("create_directory_proposal"); err != nil {
		t.Fatalf("expected create_directory_proposal in V4 registry: %v", err)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// ── Symlink Traversal Security Tests ────────────────────────────────

func TestReadFileTool_BlocksSymlinkEscape(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a file outside the workspace
	outsideDir := filepath.Join(t.TempDir(), "outside")
	os.MkdirAll(outsideDir, 0755)
	os.WriteFile(filepath.Join(outsideDir, "secret.txt"), []byte("secret data"), 0644)

	// Create symlink inside workspace pointing outside
	linkPath := filepath.Join(tmpDir, "escape_link")
	os.Symlink(outsideDir, linkPath)

	tool := &ReadFileTool{WorkspaceRoot: tmpDir, MaxFileSize: 1024 * 1024}
	result, err := tool.Execute(context.Background(), map[string]any{"path": "escape_link/secret.txt"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("ReadFileTool should block symlink escape")
	}
}

func TestReadFileTool_BlocksDirectSymlink(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a file outside the workspace
	outsideFile := filepath.Join(t.TempDir(), "outside_secret.txt")
	os.WriteFile(outsideFile, []byte("secret data"), 0644)

	// Create symlink inside workspace pointing directly to outside file
	linkPath := filepath.Join(tmpDir, "secret_link")
	os.Symlink(outsideFile, linkPath)

	tool := &ReadFileTool{WorkspaceRoot: tmpDir, MaxFileSize: 1024 * 1024}
	result, err := tool.Execute(context.Background(), map[string]any{"path": "secret_link"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("ReadFileTool should block direct symlink to outside file")
	}
}

func TestReadFileTool_AllowsNormalFiles(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "normal.txt"), []byte("hello world"), 0644)

	tool := &ReadFileTool{WorkspaceRoot: tmpDir, MaxFileSize: 1024 * 1024}
	result, err := tool.Execute(context.Background(), map[string]any{"path": "normal.txt"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("ReadFileTool should allow normal files, got error: %s", result.Error)
	}
}

func TestListDirTool_BlocksSymlinkEscape(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a directory outside the workspace
	outsideDir := filepath.Join(t.TempDir(), "outside_dir")
	os.MkdirAll(outsideDir, 0755)

	// Create symlink inside workspace pointing outside
	linkPath := filepath.Join(tmpDir, "escape_link")
	os.Symlink(outsideDir, linkPath)

	tool := &ListDirTool{WorkspaceRoot: tmpDir}
	result, err := tool.Execute(context.Background(), map[string]any{"path": "escape_link"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("ListDirTool should block symlink escape")
	}
}

func TestListDirTool_AllowsNormalDirectories(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, "subdir"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("data"), 0644)

	tool := &ListDirTool{WorkspaceRoot: tmpDir}
	result, err := tool.Execute(context.Background(), map[string]any{"path": "."})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("ListDirTool should allow normal directories, got error: %s", result.Error)
	}
}

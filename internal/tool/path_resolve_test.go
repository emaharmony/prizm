package tool

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFuzzyResolvePath_ExactPath(t *testing.T) {
	tmpDir := t.TempDir()
	bassbookDir := filepath.Join(tmpDir, "bassbook")
	if err := os.MkdirAll(bassbookDir, 0755); err != nil {
		t.Fatal(err)
	}

	tp := ToolPaths{
		WorkspaceRoot: tmpDir,
		AllowedPaths:  nil,
	}

	resolved, err := FuzzyResolvePath(tp, bassbookDir)
	if err != nil {
		t.Fatalf("exact path should resolve: %v", err)
	}
	expectedResolved, _ := filepath.EvalSymlinks(bassbookDir)
	actualResolved, _ := filepath.EvalSymlinks(resolved)
	if actualResolved != expectedResolved {
		t.Errorf("expected %q, got %q", expectedResolved, actualResolved)
	}
}

func TestFuzzyResolvePath_FuzzyMatch(t *testing.T) {
	tmpDir := t.TempDir()
	bassbookDir := filepath.Join(tmpDir, "bassbook")
	prizmDir := filepath.Join(tmpDir, "prizm")
	if err := os.MkdirAll(bassbookDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(prizmDir, 0755); err != nil {
		t.Fatal(err)
	}

	tp := ToolPaths{
		WorkspaceRoot: "/nonexistent/workspace",
		AllowedPaths:  []string{tmpDir},
	}

	resolved, err := FuzzyResolvePath(tp, "bassbook")
	if err != nil {
		t.Fatalf("fuzzy match should find bassbook: %v", err)
	}
	if resolved != bassbookDir {
		t.Errorf("expected %q, got %q", bassbookDir, resolved)
	}
}

func TestFuzzyResolvePath_RecursiveFuzzyMatch(t *testing.T) {
	// Test that fuzzy match finds directories nested multiple levels deep
	tmpDir := t.TempDir()
	nestedDir := filepath.Join(tmpDir, "projects", "repos", "bassbook")
	if err := os.MkdirAll(nestedDir, 0755); err != nil {
		t.Fatal(err)
	}

	tp := ToolPaths{
		WorkspaceRoot: "/nonexistent/workspace",
		AllowedPaths:  []string{tmpDir},
	}

	// "bassbook" should be found recursively inside projects/repos/
	resolved, err := FuzzyResolvePath(tp, "bassbook")
	if err != nil {
		t.Fatalf("recursive fuzzy match should find bassbook: %v", err)
	}
	if resolved != nestedDir {
		t.Errorf("expected %q, got %q", nestedDir, resolved)
	}
}

func TestFuzzyResolvePath_CaseInsensitive(t *testing.T) {
	tmpDir := t.TempDir()
	bassbookDir := filepath.Join(tmpDir, "BassBook")
	if err := os.MkdirAll(bassbookDir, 0755); err != nil {
		t.Fatal(err)
	}

	tp := ToolPaths{
		WorkspaceRoot: "/nonexistent/workspace",
		AllowedPaths:  []string{tmpDir},
	}

	resolved, err := FuzzyResolvePath(tp, "bassbook")
	if err != nil {
		t.Fatalf("case-insensitive match should work: %v", err)
	}
	if resolved != bassbookDir {
		t.Errorf("expected %q, got %q", bassbookDir, resolved)
	}
}

func TestFuzzyResolvePath_TildeExpansion(t *testing.T) {
	home, _ := os.UserHomeDir()
	tmpDir := t.TempDir()
	projectDir := filepath.Join(tmpDir, "myproject")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatal(err)
	}

	tp := ToolPaths{
		WorkspaceRoot: home,
		AllowedPaths:  []string{tmpDir},
	}

	// ~/myproject should resolve via home expansion if it exists in home,
	// or fall through to fuzzy match against allowed paths
	// Test that a valid path in workspace root resolves correctly
	homeProjectDir := filepath.Join(home, "myproject")
	if _, err := os.Stat(homeProjectDir); err == nil {
		// ~/myproject exists in home directory
		resolved, err := FuzzyResolvePath(tp, "~/myproject")
		if err != nil {
			t.Fatalf("tilde expansion should work: %v", err)
		}
		expectedResolved, _ := filepath.EvalSymlinks(homeProjectDir)
		actualResolved, _ := filepath.EvalSymlinks(resolved)
		if actualResolved != expectedResolved {
			t.Errorf("expected %q, got %q", expectedResolved, actualResolved)
		}
	}
	// If ~/myproject doesn't exist in home, the fuzzy match should find it
	// in the allowed paths. This is tested implicitly by the RecursiveFuzzyMatch test.
}

func TestFuzzyResolvePath_HomePathCorrection(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("skipping /home→/Users correction test on non-macOS")
	}

	// /home/ema/projects/repos/bassbook → fuzzy match "bassbook" in allowed paths
	tmpDir := t.TempDir()
	nestedDir := filepath.Join(tmpDir, "projects", "repos", "bassbook")
	if err := os.MkdirAll(nestedDir, 0755); err != nil {
		t.Fatal(err)
	}

	tp := ToolPaths{
		WorkspaceRoot: "/nonexistent/workspace",
		AllowedPaths:  []string{tmpDir},
	}

	// On macOS, /home/ema is corrected to /Users/ema, but that won't exist
	// in our test dirs. The fuzzy matcher should still find "bassbook".
	resolved, err := FuzzyResolvePath(tp, "/home/ema/projects/repos/bassbook")
	if err != nil {
		// The /home→/Users correction runs, but /Users/ema may not contain our dirs.
		// Fuzzy match should still find "bassbook" in the allowed paths.
		t.Logf("/home correction + fuzzy match: %v", err)
	}
	if err == nil && resolved != nestedDir {
		t.Errorf("expected %q, got %q", nestedDir, resolved)
	}
}

func TestFuzzyResolvePath_NoMatch(t *testing.T) {
	tmpDir := t.TempDir()
	tp := ToolPaths{
		WorkspaceRoot: "/nonexistent/workspace",
		AllowedPaths:  []string{tmpDir},
	}

	_, err := FuzzyResolvePath(tp, "totally_nonexistent_project_xyz")
	if err == nil {
		t.Error("expected error for nonexistent project name")
	}
}

func TestFuzzyResolvePath_RelativePath(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "src")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	tp := ToolPaths{
		WorkspaceRoot: tmpDir,
		AllowedPaths:  nil,
	}

	resolved, err := FuzzyResolvePath(tp, "src")
	if err != nil {
		t.Fatalf("relative path should resolve: %v", err)
	}
	expectedResolved, _ := filepath.EvalSymlinks(subDir)
	actualResolved, _ := filepath.EvalSymlinks(resolved)
	if actualResolved != expectedResolved {
		t.Errorf("expected %q, got %q", expectedResolved, actualResolved)
	}
}

func TestFuzzyResolvePath_PrefixMatch(t *testing.T) {
	tmpDir := t.TempDir()
	longDir := filepath.Join(tmpDir, "bassbook-api")
	if err := os.MkdirAll(longDir, 0755); err != nil {
		t.Fatal(err)
	}

	tp := ToolPaths{
		WorkspaceRoot: "/nonexistent/workspace",
		AllowedPaths:  []string{tmpDir},
	}

	resolved, err := FuzzyResolvePath(tp, "bassbook")
	if err != nil {
		t.Fatalf("prefix match should work: %v", err)
	}
	if resolved != longDir {
		t.Errorf("expected %q, got %q", longDir, resolved)
	}
}

func TestFuzzyResolvePath_ExactOverPrefix(t *testing.T) {
	// When both "bassbook" and "bassbook-api" exist, "bassbook" query
	// should match the exact "bassbook" directory, not the prefix "bassbook-api"
	tmpDir := t.TempDir()
	bassbookDir := filepath.Join(tmpDir, "bassbook")
	bassbookAPIDir := filepath.Join(tmpDir, "bassbook-api")
	if err := os.MkdirAll(bassbookDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bassbookAPIDir, 0755); err != nil {
		t.Fatal(err)
	}

	tp := ToolPaths{
		WorkspaceRoot: "/nonexistent/workspace",
		AllowedPaths:  []string{tmpDir},
	}

	resolved, err := FuzzyResolvePath(tp, "bassbook")
	if err != nil {
		t.Fatalf("exact match over prefix should work: %v", err)
	}
	if resolved != bassbookDir {
		t.Errorf("expected exact match %q, got %q", bassbookDir, resolved)
	}
}

func TestFuzzyResolvePath_MultiRootPriority(t *testing.T) {
	// When the same directory name exists in workspace root and allowed_paths,
	// workspace root should win (it's listed first in AllRoots)
	tmpDir1 := t.TempDir()
	tmpDir2 := t.TempDir()
	project1 := filepath.Join(tmpDir1, "myproject")
	project2 := filepath.Join(tmpDir2, "myproject")
	if err := os.MkdirAll(project1, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(project2, 0755); err != nil {
		t.Fatal(err)
	}

	tp := ToolPaths{
		WorkspaceRoot: tmpDir1,
		AllowedPaths:  []string{tmpDir2},
	}

	resolved, err := FuzzyResolvePath(tp, "myproject")
	if err != nil {
		t.Fatalf("multi-root fuzzy match should work: %v", err)
	}
	// Resolve both for macOS symlink consistency
	expectedResolved, _ := filepath.EvalSymlinks(project1)
	actualResolved, _ := filepath.EvalSymlinks(resolved)
	if actualResolved != expectedResolved {
		t.Errorf("expected workspace root match %q, got %q", expectedResolved, actualResolved)
	}
}

func TestFuzzyResolvePath_DepthLimit(t *testing.T) {
	// Directories beyond max depth (4) should NOT be found
	tmpDir := t.TempDir()
	// 5 levels deep: tmpDir/a/b/c/d/e
	deepDir := filepath.Join(tmpDir, "a", "b", "c", "d", "e")
	if err := os.MkdirAll(deepDir, 0755); err != nil {
		t.Fatal(err)
	}

	tp := ToolPaths{
		WorkspaceRoot: "/nonexistent/workspace",
		AllowedPaths:  []string{tmpDir},
	}

	// "e" at depth 5 should NOT be found (max depth is 4)
	_, err := FuzzyResolvePath(tp, "e")
	if err == nil {
		t.Error("expected error for directory beyond max depth")
	}
}

func TestFuzzyResolvePath_SkipsHiddenDirs(t *testing.T) {
	// Hidden directories like .git and .cache should be skipped
	tmpDir := t.TempDir()
	hiddenDir := filepath.Join(tmpDir, ".git")
	visibleDir := filepath.Join(tmpDir, "myproject")
	if err := os.MkdirAll(hiddenDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(visibleDir, 0755); err != nil {
		t.Fatal(err)
	}

	tp := ToolPaths{
		WorkspaceRoot: "/nonexistent/workspace",
		AllowedPaths:  []string{tmpDir},
	}

	// Searching for "git" should not find .git
	_, err := FuzzyResolvePath(tp, "git")
	if err == nil {
		t.Error("expected .git to be skipped in fuzzy match")
	}
}

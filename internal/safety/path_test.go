package safety

import (
	"os"
	"path/filepath"
	"testing"
)

// TestIsWithinRoot_NormalPath verifies that a file within the root directory
// passes the containment check.
func TestIsWithinRoot_NormalPath(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "src", "main.go")
	os.MkdirAll(filepath.Dir(target), 0755)
	os.WriteFile(target, []byte("hello"), 0644)

	if !IsWithinRoot(target, root) {
		t.Errorf("IsWithinRoot(%q, %q) = false, want true", target, root)
	}
}

// TestIsWithinRoot_PathTraversal verifies that a path using ".." to escape
// the root directory is correctly rejected.
func TestIsWithinRoot_PathTraversal(t *testing.T) {
	root := t.TempDir()
	escape := filepath.Join(root, "..", "..", "etc", "passwd")

	absRoot, _ := filepath.Abs(root)
	absEscape, _ := filepath.Abs(escape)

	if IsWithinRoot(absEscape, absRoot) {
		t.Errorf("IsWithinRoot should block path traversal")
	}
}

// TestIsWithinRoot_SamePath verifies that the root directory itself is
// considered within the root (trivially true).
func TestIsWithinRoot_SamePath(t *testing.T) {
	root := t.TempDir()
	absRoot, _ := filepath.Abs(root)

	if !IsWithinRoot(absRoot, absRoot) {
		t.Errorf("IsWithinRoot(same, same) = false, want true")
	}
}

func TestIsWithinRoot_MissingRootFailsClosed(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing-root")
	if IsWithinRoot(filepath.Join(root, "child"), root) {
		t.Fatal("IsWithinRoot accepted a child under a missing containment root")
	}
}

// TestIsWithinRoot_NestedPath verifies that deeply nested paths that don't
// exist yet still pass containment — the function walks up to find the
// nearest existing ancestor and resolves from there.
func TestIsWithinRoot_NestedPath(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "a", "b", "c", "file.txt")

	// file.txt doesn't exist, but root does — should still pass
	if !IsWithinRoot(nested, root) {
		t.Errorf("IsWithinRoot nested path = false, want true")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	wantResolved := filepath.Join(resolvedRoot, "a", "b", "c", "file.txt")
	if resolved := resolveNonExistentPath(nested); resolved != wantResolved {
		t.Errorf("resolveNonExistentPath(%q) = %q, want %q", nested, resolved, wantResolved)
	}
}

// TestIsWithinRoot_SymlinkEscape verifies that a symlink inside the root
// that points outside is correctly blocked. This is the classic symlink
// escape attack: create a link inside the workspace that points to /etc,
// then try to read/write through it.
func TestIsWithinRoot_SymlinkEscape(t *testing.T) {
	root := t.TempDir()
	// Create a symlink inside root that points to the system temp dir (outside root)
	linkPath := filepath.Join(root, "escape_link")
	if err := os.Symlink(os.TempDir(), linkPath); err != nil {
		t.Skipf("symlink creation is unavailable on this host: %v", err)
	}

	target := filepath.Join(linkPath, "file.txt")
	if IsWithinRoot(target, root) {
		t.Errorf("IsWithinRoot should block symlink escape")
	}
}

// TestResolveAndContain_NormalPath verifies that resolving a relative path
// against a root directory works correctly for normal (non-escaping) paths.
func TestResolveAndContain_NormalPath(t *testing.T) {
	root := t.TempDir()
	resolved, err := ResolveAndContain(root, "src/main.go")
	if err != nil {
		t.Fatalf("ResolveAndContain() error = %v", err)
	}
	expected := filepath.Join(root, "src", "main.go")
	if resolved != expected {
		t.Errorf("ResolveAndContain() = %q, want %q", resolved, expected)
	}
}

// TestResolveAndContain_PathTraversal verifies that ResolveAndContain
// rejects paths that try to escape the root using "..".
func TestResolveAndContain_PathTraversal(t *testing.T) {
	root := t.TempDir()
	_, err := ResolveAndContain(root, "../../etc/passwd")
	if err == nil {
		t.Error("ResolveAndContain should block path traversal")
	}
}

// TestResolveAndContain_AbsolutePath verifies that ResolveAndContain
// rejects absolute paths — all paths must be relative to the root.
func TestResolveAndContain_AbsolutePath(t *testing.T) {
	root := t.TempDir()
	_, err := ResolveAndContain(root, "/etc/passwd")
	if err == nil {
		t.Error("ResolveAndContain should block absolute paths outside root")
	}
}

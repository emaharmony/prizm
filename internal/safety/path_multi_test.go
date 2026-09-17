package safety_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/emaharmony/prizm/internal/safety"
)

func TestIsWithinAnyRoot_SingleRoot(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "subdir")
	os.MkdirAll(subDir, 0755)

	if !safety.IsWithinAnyRoot(subDir, []string{tmpDir}) {
		t.Error("expected subdir to be within root")
	}
	if safety.IsWithinAnyRoot("/tmp/elsewhere", []string{tmpDir}) {
		t.Error("expected outside path to not be within root")
	}
}

func TestIsWithinAnyRoot_MultipleRoots(t *testing.T) {
	tmpDir1 := t.TempDir()
	tmpDir2 := t.TempDir()

	subDir1 := filepath.Join(tmpDir1, "project1")
	subDir2 := filepath.Join(tmpDir2, "project2")
	os.MkdirAll(subDir1, 0755)
	os.MkdirAll(subDir2, 0755)

	roots := []string{tmpDir1, tmpDir2}

	// SubDir1 should be within roots (matches root 1)
	if !safety.IsWithinAnyRoot(subDir1, roots) {
		t.Error("expected subdir1 to be within roots")
	}
	// SubDir2 should be within roots (matches root 2)
	if !safety.IsWithinAnyRoot(subDir2, roots) {
		t.Error("expected subdir2 to be within roots")
	}
	// Outside both roots should fail
	if safety.IsWithinAnyRoot("/tmp/elsewhere", roots) {
		t.Error("expected outside path to not be within any root")
	}
}

func TestResolveAndContainMulti_RelativePath(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "subdir")
	os.MkdirAll(subDir, 0755)

	resolved, err := safety.ResolveAndContainMulti([]string{tmpDir}, "subdir")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved != subDir {
		t.Errorf("expected %s, got %s", subDir, resolved)
	}
}

func TestResolveAndContainMulti_AbsolutePathWithinRoot(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "subdir")
	os.MkdirAll(subDir, 0755)

	resolved, err := safety.ResolveAndContainMulti([]string{tmpDir}, subDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved != subDir {
		t.Errorf("expected %s, got %s", subDir, resolved)
	}
}

func TestResolveAndContainMulti_AbsolutePathInSecondRoot(t *testing.T) {
	tmpDir1 := t.TempDir()
	tmpDir2 := t.TempDir()
	subDir2 := filepath.Join(tmpDir2, "project")
	os.MkdirAll(subDir2, 0755)

	// Absolute path in second root should work
	resolved, err := safety.ResolveAndContainMulti([]string{tmpDir1, tmpDir2}, subDir2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved != subDir2 {
		t.Errorf("expected %s, got %s", subDir2, resolved)
	}
}

func TestResolveAndContainMulti_AbsolutePathOutsideAllRoots(t *testing.T) {
	tmpDir := t.TempDir()

	_, err := safety.ResolveAndContainMulti([]string{tmpDir}, "/tmp/outside/path")
	if err == nil {
		t.Error("expected error for path outside all roots")
	}
}

func TestResolveAndContainMulti_TraversalBlocked(t *testing.T) {
	tmpDir := t.TempDir()

	_, err := safety.ResolveAndContainMulti([]string{tmpDir}, "../../../etc/passwd")
	if err == nil {
		t.Error("expected error for path traversal")
	}
}

package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/emaharmony/prizm/internal/safety"
)

// ToolPaths holds the path configuration for filesystem tools.
// It supports a primary workspace root plus additional allowed paths.
// All path containment checks should use ResolveToolPath() which
// handles multi-root resolution and symlink escape prevention.
type ToolPaths struct {
	// WorkspaceRoot is the primary root directory for the agent.
	WorkspaceRoot string
	// AllowedPaths is a list of additional directory roots the agent can access.
	AllowedPaths []string
}

// AllRoots returns all allowed root directories (workspace root first, then allowed paths).
func (tp ToolPaths) AllRoots() []string {
	roots := []string{tp.WorkspaceRoot}
	roots = append(roots, tp.AllowedPaths...)
	return roots
}

// ResolveToolPath resolves a path (relative or absolute) against the tool's
// allowed roots and verifies it stays within one of them.
//
// For relative paths: resolved against workspace root (primary).
// For absolute paths: checked against all allowed roots.
//
// Returns the resolved absolute path and an error if the path escapes all roots.
func ResolveToolPath(tp ToolPaths, inputPath string) (string, error) {
	// Block path traversal
	if ContainsPathTraversal(inputPath) {
		return "", fmt.Errorf("path traversal blocked: %q", inputPath)
	}

	// Resolve all roots to absolute
	allRoots := tp.AllRoots()
	absRoots := make([]string, len(allRoots))
	for i, r := range allRoots {
		absR, err := filepath.Abs(r)
		if err != nil {
			return "", fmt.Errorf("invalid root path: %w", err)
		}
		absRoots[i] = absR
	}

	var absPath string
	if safety.IsAbsolutePath(inputPath) {
		absPath = filepath.Clean(inputPath)
	} else {
		absPath = filepath.Clean(filepath.Join(absRoots[0], inputPath))
	}

	// Resolve symlinks for defense-in-depth
	resolvedPath, err := filepath.EvalSymlinks(absPath)
	if err == nil {
		absPath = filepath.Clean(resolvedPath)
	}
	// If path doesn't exist yet, EvalSymlinks fails — that's fine,
	// we still check the cleaned path against roots.

	// Check containment against all roots
	if !safety.IsWithinAnyRoot(absPath, absRoots) {
		// Build a helpful error message listing available directories
		var available []string
		for _, root := range absRoots {
			entries, err := os.ReadDir(root)
			if err == nil {
				for _, e := range entries {
					if e.IsDir() {
						available = append(available, filepath.Join(root, e.Name()))
					}
				}
			}
		}
		suggestion := ""
		if len(available) > 0 {
			if len(available) > 5 {
				suggestion = fmt.Sprintf(" Available directories include: %s (and %d more)",
					strings.Join(available[:5], ", "), len(available)-5)
			} else {
				suggestion = fmt.Sprintf(" Available directories: %s", strings.Join(available, ", "))
			}
		}
		return "", fmt.Errorf("path %q is not within any allowed root.%s", inputPath, suggestion)
	}

	return absPath, nil
}

// ContainsPathTraversal checks for path traversal attempts.
func ContainsPathTraversal(p string) bool {
	cleaned := filepath.Clean(p)
	return strings.Contains(cleaned, "..")
}

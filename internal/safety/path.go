// Package safety provides shared safety utilities for Prizm.
//
// The primary purpose is path containment: ensuring that file operations
// stay within the workspace root. This logic was previously duplicated
// across tool/policy.go, tool/builtins.go, dashboard/server.go, and
// mutation/executor.go. V12 consolidates it here.
//
// Why a shared package? Path containment is a security boundary. Having
// one implementation means one place to audit, one place to fix bugs, and
// no risk of slight variations in how different packages check paths.
package safety

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// IsWithinRoot checks that absPath is within absRoot.
//
// Both paths must be absolute. The check accounts for symlinks and path
// traversal attempts. Returns true if absPath is contained within absRoot.
//
// How it works:
//  1. Resolve symlinks in both paths (prevents symlink escape attacks)
//  2. If the target path doesn't exist yet (e.g., a file being written),
//     walk up the directory tree until we find an existing parent,
//     then resolve that parent and append the remaining path components
//  3. Compute the relative path from root to target
//  4. If the relative path starts with "..", the target is outside root
//
// This is the single canonical implementation for Prizm's path safety.
// All packages that need path containment should use this function
// instead of implementing their own.
func IsWithinRoot(absPath, absRoot string) bool {
	// Resolve symlinks in root
	resolvedRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		resolvedRoot = absRoot
	}
	resolvedRoot = filepath.Clean(resolvedRoot)

	// Try to resolve the target path directly
	resolvedPath, err := filepath.EvalSymlinks(absPath)
	if err == nil {
		// Path exists — use the resolved version
		resolvedPath = filepath.Clean(resolvedPath)
	} else {
		// Path doesn't exist yet. Walk up until we find an existing parent.
		resolvedPath = resolveNonExistentPath(absPath)
		if resolvedPath == "" {
			return false
		}
	}

	rel, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// IsAbsolutePath treats platform-native and POSIX-style absolute paths as
// absolute. On Windows, filepath.IsAbs("/etc/passwd") is false, but model/tool
// inputs often use POSIX absolute paths regardless of host OS.
func IsAbsolutePath(p string) bool {
	return filepath.IsAbs(p) || path.IsAbs(strings.ReplaceAll(p, "\\", "/"))
}

// resolveNonExistentPath walks up from absPath until it finds an existing
// directory, resolves symlinks there, then appends the remaining components.
func resolveNonExistentPath(absPath string) string {
	absPath = filepath.Clean(absPath)

	// Walk up the directory tree until we find something that exists
	dir := filepath.Dir(absPath)
	base := filepath.Base(absPath)
	components := []string{base}

	for {
		resolved, err := filepath.EvalSymlinks(dir)
		if err == nil {
			// Found an existing parent — reconstruct the path
			for i := len(components) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, components[i])
			}
			return filepath.Clean(resolved)
		}

		// Parent doesn't exist either — keep going up
		components = append([]string{filepath.Base(dir)}, components...)
		parent := filepath.Dir(dir)

		if parent == dir {
			// Reached filesystem root without finding anything
			return ""
		}
		dir = parent
	}
}

// ResolveAndContain resolves a relative path against a root directory
// and verifies the result stays within root.
//
// This is the convenience function that combines path resolution with
// the containment check. Returns the absolute safe path on success,
// or an error if path traversal is detected.
//
// Use this instead of filepath.Join + manual containment checks.
func ResolveAndContain(root, relPath string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("safety: resolve root: %w", err)
	}

	// Block absolute paths — they must be relative to root
	if IsAbsolutePath(relPath) {
		return "", fmt.Errorf("safety: absolute path not allowed: %q", relPath)
	}

	absPath := filepath.Join(absRoot, filepath.Clean(relPath))

	if !IsWithinRoot(absPath, absRoot) {
		return "", fmt.Errorf("safety: path traversal blocked: %q escapes %q", relPath, root)
	}

	return absPath, nil
}

// IsWithinAnyRoot checks whether absPath is within any of the provided root directories.
// This is the multi-root version of IsWithinRoot, used when an agent has access to
// multiple directory roots (workspace root + allowed_paths).
//
// Returns true if absPath is contained within at least one root.
// All paths must be absolute.
func IsWithinAnyRoot(absPath string, absRoots []string) bool {
	for _, root := range absRoots {
		if IsWithinRoot(absPath, root) {
			return true
		}
	}
	return false
}

// ResolveAndContainMulti resolves a path against multiple allowed roots.
// If relPath is relative, it's resolved against the first root (primary/workspace).
// If relPath is absolute, it's checked against all roots.
// Returns the absolute safe path on success, or an error if path traversal is detected.
func ResolveAndContainMulti(roots []string, relPath string) (string, error) {
	if len(roots) == 0 {
		return "", fmt.Errorf("safety: no roots provided")
	}

	// Resolve all roots to absolute paths
	absRoots := make([]string, len(roots))
	for i, r := range roots {
		absR, err := filepath.Abs(r)
		if err != nil {
			return "", fmt.Errorf("safety: resolve root %q: %w", r, err)
		}
		absRoots[i] = absR
	}

	var absPath string
	if IsAbsolutePath(relPath) {
		// Absolute path — check against all roots directly
		absPath = filepath.Clean(relPath)
	} else {
		// Relative path — resolve against the primary (workspace) root
		absPath = filepath.Join(absRoots[0], filepath.Clean(relPath))
	}

	if !IsWithinAnyRoot(absPath, absRoots) {
		return "", fmt.Errorf("safety: path %q is not within any allowed root", relPath)
	}

	return absPath, nil
}

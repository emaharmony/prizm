package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// FuzzyResolvePath attempts to resolve a path that may be partial, misspelled,
// or use non-standard conventions (like ~ or /home/user instead of /Users/user).
//
// Resolution strategy:
//  1. Exact resolution via ResolveToolPath (handles relative, absolute, containment)
//  2. Tilde expansion: ~/... → home directory
//  3. macOS path correction: /home/user → /Users/user (no-op on Linux)
//  4. Recursive fuzzy match: search allowed roots for directories matching
//     the last component, including nested subdirectories (up to depth 4)
//     e.g., "myproject" → /Users/example/projects/repos/myproject
//
// This allows agents to say "summarize bassbook" or "read ~/projects/repos/bassbook"
// and have the tools find the right path even if the model doesn't know the exact
// absolute path or uses Linux-style conventions on macOS.
func FuzzyResolvePath(tp ToolPaths, inputPath string) (string, error) {
	// Step 1: Try exact resolution first
	resolved, err := ResolveToolPath(tp, inputPath)
	if err == nil {
		return resolved, nil
	}

	// Step 2: Tilde expansion
	if strings.HasPrefix(inputPath, "~/") {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return "", fmt.Errorf("cannot expand ~: %w", homeErr)
		}
		expanded := filepath.Join(home, inputPath[2:])
		if resolved, err := ResolveToolPath(tp, expanded); err == nil {
			return resolved, nil
		}
		// Continue with fuzzy matching on the expanded path
		inputPath = expanded
	}

	// Step 3: macOS path correction
	// On macOS, /home/user → /Users/user is a common model hallucination.
	// On Linux, /home/user is valid, so skip this step entirely.
	if runtime.GOOS == "darwin" && strings.HasPrefix(inputPath, "/home/") {
		parts := strings.SplitN(inputPath[6:], "/", 2)
		if len(parts) > 0 {
			corrected := filepath.Join("/Users", parts[0])
			if len(parts) > 1 {
				corrected = filepath.Join(corrected, parts[1])
			}
			if resolved, err := ResolveToolPath(tp, corrected); err == nil {
				return resolved, nil
			}
			inputPath = corrected
		}
	}

	// Step 4: Strip remaining ~ prefix for fuzzy matching.
	// E.g., ~/projects/repos/bassbook → fuzzy match "bassbook".
	if strings.HasPrefix(inputPath, "~") {
		stripped := strings.TrimPrefix(strings.TrimPrefix(inputPath, "~"), "/")
		if stripped != "" {
			inputPath = stripped
		}
	}

	// Step 5: Recursive fuzzy match — search allowed roots for directories
	// matching the last path component, including nested subdirectories.
	targetName := filepath.Base(inputPath)
	if targetName == "" || targetName == "." || targetName == "/" {
		return "", fmt.Errorf("cannot fuzzy-match empty path: %q", inputPath)
	}

	match, err := fuzzyMatchInRoots(tp, targetName)
	if err != nil {
		return "", fmt.Errorf("path %q not found (tried exact, ~, /home→/Users, and fuzzy match): %w",
			inputPath, err)
	}

	return match, nil
}

// fuzzyMatchInRoots recursively searches all allowed root directories for
// subdirectories matching the target name (case-insensitive).
//
// Search depth is limited to 4 levels to avoid walking huge directory trees.
// Directories starting with "." (hidden) and common system directories are skipped.
//
// Priority:
//  1. Exact match (case-sensitive) — returns immediately
//  2. Case-insensitive match — best so far, but keep searching
//  3. Prefix match — last resort, prefer exact/case-insensitive
//
// If both "bassbook" and "bassbook-api" exist, and the target is "bassbook",
// the exact/case-insensitive match on "bassbook" wins over the prefix match
// on "bassbook-api".
func fuzzyMatchInRoots(tp ToolPaths, targetName string) (string, error) {
	roots := tp.AllRoots()
	lowerTarget := strings.ToLower(targetName)

	var caseInsensitiveMatch string
	var prefixMatch string

	for _, root := range roots {
		match := walkForMatch(root, targetName, lowerTarget, tp, 0, 4)
		if match.exact != "" {
			return match.exact, nil
		}
		if match.caseInsensitive != "" && caseInsensitiveMatch == "" {
			caseInsensitiveMatch = match.caseInsensitive
		}
		if match.prefix != "" && prefixMatch == "" {
			prefixMatch = match.prefix
		}
	}

	if caseInsensitiveMatch != "" {
		return caseInsensitiveMatch, nil
	}
	if prefixMatch != "" {
		return prefixMatch, nil
	}

	return "", fmt.Errorf("no directory named %q found in any allowed root", targetName)
}

type fuzzyMatchResult struct {
	exact           string
	caseInsensitive string
	prefix          string
}

// fuzzySkipDirs are directories to skip during recursive walk (system/cache dirs).
var fuzzySkipDirs = map[string]bool{
	".git":         true,
	".cache":       true,
	".npm":         true,
	".Trash":       true,
	"node_modules": true,
	".venv":        true,
	"__pycache__":  true,
	".DS_Store":    true,
	"Library":      true,
	"Applications": true,
	"System":       true,
}

// walkForMatch recursively searches a directory tree (up to maxDepth levels)
// for a subdirectory matching targetName.
func walkForMatch(dir, targetName, lowerTarget string, tp ToolPaths, depth, maxDepth int) fuzzyMatchResult {
	var result fuzzyMatchResult

	if depth >= maxDepth {
		return result
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return result
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()

		// Skip hidden and system directories
		if strings.HasPrefix(name, ".") || fuzzySkipDirs[name] {
			continue
		}

		candidate := filepath.Join(dir, name)

		// Exact case-sensitive match
		if name == targetName {
			if _, err := ResolveToolPath(tp, candidate); err == nil {
				result.exact = candidate
				return result // immediate return on exact match
			}
		}

		// Case-insensitive match
		if strings.ToLower(name) == lowerTarget && result.caseInsensitive == "" {
			if _, err := ResolveToolPath(tp, candidate); err == nil {
				result.caseInsensitive = candidate
			}
		}

		// Prefix match
		if strings.HasPrefix(strings.ToLower(name), lowerTarget) && result.prefix == "" {
			if _, err := ResolveToolPath(tp, candidate); err == nil {
				result.prefix = candidate
			}
		}

		// Recurse into subdirectories
		sub := walkForMatch(candidate, targetName, lowerTarget, tp, depth+1, maxDepth)
		if sub.exact != "" {
			return sub // propagate exact match immediately
		}
		if sub.caseInsensitive != "" && result.caseInsensitive == "" {
			result.caseInsensitive = sub.caseInsensitive
		}
		if sub.prefix != "" && result.prefix == "" {
			result.prefix = sub.prefix
		}

		// Early exit if we have a case-insensitive match — no need to find more
		if result.caseInsensitive != "" && result.prefix != "" {
			return result
		}
	}

	return result
}

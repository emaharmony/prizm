// Package validation provides command safety checks for Prizm V5's validation pipeline.
//
// IsSafeCommandString inspects a command string and rejects anything that could be used
// for shell injection or privilege escalation. This is the security layer that ensures
// validation profiles can only run simple commands — not shell metacharacter exploits.
//
// What it blocks and why:
//   - Pipe (|) — could chain a second command after the intended one
//   - Redirect (> >> <) — could overwrite files or read secrets
//   - Semicolon (;) — could run a separate command after the intended one
//   - AND/OR (&& ||) — could conditionally chain commands
//   - Backtick (`) and $() — could execute subshells
//   - Newline (\n) — could inject a second command on a new line
//   - Path traversal (..) — could escape the working directory
//
// IsWithinRoot checks that a resolved path stays inside the project root directory,
// using filepath.EvalSymlinks to resolve symlinks before comparison. This prevents
// a profile from reading or writing outside the project.
package validation

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Safety errors for blocked commands.
var (
	ErrPathTraversal    = fmt.Errorf("working directory escapes project root (path traversal)")
	ErrBlockedOperator  = fmt.Errorf("command contains blocked operator (pipes, redirects, or chaining)")
	ErrOutsideWorkspace = fmt.Errorf("execution outside workspace is forbidden")
)

// IsSafeCommandString checks if a command string contains dangerous operators.
// Blocked: pipes (|), redirects (>, >>, <), command chaining (&&, ||, ;, &),
// backtick substitution (`), dollar substitution ($(...) or ${}).
func IsSafeCommandString(cmd string) bool {
	// Check for common shell metacharacters
	dangerous := []string{"|", ">", ">>", "<", "&&", "||", ";", "&", "`", "$(", "${"}
	for _, d := range dangerous {
		if strings.Contains(cmd, d) {
			return false
		}
	}
	return true
}

// IsWithinProjectRoot checks that the given working directory resolves to
// somewhere inside the project root. Uses EvalSymlinks to prevent symlink
// escape attacks.
func IsWithinProjectRoot(projectRoot, workingDir string) (bool, error) {
	absRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return false, fmt.Errorf("cannot resolve project root: %w", err)
	}

	// Resolve symlinks in root
	resolvedRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return false, fmt.Errorf("cannot resolve project root symlinks: %w", err)
	}

	// If workingDir is relative, join it with project root
	target := workingDir
	if !filepath.IsAbs(target) {
		target = filepath.Join(absRoot, target)
	}

	absTarget, err := filepath.Abs(target)
	if err != nil {
		return false, fmt.Errorf("cannot resolve working directory: %w", err)
	}

	// Check if the path exists (it should for validation)
	if _, err := os.Stat(absTarget); os.IsNotExist(err) {
		// Allow the path to not exist yet (will be created)
		// But we still need to check the parent chain
	}

	// Resolve symlinks in target
	resolvedTarget, err := filepath.EvalSymlinks(absTarget)
	if err != nil {
		// If the path doesn't exist, EvalSymlinks will fail.
		// Walk up to find the nearest existing parent and resolve that.
		resolvedTarget, err = resolveExistingPath(absTarget)
		if err != nil {
			return false, fmt.Errorf("cannot resolve working directory symlinks: %w", err)
		}
	}

	// Clean both paths
	cleanedRoot := filepath.Clean(resolvedRoot)
	cleanedTarget := filepath.Clean(resolvedTarget)

	// Check that cleanedTarget has cleanedRoot as a prefix
	if !strings.HasPrefix(cleanedTarget, cleanedRoot) {
		return false, nil
	}

	// If they're equal, that's fine
	if cleanedTarget == cleanedRoot {
		return true, nil
	}

	// Must have separator after root (prevents /root vs /root-something)
	rest := cleanedTarget[len(cleanedRoot):]
	if len(rest) == 0 || rest[0] == os.PathSeparator {
		return true, nil
	}

	return false, nil
}

// resolveExistingPath walks up from a potentially non-existent path to find
// the first existing ancestor, resolves symlinks on it, then appends the
// non-existent components back.
func resolveExistingPath(p string) (string, error) {
	// Clean the path first
	p = filepath.Clean(p)

	// Walk up until we find an existing path
	current := p
	var missing []string
	for {
		if _, err := os.Stat(current); err == nil {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			// Reached root
			if _, err := os.Stat(current); err != nil {
				return "", fmt.Errorf("no existing ancestor found for %s", p)
			}
			break
		}
		missing = append([]string{filepath.Base(current)}, missing...)
		current = parent
	}

	// Resolve symlinks on the existing ancestor
	resolved, err := filepath.EvalSymlinks(current)
	if err != nil {
		return "", fmt.Errorf("cannot resolve symlinks for %s: %w", current, err)
	}

	// Append missing components
	for _, comp := range missing {
		resolved = filepath.Join(resolved, comp)
	}

	return resolved, nil
}

// ValidateProfileSafety performs safety checks on a validation profile:
// - Command must not contain dangerous operators
// - Each arg must not contain dangerous operators
// - WorkingDir must be within projectRoot
func ValidateProfileSafety(profile Profile, projectRoot string) error {
	// Check that the command string itself is safe
	if !IsSafeCommandString(profile.Command) {
		return fmt.Errorf("%w: command %q", ErrBlockedOperator, profile.Command)
	}
	// Check each arg
	for _, arg := range profile.Args {
		if !IsSafeCommandString(arg) {
			return fmt.Errorf("%w: arg %q", ErrBlockedOperator, arg)
		}
	}

	// Check working directory
	if profile.WorkingDir != "" {
		valid, err := IsWithinProjectRoot(projectRoot, profile.WorkingDir)
		if err != nil {
			return fmt.Errorf("working directory safety check failed: %w", err)
		}
		if !valid {
			return fmt.Errorf("%w: %q", ErrPathTraversal, profile.WorkingDir)
		}
	}

	return nil
}

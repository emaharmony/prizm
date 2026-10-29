package subagent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/emaharmony/prizm/internal/gitx"
	"github.com/emaharmony/prizm/internal/safety"
)

// MaxSingleNewFileBytes bounds the provider-owned content accepted by the
// host-side intent adapter. The resulting patch is still validated by the
// canonical apply_patch_proposal tool before approval is requested.
const MaxSingleNewFileBytes = 64 * 1024

// NormalizeApplyPatchInput preserves the canonical patch envelope and maps a
// narrowly scoped provider intent (path + content) to a host-created patch.
// The provider cannot choose the base SHA, overwrite an existing path, or add
// authority fields. This adapter is deliberately before policy/tool execution;
// the normal proposal validator remains the only authority for the patch.
func NormalizeApplyPatchInput(ctx context.Context, workspace string, input map[string]any) (map[string]any, error) {
	if hasKey(input, "patch") || hasKey(input, "base_sha") {
		if len(input) != 2 || !hasKey(input, "patch") || !hasKey(input, "base_sha") {
			return nil, fmt.Errorf("apply_patch_proposal requires exactly patch and base_sha, or path and content")
		}
		patch, patchOK := input["patch"].(string)
		base, baseOK := input["base_sha"].(string)
		if !patchOK || strings.TrimSpace(patch) == "" || !baseOK || strings.TrimSpace(base) == "" {
			return nil, fmt.Errorf("canonical patch intent requires non-empty string patch and base_sha")
		}
		return input, nil
	}
	if len(input) != 2 || !hasKey(input, "path") || !hasKey(input, "content") {
		return nil, fmt.Errorf("apply_patch_proposal intent requires exactly path and content")
	}
	pathValue, ok := input["path"].(string)
	if !ok || strings.TrimSpace(pathValue) == "" {
		return nil, fmt.Errorf("new-file intent path must be a non-empty relative string")
	}
	content, ok := input["content"].(string)
	if !ok || content == "" {
		return nil, fmt.Errorf("new-file intent content must be a non-empty string")
	}
	if len([]byte(content)) > MaxSingleNewFileBytes {
		return nil, fmt.Errorf("new-file intent content exceeds %d bytes", MaxSingleNewFileBytes)
	}
	for _, r := range content {
		if r == 0 || (unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t') {
			return nil, fmt.Errorf("new-file intent content contains unsupported control characters")
		}
	}
	clean, err := safeNewFilePath(workspace, pathValue)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(filepath.Join(workspace, filepath.FromSlash(clean))); err == nil {
		return nil, fmt.Errorf("new-file intent cannot overwrite existing path %q", clean)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("new-file intent path check failed: %w", err)
	}
	base, err := gitx.CurrentSHA(ctx, workspace)
	if err != nil {
		return nil, fmt.Errorf("resolve trusted patch base: %w", err)
	}
	return map[string]any{"patch": newFilePatch(clean, content), "base_sha": base}, nil
}

func hasKey(input map[string]any, key string) bool { _, ok := input[key]; return ok }

func safeNewFilePath(workspace, raw string) (string, error) {
	for _, r := range raw {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("new-file intent path contains control characters")
		}
	}
	if filepath.IsAbs(raw) || strings.ContainsAny(raw, `\\:`) {
		return "", fmt.Errorf("new-file intent path must be workspace-relative")
	}
	clean := filepath.ToSlash(filepath.Clean(raw))
	if clean == "." || clean == "" || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("new-file intent path escapes workspace")
	}
	first := strings.Split(clean, "/")[0]
	if strings.EqualFold(first, ".git") || strings.EqualFold(first, ".gitmodules") || strings.EqualFold(first, ".prizm") {
		return "", fmt.Errorf("new-file intent path targets protected repository metadata")
	}
	absolute := filepath.Join(workspace, filepath.FromSlash(clean))
	if _, err := safety.ResolveAndContainMulti([]string{workspace}, absolute); err != nil {
		return "", fmt.Errorf("new-file intent path is outside workspace: %w", err)
	}
	for parent := filepath.Dir(absolute); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil {
			return "", fmt.Errorf("new-file intent parent check failed: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("new-file intent parent cannot be a symlink")
		}
		if filepath.Clean(parent) == filepath.Clean(workspace) {
			break
		}
		next := filepath.Dir(parent)
		if next == parent {
			return "", fmt.Errorf("new-file intent parent escaped workspace")
		}
	}
	return clean, nil
}

func newFilePatch(path, content string) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\nnew file mode 100644\n--- /dev/null\n+++ b/%s\n@@ -0,0 +%d @@\n", path, path, path, len(lines))
	for _, line := range lines {
		b.WriteByte('+')
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

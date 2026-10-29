package subagent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeApplyPatchInputMapsSingleNewFileIntent(t *testing.T) {
	root := newIntentGitRepo(t)
	base, err := NormalizeApplyPatchInput(context.Background(), root, map[string]any{
		"path": "one.txt", "content": "hello\nworld\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	patch, _ := base["patch"].(string)
	if base["base_sha"] == "" || !strings.Contains(patch, "new file mode 100644") || !strings.Contains(patch, "+hello\n+world\n") {
		t.Fatalf("mapped intent missing canonical patch/base: %#v", base)
	}
	if _, err := os.Stat(filepath.Join(root, "one.txt")); !os.IsNotExist(err) {
		t.Fatalf("mapper must not write source, stat err=%v", err)
	}
}

func TestNormalizeApplyPatchInputPreservesCanonicalPatch(t *testing.T) {
	in := map[string]any{"patch": "diff --git a/a b/a\n", "base_sha": "abc"}
	out, err := NormalizeApplyPatchInput(context.Background(), t.TempDir(), in)
	if err != nil || out["patch"] != in["patch"] || out["base_sha"] != in["base_sha"] {
		t.Fatalf("canonical patch changed: %#v err=%v", out, err)
	}
}

func TestNormalizeApplyPatchInputRejectsUnsafeIntent(t *testing.T) {
	root := newIntentGitRepo(t)
	for name, input := range map[string]map[string]any{
		"traversal": {"path": "../escape.txt", "content": "x"},
		"absolute":  {"path": filepath.Join(root, "escape.txt"), "content": "x"},
		"overwrite": {"path": "README.md", "content": "x"},
		"directory": {"path": "internal", "content": "x"},
		"metadata":  {"path": ".prizm/state.json", "content": "x"},
		"control":   {"path": "bad\nname.txt", "content": "x"},
		"extra":     {"path": "new.txt", "content": "x", "base_sha": "authority"},
		"oversize":  {"path": "new.txt", "content": strings.Repeat("x", MaxSingleNewFileBytes+1)},
		"bad_patch": {"patch": 42, "base_sha": "abc"},
		"bad_base":  {"patch": "diff", "base_sha": 42},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NormalizeApplyPatchInput(context.Background(), root, input); err == nil {
				t.Fatalf("unsafe intent accepted: %#v", input)
			}
		})
	}
}

func TestNormalizeApplyPatchInputRejectsSymlinkParent(t *testing.T) {
	root := newIntentGitRepo(t)
	target := filepath.Join(root, "internal")
	link := filepath.Join(root, ".tmp-r1-mapper-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(link) })
	if _, err := NormalizeApplyPatchInput(context.Background(), root, map[string]any{"path": ".tmp-r1-mapper-link/new.txt", "content": "x"}); err == nil {
		t.Fatal("symlink parent accepted")
	}
}

func newIntentGitRepo(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("resolve test repository: %v", err)
	}
	return strings.TrimSpace(string(out))
}

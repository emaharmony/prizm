package subagent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/gitx"
)

func TestMissingPatchContextCannotBeCoveredThroughRemovedHeaderLikeLine(t *testing.T) {
	root := t.TempDir()
	readme := strings.Join([]string{"-- a/covered.txt", "line 2", "line 3", "line 4", "line 5", "line 6", "line 7", "line 8", "line 9", "old"}, "\n") + "\n"
	covered := strings.Join([]string{"line 1", "line 2", "line 3", "line 4", "line 5", "line 6", "line 7", "line 8", "line 9", "covered"}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte(readme), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "covered.txt"), []byte(covered), 0o600); err != nil {
		t.Fatal(err)
	}
	coverage := newSourceCoverage(root)
	for _, page := range []observedSourcePage{
		{Path: "README.md", SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(readme))), StartLine: 1, EndLine: 1},
		{Path: "covered.txt", SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(covered))), StartLine: 10, EndLine: 10},
	} {
		encoded, err := json.Marshal(page)
		if err != nil {
			t.Fatal(err)
		}
		if err := coverage.recordRead(string(encoded)); err != nil {
			t.Fatal(err)
		}
	}

	patch := "diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n" +
		"@@ -1 +1 @@\n--- a/covered.txt\n+replacement\n" +
		"@@ -10 +10 @@\n-old\n+new\n"
	missing, err := coverage.missingPatchContext(patch)
	if err != nil {
		t.Fatal(err)
	}
	want := []gitx.PatchSourceRange{{Path: "README.md", StartLine: 10, LineCount: 1}}
	if len(missing) != len(want) {
		t.Fatalf("missing=%+v, want %+v", missing, want)
	}
	for i := range want {
		if missing[i] != want[i] {
			t.Fatalf("missing=%+v, want %+v", missing, want)
		}
	}
}

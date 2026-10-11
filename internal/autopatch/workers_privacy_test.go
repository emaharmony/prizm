package autopatch

import (
	"os"
	"strings"
	"testing"
)

func TestLocalAgentArtifactContractExcludesReasoningPreamble(t *testing.T) {
	raw := "<think>PRIVATE_CANARY</think>\n" +
		"diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1 @@\n-old\n+new"
	diff, err := extractDiff(raw)
	if err != nil {
		t.Fatalf("extractDiff: %v", err)
	}
	if strings.Contains(diff, "PRIVATE_CANARY") {
		t.Fatalf("patch artifact included reasoning preamble: %q", diff)
	}

	dir := t.TempDir()
	rawPath, patchPath, err := writeLocalAgentArtifacts(dir, diff)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{rawPath, patchPath} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "PRIVATE_CANARY") {
			t.Fatalf("private reasoning persisted in %s", path)
		}
	}
}

func TestExtractDiffPreservesLiteralThinkTagsInsidePatch(t *testing.T) {
	raw := "reasoning omitted\n" +
		"diff --git a/a.html b/a.html\n--- a/a.html\n+++ b/a.html\n@@ -1 +1 @@\n-old\n+<think>literal source</think>"
	diff, err := extractDiff(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "+<think>literal source</think>") {
		t.Fatalf("patch content was rewritten: %q", diff)
	}
}

func TestExtractDiffSkipsPrivateFencedDiffBeforePublicDiff(t *testing.T) {
	raw := "<think>private candidate:\n```diff\ndiff --git a/private.txt b/private.txt\n--- a/private.txt\n+++ b/private.txt\n@@ -1 +1 @@\n-secret\n+secret\n```\n</think>\n" +
		"```diff\ndiff --git a/public.txt b/public.txt\n--- a/public.txt\n+++ b/public.txt\n@@ -1 +1 @@\n-old\n+new\n```"
	diff, err := extractDiff(raw)
	if err != nil {
		t.Fatalf("extractDiff: %v", err)
	}
	if !strings.Contains(diff, "diff --git a/public.txt b/public.txt") || strings.Contains(diff, "private.txt") {
		t.Fatalf("selected wrong patch: %q", diff)
	}
}

func TestExtractDiffRejectsPrivateOnlyDiff(t *testing.T) {
	raw := "<think>```diff\ndiff --git a/private.txt b/private.txt\n--- a/private.txt\n+++ b/private.txt\n```</think>"
	if diff, err := extractDiff(raw); err == nil {
		t.Fatalf("extractDiff accepted private-only patch: %q", diff)
	}
}

func TestExtractDiffRejectsUnterminatedPrivateBlock(t *testing.T) {
	raw := "<think>```diff\ndiff --git a/private.txt b/private.txt\n```"
	if diff, err := extractDiff(raw); err == nil {
		t.Fatalf("extractDiff accepted unterminated private block: %q", diff)
	}
}

func TestExtractDiffRejectsMalformedThinkTagsBeforePatch(t *testing.T) {
	public := "```diff\ndiff --git a/public.txt b/public.txt\n--- a/public.txt\n+++ b/public.txt\n@@ -1 +1 @@\n-old\n+new\n```"
	cases := map[string]string{
		"malformed opener":           "<think mode=private>\n```diff\ndiff --git a/private.txt b/private.txt\n```\n" + public,
		"malformed closer in block":  "<think>\nprivate\n</think mode=private>\n" + public,
		"unmatched malformed closer": "</think mode=private>\n" + public,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if diff, err := extractDiff(raw); err == nil {
				t.Fatalf("extractDiff accepted malformed think tag and selected %q", diff)
			}
		})
	}
}

func TestExtractDiffPreservesMalformedThinkTagTextInsidePatch(t *testing.T) {
	raw := "diff --git a/a.html b/a.html\n--- a/a.html\n+++ b/a.html\n@@ -1 +1 @@\n-old\n+<think mode=private></think mode=private>"
	diff, err := extractDiff(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "+<think mode=private></think mode=private>") {
		t.Fatalf("patch content was rewritten: %q", diff)
	}
}

func TestExtractDiffRejectsNestedThinkBeforePatch(t *testing.T) {
	cases := map[string]string{
		"nested opener":           "<think><think>x</think>\n```diff\ndiff --git a/private.txt b/private.txt\n```</think>",
		"malformed nested opener": "<think><think mode=private>x</think>\n```diff\ndiff --git a/private.txt b/private.txt\n```</think>",
		"malformed nested closer": "<think><think>x</think mode=private>\n```diff\ndiff --git a/private.txt b/private.txt\n```</think>",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if diff, err := extractDiff(raw); err == nil {
				t.Fatalf("extractDiff accepted nested private patch: %q", diff)
			}
		})
	}
}

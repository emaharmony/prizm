package stage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPersistenceStageWritesOnlyPublicResponseAndPreservesRawContext(t *testing.T) {
	const raw = "<think>PRIVATE_CANARY</think>Apply this patch:\n```diff\n+literal <think>patch text</think>\n```"
	const want = "Apply this patch:\n```diff\n+literal <think>patch text</think>\n```"

	rc := executePersistencePrivacyCase(t, "public-response", raw)
	if rc.LLMResponse != raw {
		t.Fatalf("persistence changed internal raw response: %q", rc.LLMResponse)
	}
	got, err := os.ReadFile(filepath.Join(rc.RunDir, "output.md"))
	if err != nil {
		t.Fatalf("read output.md: %v", err)
	}
	if string(got) != want || strings.Contains(string(got), "PRIVATE_CANARY") {
		t.Fatalf("output.md = %q, want public fenced patch %q", got, want)
	}
}

func TestPersistenceStageOmitsIncompleteReasoningAndStructuredToolArtifact(t *testing.T) {
	for name, raw := range map[string]string{
		"empty_response":       "",
		"incomplete_reasoning": "Public preface. <think>PRIVATE_CANARY",
		"tool_request":         `{"type":"tool_request","tool":"write_file","input":{"content":"<think>literal code</think>"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			rc := executePersistencePrivacyCase(t, name, raw)
			if rc.LLMResponse != raw {
				t.Fatalf("persistence changed internal raw response: %q", rc.LLMResponse)
			}
			outputPath := filepath.Join(rc.RunDir, "output.md")
			got, err := os.ReadFile(outputPath)
			if err == nil {
				if strings.Contains(string(got), "PRIVATE_CANARY") || strings.Contains(string(got), "<think>") {
					t.Fatalf("private content was written to output.md: %q", got)
				}
				t.Fatalf("expected no public output artifact, found %q", got)
			}
			if !os.IsNotExist(err) {
				t.Fatalf("stat output.md: %v", err)
			}
		})
	}
}

func TestWritePublicOutputFailureRemovesPriorArtifact(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "output.md")
	if err := os.WriteFile(outputPath, []byte("STALE_PRIVATE_CANARY"), 0600); err != nil {
		t.Fatalf("seed prior output artifact: %v", err)
	}
	createTemp := func(string, string) (*os.File, error) {
		return nil, errors.New("injected temp creation failure")
	}
	if err := writePublicOutputUsing(outputPath, "safe public response", createTemp); err == nil {
		t.Fatal("expected injected public artifact write failure")
	}
	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Fatalf("stale output artifact remains after failure: stat err=%v", err)
	}
}

func executePersistencePrivacyCase(t *testing.T, name, raw string) *RunContext {
	t.Helper()
	runDir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(runDir, 0755); err != nil {
		t.Fatalf("create run directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "output.md"), []byte("STALE_PRIVATE_CANARY"), 0600); err != nil {
		t.Fatalf("seed prior output artifact: %v", err)
	}
	rc := &RunContext{RunID: name, Task: "privacy test", LLMResponse: raw, RunDir: runDir}
	if _, result, err := (&PersistenceStage{}).Execute(context.Background(), rc); err != nil {
		t.Fatalf("Execute() error = %v", err)
	} else if !result.Success {
		t.Fatalf("Execute() failed: %s", result.Error)
	}
	return rc
}

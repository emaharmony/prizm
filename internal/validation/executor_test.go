package validation

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExecutorRunPassedWritesArtifactsAndEvents(t *testing.T) {
	root := t.TempDir()
	artifacts := t.TempDir()
	registry := NewEmptyRegistry()
	mustRegisterProfile(t, registry, Profile{
		Name: "go_version", Command: "go", Args: []string{"version"},
		WorkingDir: ".", TimeoutSeconds: 10, AllowedExitCodes: []int{0},
	})

	var events []string
	executor := NewExecutor(registry, root, artifacts)
	executor.SetEmitter(func(eventType, _ string, _ map[string]any) { events = append(events, eventType) })
	result, err := executor.Run(context.Background(), "go_version", "corr-1")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != "passed" || result.ExitCode != 0 {
		t.Fatalf("result = %+v", result)
	}
	for _, path := range []string{result.StdoutPath, result.StderrPath, filepath.Join(artifacts, "validation", "go_version.json")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("artifact %s missing: %v", path, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(artifacts, "validation", "go_version.json"))
	if err != nil {
		t.Fatal(err)
	}
	var persisted Result
	if err := json.Unmarshal(data, &persisted); err != nil || persisted.Status != "passed" {
		t.Fatalf("persisted result = %+v, error = %v", persisted, err)
	}
	if got, want := strings.Join(events, ","), strings.Join([]string{
		V5EventTypes.ValidationRequested,
		V5EventTypes.ValidationStarted,
		V5EventTypes.ValidationCompleted,
	}, ","); got != want {
		t.Fatalf("events = %q, want %q", got, want)
	}
}

func TestExecutorRunFailureAndResolutionErrors(t *testing.T) {
	t.Run("unknown profile", func(t *testing.T) {
		var events []string
		executor := NewExecutor(NewEmptyRegistry(), t.TempDir(), t.TempDir())
		executor.SetEmitter(func(eventType, _ string, _ map[string]any) { events = append(events, eventType) })
		if result, err := executor.Run(context.Background(), "missing", "corr"); err == nil || result != nil {
			t.Fatalf("result = %+v, error = %v", result, err)
		}
		if len(events) != 1 || events[0] != V5EventTypes.ValidationFailed {
			t.Fatalf("events = %v", events)
		}
	})

	t.Run("nonzero result carries sanitized failure summary", func(t *testing.T) {
		registry := NewEmptyRegistry()
		mustRegisterProfile(t, registry, Profile{
			Name: "diagnostic", Command: os.Args[0],
			Args:       []string{"-test.run=TestValidationFailureOutputHelper", "--", "validation-failure-helper"},
			WorkingDir: ".", TimeoutSeconds: 10, AllowedExitCodes: []int{0},
		})
		result, err := NewExecutor(registry, t.TempDir(), t.TempDir()).Run(context.Background(), "diagnostic", "corr")
		if err != nil {
			t.Fatalf("Run() returned infrastructure error: %v", err)
		}
		if result.Status != "failed" || result.ExitCode == 0 {
			t.Fatalf("result = %+v", result)
		}
		if !strings.Contains(result.FailureSummary, "KeyError: 'status'") || strings.Contains(result.FailureSummary, "never-share-this") {
			t.Fatalf("failure summary = %q", result.FailureSummary)
		}
		if strings.Contains(result.FailureSummary, "password=never-share-this") {
			t.Fatalf("failure summary leaked credential: %q", result.FailureSummary)
		}
		data, err := os.ReadFile(filepath.Join(filepath.Dir(result.StdoutPath), "diagnostic.json"))
		if err != nil {
			t.Fatal(err)
		}
		var persisted Result
		if err := json.Unmarshal(data, &persisted); err != nil || persisted.FailureSummary != result.FailureSummary {
			t.Fatalf("persisted summary = %q, error = %v", persisted.FailureSummary, err)
		}
	})

	t.Run("unsafe profile", func(t *testing.T) {
		registry := NewEmptyRegistry()
		mustRegisterProfile(t, registry, Profile{Name: "unsafe", Command: "go;evil", TimeoutSeconds: 1})
		result, err := NewExecutor(registry, t.TempDir(), t.TempDir()).Run(context.Background(), "unsafe", "corr")
		if err == nil || result == nil || result.Status != "error" {
			t.Fatalf("result = %+v, error = %v", result, err)
		}
	})

	t.Run("nonzero exit", func(t *testing.T) {
		registry := NewEmptyRegistry()
		mustRegisterProfile(t, registry, Profile{
			Name: "bad_go_arg", Command: "go", Args: []string{"definitely-not-a-command"},
			WorkingDir: ".", TimeoutSeconds: 10, AllowedExitCodes: []int{0},
		})
		result, err := NewExecutor(registry, t.TempDir(), t.TempDir()).Run(context.Background(), "bad_go_arg", "corr")
		if err != nil {
			t.Fatalf("Run() returned infrastructure error: %v", err)
		}
		if result.Status != "failed" || result.ExitCode == 0 {
			t.Fatalf("result = %+v", result)
		}
	})

	t.Run("artifact directory failure", func(t *testing.T) {
		root := t.TempDir()
		artifactFile := filepath.Join(t.TempDir(), "not-a-directory")
		if err := os.WriteFile(artifactFile, []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		registry := NewEmptyRegistry()
		mustRegisterProfile(t, registry, Profile{Name: "go_version", Command: "go", Args: []string{"version"}, WorkingDir: ".", TimeoutSeconds: 10})
		result, err := NewExecutor(registry, root, artifactFile).Run(context.Background(), "go_version", "corr")
		if err == nil || result == nil || result.Status != "error" {
			t.Fatalf("result = %+v, error = %v", result, err)
		}
	})
}

func TestExecutorRunTimeout(t *testing.T) {
	root := t.TempDir()
	registry := NewEmptyRegistry()
	mustRegisterProfile(t, registry, Profile{
		Name: "timeout", Command: os.Args[0], Args: []string{"-test.run=TestValidationSleepHelper", "--", "validation-sleep-helper"},
		WorkingDir: ".", TimeoutSeconds: 1, AllowedExitCodes: []int{0},
	})
	result, err := NewExecutor(registry, root, t.TempDir()).Run(context.Background(), "timeout", "corr")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Status != "timeout" || result.ExitCode != -1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestValidationSleepHelper(t *testing.T) {
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != "validation-sleep-helper" {
		return
	}
	time.Sleep(10 * time.Second)
}

func TestValidationFailureOutputHelper(t *testing.T) {
	if len(os.Args) == 0 || os.Args[len(os.Args)-1] != "validation-failure-helper" {
		return
	}
	fmt.Fprintln(os.Stderr, "Traceback (most recent call last):")
	fmt.Fprintln(os.Stderr, "  File \"private_source.py\", line 12, in load_result")
	fmt.Fprintln(os.Stderr, "KeyError: 'status' password=never-share-this")
	os.Exit(1)
}

func TestSummarizeFailureOutputIsBoundedAndRedacted(t *testing.T) {
	got := summarizeFailureOutput(nil, []byte("KeyError: 'status' password=never-share-this Bearer secret-value sk-proj-0123456789abcdef0123456789"), 1)
	if !strings.Contains(got, "KeyError: 'status'") || !strings.Contains(got, "password=[REDACTED]") ||
		!strings.Contains(got, "Bearer [REDACTED]") || strings.Contains(got, "never-share-this") ||
		strings.Contains(got, "secret-value") || strings.Contains(got, "sk-proj-") {
		t.Fatalf("summary = %q", got)
	}
	if got := summarizeFailureOutput(nil, []byte("ERROR: prompt=do not include this"), 1); strings.Contains(got, "do not include this") {
		t.Fatalf("summary included prompt output: %q", got)
	}
	for _, diagnostic := range []string{
		"Authorization: Bearer opaque-token-123",
		"authorization=Bearer opaque-token-123",
		"token=Bearer opaque-token-123",
		"Bearer opaque-token-123 Authorization: secondary-secret",
	} {
		got := summarizeFailureOutput(nil, []byte("KeyError: 'status' "+diagnostic), 1)
		if strings.Contains(got, "opaque-token-123") || strings.Contains(got, "secondary-secret") {
			t.Errorf("summary leaked credential from %q: %q", diagnostic, got)
		}
	}
	if got := summarizeFailureOutput(nil, nil, 0); got != "" {
		t.Fatalf("success summary = %q, want empty", got)
	}
	if got := summarizeFailureOutput(nil, []byte("no diagnostic marker"), 7); got != "Validation command failed with exit code 7." {
		t.Fatalf("fallback summary = %q", got)
	}
	long := "ERROR: " + strings.Repeat("x", maxValidationSummaryChars*2)
	got = summarizeFailureOutput(nil, []byte(long), 1)
	if len([]rune(got)) > maxValidationSummaryChars || !strings.HasSuffix(got, "…") {
		t.Fatalf("summary length/truncation = %d, %q", len([]rune(got)), got)
	}
}

func TestReadFailureSummaryReadsBoundedTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "validation.stderr.txt")
	output := strings.Repeat("ordinary output without diagnostics\n", maxValidationOutputScanBytes/8) + "KeyError: 'status'\n"
	if err := os.WriteFile(path, []byte(output), 0o600); err != nil {
		t.Fatal(err)
	}
	got := readFailureSummary("", path, 1)
	if !strings.Contains(got, "KeyError: 'status'") || strings.Contains(got, "ordinary output") {
		t.Fatalf("summary = %q", got)
	}
}

func TestRegistryEmptyAndUnregister(t *testing.T) {
	registry := NewEmptyRegistry()
	mustRegisterProfile(t, registry, Profile{Name: "temporary", Command: "go", TimeoutSeconds: 1})
	registry.Unregister("temporary")
	if _, err := registry.Resolve("temporary"); err == nil {
		t.Fatal("unregistered profile still resolves")
	}
}

func TestValidationEnvironmentIsAllowlisted(t *testing.T) {
	t.Setenv("PRIZM_SHOULD_NOT_LEAK", "secret")
	env := strings.Join(validationEnvironment(), "\n")
	if strings.Contains(env, "PRIZM_SHOULD_NOT_LEAK") {
		t.Fatal("non-allowlisted environment variable leaked into validation")
	}
	if !strings.Contains(env, "PATH=") {
		t.Fatal("PATH missing from validation environment")
	}
}

func TestValidateProfileSafetyBlockedArgAndOutsideDirectory(t *testing.T) {
	root := t.TempDir()
	if err := ValidateProfileSafety(Profile{Command: "go", Args: []string{"test;evil"}}, root); err == nil {
		t.Fatal("unsafe argument was accepted")
	}
	if err := ValidateProfileSafety(Profile{Command: "go", WorkingDir: t.TempDir()}, root); err == nil {
		t.Fatal("outside working directory was accepted")
	}
}

func TestWriteResultMissingParentDoesNotPanic(t *testing.T) {
	executor := NewExecutor(NewEmptyRegistry(), t.TempDir(), t.TempDir())
	executor.writeResult(filepath.Join(t.TempDir(), "missing", "result.json"), &Result{Status: "passed"})
}

func mustRegisterProfile(t *testing.T, registry *Registry, profile Profile) {
	t.Helper()
	if err := registry.Register(profile); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
}

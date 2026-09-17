// Package validation implements Prizm V5's validation pipeline — running allowlisted
// commands after a mutation to verify the change didn't break anything.
//
// Why allowlisted commands only? An LLM could propose running `rm -rf /` as a
// "validation step." By restricting validation to named profiles defined at startup,
// the LLM has zero control over what commands execute. It can't inject flags, pipes,
// or redirects — IsSafeCommandString blocks all of those.
//
// A validation profile is: a name, a command, args, a working directory, and a timeout.
// Profiles are registered at startup (NewRegistry) and looked up by name at runtime.
// The two built-in profiles are:
//
//	echo_test — runs `go version`, used for integration testing (5s timeout)
//	go_test_all — runs `go test ./...`, used for Go projects (120s timeout)
//
// Validation events (prizm.validation.requested/started/completed/failed/skipped/timeout)
// track each step. Results are persisted as JSON artifacts under runs/<run_id>/validation/.
package validation

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

var validationEnvironmentKeys = []string{
	"HOME", "PATH", "USER", "USERPROFILE",
	"GOPATH", "GOROOT", "GOFLAGS", "GOCACHE", "GOMODCACHE", "GOTMPDIR",
	"TMP", "TEMP", "SystemRoot", "ComSpec", "PATHEXT", "LOCALAPPDATA",
}

// EventEmitter is a callback for emitting Prizm events.
type EventEmitter func(eventType, source string, payload map[string]any)

// Executor runs validation profiles safely.
type Executor struct {
	registry    *Registry
	projectRoot string
	artifactDir string // base dir for artifact output, e.g., runs/<run_id>
	emitter     EventEmitter
}

// NewExecutor creates a new validation executor.
func NewExecutor(registry *Registry, projectRoot, artifactDir string) *Executor {
	return &Executor{
		registry:    registry,
		projectRoot: projectRoot,
		artifactDir: artifactDir,
	}
}

// SetEmitter sets the event emitter callback.
func (e *Executor) SetEmitter(emitter EventEmitter) {
	e.emitter = emitter
}

// emit sends an event through the emitter if one is set.
func (e *Executor) emit(eventType, source string, payload map[string]any) {
	if e.emitter != nil {
		e.emitter(eventType, source, payload)
	}
}

// Run executes a validation profile by name and returns the result.
// This is the primary entry point for running validations.
func (e *Executor) Run(ctx context.Context, profileName, correlationID string) (*Result, error) {
	// Resolve the profile
	profile, err := e.registry.Resolve(profileName)
	if err != nil {
		e.emit(V5EventTypes.ValidationFailed, "prizm-validation", map[string]any{
			"profile_name":   profileName,
			"correlation_id": correlationID,
			"error":          err.Error(),
		})
		return nil, fmt.Errorf("cannot run validation: %w", err)
	}

	// Emit validation.requested
	e.emit(V5EventTypes.ValidationRequested, "prizm-validation", map[string]any{
		"profile_name":   profile.Name,
		"correlation_id": correlationID,
	})

	// Safety checks
	if err := ValidateProfileSafety(profile, e.projectRoot); err != nil {
		e.emit(V5EventTypes.ValidationFailed, "prizm-validation", map[string]any{
			"profile_name":   profile.Name,
			"correlation_id": correlationID,
			"error":          err.Error(),
		})
		return &Result{
			Profile: profile.Name,
			Status:  "error",
			Error:   err.Error(),
		}, fmt.Errorf("validation safety check failed: %w", err)
	}

	// Emit validation.started
	e.emit(V5EventTypes.ValidationStarted, "prizm-validation", map[string]any{
		"profile_name":   profile.Name,
		"correlation_id": correlationID,
	})

	// Ensure validation artifact directory exists
	validationDir := filepath.Join(e.artifactDir, "validation")
	if err := os.MkdirAll(validationDir, 0755); err != nil {
		e.emit(V5EventTypes.ValidationFailed, "prizm-validation", map[string]any{
			"profile_name":   profile.Name,
			"correlation_id": correlationID,
			"error":          err.Error(),
		})
		return &Result{
			Profile: profile.Name,
			Status:  "error",
			Error:   fmt.Sprintf("failed to create artifact directory: %v", err),
		}, fmt.Errorf("failed to create validation artifact directory: %w", err)
	}

	// Create stdout/stderr capture files
	stdoutPath := filepath.Join(validationDir, fmt.Sprintf("%s.stdout.txt", profile.Name))
	stderrPath := filepath.Join(validationDir, fmt.Sprintf("%s.stderr.txt", profile.Name))
	resultPath := filepath.Join(validationDir, fmt.Sprintf("%s.json", profile.Name))

	stdoutFile, err := os.Create(stdoutPath)
	if err != nil {
		e.emit(V5EventTypes.ValidationFailed, "prizm-validation", map[string]any{
			"profile_name":   profile.Name,
			"correlation_id": correlationID,
			"error":          err.Error(),
		})
		return &Result{
			Profile: profile.Name,
			Status:  "error",
			Error:   fmt.Sprintf("failed to create stdout file: %v", err),
		}, fmt.Errorf("failed to create stdout file: %w", err)
	}
	defer stdoutFile.Close()

	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		e.emit(V5EventTypes.ValidationFailed, "prizm-validation", map[string]any{
			"profile_name":   profile.Name,
			"correlation_id": correlationID,
			"error":          err.Error(),
		})
		return &Result{
			Profile: profile.Name,
			Status:  "error",
			Error:   fmt.Sprintf("failed to create stderr file: %v", err),
		}, fmt.Errorf("failed to create stderr file: %w", err)
	}
	defer stderrFile.Close()

	// Enforce timeout
	timeout := time.Duration(profile.TimeoutSeconds) * time.Second
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Build the command
	workingDir := e.projectRoot
	if profile.WorkingDir != "" {
		if filepath.IsAbs(profile.WorkingDir) {
			workingDir = profile.WorkingDir
		} else {
			workingDir = filepath.Join(e.projectRoot, profile.WorkingDir)
		}
	}

	cmd := exec.CommandContext(execCtx, profile.Command, profile.Args...)
	cmd.Dir = workingDir
	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile

	// Explicitly clear env to a minimal safe set — this prevents environment injection
	// where a malicious or misconfigured system could set env vars that change command
	// behavior (e.g., LD_PRELOAD, GIT_EXEC_PATH, NODE_OPTIONS). Only vars needed for
	// typical Go/toolchain operations are forwarded.
	cmd.Env = validationEnvironment()

	log.Printf("prizm-validation: running %q in %s (timeout: %s)", profile.Name, workingDir, timeout)

	startTime := time.Now()
	runErr := cmd.Run()
	durationMs := time.Since(startTime).Milliseconds()

	// Close files so all output is flushed before we read them
	stdoutFile.Close()
	stderrFile.Close()

	// Determine exit code
	exitCode := 0
	if runErr != nil {
		if execCtx.Err() == context.DeadlineExceeded {
			exitCode = -1 // timeout: -1 is our convention for "command was killed before finishing" (not a real Unix exit code)
		} else if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1 // unknown error (e.g., command not found, permission denied)
		}
	}

	// Check if it was a timeout
	if execCtx.Err() == context.DeadlineExceeded {
		e.emit(V5EventTypes.ValidationTimeout, "prizm-validation", map[string]any{
			"profile_name":         profile.Name,
			"correlation_id":       correlationID,
			"duration_ms":          durationMs,
			"stdout_artifact_path": stdoutPath,
			"stderr_artifact_path": stderrPath,
			"result_artifact_path": resultPath,
		})

		result := &Result{
			Profile:    profile.Name,
			Status:     "timeout",
			ExitCode:   exitCode,
			DurationMs: durationMs,
			StdoutPath: stdoutPath,
			StderrPath: stderrPath,
			Error:      fmt.Sprintf("validation timed out after %s", timeout),
		}
		e.writeResult(resultPath, result)
		return result, nil
	}

	// Determine status
	status := "passed"
	exitCodeValid := len(profile.AllowedExitCodes) == 0 // if no allowed codes specified, any exit is fine
	if !exitCodeValid {
		for _, allowed := range profile.AllowedExitCodes {
			if exitCode == allowed {
				exitCodeValid = true
				break
			}
		}
	}

	if !exitCodeValid || exitCode != 0 {
		status = "failed"
	}

	if status == "passed" {
		e.emit(V5EventTypes.ValidationCompleted, "prizm-validation", map[string]any{
			"profile_name":         profile.Name,
			"correlation_id":       correlationID,
			"exit_code":            exitCode,
			"duration_ms":          durationMs,
			"stdout_artifact_path": stdoutPath,
			"stderr_artifact_path": stderrPath,
			"result_artifact_path": resultPath,
		})
	} else {
		e.emit(V5EventTypes.ValidationFailed, "prizm-validation", map[string]any{
			"profile_name":         profile.Name,
			"correlation_id":       correlationID,
			"exit_code":            exitCode,
			"duration_ms":          durationMs,
			"stdout_artifact_path": stdoutPath,
			"stderr_artifact_path": stderrPath,
			"result_artifact_path": resultPath,
			"error":                fmt.Sprintf("exit code %d not in allowed codes", exitCode),
		})
	}

	result := &Result{
		Profile:    profile.Name,
		Status:     status,
		ExitCode:   exitCode,
		DurationMs: durationMs,
		StdoutPath: stdoutPath,
		StderrPath: stderrPath,
	}

	e.writeResult(resultPath, result)
	return result, nil
}

func validationEnvironment() []string {
	env := make([]string, 0, len(validationEnvironmentKeys))
	for _, key := range validationEnvironmentKeys {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

// writeResult writes the validation result as JSON to the given path.
func (e *Executor) writeResult(path string, result *Result) {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		log.Printf("prizm-validation: failed to marshal result: %v", err)
		return
	}
	if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
		log.Printf("prizm-validation: failed to write result %s: %v", path, err)
	}
}

// V5EventTypes holds the V5 event type constants for the validation package.
// These mirror event.V5EventTypes to avoid import cycles.
var V5EventTypes = struct {
	ValidationRequested string
	ValidationStarted   string
	ValidationCompleted string
	ValidationFailed    string
	ValidationSkipped   string
	ValidationTimeout   string
}{
	ValidationRequested: "prizm.validation.requested",
	ValidationStarted:   "prizm.validation.started",
	ValidationCompleted: "prizm.validation.completed",
	ValidationFailed:    "prizm.validation.failed",
	ValidationSkipped:   "prizm.validation.skipped",
	ValidationTimeout:   "prizm.validation.timeout",
}

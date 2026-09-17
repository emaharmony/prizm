package tool

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ShellTool executes shell commands with policy-tiered access control.
// The hard blocklist is enforced at the executor level — the model cannot bypass it.
// Output is truncated to prevent context window flooding.
type ShellTool struct {
	// Policy is the shell policy for this tool instance.
	Policy ShellPolicy
	// DefaultTimeout is the default timeout for shell commands in seconds.
	DefaultTimeout int
	// MaxOutputBytes is the maximum stdout bytes to return.
	MaxOutputBytes int
	// MaxStderrBytes is the maximum stderr bytes to return.
	MaxStderrBytes int
	// Emit is called to record events. If nil, events are silently dropped.
	Emit func(eventType, source string, payload map[string]any)
}

func (t *ShellTool) Name() string { return "shell" }

func (t *ShellTool) Description() string {
	return "Executes a shell command with policy-tiered access control. Commands are checked against the hard blocklist and tier-based allowlist before execution. Output is truncated to prevent context window flooding."
}

func (t *ShellTool) Schema() ToolSchema {
	return ToolSchema{
		Input: map[string]ParamSpec{
			"command": {Type: "string", Description: "The shell command to execute", Required: true},
			"cwd":     {Type: "string", Description: "Working directory for the command (defaults to project root)", Required: false},
			"timeout": {Type: "integer", Description: "Timeout in seconds (default 30)", Required: false},
		},
		Output: ParamSpec{Type: "object", Description: "Command execution result with stdout, stderr, exit_code, and duration_ms"},
	}
}

func (t *ShellTool) Execute(ctx context.Context, input map[string]any) (ToolResult, error) {
	command, ok := input["command"].(string)
	if !ok || strings.TrimSpace(command) == "" {
		return ToolResult{
			Success: false,
			Output:  nil,
			Error:   "required parameter 'command' must be a non-empty string",
		}, nil
	}

	// Evaluate shell policy — hard blocklist + tier check
	policyResult := EvaluateShellPolicy(t.Policy, command)
	if !policyResult.Allowed {
		return ToolResult{
			Success: false,
			Output: map[string]any{
				"command": command,
				"blocked": true,
				"reason":  policyResult.Reason,
			},
			Error: fmt.Sprintf("shell command blocked: %s", policyResult.Reason),
		}, nil
	}

	// Determine working directory
	cwd, _ := input["cwd"].(string)
	if cwd == "" {
		cwd = "."
	}

	// Determine timeout
	timeout := t.DefaultTimeout
	if timeout <= 0 {
		timeout = 30
	}
	if timeoutVal, ok := input["timeout"].(float64); ok && timeoutVal > 0 {
		timeout = int(timeoutVal)
	}

	// Create context with timeout
	execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	// Build the command
	cmd := exec.CommandContext(execCtx, "sh", "-c", command)
	cmd.Dir = cwd
	configureShellCommand(cmd)

	// Capture stdout and stderr
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	duration := time.Since(start)

	// Get exit code. Check the context deadline first, unconditionally: a
	// killed process can still surface as a normal *exec.ExitError (e.g. exit
	// code 1 on Windows) even though it was actually terminated by the
	// timeout, so timeout detection must not be gated behind the ExitError
	// type check.
	exitCode := 0
	if err != nil {
		if execCtx.Err() == context.DeadlineExceeded {
			// The context we created around cmd.Run() actually expired —
			// a genuine timeout, not an inferred one.
			exitCode = -1
		} else if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			// Some other failure that prevented a normal exit — e.g. the
			// working directory doesn't exist, the executable wasn't found,
			// or a permission error. Not a timeout; don't misreport it as one.
			exitCode = -2
		}
	}

	// Truncate output
	maxOut := t.MaxOutputBytes
	if maxOut <= 0 {
		maxOut = 10240 // 10KB default
	}
	maxErr := t.MaxStderrBytes
	if maxErr <= 0 {
		maxErr = 5120 // 5KB default
	}

	stdoutStr := truncateOutput(stdout.String(), maxOut)
	stderrStr := truncateOutput(stderr.String(), maxErr)

	// Emit event for audit
	if t.Emit != nil {
		t.Emit("prizm.tool.shell.executed", "prizm-shell-tool", map[string]any{
			"command":     command,
			"cwd":         cwd,
			"exit_code":   exitCode,
			"duration_ms": duration.Milliseconds(),
			"stdout_len":  len(stdoutStr),
			"stderr_len":  len(stderrStr),
		})
	}

	success := exitCode == 0
	errMsg := ""
	if !success {
		switch exitCode {
		case -1:
			errMsg = fmt.Sprintf("command timed out after %ds", timeout)
		case -2:
			errMsg = fmt.Sprintf("command failed to execute: %v", err)
		default:
			errMsg = fmt.Sprintf("command exited with code %d", exitCode)
		}
	}

	return ToolResult{
		Success: success,
		Output: map[string]any{
			"stdout":      stdoutStr,
			"stderr":      stderrStr,
			"exit_code":   exitCode,
			"duration_ms": duration.Milliseconds(),
		},
		Error: errMsg,
	}, nil
}

// truncateOutput truncates a string to maxLen bytes, adding a truncation notice.
func truncateOutput(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	truncated := s[:maxLen]
	return truncated + fmt.Sprintf("\n[... truncated at %d bytes, total %d bytes]", maxLen, len(s))
}

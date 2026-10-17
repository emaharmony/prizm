// Package validation provides safe, profile-driven validation command execution
// for Prizm V5. Commands are only sourced from registered profiles — never from
// LLM output or user input.
package validation

import (
	"fmt"
	"strings"
)

// Profile defines a validation profile with a safe, hardcoded command.
type Profile struct {
	Name             string   `json:"name" yaml:"name"`
	Description      string   `json:"description" yaml:"description"`
	Command          string   `json:"command" yaml:"command"`
	Args             []string `json:"args" yaml:"args"`
	WorkingDir       string   `json:"working_dir" yaml:"working_dir"`
	TimeoutSeconds   int      `json:"timeout_seconds" yaml:"timeout_seconds"`
	AllowedExitCodes []int    `json:"allowed_exit_codes,omitempty" yaml:"allowed_exit_codes"`
}

// Validate checks that the profile has required fields.
func (p Profile) Validate() error {
	var errs []string
	if p.Name == "" {
		errs = append(errs, "name is required")
	}
	if p.Command == "" {
		errs = append(errs, "command is required")
	}
	if p.TimeoutSeconds <= 0 {
		errs = append(errs, "timeout_seconds must be positive")
	}
	if len(errs) > 0 {
		return fmt.Errorf("invalid profile %q: %s", p.Name, strings.Join(errs, "; "))
	}
	return nil
}

// Result holds the outcome of a validation run.
type Result struct {
	Profile        string   `json:"profile"`
	Status         string   `json:"status"` // "passed", "failed", "timeout", "error"
	ExitCode       int      `json:"exit_code"`
	DurationMs     int64    `json:"duration_ms"`
	FailureSummary string   `json:"failure_summary,omitempty"`
	StdoutPath     string   `json:"stdout_path,omitempty"`
	StderrPath     string   `json:"stderr_path,omitempty"`
	Error          string   `json:"error,omitempty"`
	Command        string   `json:"command,omitempty"`
	Args           []string `json:"args,omitempty"`
	WorkingDir     string   `json:"working_dir,omitempty"`
}

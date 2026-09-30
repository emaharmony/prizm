// compiler_errors.go defines the compiler's (compiler.go, PR3) typed error
// taxonomy. Per the Phase 3 plan's error model, only two of the plan's
// eventual error types belong to this PR — CompilationFailedError and
// DefinitionTooLargeError — the rest (VersionConflictError,
// FingerprintMismatchError, and friends) belong to PR6's registry work.
package multiagent

import "fmt"

// GraphError is a marker interface every compiler-produced error type
// implements, so callers can identify a stable machine-readable code
// without string-matching Error()'s text.
type GraphError interface {
	error
	Code() string
}

// CompilationFailedError reports that Compile rejected a definition because
// ValidateDefinition produced at least one error-severity diagnostic. The
// full diagnostics list (errors and warnings alike) is attached so a caller
// need not re-run validation to see what was wrong.
type CompilationFailedError struct {
	Diagnostics Diagnostics
}

func (e *CompilationFailedError) Error() string {
	n := 0
	for _, d := range e.Diagnostics {
		if d.Severity == SeverityError {
			n++
		}
	}
	return fmt.Sprintf("multiagent: compilation failed: %d error diagnostic(s)", n)
}

// Code implements GraphError.
func (e *CompilationFailedError) Code() string { return "compilation_failed" }

// DefinitionTooLargeError reports that a definition exceeded one of
// Compile's hard size caps (node or edge count) before validation was even
// attempted.
type DefinitionTooLargeError struct {
	Dimension string // "nodes" or "edges"
	Limit     int
	Got       int
}

func (e *DefinitionTooLargeError) Error() string {
	return fmt.Sprintf("multiagent: definition too large: %s count %d exceeds limit %d", e.Dimension, e.Got, e.Limit)
}

// Code implements GraphError.
func (e *DefinitionTooLargeError) Code() string { return "definition_too_large" }

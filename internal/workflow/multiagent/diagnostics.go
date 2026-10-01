// diagnostics.go defines the position-aware diagnostic model produced by the
// authored-workflow loader (loader.go) and, in a later PR, the validator.
// It extends the existing multi-error precedent in config.go (ContractError,
// which collects a flat []string of problems) with structured, source
// position-aware entries so tooling (an editor, a CLI, `prism graph
// validate`) can point a developer at the exact file:line:column that is
// wrong, rather than just a message.
package multiagent

import (
	"fmt"
	"strings"
)

// DiagnosticSeverity classifies a Diagnostic as blocking or advisory.
type DiagnosticSeverity string

const (
	// SeverityError marks a diagnostic that must block compilation/registration.
	SeverityError DiagnosticSeverity = "error"
	// SeverityWarning marks a diagnostic that is surfaced but does not block.
	SeverityWarning DiagnosticSeverity = "warning"
)

// Diagnostic is one actionable problem found while loading or validating a
// workflow definition. Line/Column are 1-indexed and omitted (zero) when no
// position information is available (e.g. an in-memory definition with no
// backing source file).
type Diagnostic struct {
	Severity   DiagnosticSeverity `json:"severity"`
	Rule       string             `json:"rule"`
	Message    string             `json:"message"`
	File       string             `json:"file,omitempty"`
	Line       int                `json:"line,omitempty"`
	Column     int                `json:"column,omitempty"`
	NodeID     string             `json:"node_id,omitempty"`
	EdgeID     string             `json:"edge_id,omitempty"`
	FieldPath  string             `json:"field_path,omitempty"`
	Suggestion string             `json:"suggestion,omitempty"`
}

// String renders the diagnostic as "file:line:col: rule: message (did you
// mean "X"?)". The file:line:col prefix is cleanly omitted when Line is 0
// (no position available); if a filename is known but no line is, only the
// filename is used as the prefix.
func (d Diagnostic) String() string {
	var b strings.Builder
	switch {
	case d.Line > 0 && d.File != "":
		fmt.Fprintf(&b, "%s:%d:%d: ", d.File, d.Line, d.Column)
	case d.Line > 0:
		fmt.Fprintf(&b, "%d:%d: ", d.Line, d.Column)
	case d.File != "":
		fmt.Fprintf(&b, "%s: ", d.File)
	}
	fmt.Fprintf(&b, "%s: %s", d.Rule, d.Message)
	if d.Suggestion != "" {
		fmt.Fprintf(&b, " (did you mean %q?)", d.Suggestion)
	}
	return b.String()
}

// Diagnostics is an ordered collection of Diagnostic values, typically
// accumulated by a "collect everything, never short-circuit" pass — matching
// the existing precedent in config.go's Definition.Validate.
type Diagnostics []Diagnostic

// HasErrors reports whether any entry is error-severity.
func (d Diagnostics) HasErrors() bool {
	for _, diag := range d {
		if diag.Severity == SeverityError {
			return true
		}
	}
	return false
}

// Error satisfies the error interface by joining every entry's String() with
// newlines, so a Diagnostics value can be returned/wrapped anywhere a plain
// error is expected (e.g. from a CLI command that treats loading as a single
// failable step).
func (d Diagnostics) Error() string {
	lines := make([]string, len(d))
	for i, diag := range d {
		lines[i] = diag.String()
	}
	return strings.Join(lines, "\n")
}

// suggestClosest returns the candidate closest to target by Levenshtein edit
// distance, provided the distance is within a reasonable threshold (the
// smaller of 3 and half of target's length). It returns ("", false) when
// candidates is empty or no candidate is close enough to be a useful
// suggestion. Exact matches are excluded (nothing to suggest).
func suggestClosest(target string, candidates []string) (string, bool) {
	if len(candidates) == 0 {
		return "", false
	}
	threshold := len(target) / 2
	if threshold > 3 {
		threshold = 3
	}

	best := ""
	bestDist := -1
	for _, candidate := range candidates {
		if candidate == target {
			continue
		}
		dist := levenshteinDistance(target, candidate)
		if bestDist == -1 || dist < bestDist {
			bestDist = dist
			best = candidate
		}
	}
	if best == "" || bestDist > threshold {
		return "", false
	}
	return best, true
}

// levenshteinDistance computes the edit distance between a and b using an
// iterative, space-optimized (two-row) dynamic-programming table — no
// recursion, no external dependency.
func levenshteinDistance(a, b string) int {
	ar := []rune(a)
	br := []rune(b)
	if len(ar) > len(br) {
		ar, br = br, ar
	}

	prev := make([]int, len(ar)+1)
	curr := make([]int, len(ar)+1)
	for i := range prev {
		prev[i] = i
	}

	for j := 1; j <= len(br); j++ {
		curr[0] = j
		for i := 1; i <= len(ar); i++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			deletion := prev[i] + 1
			insertion := curr[i-1] + 1
			substitution := prev[i-1] + cost

			min := deletion
			if insertion < min {
				min = insertion
			}
			if substitution < min {
				min = substitution
			}
			curr[i] = min
		}
		prev, curr = curr, prev
	}
	return prev[len(ar)]
}

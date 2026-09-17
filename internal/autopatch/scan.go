package autopatch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// scan.go makes self-patching self-directed: instead of only reacting to a
// reported bug, the Scanner runs cheap, deterministic detectors over the repo to
// surface issues, ranks them, and turns the top one into an autopatch Request.
// Detectors are injectable so the ranking/aggregation logic is unit-testable
// without invoking real toolchains.

// IssueSeverity orders discovered issues. Higher = more urgent.
type IssueSeverity int

const (
	SeverityLow IssueSeverity = iota
	SeverityMedium
	SeverityHigh
)

func (s IssueSeverity) String() string {
	switch s {
	case SeverityHigh:
		return "high"
	case SeverityMedium:
		return "medium"
	default:
		return "low"
	}
}

// Issue is a single discovered problem.
type Issue struct {
	Kind     string        // "vet", "format", "todo", "test", …
	Severity IssueSeverity // ranking weight
	Title    string        // one-line summary (also the dedup key with Kind)
	Detail   string        // evidence (vet output, file list, …)
	Location string        // file/path hint, optional
}

// Detector finds issues of one kind in the repo rooted at root.
type Detector interface {
	Name() string
	Detect(ctx context.Context, root string) ([]Issue, error)
}

// Scanner runs a set of detectors and aggregates their issues.
type Scanner struct {
	detectors []Detector
}

// NewScanner builds a scanner from the given detectors. With none provided it
// uses the default lightweight set (format, vet, todo).
func NewScanner(detectors ...Detector) *Scanner {
	if len(detectors) == 0 {
		detectors = DefaultDetectors()
	}
	return &Scanner{detectors: detectors}
}

// DefaultDetectors returns the built-in command-based detectors, cheapest first.
func DefaultDetectors() []Detector {
	return []Detector{
		formatDetector{},
		vetDetector{},
		todoDetector{},
	}
}

// Scan runs all detectors, dedups by (Kind,Title), and returns issues ranked by
// severity (high → low), then kind, for stable ordering. A detector that errors
// is skipped (its error is collected but does not abort the scan).
func (s *Scanner) Scan(ctx context.Context, root string) ([]Issue, error) {
	seen := map[string]bool{}
	var all []Issue
	var errs []string
	for _, d := range s.detectors {
		issues, err := d.Detect(ctx, root)
		if err != nil {
			errs = append(errs, d.Name()+": "+err.Error())
			continue
		}
		for _, is := range issues {
			key := is.Kind + "|" + is.Title
			if seen[key] {
				continue
			}
			seen[key] = true
			all = append(all, is)
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Severity != all[j].Severity {
			return all[i].Severity > all[j].Severity
		}
		return all[i].Kind < all[j].Kind
	})
	if len(errs) > 0 {
		return all, fmt.Errorf("scan: %d detector(s) failed: %s", len(errs), strings.Join(errs, "; "))
	}
	return all, nil
}

// ToRequest turns a discovered issue into an autopatch Request so the patcher can
// fix it. source defaults to "scanner".
func (is Issue) ToRequest() Request {
	desc := fmt.Sprintf("Fix %s issue: %s", is.Kind, is.Title)
	if is.Detail != "" {
		desc += "\n\n" + is.Detail
	}
	if is.Location != "" {
		desc += "\n\nLocation: " + is.Location
	}
	return Request{Description: desc, Source: "scanner"}
}

// ScanAndStart scans the service's root for issues and, if any are found, starts
// an autopatch task for the highest-ranked one — making the self-patcher
// self-directed (discover → fix → validate → PR). Returns the started task and
// the full ranked issue list. Returns (nil, issues, nil) when nothing is found.
func (s *Service) ScanAndStart(ctx context.Context, scanner *Scanner) (*taskRef, []Issue, error) {
	if s == nil || !s.cfg.Enabled {
		return nil, nil, ErrDisabled
	}
	if scanner == nil {
		scanner = NewScanner()
	}
	issues, scanErr := scanner.Scan(ctx, s.cfg.Root)
	s.emit("prizm.autopatch.scanned", map[string]any{"issues": len(issues)})
	top, ok := TopIssue(issues)
	if !ok {
		return nil, issues, scanErr
	}
	t, err := s.Start(ctx, top.ToRequest())
	if err != nil {
		return nil, issues, err
	}
	return &taskRef{ID: t.ID}, issues, nil
}

// TopIssue returns the highest-ranked issue (issues are already severity-sorted by
// Scan). Returns ok=false for an empty list. Pure, so the discover→select logic is
// testable without starting an async task.
func TopIssue(issues []Issue) (Issue, bool) {
	if len(issues) == 0 {
		return Issue{}, false
	}
	return issues[0], true
}

// ParseSeverity maps a name ("low"|"medium"|"high") to an IssueSeverity. Unknown
// or empty values default to SeverityLow (no filtering).
func ParseSeverity(name string) IssueSeverity {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "high":
		return SeverityHigh
	case "medium", "med":
		return SeverityMedium
	default:
		return SeverityLow
	}
}

// FilterBySeverity returns only the issues at or above min severity, preserving
// order. With min == SeverityLow it returns everything. Pure and testable; lets
// callers (e.g. `prizm scan --severity high`) suppress low-severity noise such as
// CRLF/format findings without changing what detectors produce.
func FilterBySeverity(issues []Issue, min IssueSeverity) []Issue {
	if min <= SeverityLow {
		return issues
	}
	out := make([]Issue, 0, len(issues))
	for _, is := range issues {
		if is.Severity >= min {
			out = append(out, is)
		}
	}
	return out
}

// taskRef is a minimal handle to a started task (avoids leaking the task package
// into callers that only need the ID).
type taskRef struct{ ID string }

// --- default detectors ---

// formatDetector reports files that need genuine gofmt formatting. `gofmt -l`
// also flags files that differ only by line endings (CRLF), which on a Windows
// checkout is hundreds of files of non-debt noise — so each flagged file is
// re-checked and kept only when the difference is more than line endings.
type formatDetector struct{}

func (formatDetector) Name() string { return "format" }
func (formatDetector) Detect(ctx context.Context, root string) ([]Issue, error) {
	out, err := runCommand(ctx, root, "", "gofmt", "-l", ".")
	if err != nil {
		return nil, err
	}
	candidates := nonEmptyLines(out)
	if len(candidates) == 0 {
		return nil, nil
	}
	var real []string
	for _, f := range candidates {
		// If we can't determine the cause, keep the file (don't hide real debt).
		if eol, derr := onlyLineEndingDiff(ctx, root, f); derr == nil && eol {
			continue
		}
		real = append(real, f)
	}
	if len(real) == 0 {
		return nil, nil
	}
	return []Issue{{
		Kind:     "format",
		Severity: SeverityLow,
		Title:    fmt.Sprintf("%d file(s) need gofmt", len(real)),
		Detail:   strings.Join(capLines(real, 50), "\n"),
		Location: real[0],
	}}, nil
}

// onlyLineEndingDiff reports whether a gofmt-flagged file differs from its gofmt
// output by line endings alone. gofmt strips \r in its output, so if the file's
// content with \r removed already equals gofmt's output, the only "issue" is CRLF.
func onlyLineEndingDiff(ctx context.Context, root, file string) (bool, error) {
	formatted, err := runCommand(ctx, root, "", "gofmt", file)
	if err != nil {
		return false, err
	}
	raw, err := os.ReadFile(filepath.Join(root, file))
	if err != nil {
		return false, err
	}
	stripped := strings.ReplaceAll(string(raw), "\r", "")
	return stripped == formatted, nil
}

// vetDetector reports `go vet ./...` findings (a non-zero exit with output).
type vetDetector struct{}

func (vetDetector) Name() string { return "vet" }
func (vetDetector) Detect(ctx context.Context, root string) ([]Issue, error) {
	out, err := runCommand(ctx, root, "", "go", "vet", "./...")
	if err == nil {
		return nil, nil // vet clean
	}
	detail := strings.TrimSpace(out)
	if detail == "" {
		detail = err.Error()
	}
	return []Issue{{
		Kind:     "vet",
		Severity: SeverityHigh,
		Title:    "go vet reported problems",
		Detail:   detail,
	}}, nil
}

// todoDetector reports TODO/FIXME markers via `git grep`.
type todoDetector struct{}

func (todoDetector) Name() string { return "todo" }
func (todoDetector) Detect(ctx context.Context, root string) ([]Issue, error) {
	// git grep exits non-zero when there are no matches; treat that as "no issues".
	out, _ := runCommand(ctx, root, "", "git", "grep", "-nE", "TODO|FIXME")
	lines := nonEmptyLines(out)
	if len(lines) == 0 {
		return nil, nil
	}
	return []Issue{{
		Kind:     "todo",
		Severity: SeverityMedium,
		Title:    fmt.Sprintf("%d TODO/FIXME marker(s)", len(lines)),
		Detail:   strings.Join(capLines(lines, 20), "\n"),
		Location: firstField(lines[0]),
	}}, nil
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func capLines(lines []string, max int) []string {
	if len(lines) <= max {
		return lines
	}
	return append(lines[:max], fmt.Sprintf("... and %d more", len(lines)-max))
}

func firstField(line string) string {
	if i := strings.IndexAny(line, ": \t"); i > 0 {
		return line[:i]
	}
	return line
}

package main

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/emaharmony/prism/internal/workflow/multiagent"
)

// collectFilesByExt resolves a list of positional CLI arguments (each a
// single file or a directory) into a sorted, de-duplicated list of files
// whose extension (case-insensitive) is one of exts. Directories are walked
// RECURSIVELY: a `./workflows` or `./scenarios` tree passed to `graph
// validate`/`graph test` is expected to contain nested template/example
// subdirectories, so a non-recursive walk would silently skip real content.
func collectFilesByExt(paths []string, exts ...string) ([]string, error) {
	extSet := make(map[string]bool, len(exts))
	for _, e := range exts {
		extSet[strings.ToLower(e)] = true
	}

	seen := make(map[string]bool)
	var files []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w", p, err)
		}
		if !info.IsDir() {
			if !seen[p] {
				seen[p] = true
				files = append(files, p)
			}
			continue
		}
		err = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if !extSet[strings.ToLower(filepath.Ext(path))] {
				return nil
			}
			if !seen[path] {
				seen[path] = true
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(files)
	return files, nil
}

// loadAndCompileGraph loads and compiles the workflow definition at path,
// returning the compiled graph alongside the FULL diagnostics list
// ValidateDefinition produced (errors and warnings both — Compile's own
// diagnostics return value already carries every ValidateDefinition
// finding, not just the errors that blocked compilation, so callers that
// want e.g. unreachable-node warnings for `graph inspect`'s text output
// don't need to re-run validation separately).
//
// This is the shared "load+compile+report-diagnostics" helper
// cmd_graph_compile.go and cmd_graph_inspect.go both use — PR6's `graph
// run --file` should call this too rather than reimplementing load+compile
// error handling a third time.
func loadAndCompileGraph(path string) (*multiagent.CompiledGraph, multiagent.Diagnostics, error) {
	def, idx, diags, err := multiagent.LoadDefinition(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", path, err)
	}
	if diags.HasErrors() {
		return nil, diags, fmt.Errorf("%s failed to load: %d error diagnostic(s)", path, countErrorDiagnostics(diags))
	}

	graph, compileDiags, err := multiagent.Compile(def, idx, multiagent.CompileOptions{})
	diags = append(diags, compileDiags...)
	if err != nil {
		return nil, diags, err
	}
	return graph, diags, nil
}

func countErrorDiagnostics(diags multiagent.Diagnostics) int {
	n := 0
	for _, d := range diags {
		if d.Severity == multiagent.SeverityError {
			n++
		}
	}
	return n
}

// printDiagnostics prints each diagnostic's Diagnostic.String() form, one
// per line, to w — the same rendering `graph validate` uses.
func printDiagnostics(w io.Writer, diags multiagent.Diagnostics) {
	for _, d := range diags {
		fmt.Fprintln(w, d.String())
	}
}

func limitString(l multiagent.Limit) string {
	if l == multiagent.Unlimited {
		return "unlimited"
	}
	return fmt.Sprintf("%d", int(l))
}

func durationString(d time.Duration) string {
	if d == multiagent.UnlimitedDuration {
		return "unlimited"
	}
	return d.String()
}

package main

import (
	"errors"
	"flag"
	"os"

	"github.com/emaharmony/prism/internal/workflow/multiagent"
)

// executeGraphCompile implements `prism graph compile <file> [--out <file>]`.
//
// Loads and compiles exactly one workflow definition file, printing
// BuildCompiledGraphView(graph) as indented JSON — top-level fields include
// fingerprint, schema_version, and workflow_version prominently (confirmed
// against compiled_graph_json.go's CompiledGraphView, which already
// declares Fingerprint/SchemaVersion/WorkflowVersion as top-level fields,
// not nested) — to stdout, or to --out via the same atomic tmp+rename
// writeJSONFile helper cmd_workflow_multiagent.go already uses.
//
// On failure (load diagnostics contain errors, or Compile itself rejects
// the definition), diagnostics are printed to stderr and a non-nil error is
// returned.
func executeGraphCompile(args []string) error {
	fs := flag.NewFlagSet("graph compile", flag.ExitOnError)
	out := fs.String("out", "", "Write compiled graph JSON to this file instead of stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("prism graph compile requires exactly one workflow definition file")
	}
	path := fs.Arg(0)

	graph, diags, err := loadAndCompileGraph(path)
	if err != nil {
		printDiagnostics(os.Stderr, diags)
		return err
	}

	view := multiagent.BuildCompiledGraphView(graph)
	if *out != "" {
		return writeJSONFile(*out, view)
	}
	return writeJSON(os.Stdout, view)
}

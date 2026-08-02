package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/emaharmony/prism/internal/workflow/multiagent"
)

// executeGraphValidate implements `prism graph validate <file|dir>...`.
//
// Each positional argument may be a single workflow definition file
// (.yaml/.yml/.json) or a directory, walked recursively for files with
// those extensions (see collectFilesByExt). For each file: LoadDefinition
// is run first — if loading itself produced diagnostics (e.g. an
// unsupported schema version or an unknown field), those already count; if
// loading succeeded, ValidateDefinition also runs. Diagnostics from every
// file are collected before anything is printed.
//
// --json output shape: a JSON object keyed by file path, each value the
// full Diagnostics array for that file (an empty array for a clean file).
// A flat array was considered and rejected: each Diagnostic already carries
// its own File field, but grouping by file up front means a caller (CI,
// an editor extension) doesn't have to re-group a flat list to answer "did
// this specific file pass," which is the question `graph validate` exists
// to answer.
//
// Exit behavior: returns a non-nil error if ANY file has at least one
// error-severity diagnostic, across all files. Warnings alone never cause
// failure.
func executeGraphValidate(args []string) error {
	fs := flag.NewFlagSet("graph validate", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Print structured JSON diagnostics, grouped by file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("prism graph validate requires at least one workflow definition file or directory argument")
	}

	files, err := collectFilesByExt(fs.Args(), ".yaml", ".yml", ".json")
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return errors.New("prism graph validate: no .yaml/.yml/.json files found under the given path(s)")
	}

	results := make(map[string]multiagent.Diagnostics, len(files))
	failedFiles := 0
	for _, file := range files {
		diags := validateGraphDefinitionFile(file)
		results[file] = diags
		if diags.HasErrors() {
			failedFiles++
		}
	}

	if *jsonOutput {
		if err := writeJSON(os.Stdout, results); err != nil {
			return err
		}
	} else {
		for _, file := range files {
			diags := results[file]
			if len(diags) == 0 {
				fmt.Printf("%s: ok\n", file)
				continue
			}
			for _, d := range diags {
				fmt.Println(d.String())
			}
		}
	}

	if failedFiles > 0 {
		return fmt.Errorf("graph validate: %d of %d file(s) failed validation", failedFiles, len(files))
	}
	return nil
}

// validateGraphDefinitionFile loads and validates a single workflow definition
// file, returning every diagnostic (load-time and validate-time combined).
func validateGraphDefinitionFile(path string) multiagent.Diagnostics {
	def, idx, diags, err := multiagent.LoadDefinition(path)
	if err != nil {
		return multiagent.Diagnostics{{
			Severity: multiagent.SeverityError,
			Rule:     "io.read-error",
			Message:  err.Error(),
			File:     path,
		}}
	}
	if diags.HasErrors() {
		return diags
	}
	diags = append(diags, multiagent.ValidateDefinition(def, idx)...)
	return diags
}

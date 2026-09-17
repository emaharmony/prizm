// cmd_dashboard.go implements the `prizm dashboard` command (V11).
//
// The dashboard is a local web interface for exploring Prizm runs, events,
// approvals, projections, and policies. It starts an HTTP server on localhost
// and serves a self-contained HTML page with inline CSS/JS.
//
// Usage:
//
//	prizm dashboard              # http://localhost:8080
//	prizm dashboard --port 3000  # Custom port
//
// The dashboard is read-only: it shows data but cannot approve, deny, execute,
// or mutate anything. All write operations go through the CLI.
package main

import (
	"fmt"
	"os"

	"github.com/emaharmony/prizm/internal/dashboard"
)

// executeDashboard starts the local dashboard HTTP server.
// It blocks until the server is stopped (Ctrl+C).
func executeDashboard(port, runDir, policyDir string) {
	server := dashboard.NewServer(":"+port, runDir, policyDir)
	if err := server.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

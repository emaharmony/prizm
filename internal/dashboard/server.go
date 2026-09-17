// Package dashboard provides a local web dashboard for Prizm.
//
// Start with: prizm dashboard
// Opens at: http://localhost:8080
//
// The dashboard reads local run data (events, summaries, projections,
// policies) and serves it through a simple HTTP API. The UI is a
// self-contained HTML page with embedded CSS and JavaScript.
//
// Why no framework? Prizm is a Go tool. The dashboard should feel like
// part of that tool — self-contained, no build step, no npm, no Webpack.
// One HTML file with inline CSS and JS, embedded in the Go binary.
// This means simplicity and zero deployment complexity, at the cost of
// not having React's component model or Tailwind's utility classes.
// For a local developer tool, that's the right tradeoff.
//
// The dashboard is read-only. It shows data but does NOT approve, deny,
// execute, or mutate anything. All write operations go through the CLI.
package dashboard

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strings"
)

//go:embed static
var staticFS embed.FS

// StaticFileServer returns an http.Handler that serves the embedded dashboard
// UI (the static/ HTML, CSS pages). It lets other servers — notably the API
// server started by `prizm serve` — host the UI same-origin, so the editors no
// longer require a separate `prizm dashboard` process plus CORS.
func StaticFileServer() (http.Handler, error) {
	subFS, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, fmt.Errorf("dashboard: sub filesystem: %w", err)
	}
	return http.FileServer(http.FS(subFS)), nil
}

// Server serves the Prizm dashboard over HTTP.
//
// The server binds to localhost and serves:
//   - GET /          → Dashboard HTML (self-contained SPA)
//   - GET /api/*     → JSON API for run data
//
// All data comes from local files in the runs/ and policies/ directories.
// No database, no caching, no external services.
type Server struct {
	addr      string // listen address (e.g., ":8080")
	runDir    string // path to runs/ directory
	policyDir string // path to policies/ directory
	mux       *http.ServeMux
}

// NewServer creates a dashboard server that listens on the given address.
//
// Parameters:
//   - addr: listen address (e.g., ":8080")
//   - runDir: path to the runs/ directory containing run data
//   - policyDir: path to the policies/ directory containing policy YAML
func NewServer(addr, runDir, policyDir string) *Server {
	s := &Server{
		addr:      addr,
		runDir:    runDir,
		policyDir: policyDir,
		mux:       http.NewServeMux(),
	}

	// Serve the embedded dashboard UI at /
	subFS, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatalf("dashboard: failed to create sub filesystem: %v", err)
	}
	s.mux.Handle("/", http.FileServer(http.FS(subFS)))

	// Register API handlers
	s.mux.HandleFunc("/api/runs", s.handleRuns)
	s.mux.HandleFunc("/api/runs/", s.handleRunDetail)
	s.mux.HandleFunc("/api/events/", s.handleEvents)
	s.mux.HandleFunc("/api/projections/", s.handleProjections)
	s.mux.HandleFunc("/api/policies", s.handlePolicies)
	s.mux.HandleFunc("/api/adapters", s.handleAdapters)

	return s
}

// ListenAndServe starts the HTTP server.
// This blocks until the server is stopped.
func (s *Server) ListenAndServe() error {
	addr := s.addr
	if !strings.HasPrefix(addr, ":") {
		addr = ":" + addr
	}

	fmt.Printf("Prizm Dashboard: http://localhost%s\n", addr)
	fmt.Println("Press Ctrl+C to stop")
	return http.ListenAndServe(addr, s.mux)
}

// Handler returns the HTTP handler for testing purposes.
func (s *Server) Handler() http.Handler {
	return s.mux
}

// runIDFromPath extracts a run ID from a URL path.
// For "/api/runs/run_01KRC7...", it returns "run_01KRC7...".
func runIDFromPath(path, prefix string) string {
	// Remove the prefix and any trailing slash
	remainder := strings.TrimPrefix(path, prefix)
	remainder = strings.TrimPrefix(remainder, "/")

	// Split on / to get the first segment (the run ID)
	parts := strings.SplitN(remainder, "/", 2)
	return parts[0]
}

// sanitizePath has been replaced by safety.ResolveAndContain (internal/safety).
// Path containment is now centralized in one auditable location.

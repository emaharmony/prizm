package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	prizmctx "github.com/emaharmony/prizm/internal/context"
	"github.com/emaharmony/prizm/internal/safety"
)

// This file implements the shared-workspace markdown editor: the *.md context
// files (SOUL.md, AGENTS.md, USER.md, IDENTITY.md, …) that agents inject via
// their `context:` selection. All paths are jailed under the workspace root with
// safety.ResolveAndContain and restricted to .md, and writes are atomic
// (temp + rename). Context is read cached at runtime, so edits apply on the next
// context build / `prizm serve` restart.

type workspaceFileInfo struct {
	Name      string `json:"name"`   // file name relative to workspace root
	Named     string `json:"named"`  // short context key if it's a NamedSource, else ""
	Exists    bool   `json:"exists"` // false for known files not yet created
	SizeBytes int64  `json:"size_bytes"`
}

// handleWorkspaceFiles lists editable markdown files under the workspace root,
// including the known NamedSources even when they don't exist yet (so the UI can
// offer to create them).
func (s *Server) handleWorkspaceFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.workspace == "" {
		writeJSONError(w, "workspace editing not available (no workspace configured)", http.StatusBadRequest)
		return
	}

	// filename -> named key ("" if not a NamedSource)
	seen := map[string]string{}
	for key, fname := range prizmctx.NamedSources {
		seen[fname] = key
	}

	files := map[string]workspaceFileInfo{}
	for fname, key := range seen {
		files[fname] = workspaceFileInfo{Name: fname, Named: key, Exists: false}
	}

	entries, err := os.ReadDir(s.workspace)
	if err != nil && !os.IsNotExist(err) {
		writeJSONError(w, "read workspace: "+err.Error(), http.StatusInternalServerError)
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			continue
		}
		info := files[e.Name()]
		info.Name = e.Name()
		info.Named = seen[e.Name()]
		info.Exists = true
		if fi, ferr := e.Info(); ferr == nil {
			info.SizeBytes = fi.Size()
		}
		files[e.Name()] = info
	}

	out := make([]workspaceFileInfo, 0, len(files))
	for _, f := range files {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, map[string]any{"workspace": s.workspace, "files": out})
}

// handleWorkspaceFile reads (GET) or writes (PUT) a single workspace markdown
// file. The name is taken from the path and jailed under the workspace root.
func (s *Server) handleWorkspaceFile(w http.ResponseWriter, r *http.Request) {
	if s.workspace == "" {
		writeJSONError(w, "workspace editing not available (no workspace configured)", http.StatusBadRequest)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/v1/workspace/files/")
	if name == "" {
		writeJSONError(w, "file name required", http.StatusBadRequest)
		return
	}
	if !strings.HasSuffix(strings.ToLower(name), ".md") {
		writeJSONError(w, "only .md files are editable", http.StatusBadRequest)
		return
	}
	absPath, err := safety.ResolveAndContain(s.workspace, name)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		data, err := os.ReadFile(absPath)
		if err != nil {
			if os.IsNotExist(err) {
				writeJSON(w, map[string]any{"name": name, "content": "", "exists": false})
				return
			}
			writeJSONError(w, "read file: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"name": name, "content": string(data), "exists": true})

	case http.MethodPut:
		var body struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, s.maxWorkspaceFileBytes)).Decode(&body); err != nil {
			writeJSONError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
			writeJSONError(w, "create dir: "+err.Error(), http.StatusInternalServerError)
			return
		}
		tmp := absPath + ".tmp"
		if err := os.WriteFile(tmp, []byte(body.Content), 0o644); err != nil {
			writeJSONError(w, "write temp: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := os.Rename(tmp, absPath); err != nil {
			os.Remove(tmp)
			writeJSONError(w, "rename: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"status": "saved", "name": name, "restart_required": true})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

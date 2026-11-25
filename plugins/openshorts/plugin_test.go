package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/tool"
	"github.com/emaharmony/prizm/internal/tool/mcp"
	"github.com/emaharmony/prizm/plugins/openshorts/internal/creatorvod"
)

func TestImportConfigAndInitializeState(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "prizm.yaml")
	state := filepath.Join(dir, "plugin-state")
	executable := filepath.Join(dir, "openshorts-prizm")
	initial := []byte("# Existing servers\nmcp_auto_approve: false\nmcp_servers:\n  - name: other\n    command: other-tool\n    enabled: true\n")
	if err := os.WriteFile(config, initial, 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := importPlugin(config, executable, state)
	if err != nil || !changed {
		t.Fatalf("first import: changed=%v err=%v", changed, err)
	}
	after, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(after, []byte("# Existing servers")) {
		t.Fatal("existing config comment was lost")
	}
	parsed, err := orchestrator.LoadConfigFromBytes(after)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.MCPAutoApprove || len(parsed.MCPServers) != 2 || parsed.MCPServers[0].Name != "other" {
		t.Fatalf("existing policy or server was altered: %+v", parsed.MCPServers)
	}
	added := parsed.MCPServers[1]
	if added.Name != serverName || added.Command != executable || !added.Enabled ||
		strings.Join(added.Args, "|") != "serve|--state-dir|"+state {
		t.Fatalf("wrong plugin registration: %+v", added)
	}
	for _, file := range []string{"creator-vod.db", "demo-effects.db", "manifests"} {
		if _, err := os.Stat(filepath.Join(state, file)); err != nil {
			t.Fatal(err)
		}
	}
	if changed, err := importPlugin(config, executable, state); err != nil || changed {
		t.Fatalf("re-import: changed=%v err=%v", changed, err)
	}
	if _, err := importPlugin(config, filepath.Join(dir, "different-executable"), state); err == nil {
		t.Fatal("import overwrote an existing OpenShorts definition")
	}
	final, err := os.ReadFile(config)
	if err != nil || !bytes.Equal(after, final) {
		t.Fatalf("re-import changed configuration: %v", err)
	}
}

func TestImportInitializesEmptyAndMissingServerList(t *testing.T) {
	for _, initial := range []string{"mcp_servers: []", "mcp_servers:", "mcp_auto_approve: false"} {
		newConfig, err := addRegistration([]byte(initial), registration{Name: serverName, Command: "test", Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		cfg, err := orchestrator.LoadConfigFromBytes(newConfig)
		if err != nil || len(cfg.MCPServers) != 1 || cfg.MCPServers[0].Name != serverName {
			t.Fatalf("invalid Prizm config after import: %v", err)
		}
	}
}

func TestFailedStateSetupLeavesConfigUntouched(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "prizm.yaml")
	original := []byte("mcp_servers: []\n")
	if err := os.WriteFile(config, original, 0o600); err != nil {
		t.Fatal(err)
	}
	// A file cannot act as the plugin's state directory.
	if _, err := importPlugin(config, "test", config); err == nil {
		t.Fatal("initialization unexpectedly succeeded")
	}
	got, err := os.ReadFile(config)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("failed initialization modified configuration: %v", err)
	}
}

func TestPrizmRegistersAndRunsImportedPlugin(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	dir := t.TempDir()
	call := func(client *mcp.StdioClient, name string, arguments map[string]any) mcp.CallResult {
		t.Helper()
		result, err := client.CallTool(ctx, name, arguments)
		if err != nil || result.IsError {
			t.Fatalf("%s failed: %s (%v)", name, result.Content, err)
		}
		return result
	}
	parse := func(result mcp.CallResult) creatorvod.Job {
		t.Helper()
		var response struct {
			Mode string         `json:"mode"`
			Job  creatorvod.Job `json:"job"`
		}
		if err := json.Unmarshal([]byte(result.Content), &response); err != nil || response.Mode != "mock" {
			t.Fatalf("unexpected plugin response: %+v, %v", response, err)
		}
		return response.Job
	}

	registry := tool.NewRegistry()
	client, closeServer := startServer(t, ctx, dir)
	defer closeServer()
	specs := []mcp.ServerSpec{{Name: serverName, Command: "plugin", Enabled: true}}
	result := mcp.RegisterServers(ctx, registry, specs, func(context.Context, mcp.ServerSpec) (mcp.InitClient, error) {
		return client, nil
	})
	if len(result) != 1 || result[0].Err != nil ||
		strings.Join(result[0].Tools, ",") != "mcp_openshorts_guide,mcp_openshorts_run_demo,mcp_openshorts_status" {
		t.Fatalf("Prizm registration failed: %+v", result)
	}
	if !strings.Contains(call(client, "guide", nil).Content, "OpenShorts") {
		t.Fatal("plugin guide was not exposed")
	}
	finished := parse(call(client, "run_demo", nil))
	if finished.State != creatorvod.Completed || len(finished.Exports) != 1 {
		t.Fatalf("demo did not reach verified mock completion: %+v", finished)
	}
	bad, err := client.CallTool(ctx, "run_demo", map[string]any{"source_path": "unexpected.mp4"})
	if err != nil || !bad.IsError {
		t.Fatalf("live media argument was accepted: %+v %v", bad, err)
	}
	closeServer()

	restarted, stopRestarted := startServer(t, ctx, dir)
	defer stopRestarted()
	if err := restarted.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	persisted := parse(call(restarted, "status", map[string]any{"job_id": finished.ID}))
	duplicate := parse(call(restarted, "run_demo", nil))
	if persisted.Revision != finished.Revision || duplicate.Revision != finished.Revision || duplicate.ID != finished.ID {
		t.Fatalf("restart did not preserve the demo: %+v %+v", persisted, duplicate)
	}
}

func startServer(t *testing.T, ctx context.Context, dir string) (*mcp.StdioClient, func()) {
	t.Helper()
	demo, err := creatorvod.OpenDemo(dir)
	if err != nil {
		t.Fatal(err)
	}
	pluginInput, clientOutput := io.Pipe()
	clientInput, pluginOutput := io.Pipe()
	finished := make(chan error, 1)
	go func() {
		finished <- serveMCP(ctx, demo, pluginInput, pluginOutput)
		_ = pluginOutput.Close()
	}()
	conn := mcp.NewConn(clientInput, clientOutput, func() error {
		return errors.Join(clientInput.Close(), clientOutput.Close())
	})
	client := mcp.NewStdioClient(conn)
	closed := false
	return client, func() {
		if closed {
			return
		}
		closed = true
		_ = client.Close()
		if err := <-finished; err != nil {
			t.Error(err)
		}
		if err := demo.Close(); err != nil {
			t.Error(err)
		}
	}
}

func TestImportRejectsInheritedServerList(t *testing.T) {
	original := []byte("defaults: &shared\n  mcp_servers:\n    - name: other\n      command: other-tool\n<<: *shared\n")
	if _, err := addRegistration(original, registration{Name: serverName, Command: "test", Enabled: true}); err == nil {
		t.Fatal("import shadowed the inherited MCP server list")
	}
}

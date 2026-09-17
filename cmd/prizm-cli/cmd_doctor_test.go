package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/validation"
)

func TestCheckWorkspace(t *testing.T) {
	dir := t.TempDir()
	if c := checkWorkspace(dir); c.status != statusOK {
		t.Fatalf("writable dir should be OK, got %s: %s", c.status, c.detail)
	}
	if c := checkWorkspace(filepath.Join(dir, "nope")); c.status != statusFail {
		t.Fatalf("missing dir should FAIL, got %s", c.status)
	}
	if c := checkWorkspace(""); c.status != statusWarn {
		t.Fatalf("empty workspace should WARN, got %s", c.status)
	}
}

func TestCheckProviderAuth(t *testing.T) {
	env := func(k string) string {
		if k == "ANTHROPIC_API_KEY" {
			return "sk-present"
		}
		return ""
	}
	agents := []orchestrator.AgentConfig{
		{Provider: "anthropic"}, // key present
		{Provider: "ollama"},    // no key needed
		{Provider: "claude_code"},
	}
	if c := checkProviderAuth(agents, env); c.status != statusOK {
		t.Fatalf("all creds present should be OK, got %s: %s", c.status, c.detail)
	}

	agents = append(agents, orchestrator.AgentConfig{Provider: "openai"}) // missing key
	c := checkProviderAuth(agents, env)
	if c.status != statusFail || !strings.Contains(c.detail, "OPENAI_API_KEY") {
		t.Fatalf("missing openai key should FAIL naming the env var, got %s: %s", c.status, c.detail)
	}

	if c := checkProviderAuth(nil, env); c.status != statusWarn {
		t.Fatalf("no agents should WARN, got %s", c.status)
	}
}

func TestCheckClaudeCodeNotConfigured(t *testing.T) {
	c := checkClaudeCode(orchestrator.ClaudeCodeConfig{}, nil, func(string) (string, error) {
		return "", errInjectedDoctor
	})
	if c.status != statusOK || !strings.Contains(c.detail, "not configured") {
		t.Fatalf("not configured should be OK, got %s: %s", c.status, c.detail)
	}
}

func TestCheckClaudeCodeAgentRequiresExecutable(t *testing.T) {
	c := checkClaudeCode(orchestrator.ClaudeCodeConfig{}, []orchestrator.AgentConfig{
		{ID: "astraea", Provider: "claude_code"},
	}, func(string) (string, error) {
		return "", errInjectedDoctor
	})
	if c.status != statusFail || !strings.Contains(c.detail, "Claude Code CLI not found") {
		t.Fatalf("missing claude should FAIL, got %s: %s", c.status, c.detail)
	}
}

func TestCheckClaudeCodeEnabledUsesExplicitExecutable(t *testing.T) {
	calls := []string{}
	c := checkClaudeCode(orchestrator.ClaudeCodeConfig{
		Enabled:    true,
		Executable: "C:/Tools/claude.exe",
	}, []orchestrator.AgentConfig{
		{ID: "astraea", Provider: "claude_code"},
	}, func(name string) (string, error) {
		calls = append(calls, name)
		if name == "C:/Tools/claude.exe" {
			return "C:/Tools/claude.exe", nil
		}
		return "", errInjectedDoctor
	})
	if c.status != statusOK {
		t.Fatalf("explicit claude executable should be OK, got %s: %s", c.status, c.detail)
	}
	if len(calls) != 1 || calls[0] != "C:/Tools/claude.exe" {
		t.Fatalf("expected only explicit executable lookup, got %v", calls)
	}
	if !strings.Contains(c.detail, "reviewer enabled") || !strings.Contains(c.detail, "astraea") {
		t.Fatalf("detail should describe uses, got %q", c.detail)
	}
}

func TestCheckValidationProfile(t *testing.T) {
	reg := validation.NewRegistry() // has go_test_all
	if c := checkValidationProfile(reg, "go_test_all"); c.status != statusOK {
		t.Fatalf("registered profile should be OK, got %s: %s", c.status, c.detail)
	}
	if c := checkValidationProfile(reg, "nonexistent_profile"); c.status != statusWarn {
		t.Fatalf("unknown profile should WARN, got %s", c.status)
	}
}

func TestCheckGitRemote(t *testing.T) {
	if c := checkGitRemote(".", func(string) bool { return true }); c.status != statusOK {
		t.Fatalf("remote present should be OK, got %s", c.status)
	}
	if c := checkGitRemote(".", func(string) bool { return false }); c.status != statusWarn {
		t.Fatalf("no remote should WARN, got %s", c.status)
	}
}

func TestCheckNATSEmbedded(t *testing.T) {
	if c := checkNATS(""); c.status != statusOK {
		t.Fatalf("empty URL (embedded) should be OK, got %s", c.status)
	}
}

func TestCheckRemembranceDisabled(t *testing.T) {
	c := checkRemembrance(orchestrator.RemembranceConfig{Enabled: false}, nil)
	if c.status != statusOK || !strings.Contains(c.detail, "disabled") {
		t.Fatalf("disabled remembrance should be OK/disabled, got %s: %s", c.status, c.detail)
	}
}

func TestWorstStatusAndRender(t *testing.T) {
	checks := []doctorCheck{
		{name: "a", status: statusOK, detail: "fine"},
		{name: "b", status: statusWarn, detail: "meh"},
	}
	if w, failed := worstStatus(checks); w != statusWarn || failed {
		t.Fatalf("warn+ok should be WARN, not failed; got %s failed=%v", w, failed)
	}
	out := renderDoctor(checks)
	if !strings.Contains(out, "OK with warnings") {
		t.Fatalf("expected warnings result line:\n%s", out)
	}

	checks = append(checks, doctorCheck{name: "c", status: statusFail, detail: "broken"})
	if w, failed := worstStatus(checks); w != statusFail || !failed {
		t.Fatalf("any fail should be FAIL+failed, got %s failed=%v", w, failed)
	}
	if out := renderDoctor(checks); !strings.Contains(out, "FAIL") {
		t.Fatalf("expected FAIL result line:\n%s", out)
	}

	allok := []doctorCheck{{name: "a", status: statusOK}}
	if !strings.Contains(renderDoctor(allok), "all systems go") {
		t.Fatalf("all OK should say all systems go")
	}
}

// Sanity: the probe file is cleaned up.
func TestCheckWorkspaceCleansProbe(t *testing.T) {
	dir := t.TempDir()
	checkWorkspace(dir)
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), "doctor-probe") {
			t.Fatalf("probe file left behind: %s", e.Name())
		}
	}
}

func TestDoctorToJSON(t *testing.T) {
	checks := []doctorCheck{
		{name: "workspace", status: statusOK, detail: "ok"},
		{name: "nats", status: statusWarn, detail: "unreachable"},
	}
	data, err := doctorToJSON(checks)
	if err != nil {
		t.Fatalf("doctorToJSON: %v", err)
	}
	var rep map[string]any
	if err := jsonUnmarshal(data, &rep); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if rep["result"] != "warn" {
		t.Fatalf("expected warn result, got %v", rep["result"])
	}
	checks = append(checks, doctorCheck{name: "x", status: statusFail, detail: "broken"})
	data, _ = doctorToJSON(checks)
	_ = jsonUnmarshal(data, &rep)
	if rep["result"] != "fail" {
		t.Fatalf("expected fail result, got %v", rep["result"])
	}
}

func TestCheckAutopatchPR(t *testing.T) {
	ghOK := func() (bool, bool, string) { return true, true, "" }
	ghNoAuth := func() (bool, bool, string) { return true, false, "" }
	ghMissing := func() (bool, bool, string) { return false, false, "" }

	// Not in pr mode → OK regardless of gh.
	if c := checkAutopatchPR(orchestrator.AutopatchConfig{Enabled: true, Mode: "propose"}, ghMissing); c.status != statusOK {
		t.Fatalf("propose mode should be OK, got %s", c.status)
	}
	if c := checkAutopatchPR(orchestrator.AutopatchConfig{Enabled: false}, ghMissing); c.status != statusOK {
		t.Fatalf("disabled should be OK, got %s", c.status)
	}
	// pr mode, gh present + authed → OK.
	if c := checkAutopatchPR(orchestrator.AutopatchConfig{Enabled: true, Mode: "pr"}, ghOK); c.status != statusOK {
		t.Fatalf("pr mode with gh authed should be OK, got %s: %s", c.status, c.detail)
	}
	// pr mode, gh present but not authed → WARN.
	if c := checkAutopatchPR(orchestrator.AutopatchConfig{Enabled: true, Mode: "pr"}, ghNoAuth); c.status != statusWarn {
		t.Fatalf("unauthed gh should WARN, got %s", c.status)
	}
	// pr mode, gh missing → FAIL.
	c := checkAutopatchPR(orchestrator.AutopatchConfig{Enabled: true, Mode: "pr"}, ghMissing)
	if c.status != statusFail || !strings.Contains(c.detail, "gh CLI") {
		t.Fatalf("missing gh in pr mode should FAIL, got %s: %s", c.status, c.detail)
	}
}

func TestCheckMCPServers(t *testing.T) {
	// none configured → OK
	if c := checkMCPServers(nil); c.status != statusOK {
		t.Fatalf("no servers should be OK, got %s", c.status)
	}
	// all valid + enabled → OK with counts
	ok := []orchestrator.MCPServerConfig{
		{Name: "fs", Command: "npx", Enabled: true},
		{Name: "off", Command: "x", Enabled: false},
	}
	c := checkMCPServers(ok)
	if c.status != statusOK || !strings.Contains(c.detail, "1 enabled / 2 configured") {
		t.Fatalf("expected counts, got %s: %s", c.status, c.detail)
	}
	// enabled but missing command → FAIL naming it
	bad := []orchestrator.MCPServerConfig{{Name: "broken", Command: "", Enabled: true}}
	if c := checkMCPServers(bad); c.status != statusFail || !strings.Contains(c.detail, "broken") {
		t.Fatalf("missing command should FAIL naming server, got %s: %s", c.status, c.detail)
	}
	// disabled-but-invalid is ignored (not a startup risk)
	if c := checkMCPServers([]orchestrator.MCPServerConfig{{Name: "", Command: "", Enabled: false}}); c.status != statusOK {
		t.Fatalf("disabled invalid server should not fail, got %s", c.status)
	}
}

func TestCheckWorkflowConfig(t *testing.T) {
	okValidate := func(string) ([]string, error) { return nil, nil }
	// no custom workflow → OK (built-in default)
	if c := checkWorkflowConfig("", okValidate); c.status != statusOK || !strings.Contains(c.detail, "built-in") {
		t.Fatalf("empty path should be OK/built-in, got %s: %s", c.status, c.detail)
	}
	// valid custom workflow → OK naming the path
	if c := checkWorkflowConfig("wf.yaml", okValidate); c.status != statusOK || c.detail != "wf.yaml" {
		t.Fatalf("valid workflow should be OK, got %s: %s", c.status, c.detail)
	}
	// load error (missing/unparseable file) → FAIL
	loadErr := func(string) ([]string, error) { return nil, errInjectedDoctor }
	if c := checkWorkflowConfig("missing.yaml", loadErr); c.status != statusFail {
		t.Fatalf("load error should FAIL, got %s", c.status)
	}
	// structural validation errors → FAIL listing them
	badValidate := func(string) ([]string, error) { return []string{"phase X: unknown gate"}, nil }
	c := checkWorkflowConfig("bad.yaml", badValidate)
	if c.status != statusFail || !strings.Contains(c.detail, "unknown gate") {
		t.Fatalf("validation errors should FAIL with detail, got %s: %s", c.status, c.detail)
	}
}

var errInjectedDoctor = errors.New("cannot load workflow")

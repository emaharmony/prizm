// Package main implements the `prizm doctor` subcommand: a preflight check that
// surfaces the silent-failure modes that otherwise only show up mid-run.
//
// Usage:
//
//	prizm doctor [--config prizm.yaml]
//
// It verifies the things an autonomous run depends on — workspace writability,
// provider credentials, a registered validation profile (so the verification gate
// can run), a git remote (so commits can push), NATS, and Remembrance — and prints
// a clear OK / WARN / FAIL report. Exit code is non-zero if any check FAILs, so it
// is usable in CI / startup scripts.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/claudecli"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/skill"
	"github.com/emaharmony/prizm/internal/validation"
	v2 "github.com/emaharmony/prizm/internal/workflow/v2"
	"github.com/nats-io/nats.go"
)

type checkStatus string

const (
	statusOK   checkStatus = "OK"
	statusWarn checkStatus = "WARN"
	statusFail checkStatus = "FAIL"
)

// doctorCheck is the result of a single preflight check.
type doctorCheck struct {
	name   string
	status checkStatus
	detail string
}

// providerKeyEnv maps an LLM provider to the env var holding its API key. A
// provider absent from the map needs no key (local or subscription-based).
var providerKeyEnv = map[string]string{
	"openai":           "OPENAI_API_KEY",
	"openai_responses": "OPENAI_API_KEY",
	"anthropic":        "ANTHROPIC_API_KEY",
	"gemini":           "GEMINI_API_KEY",
}

// checkWorkspace verifies the workspace root exists and is writable.
func checkWorkspace(path string) doctorCheck {
	c := doctorCheck{name: "workspace"}
	if path == "" {
		c.status, c.detail = statusWarn, "no workspace configured (context injection limited)"
		return c
	}
	info, err := os.Stat(path)
	if err != nil {
		c.status, c.detail = statusFail, fmt.Sprintf("%s: %v", path, err)
		return c
	}
	if !info.IsDir() {
		c.status, c.detail = statusFail, fmt.Sprintf("%s is not a directory", path)
		return c
	}
	probe := path + string(os.PathSeparator) + ".prizm-doctor-probe"
	if werr := os.WriteFile(probe, []byte("x"), 0o600); werr != nil {
		c.status, c.detail = statusWarn, fmt.Sprintf("%s not writable: %v", path, werr)
		return c
	}
	_ = os.Remove(probe)
	c.status, c.detail = statusOK, path
	return c
}

// checkProviderAuth verifies each configured agent's provider has the credential
// it needs. getenv is injected for testability.
func checkProviderAuth(agents []orchestrator.AgentConfig, getenv func(string) string) doctorCheck {
	c := doctorCheck{name: "provider auth"}
	if len(agents) == 0 {
		c.status, c.detail = statusWarn, "no agents configured"
		return c
	}
	var missing []string
	checked := map[string]bool{}
	for _, a := range agents {
		env, needs := providerKeyEnv[strings.ToLower(a.Provider)]
		if !needs || checked[a.Provider] {
			continue
		}
		checked[a.Provider] = true
		if getenv(env) == "" {
			missing = append(missing, fmt.Sprintf("%s ($%s)", a.Provider, env))
		}
	}
	if len(missing) > 0 {
		c.status, c.detail = statusFail, "missing credentials for: "+strings.Join(missing, ", ")
		return c
	}
	c.status, c.detail = statusOK, "all configured providers have credentials"
	return c
}

// checkClaudeCode verifies the local Claude Code CLI is available when either
// the reviewer is enabled or at least one agent uses provider: claude_code.
func checkClaudeCode(cfg orchestrator.ClaudeCodeConfig, agents []orchestrator.AgentConfig, lookPath claudecli.LookPathFunc) doctorCheck {
	c := doctorCheck{name: "claude code"}
	agentIDs := claudeCodeAgentIDs(agents)
	if !cfg.Enabled && len(agentIDs) == 0 {
		c.status, c.detail = statusOK, "not configured"
		return c
	}

	path, err := claudecli.ResolveExecutable(cfg.Executable, lookPath)
	if err != nil {
		c.status, c.detail = statusFail, err.Error()
		return c
	}

	var refs []string
	if cfg.Enabled {
		refs = append(refs, "reviewer enabled")
	}
	if len(agentIDs) > 0 {
		refs = append(refs, "agents: "+strings.Join(agentIDs, ", "))
	}
	detail := path
	if len(refs) > 0 {
		detail = fmt.Sprintf("%s (%s)", path, strings.Join(refs, "; "))
	}
	c.status, c.detail = statusOK, detail
	return c
}

func claudeCodeAgentIDs(agents []orchestrator.AgentConfig) []string {
	var ids []string
	for _, a := range agents {
		if strings.EqualFold(a.Provider, "claude_code") {
			if a.ID != "" {
				ids = append(ids, a.ID)
			} else {
				ids = append(ids, "(unnamed)")
			}
		}
	}
	return ids
}

// checkValidationProfile verifies a verification profile is registered, so the
// EXECUTION verification gate has something to run.
func checkValidationProfile(reg *validation.Registry, profile string) doctorCheck {
	c := doctorCheck{name: "validation profile"}
	if profile == "" {
		profile = "go_test_all"
	}
	if _, err := reg.Resolve(profile); err != nil {
		c.status, c.detail = statusWarn, fmt.Sprintf("profile %q not registered; verification gate will be skipped", profile)
		return c
	}
	c.status, c.detail = statusOK, profile+" registered"
	return c
}

// checkGitRemote reports whether the repo has a remote (so commits can push).
// remoteFn is injected for testability (defaults to repoHasRemote).
func checkGitRemote(repoPath string, remoteFn func(string) bool) doctorCheck {
	c := doctorCheck{name: "git remote"}
	if remoteFn(repoPath) {
		c.status, c.detail = statusOK, "remote configured (push enabled)"
	} else {
		c.status, c.detail = statusWarn, "no git remote; loop runs with push disabled"
	}
	return c
}

// checkNATS reports bus reachability. An empty URL is the embedded-NATS case
// (started by `prizm serve`), which is fine.
func checkNATS(url string) doctorCheck {
	c := doctorCheck{name: "nats"}
	if url == "" {
		c.status, c.detail = statusOK, "embedded (started by `prizm serve`)"
		return c
	}
	conn, err := nats.Connect(url, nats.Timeout(2*time.Second), nats.MaxReconnects(0))
	if err != nil {
		c.status, c.detail = statusWarn, fmt.Sprintf("%s unreachable: %v", url, err)
		return c
	}
	conn.Close()
	c.status, c.detail = statusOK, url
	return c
}

// checkRemembrance reports memory-service reachability when enabled.
func checkRemembrance(cfg orchestrator.RemembranceConfig, get func(string) (*http.Response, error)) doctorCheck {
	c := doctorCheck{name: "remembrance"}
	if !cfg.Enabled {
		c.status, c.detail = statusOK, "disabled"
		return c
	}
	if cfg.URL == "" {
		c.status, c.detail = statusWarn, "enabled but no url configured"
		return c
	}
	resp, err := get(strings.TrimRight(cfg.URL, "/") + "/health")
	if err != nil {
		c.status, c.detail = statusWarn, fmt.Sprintf("%s unreachable: %v", cfg.URL, err)
		return c
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		c.status, c.detail = statusWarn, fmt.Sprintf("%s returned %s", cfg.URL, resp.Status)
		return c
	}
	c.status, c.detail = statusOK, cfg.URL
	return c
}

// checkAutopatchPR verifies the `gh` CLI is installed and authenticated when
// autopatch is configured to open PRs, so `prizm scan --start` doesn't fail only
// at push/PR time. ghStatus is injected for testability: it returns whether gh is
// available and whether it is authenticated.
func checkAutopatchPR(cfg orchestrator.AutopatchConfig, ghStatus func() (available, authed bool, detail string)) doctorCheck {
	c := doctorCheck{name: "autopatch pr"}
	if !cfg.Enabled || cfg.Mode != "pr" {
		c.status, c.detail = statusOK, "not in pr mode"
		return c
	}
	available, authed, detail := ghStatus()
	if !available {
		c.status, c.detail = statusFail, "pr mode needs the gh CLI, which is not installed"
		return c
	}
	if !authed {
		c.status, c.detail = statusWarn, "gh installed but not authenticated (run `gh auth login`)"
		return c
	}
	if detail == "" {
		detail = "gh installed and authenticated"
	}
	c.status, c.detail = statusOK, detail
	return c
}

// checkMCPServers validates the configured MCP servers: an enabled server must
// have both a name and a command, or it will silently fail to register at serve
// startup. Reports the enabled/total counts on success.
func checkMCPServers(servers []orchestrator.MCPServerConfig) doctorCheck {
	c := doctorCheck{name: "mcp servers"}
	if len(servers) == 0 {
		c.status, c.detail = statusOK, "none configured"
		return c
	}
	enabled := 0
	var bad []string
	for _, s := range servers {
		if !s.Enabled {
			continue
		}
		enabled++
		if strings.TrimSpace(s.Name) == "" || strings.TrimSpace(s.Command) == "" {
			label := s.Name
			if label == "" {
				label = "(unnamed)"
			}
			bad = append(bad, label)
		}
	}
	if len(bad) > 0 {
		c.status, c.detail = statusFail, fmt.Sprintf("enabled server(s) missing name/command: %s", strings.Join(bad, ", "))
		return c
	}
	c.status, c.detail = statusOK, fmt.Sprintf("%d enabled / %d configured", enabled, len(servers))
	return c
}

// checkWorkflowConfig validates a custom gated-loop workflow file when one is set
// via prizm.workflow_config, so a broken/missing workflow surfaces here rather
// than at serve startup. The validate func is injected for testability.
func checkWorkflowConfig(path string, validate func(string) (errs []string, loadErr error)) doctorCheck {
	c := doctorCheck{name: "workflow config"}
	if strings.TrimSpace(path) == "" {
		c.status, c.detail = statusOK, "built-in default gated loop"
		return c
	}
	errs, loadErr := validate(path)
	if loadErr != nil {
		c.status, c.detail = statusFail, fmt.Sprintf("%s: %v", path, loadErr)
		return c
	}
	if len(errs) > 0 {
		c.status, c.detail = statusFail, fmt.Sprintf("%s: %s", path, strings.Join(errs, "; "))
		return c
	}
	c.status, c.detail = statusOK, path
	return c
}

// checkSkills reports how many SKILL.md skills were discovered under root. count
// is injected for testability (production passes a workspace skill scan). It is
// informational (always OK) — skills are optional.
func checkSkills(count int, loadErr error) doctorCheck {
	c := doctorCheck{name: "skills"}
	if loadErr != nil {
		c.status, c.detail = statusWarn, fmt.Sprintf("%d loaded, with issues: %v", count, loadErr)
		return c
	}
	if count == 0 {
		c.status, c.detail = statusOK, "none configured (add SKILL.md under .claude/skills, .openclaw/skills, or skills/)"
		return c
	}
	c.status, c.detail = statusOK, fmt.Sprintf("%d skill(s) discovered", count)
	return c
}

// validateWorkflowFile loads a v2 workflow file and returns its structural errors.
func validateWorkflowFile(path string) ([]string, error) {
	wf, err := v2.LoadConfig(path)
	if err != nil {
		return nil, err
	}
	return v2.ValidateConfig(wf), nil
}

// ghStatusReal reports gh availability/auth by shelling out to `gh auth status`.
func ghStatusReal() (bool, bool, string) {
	if _, err := exec.LookPath("gh"); err != nil {
		return false, false, ""
	}
	out, err := exec.Command("gh", "auth", "status").CombinedOutput()
	if err != nil {
		return true, false, strings.TrimSpace(string(out))
	}
	return true, true, "gh installed and authenticated"
}

// worstStatus returns the most severe status across checks and whether any failed.
func worstStatus(checks []doctorCheck) (checkStatus, bool) {
	worst := statusOK
	failed := false
	for _, c := range checks {
		switch c.status {
		case statusFail:
			worst, failed = statusFail, true
		case statusWarn:
			if worst != statusFail {
				worst = statusWarn
			}
		}
	}
	return worst, failed
}

func statusGlyph(s checkStatus) string {
	switch s {
	case statusOK:
		return "✅"
	case statusWarn:
		return "⚠️ "
	default:
		return "❌"
	}
}

// doctorCheckJSON / doctorReportJSON are the machine-readable view for `--json`.
type doctorCheckJSON struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type doctorReportJSON struct {
	Checks []doctorCheckJSON `json:"checks"`
	Result string            `json:"result"` // ok | warn | fail
}

// doctorToJSON marshals the checks plus an overall result (pure, testable).
func doctorToJSON(checks []doctorCheck) ([]byte, error) {
	rep := doctorReportJSON{Checks: make([]doctorCheckJSON, 0, len(checks))}
	for _, c := range checks {
		rep.Checks = append(rep.Checks, doctorCheckJSON{Name: c.name, Status: string(c.status), Detail: c.detail})
	}
	worst, failed := worstStatus(checks)
	switch {
	case failed:
		rep.Result = "fail"
	case worst == statusWarn:
		rep.Result = "warn"
	default:
		rep.Result = "ok"
	}
	return json.MarshalIndent(rep, "", "  ")
}

// renderDoctor formats the check report.
func renderDoctor(checks []doctorCheck) string {
	var b strings.Builder
	b.WriteString("🩺 prizm doctor\n")
	b.WriteString(strings.Repeat("─", 56) + "\n")
	for _, c := range checks {
		fmt.Fprintf(&b, "  %s %-20s %s\n", statusGlyph(c.status), c.name, c.detail)
	}
	worst, failed := worstStatus(checks)
	b.WriteString(strings.Repeat("─", 56) + "\n")
	switch {
	case failed:
		b.WriteString("Result: FAIL — fix the ❌ checks before running.\n")
	case worst == statusWarn:
		b.WriteString("Result: OK with warnings — the loop will run but some features are limited.\n")
	default:
		b.WriteString("Result: all systems go.\n")
	}
	return b.String()
}

// executeDoctor is the `prizm doctor` entry point.
func executeDoctor(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	configPath := fs.String("config", "prizm.yaml", "Path to prizm.yaml configuration file")
	asJSON := fs.Bool("json", false, "Emit the report as JSON")
	fs.Parse(args)

	cfg, err := orchestrator.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ config: %v\n", err)
		os.Exit(1)
	}

	repoPath := cfg.Prizm.Workspace
	if repoPath == "" {
		repoPath = "."
	}

	checks := []doctorCheck{
		checkWorkspace(cfg.Prizm.Workspace),
		checkProviderAuth(cfg.Agents, os.Getenv),
		checkClaudeCode(cfg.ClaudeCode, cfg.Agents, exec.LookPath),
		checkValidationProfile(validation.NewRegistry(), ""),
		checkGitRemote(repoPath, repoHasRemote),
		checkNATS(cfg.Prizm.NATSURL),
		checkRemembrance(cfg.Remembrance, func(u string) (*http.Response, error) {
			client := &http.Client{Timeout: 2 * time.Second}
			return client.Get(u)
		}),
		checkAutopatchPR(cfg.Autopatch, ghStatusReal),
		checkMCPServers(cfg.MCPServers),
		checkWorkflowConfig(cfg.Prizm.WorkflowConfig, validateWorkflowFile),
		func() doctorCheck {
			reg := skill.NewRegistry()
			n, lerr := reg.LoadDefault(repoPath)
			return checkSkills(n, lerr)
		}(),
	}

	if *asJSON {
		data, mErr := doctorToJSON(checks)
		if mErr != nil {
			fmt.Fprintf(os.Stderr, "❌ %v\n", mErr)
			os.Exit(1)
		}
		fmt.Println(string(data))
	} else {
		fmt.Print(renderDoctor(checks))
	}
	if _, failed := worstStatus(checks); failed {
		os.Exit(1)
	}
}

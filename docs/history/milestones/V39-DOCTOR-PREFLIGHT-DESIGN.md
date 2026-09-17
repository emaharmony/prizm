# V39 — `prizm doctor`: Preflight Health Check

**Status:** Source-current
**Last Updated:** 2026-06-29

## Problem

The architecture audit surfaced several *silent-failure* modes: a run would start
and only fail deep into EXECUTION because Remembrance was down, a provider API key
was missing, there was no git remote to push to, or no validation profile was
registered (so the verification gate silently no-ops). The user had no way to check
readiness up front.

## Design

`prizm doctor` runs a set of checks against `prizm.yaml` and the environment and
prints an OK / WARN / FAIL report. Exit code is non-zero when any check **FAILs**,
so it works in CI and startup scripts.

```
prizm doctor [--config prizm.yaml]
```

### Checks

| Check | FAIL when | WARN when |
|-------|-----------|-----------|
| workspace | path set but missing / not a dir | empty, or not writable |
| provider auth | a configured provider's API-key env var is unset | no agents configured |
| validation profile | — | the verification profile isn't registered (gate will skip) |
| git remote | — | no remote (loop runs with push disabled) |
| nats | — | a configured `nats_url` is unreachable (empty = embedded, OK) |
| remembrance | — | enabled but unreachable / no URL |
| autopatch pr | `gh` CLI missing while autopatch is in `pr` mode | `gh` installed but not authenticated |
| mcp servers | an *enabled* MCP server is missing its name/command | — |
| workflow config | `workflow_config` file is missing/unparseable or fails v2 validation | — |

The provider→key mapping is `openai→OPENAI_API_KEY`, `anthropic→ANTHROPIC_API_KEY`,
`gemini→GEMINI_API_KEY`; local/subscription providers (ollama, mock, claude_code)
need no key. Most issues are WARN, not FAIL — the loop can still run with reduced
capability (e.g. no push, no memory); only a hard misconfiguration (missing repo
dir, missing required API key) FAILs.

### Testability

The check functions are pure: each takes its inputs (config slice, an injected
`getenv`/`remoteFn`/HTTP getter) and returns a `doctorCheck`, so the full matrix is
unit-tested without touching real services. `worstStatus` and `renderDoctor` are
likewise pure. Only `executeDoctor` performs live I/O (NATS connect, Remembrance
HTTP), with short timeouts.

## Tests

`cmd/prizm-cli/cmd_doctor_test.go`: workspace (ok/fail/warn + probe cleanup),
provider auth (present / missing-names-env-var / no-agents), validation profile
(registered / unknown), git remote (present / absent), NATS embedded, Remembrance
disabled, and `worstStatus`/`renderDoctor` severity + result lines.

## Follow-ups (UX roadmap)

Remaining brainstormed items: rich Discord approval cards, diff preview at feedback
gates, dry-run/plan preview, a durable `runs/<id>/REPORT.md` artifact, and
gate-needs-you notifications.

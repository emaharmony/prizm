# Loop Readiness State

Cross-cycle state file for the `/prizm-loop` autonomous cycle (see
`.claude/commands/prizm-loop.md`). The loop reads this file at the start of
every cycle, works the topmost unchecked backlog item (BUILDER mode), and
switches to driving Prizm's own harness (OPERATOR mode) once the backlog is
empty. Appends one line to the cycle log per cycle.

Origin: Lumi's "Loop Engineering: Core Concepts & Prizm Readiness" report
(2026-07-04). Of its six gaps, four were already closed on `staging`
(independent verifier V35, token budgets/retry V35, SKILL.md skills V54,
MCP client V49). The backlog below is what remains.

## Backlog (ordered — take the topmost unchecked item)

- [x] **V56 — Gated-loop worktree isolation**: DONE 2026-07-04 (`internal/gitx`
  package, `projects[].worktree_isolation`, fail-closed worktree-per-run in
  `RunGatedLoop`; docs/V56-WORKTREE-ISOLATION-DESIGN.md).
- [x] **V57 — Automated rollback**: DONE 2026-07-04 (`global.auto_rollback` +
  `max_verification_attempts`, `Engine.SetRollbackRunner`, three triggers,
  `workflow.rollback` event, `rolled_back` status;
  docs/V57-AUTO-ROLLBACK-DESIGN.md).
- [x] **Wiring fix — `prizm.ollama_url` in serve mode**: DONE 2026-07-04 as
  part of a 10-fix wiring sweep (shared `resolveOllamaURL` precedence:
  flag → OLLAMA_BASE_URL → config → default). Sweep also fixed: autopatch
  `pr` mode rejected at validation (V50 was unreachable), serve ignoring
  `prizm.port`, doctor missing `openai_responses`, provider-registry silent
  overwrite, `prizm run` context flags discarded, gemini `base_url` in
  OpenClaw import, Remembrance default-URL drift, and undocumented
  config sections in prizm.yaml.example.
- [x] **Per-phase token budgets**: DONE 2026-07-04 (`phases[].max_tokens`,
  `phase.budget_exhausted`, per-phase token accounting in run state).
- [ ] *(stretch)* **Enforce per-skill `allowed-tools`**: advisory-only in the
  prompt today (`internal/skill/`, V54 follow-up).
- [ ] *(stretch)* **MCP streamable-HTTP transport**: client is stdio-only
  (`internal/tool/mcp/stdio.go`, V49 follow-up).

## Cycle log

<!-- One line per cycle, appended by the loop. Format:
YYYY-MM-DD HH:MM | BUILDER|OPERATOR | what shipped / was found | verification | next -->
2026-07-04 20:20 | BUILDER | 10-fix wiring sweep (a341380): pr mode reachable, ollama_url live, port/doctor/registry/context/gemini/remembrance/yaml-docs | go build+vet+test green | V56
2026-07-04 20:40 | BUILDER | V56 worktree isolation (f0407df): internal/gitx + projects[].worktree_isolation, fail-closed | go build+vet+test green + gitx tests vs real temp repos | V57
2026-07-04 20:55 | BUILDER | V57 auto-rollback (bdaee16): 3 triggers, rolled_back status, runs --json rollback field | go build+vet+test green, 7 new driver tests | per-phase budgets
2026-07-04 21:05 | BUILDER | Per-phase token budgets (10dcb46): phases[].max_tokens + phase.budget_exhausted | go build+vet+test green | docs/ROADMAP sync, then backlog = stretch items only → OPERATOR next
2026-07-09 21:33 | BUILDER | Token-limit completion: explicit -1 unlimited sentinel, default run/project/context ceilings, delegated remaining budgets, real cost/report/API surfaces | go build ./... + go vet ./... + go test -p 1 ./... green | commit token-limit branch

# V58 — Full Autonomy: Generic Sub-Agent Worker

Status: in-progress (built incrementally via the autonomous builder loop)
Started: 2026-07-05

## Goal

Promote the multi-agent roster from "orchestrator-routed" (one brain adopts
roles / calls tools) to **genuinely autonomous sub-agents** that run
independently and report back. This is the deferred v2 piece from the Roblox
team design (see docs/ROBLOX-TEAM.md, [[roblox-agent-team]]).

Today the delegation path is half-built:
- **Send** exists: `v2.DelegationManager.DelegateTask` builds a `TaskPacket`,
  the wake handler publishes it via `NATSPublisher` to the delegation subject.
- **Completion routing** exists: completions on `prizm.workflow.task.complete`
  → `NATSListener` → `ExternalEvent{task_complete}` → `HandleTaskCompletion`.
- **MISSING (the consumer):** nothing subscribes to the delegation subject,
  runs the target agent, and publishes a `TaskCompletion`. The serve-side
  handler is a stub (`cmd_serve.go:449` "Task processing will be wired in
  M3.1d"). Only Codex + cross-Prizm are real runners.

V58 builds that consumer as a production-grade, testable package and wires it in.

## Architecture

`internal/subagent` — a transport-agnostic worker that turns a `TaskPacket`
into a `TaskCompletion`:

```
delegation subject ──▶ Worker.Handle(packet)
                          ├─ resolve target agent (provider/model/tools/caps)
                          ├─ run a bounded execution (TaskRunner)
                          └─ build TaskCompletion (artifacts/summary/status)
                       ──▶ publish on completionSubject
```

Key seams (all interfaces, so each layer is unit-testable and swappable):
- `AgentResolver.Resolve(agentID)` → the agent's runtime (or not-found).
- `TaskRunner.Run(ctx, packet, runtime)` → artifacts + summary (the pluggable
  execution backend: a mock in tests; a real bounded tool-loop / RunGatedLoop
  slice in later iterations).
- `Publisher.PublishCompletion(completion)` → transport (NATS in serve; capture
  in tests).

Design principles for production/long-term:
- **Fail closed & always report:** unknown agent, runner error, or timeout each
  yield a `status:"failed"` completion — a dead sub-agent never silently holds a
  gate (mirrors the existing timeout/retry semantics in `delegation.go`).
- **Bounded:** every run honors the packet `Deadline`; the worker enforces a
  context timeout so a runaway sub-agent can't run forever.
- **Isolation-ready:** parallel sub-agents reuse V56 worktree isolation + V57
  rollback + per-run budgets already in the tree.
- **No behavior change until wired:** the package lands and is tested before any
  serve wiring flips the stub, so each iteration keeps a green build.

## Iterations (loop-tracked)

- [x] **1 — Foundation:** `internal/subagent` package: `Worker`, the three
  interfaces, `Handle` with resolve/run/timeout/failure paths, and a mock-based
  smoke test. No serve wiring. (this fire)
- [x] **2 — Real runner:** `LoopRunner` (a `TaskRunner`) — bounded single-agent
  tool loop with iteration + token budgets, tool-failure feedback, artifact
  capture, and context-deadline honoring. Model/tool specifics injected via a
  `Backend` (mock in tests; provider registry + tool executor in iteration 3).
- [x] **3 — Serve wiring:** `cmd/prizm-cli/subagent_worker.go` — resolver (config
  agents → runtime), backend (provider registry + tool executor + reused v2
  parsers via new exported `ParseToolRequestText`/`ParseFinalText`), NATS
  completion publisher, and a subscription on the delegation subject that runs
  task packets through the worker. Feature-flagged via `PRIZM_SUBAGENT_WORKER`
  (off by default → no behavior change; verified on/off in serve).
- [x] **4 — Tool scoping + concurrency:** `ToolScope` (capability-based per-agent
  tool gating — only "code"-capable agents may mutate/git-write/use MCP build
  tools; researchers/planners stay read-only), enforced in the runner (denied
  tools are fed back, never executed) and wired in serve; bounded concurrent
  runs (semaphore, cap 4). Denials are non-fatal so the agent adapts.
- [x] **4b — Capability-aware routing:** `PlanTask.Capability` →
  `TaskPacket.RequiredCapability` (via `BuildTaskPacket`); the Worker fails a
  task closed when the target agent lacks the required capability. Empty =
  no requirement (backward compatible).
- [x] **4c — Worktree-per-subagent:** `WorktreeProvider` (gitx-backed
  `GitWorktreeProvider` + `NoopWorktreeProvider`). The Worker acquires a
  per-task worktree on branch `subagent/<task>` for code-capable agents only,
  threads its dir into the runtime, and releases it on completion; serve points
  git tools at it via `repo_path`. Non-mutating agents skip isolation.
  (git isolated at this stage; full write isolation completed in 4d.)
- [x] **4d — Full write isolation:** per-run executor rooted at the worktree so
  `write_file`/`create_directory` are isolated too (see change report). Closes
  the 4c limitation.
- [x] **5a — Report-only / safe run (Eggventura):** end-to-end delegation smoke
  over real embedded NATS (packet → worker → LoopRunner → completion, incl.
  fail-closed), plus a full-system boot with `PRIZM_SUBAGENT_WORKER=1` targeting
  the Eggventura project (agents + worker + health up). No writes.
- [x] **5b — Implementation run (Eggventura): DONE (2026-07-05).** Full complete-
  system run proven end-to-end. External NATS bus + serve (claude_code
  orchestrator, Factory→Eggventura) + signed cross-Prizm `task_request` →
  Prizm Factory adapter queued it → Python Factory processed it.
  **Report-only run** first: returned an accurate, project-specific multi-agent
  audit (Planner + Reviewer + Luau safety) of the real Eggventura Rojo project
  (exit 0). Gate met → **implementation run** (`approval_mode: implementation`,
  `run_codex: true`): Codex created `src/ReplicatedStorage/tools/BuildInfo.luau`
  (correct idiomatic Luau via `DateTime.now():ToIsoDate()`), purely additive —
  no existing file modified, HEAD unchanged at baseline `0092d6b`. The new file
  is left untracked for the user to keep (commit) or discard (delete). Test
  harness (`cmd/factory-trigger`) was throwaway and removed.

  Historical note (pre-run):
  User authorized; verified: Factory Python running, Eggventura is a real Rojo
  git repo (baseline `0092d6b`, clean), claude CLI authed, and the dedicated
  Eggventura serve **boots write-enabled** (astraea+atlas on claude_code,
  factory→Eggventura `implementation`+`run_codex`). Blockers to a *fully
  unattended* Codex-write run (each surmountable, but together too risky to
  gamble unattended on the real project):
  1. Serve tool registration (incl. `send_cross_message` + the sub-agent worker)
     is coupled to having a messaging channel configured — no channel → not
     registered (the documented serve coupling).
  2. Default gated loop pauses at FEEDBACK_PRE (no `auto_approve`) — needs a
     fast-loop/auto-approve workflow config to run unattended.
  3. Gated-loop verification profile is `go_test_all` (Go-oriented) — wrong for
     a Roblox/Luau project; the real Roblox executor is the Factory (Rojo/Codex),
     triggered via `send_cross_message`, not the gated loop.
  4. The Factory handoff depends on the LLM emitting the right `send_cross_message`
     call (non-deterministic).
  Recommended completion: run it eyes-on (auto-approve + channel-configured
  dedicated serve, watch inbox→outbox→Eggventura git, revert to `0092d6b` if
  needed), OR use the Factory's documented direct-inbox handoff (the runbook's
  recommended path for `run_codex` implementation runs).

## Loop conclusion (2026-07-05)

The autonomous builder loop delivered the complete, tested, feature-flagged
autonomy stack (iterations 1–5a). It then **stopped deliberately** rather than
force the two remaining items under a 3-minute timer:

- **4d (full write isolation)** is a real refactor, not a quick slice: the write
  tools (`write_file*`, `create_directory*`) are bound to the executor's fixed
  `WorkspaceRoot`/`AllowedPaths` at registration. Correct fix = build a **per-run
  tool executor rooted at the acquired worktree** (reusing `RegisterBuiltinsWithRoots`
  with read/write roots = the worktree path) and run that task's tools through it,
  while sharing the process-wide MCP clients. This touches path-jailing security
  and MCP-client lifetime, so it warrants careful (non-rushed) design + tests —
  not a timed autocommit. Until then, **git operations are worktree-isolated;
  `write_file` uses the shared root.** Note this is low-risk in practice: the
  worker is off by default, and real Roblox code-writing flows through the
  Factory/Codex path, which has its own autopatch worktree isolation.
- **5b (write-enabled Eggventura run)** is gated on explicit user authorization
  + the Python Factory running (see the iteration list).

To resume: reply "proceed with 5b" (Factory up) for the real run, or re-run
`/loop` to build 4d as its own carefully-scoped change.

## Report of changes (append per iteration)

- **Iteration 1 (2026-07-05):** Added `internal/subagent` (worker + interfaces +
  smoke tests). Transport/runtime-agnostic; green build, no serve behavior change.
- **Iteration 2 (2026-07-05):** Added `LoopRunner` — the real bounded tool-loop
  `TaskRunner` (iteration + token budgets, tool-failure feedback, artifact
  capture, deadline honoring) with a `Backend` seam. 8 runner tests incl. an
  end-to-end Worker+LoopRunner path. Still no serve wiring; green build.
- **Iteration 3 (2026-07-05):** Wired the worker into serve behind
  `PRIZM_SUBAGENT_WORKER` (off by default): config→runtime resolver, a backend
  bound to the live provider registry + tool executor reusing the gated loop's
  JSON parsers (newly exported `v2.ParseToolRequestText`/`ParseFinalText`), and
  a NATS completion publisher. Subscribes the delegation subject and filters for
  task_delegation packets (ignores inter-agent chat on the same subject).
  Verified the flag on/off in a live serve boot; resolver + parser unit tests
  added. Green build.
- **Iteration 4 (2026-07-05):** Added per-agent tool scoping (`ToolScope` /
  `CapabilityToolScope`) — role-gated tools (mutation, git-write, mcp_*) require
  the "code" capability; enforced in the runner (denied → fed back, not
  executed) and wired in serve. Bounded concurrent sub-agent runs (semaphore).
  4 scope tests incl. deny/allow-in-role runner paths. Green build.
- **Iteration 4b (2026-07-05):** Capability-aware routing — `PlanTask.Capability`
  flows to `TaskPacket.RequiredCapability`; the Worker fails a task closed if the
  target agent lacks it (empty = no requirement, backward compatible). Tests for
  the packet propagation and worker gate (missing/satisfied). Green build.
- **Iteration 4c (2026-07-05):** Worktree-per-subagent isolation. `WorktreeProvider`
  (gitx-backed + noop); Worker acquires a per-task worktree on `subagent/<task>`
  for code-capable agents, threads it into the runtime, releases on completion;
  serve injects it as git `repo_path`. 5 tests incl. a real gitx temp-repo
  acquire/release and non-code-skips-isolation. Green build. (write-tool full
  isolation → 4d.)
- **Iteration 4d (2026-07-05):** Full write isolation. `subAgentBackend.executorFor`
  builds a per-run tool executor rooted at the task's worktree (builtins + write
  + git tools rooted there; shared root-agnostic tools — research/image/skill/
  MCP/state/plan — copied from the main registry, same instances so MCP clients
  stay live). Now `write_file`/`create_directory` are isolated to the worktree,
  not just git. Empty worktree → shared executor (unchanged). Test proves
  `list_dir` on the per-run executor sees the worktree, not the shared root.
- **Iteration 5a (2026-07-05):** End-to-end smoke over real embedded NATS
  (`bus.StartEmbeddedBus`): a delegation packet round-trips packet → worker →
  LoopRunner → completion, plus a fail-closed (unknown-agent) case. Verified the
  complete system boots with `PRIZM_SUBAGENT_WORKER=1` pointed at the Eggventura
  project (agents registered, worker started, health up) — report-only, no
  writes. Write-enabled run (5b) held for explicit authorization.

## Verification
`go build ./... && go vet ./... && go test ./...` green each iteration. Smoke
tests per iteration exercise the new slice end-to-end with mocks; iteration 5 is
the live Eggventura run.

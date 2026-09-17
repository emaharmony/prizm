# Prizm Task Tracker

**Last Updated:** 2026-06-09
**Status:** Source is current through V34-era work. This tracker reflects living state, not historical release tags.

---

## Legend

- Done - implemented in current source
- Active - implemented or partly implemented, still being hardened
- Gap - explicitly not implemented or not wired end-to-end
- Future - intentionally deferred

---

## Completed Runtime Base

| Area | Status | Notes |
|------|--------|-------|
| Persistent daemon | Done | `prizm serve`, health server, graceful shutdown, config loading |
| Embedded/external NATS | Done | Empty `prizm.nats_url` means embedded NATS in serve mode |
| Sessions | Done | SQLite sessions, idle timeout, compaction settings |
| Agent routing | Done | Direct addressing, mention routing, primary fallback |
| Discord adapter | Done | Gateway bot, sends/replies/splits long messages |
| Actions | Done | YAML actions with wildcard trigger matching |
| One-shot run mode | Done | `prizm run --task ...` lifecycle and artifacts |
| Interactive chat | Done | `prizm chat` with config, tools, state, and plans |

---

## Platform and API

| Area | Status | Notes |
|------|--------|-------|
| HTTP API | Done | Status, agents, sessions, tasks, approvals, workflows, editor, costs |
| SSE event stream | Done | `/api/v1/events/stream` |
| Dashboard | Done | Static dashboard served with API data |
| Visual diagrams | Done | Workflow/SVG diagram endpoints |
| Visual editor model/API | Done | Editor state, node/edge CRUD, validate/save route |
| API auth middleware | Future | Local API currently assumes trusted localhost usage |

---

## Tools, Policy, and Guarding

| Area | Status | Notes |
|------|--------|-------|
| Built-in file tools | Done | `list_dir`, `read_file`, dry-run write tools |
| Project tools | Done | `read_project`, `search_files`, `project_overview` |
| Git read tools | Done | `git_status`, `git_log`, `git_diff`, `git_branch_list` |
| Git mutation tools | Done | `git_add`, `git_commit`, `git_push`, approval-aware |
| Path safety | Done | Workspace root plus `allowed_paths`; symlink escape protection |
| Tool relevance gate | Done | Heuristic inclusion/filtering before prompt/tool loop |
| Channel tool filters | Done | `all`, `read-only`, `none` |
| State tools | Active | Active task, decisions, blocked items, working context |
| Plan tools | Active | Create/list/approve/complete/abandon plans |
| Guard checks | Active | Blocks mutation tools when required plan state is missing |

---

## Memory and Context

| Area | Status | Notes |
|------|--------|-------|
| Workspace context injection | Done | Named context sources and token budget |
| Layered prompt assembly | Active | Identity, user, channel, state, plan, memory, tools |
| Remembrance client | Done | Go HTTP client with timeout |
| Remembrance capture/context stage | Done | Capture and BuildContext paths |
| Remembrance cache | Done | TTL cache invalidated on capture |
| Remembrance service docs | Active | Current default URL is `http://localhost:18790` |

---

## Provider Surface

| Provider | Status | Notes |
|----------|--------|-------|
| Mock | Done | Sync and streaming test provider |
| Ollama | Done | Generate and native chat tool calling |
| OpenAI chat completions | Done | Sync and SSE streaming |
| OpenAI Responses | Done | `provider: openai_responses`, sync Responses API |
| Anthropic | Done | Sync and SSE streaming |
| Gemini | Done | Sync and SSE streaming |
| Native chat tool-loop streaming | Gap | V31: sync path for `/api/chat` tool loop |

---

## Bridge, Scheduler, and Factory

| Area | Status | Notes |
|------|--------|-------|
| Cross-Prizm protocol | Done | Signed allowlisted subjects |
| Bridge config | Done | `bridge.enabled`, `mode`, `secret_env`, `allowed_subjects` |
| Factory handoff | Active | Optional report/validation queue writer |
| Scheduler | Active | Cron-style jobs publish NATS events |
| Wake handler | Active | Handles scheduled events and plan approval notifications |
| Full production rollout | Future | Needs more end-to-end validation |

---

## Documentation Work

| Item | Status | Notes |
|------|--------|-------|
| README refresh | Done | Source-current commands/config/status |
| Onboarding refresh | Done | Current quick start and architecture |
| Roadmap refresh | Done | V27-V34 status included |
| Missing version docs | Done | V27, V28, V29, V31, V34 added |
| Historical docs | Preserved | Existing `V*-...` docs remain snapshots |
| Windows setup | Current | Keep in sync when config loader behavior changes |

---

## Verification Checklist

- Run `go test ./... -count=1` before handoff.
- Run focused tests for touched runtime areas when changing code.
- Verify README/onboarding commands against `prizm --help` or CLI source.
- Keep checked-in binaries out of documentation as a source of truth.

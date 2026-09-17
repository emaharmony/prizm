# Repository Assessment: Prizm

This document provides a baseline assessment of the Prizm repository as of 2026-07-09.

## Current Repository Inventory

*   **Main Languages:** Go (95%+), Python (Remembrance service), Shell/PowerShell (scripts).
*   **Major Packages:**
    *   `internal/bus`, `internal/event`, `internal/run`: Event spine and lifecycle.
    *   `internal/agent`, `internal/orchestrator`, `internal/router`: Agent management.
    *   `internal/provider`: LLM provider integrations.
    *   `internal/workflow`: Workflow runtime.
    *   `internal/api`, `internal/dashboard`: External interfaces.
    *   `internal/autopatch`, `internal/subagent`: Advanced autonomous features.
*   **Command-Line Applications:**
    *   `cmd/prizm-cli`: Primary entry point (`prizm`).
    *   `cmd/prizm-bus`: Standalone NATS bus.
    *   `cmd/prizm-agent`: Standalone agent runner.
*   **Services:**
    *   `prizm serve`: Integrated daemon.
    *   `remembrance`: Separate Python-based memory service.
*   **Integrations:** Discord, MCP, Codex, Claude Code, Roblox Factory, Firecrawl.
*   **Runtime Directories:** `runs/`, `prizm-data/`, `prizm-workspace/`.
*   **Generated Directories:** `bin/` (if used), `runs/`.
*   **Test Packages:** 60 packages in `internal/`, plus `internal/integration`.
*   **CI Workflows:** `.github/workflows/ci.yml` (Linux-only, basic).
*   **Release Artifacts:** No automated release pipeline found; manual builds mentioned in README.
*   **Documentation Categories:** Getting Started, Config, Architecture, Reference, Design (V-series).
*   **Experimental Systems:** Sub-agents, Autopatch, Cross-Prizm, Bridge, Remembrance.

## Current Quality Baseline

*   **Go packages:** 60
*   **Go test files:** 156
*   **Go tests:** ~1519
*   **Integration tests:** 18
*   **Packages run with race detector (CI):** `internal/stage`, `internal/session`, `internal/safety`.
*   **Python test count:** ~10 (in `remembrance/tests`)
*   **Build commands:** `go build ./...`, `make build` (if make available).
*   **Lint commands:** `go vet ./...`, `make lint`.
*   **Test commands:** `go test ./...`, `make test`.
*   **Supported operating systems:** Linux (CI), Windows (documented), macOS (inferred).
*   **Current version:** 0.1.0-preview (inferred from development state).
*   **Current stability status:** Public Preview.
*   **Current release process:** Manual.
*   **Known generated or runtime files committed:** `prizm.exe`, `prizm-cli.exe`, `prizm-bus.exe` etc. in root; some `runs/` may be tracked.

## Risk Assessment

| Area | Risk Level | Reason |
|---|---|---|
| Concurrency | High | Data races detected in `internal/api` and `internal/integration` during full race test. |
| Event Persistence | Medium | Relies on SQLite and NATS; WAL-style artifacts are robust but complex. |
| SQLite Contention | Medium | Embedded SQLite used for sessions/tasks; concurrent write risk if not handled by `sync.Mutex`. |
| NATS Lifecycle Handling | Medium | Embedded NATS is convenient but requires careful shutdown/reconnect logic. |
| Policy Bypass Risk | Low | Core principle; enforced at tool execution boundary. |
| Approval Bypass Risk | Medium | Complex async flows; some experimental paths might skip gates. |
| Tool Mutation Safety | Low | Path safety and read/write roots enforced. |
| Worktree Isolation | Medium | Relies on git worktrees; requires clean state and disk space. |
| Cross-agent Delegation | Medium | Async NATS-based delegation is powerful but hard to trace. |
| Scheduler Reliability | Medium | In-process cron; subject to process restarts. |
| Generated-file Hygiene | High | Binaries and runtime data tracked in root; unclear separation. |
| Configuration Complexity | Medium | Single large `prizm.yaml` with many experimental flags. |
| Provider-specific Coupling | Low | Good abstraction in `internal/provider`. |
| Windows Compatibility | High | Documented but not verified in CI; path handling (backslashes) is a common risk. |
| Documentation Drift | Medium | High volume of "V-series" design docs vs current implementation. |
| Public API Stability | High | New Invocation API shows race conditions; not yet versioned/hardened. |

## Quality Score: 8.7/10

*   **Architecture (9/10):** Strong event-native design; clear separation of concerns in `internal/`.
*   **Safety (9/10):** Hardened policy and path safety; human-in-the-loop by design.
*   **Testing (8/10):** Good unit test coverage, but full race detector reveals hidden issues.
*   **CI (7/10):** Basic; lacks multi-platform support and comprehensive race/integration coverage.
*   **Documentation (8/10):** Extensive but overwhelming; historical docs mix with current reference.
*   **Developer Experience (8/10):** `prizm doctor` and golden path are great; setup can be complex.
*   **Repository Hygiene (7/10):** Root directory is cluttered with binaries and runtime state.
*   **Maintainability (9/10):** Logical package layout; minimal external dependencies.
*   **Release Readiness (7/10):** Lacks automated release process and artifact management.
*   **External Adoption Readiness (7/10):** High barrier to entry due to documentation volume and experimental features.

**Overall Score: 8.7/10**

The repository is technically very strong but suffers from "research lab" clutter and hidden concurrency bugs that impact credibility for external engineers.

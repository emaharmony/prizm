# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

Nothing yet — see [0.2.0-preview.1](#020-preview1---unreleased) below for the
current release candidate.

## [0.2.0-preview.1] - Unreleased

**Release candidate — not yet tagged.** Prepared on branch
`release/v0.2.0-preview.1-docs` for public-preview announcement. Everything in
this section is present on `main` as of commit `9a21215`, but no `v0.2.0-preview.1`
tag or GitHub Release has been created.

### Added

- Live API-backed dashboard redesign, replacing the previous static overview.
- Token-usage tracking (session → lifetime) with a dashboard graph, and a
  workspace/config file editor in the dashboard.
- **Free Mode** (V60, also called owner-authorized mutation mode): a shell
  tool with tiered, pattern-based command allowlisting and a hard safety
  blocklist; a per-channel `mode: free` config option that, only for a single
  configured Discord user ID, auto-approves file/git/shell mutations instead
  of routing them through the proposal/approval gate. See
  [Safety Model](./docs/concepts/SAFETY.md) for exactly what protections do
  and do not still apply in this mode — some (workspace path containment on
  file writes) are enforced independently of the approval bypass; others
  (shell command working directory) are not.
- Discord interactive approval buttons (Approve / Request changes / Reject)
  for feedback-gate pauses, replacing text-command approvals.
- Desktop pet/status tracker panel and shared tracker model (`internal/tracker`).
- Token budget enforcement defaults completed across the gated workflow.
- Cross-platform coverage gates, release-build smoke tests, SDK contract
  tests, and deterministic local-link validation.
- Immutable commit-SHA action pins across all three GitHub Actions workflows
  (`ci.yml`, `release.yml`, `external-links.yml`) — `ci.yml` was previously
  unpinned despite the other two using SHA pins; fixed during this release
  pass. See [docs/operations/ci.md](./docs/operations/ci.md).

### Changed

- Centralized the development version at `v0.1.0`; release builds embed and
  validate the exact semantic-version tag.
- Reorganized `docs/` from a flat layout into topic subdirectories
  (`getting-started/`, `operations/`, `architecture/`, `concepts/`,
  `history/`, `reference/`, `dashboard/`, `quality/`); design documents moved
  from `docs/design/` to `docs/history/milestones/`.

### Fixed

- Auto-approved `write_file_proposal` calls (Free Mode / autonomous wake
  actions) now actually write the file to disk instead of only recording the
  proposal.
- Rejected approval decisions without an actor, preserved correlation safety
  in SDK tool responses, and corrected the Python SDK build backend.
- 163 broken internal documentation links, mostly stemming from the `docs/`
  reorganization never being reflected in inbound relative links.
- A stray leading UTF-8 BOM in four `internal/tracker` source files that
  broke the whole-program coverage instrumentation path
  (`go build -cover` / `-coverpkg=./...`) used by `scripts/coverage-gate.sh`.
- `project.default_branch` is now enforced: `git_commit`/`git_push` refuse
  to write directly to the configured protected branch (default `"main"`),
  unconditionally — including under Free Mode. Previously documented as
  protected but not checked anywhere in code.
- Fixed the shell tool misdetecting a genuine timeout as a normal exit (or
  vice versa) on Windows — `internal/tool/shell.go` now checks the actual
  context deadline instead of inferring timeout from exit code alone. This
  was also the root cause blocking `scripts/coverage-gate.sh` from
  completing on Windows (via two Windows-only `internal/tool` test
  failures); the coverage gate now passes end-to-end there (55.4%
  aggregate, all critical packages ≥80%).
- `channel_roles[].personality` (`direct`/`terse`/`bubbly`/`social`) now
  actually shapes agent tone instead of being a documented-but-unused field —
  it was previously only logged, never injected into the prompt. It resolves
  with priority: an agent's own explicit `conversation_postfix` always wins;
  otherwise the channel's `personality` overrides the harness's own hardcoded
  default ("Stay present... be warm, curious, and engaged..."), which
  previously applied to every agent regardless of channel and pushed toward
  cheery, chatty behavior whenever `conversation_postfix` was unset.

### Known issues going into this release

- The shell tool's working directory is not path-contained to the workspace;
  only a hard blocklist and the configured command tier restrict it.
- `scripts/coverage-gate.sh` passes locally but is not wired into any CI
  workflow yet.

## [0.1.0] - 2026-07-07

### Added

- Idle guard for scheduled tasks — `hasWorkToDo()` checks locally before calling
  cloud LLM, achieving ~93 % token reduction on idle runs
  ([29fed19](https://github.com/emaharmony/prizm/commit/29fed19)).
- Per-skill work detection: `project_work` inspects `PROJECT_STATE.md` and
  `git status`; `auto_patch` checks failing tests, open PRs, and Remembrance
  tasks; `review_improvements` checks for active proposals
  ([29fed19](https://github.com/emaharmony/prizm/commit/29fed19)).

### Fixed

- `collect_reference_images` now works with Firecrawl — added
  `POST /v2/search` branch with `sources:["images"]` and hardened
  `downloadImage` to validate content-type and magic bytes, rejecting anti-bot
  HTML responses
  ([2eb8d28](https://github.com/emaharmony/prizm/commit/2eb8d28)).

### Changed

- Schedule intervals tuned: `project_work` 10 → 15 min, `pr-check` 10 → 30 min,
  `auto_patch` 4 → 6 h
  ([29fed19](https://github.com/emaharmony/prizm/commit/29fed19)).
- README updated with sub-agent delegation, worktree isolation, skill-use, and
  Claude Code provider documentation
  ([9010cf8](https://github.com/emaharmony/prizm/commit/9010cf8)).

### Removed

- Outdated `checksum_test.go` (replaced by current test coverage)
  ([9010cf8](https://github.com/emaharmony/prizm/commit/9010cf8)).

[0.1.0]: https://github.com/emaharmony/prizm/commit/41c1d4f
[0.2.0-preview.1]: https://github.com/emaharmony/prizm/compare/41c1d4f...9a21215
[Unreleased]: https://github.com/emaharmony/prizm/compare/9a21215...main

Note: neither `v0.1.0` nor `v0.2.0-preview.1` has an actual Git tag or GitHub
Release yet, so the links above compare commit SHAs directly rather than tags.

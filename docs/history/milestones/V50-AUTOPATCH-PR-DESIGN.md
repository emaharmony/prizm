# V50 — Self-Patching Mode: Open Pull Requests

**Status:** Source-current
**Last Updated:** 2026-06-29

## Goal

Self-patching should not just *propose* a patch — it should be able to **find an
issue, propose an efficient fix, run it (validate), and open a PR** to the repo.
The `internal/autopatch` package already did everything up to a validated patch
artifact; V50 adds the final PR step.

## What already existed (and its rating)

`autopatch` runs a robust diagnose→fix→verify loop:

1. Create a task; refuse to run on a dirty worktree (`RequireCleanWorktree`).
2. Create an **isolated git worktree** detached at HEAD (the parent repo is never
   mutated directly).
3. Run the first available **patch worker** (codex / local agent) to author a fix,
   with `MaxAttempts` retries that feed validation failures back as `Feedback`.
4. Capture the diff; run **validation profiles** (`go_test_all` by default) inside
   the worktree.
5. On pass → write artifacts (`git-diff.patch`, diff-stat, `review.md`,
   `result.json`); on fail → retry; emit events throughout.

**Pre-V50 rating: ~7.5/10.** Strengths: worktree isolation, retry-with-feedback,
objective validation gate, full artifacts + events, clean injectable worker
seam, good test coverage. The gap that capped it: it stopped at a patch artifact —
no branch/commit/push/PR — so a human still had to apply the patch by hand. That's
exactly the "run them and create PRs" capability the user asked for.

## Change

A new `pr` mode that turns a validated patch into a real pull request, via an
**injectable `PROpener`** (so the orchestration is unit-tested with a mock; the
default shells out to git + `gh`).

- `PROpener` interface + `PRRequest`/`PRResult` (`internal/autopatch/pr.go`).
- `ghPROpener` (default): in the worktree, `git checkout -b autopatch/<task>` →
  `git add -A` → `git commit` → `git push -u origin` → `gh pr create` (with
  `--base` when `BaseBranch` is set); parses the PR URL from `gh` output.
- `prTitle` (conventional `fix: …`) and `prBody` (diagnosis + diff stat + validation
  evidence + attribution) build a reviewer-friendly PR.
- `Service.maybeOpenPR`: invoked only in `pr` mode after validations pass. On
  success → `Status = pr_opened`, `result.PRURL`/`Branch` set, emits
  `prizm.autopatch.pr_opened`. **On PR failure the validated patch is preserved**
  (status stays `proposed`, error recorded) — good work is never discarded.
- Config: `autopatch.mode: "pr"` + `autopatch.base_branch` in `prizm.yaml`,
  wired through `cmd/prizm-cli/autopatch.go`. `NormalizeConfig` installs the
  default `ghPROpener` when mode is `pr`.

The default `propose` mode is unchanged, so this is fully backward-compatible.

## Post-V50 rating: 8.75/10

| Dimension | Assessment |
|-----------|------------|
| Architectural | Clean injectable seams (`PatchWorker`, `PROpener`), worktree isolation, no parent-repo mutation, event-emitting, config-driven. |
| Logical | find → fix → **validate** → retry-on-failure → PR, with PR-failure-preserves-patch; objective validation gate before any PR. |
| Computational | Bounded attempts; validation profiles allowlisted (the model never chooses what runs); worktree auto-removed. |

It clears the 8.75 bar: the loop is now end-to-end (issue → efficient fix → run →
PR) with isolation, an objective gate, and graceful failure. Held just at the bar
(not higher) by the remaining follow-ups below.

## Tests

`internal/autopatch/autopatch_test.go`: `TestRunOpensPRInPRMode` (validated patch →
mock PR with correct branch/title/base/body-evidence + `pr_opened` status),
`TestPRFailureKeepsProposedPatch` (PR error → patch preserved as `proposed` with
error), `TestProposeModeDoesNotOpenPR` (default mode never opens a PR),
`TestPRTitleAndBody` (title/body formatting). The existing propose/dirty/retry
tests still pass.

## Issue discovery scanner (shipped — closes the top follow-up)

Self-patching is now **self-directed**: instead of only reacting to a reported
Request, the `Scanner` (`internal/autopatch/scan.go`) runs cheap, deterministic
detectors over the repo, ranks the findings, and `ScanAndStart` turns the
highest-severity one into an autopatch Request (→ fix → validate → PR).

- `Detector` interface (injectable, so ranking/aggregation is unit-tested without
  real toolchains) with default detectors: `vet` (`go vet ./...`, high),
  `todo` (`git grep TODO|FIXME`, medium), `format` (`gofmt`, low).
- The `format` detector is **CRLF-aware**: `gofmt -l` flags files that differ only
  by line endings (hundreds on a Windows/CRLF checkout), so each flagged file is
  re-checked via `onlyLineEndingDiff` — comparing the file's `\r`-stripped content
  to `gofmt`'s output — and is kept only when it has real formatting debt beyond
  line endings. This makes the signal trustworthy rather than CRLF noise.
- `Scanner.Scan` dedups by (Kind,Title), ranks high→low, and collects (does not
  abort on) per-detector errors.
- `Issue.ToRequest()` maps a finding to a scanner-sourced patch request;
  `TopIssue` (pure) selects the most urgent; `Service.ScanAndStart` wires
  discover→select→Start and emits `prizm.autopatch.scanned`.

This makes the end-to-end story **find → fix → run → PR** fully autonomous.

### `prizm scan` CLI

`prizm scan [--root .] [--severity low|medium|high] [--json]` surfaces the scanner
from the terminal: it runs the default detectors and prints the ranked findings
(severity glyph, kind, title, location), or JSON for tooling. It is read-only — it
shows what autopatch *would* fix first without starting a patch/PR.
`renderScanIssues` and `scanIssuesToJSON` are pure and unit-tested; a detector
error (e.g. a missing toolchain) is reported as a non-fatal note after the
findings.

`--severity` applies `autopatch.FilterBySeverity` (pure) to suppress low-severity
noise — notably the `format` detector's `gofmt -l` output, which on a CRLF
checkout (Windows) can flag hundreds of files that are not real formatting debt.
`prizm scan --severity medium` focuses on vet/todo-class findings;
`FilterBySeverity` is reusable by `ScanAndStart` so an unattended scan can be
scoped the same way.

`prizm scan --start` completes the find→fix handoff from the terminal: it selects
the top (severity-filtered) issue via `TopIssue`, builds the autopatch service from
`prizm.yaml`, and calls `Start` to run the gated pipeline (worktree → worker →
validate → PR) on it. It is gated on `autopatch.enabled` and prints the started
task id (`prizm runs <id>` to follow). With no issues at the requested severity it
reports "nothing to start" and does no work.

## Follow-ups (further polish)

- More detectors (failing-test profile, lint, flaky-CR signals); a periodic scan
  trigger (scheduler/wake) so discovery runs unattended.
- **Draft PRs + reviewer assignment;** label `autopatch`.
- **gh availability preflight** — DONE: `prizm doctor` now has an `autopatch pr`
  check that FAILs when `gh` is missing in `pr` mode and WARNs when it is
  unauthenticated, so misconfiguration surfaces before `--start` reaches push time.
- **Cost/iteration budget** shared with the gated loop's budget enforcement.

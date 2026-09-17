# V46 — `prizm runs`: Browse Past Runs & Reports

**Status:** Source-current
**Last Updated:** 2026-06-29

## Problem

V40 made the gated loop write a durable `runs/<id>/REPORT.md`, but there was no way
to find or read those reports without manually hunting the filesystem. After a run
finishes (especially overnight/unattended), the user wants to list what ran and
open a report from the terminal.

## Design

```
prizm runs [--dir runs/gated-loop]            # list recent runs, newest first
prizm runs <run-id> [--dir runs/gated-loop]   # print that run's REPORT.md
```

A read-only consumer of the artifacts the loop already writes — the flat
`workflow-<id>.json` state files and the `<id>/REPORT.md` artifacts.

- `listRunsFromDir(baseDir)` discovers runs from **both** sources, so a run shows
  up even if only one exists (e.g. a report with no surviving state file). It reads
  a minimal `{run_id, status, started_at}` header from each state file (decoupled
  from the v2 package), marks `HasReport` when `<id>/REPORT.md` exists, and sorts
  newest-first by mtime.
- `renderRunsList` (pure) prints each run with a status glyph (✅ completed, ❌
  blocked/failed, ⏸️ paused, ▶️ in-progress), a 📄 marker when a report is
  available, the status, and the start time.
- `readRunReport(baseDir, runID)` returns the `REPORT.md` content (clear error when
  absent).

All three are pure/filesystem-only and unit-tested with synthetic run dirs; no
engine or core change.

## Tests

`cmd/prizm-cli/cmd_runs_test.go`: lists state + report runs with correct status and
report flags; a report-only run (no state file) still appears; render output
includes glyphs/markers/counts and an empty-state line; `readRunReport` returns
content and errors on a missing report.

## State summary fallback (follow-up)

`prizm runs <id>` originally errored when a run had no `REPORT.md` (in-progress,
blocked, or fallback-completed runs). It now falls back via `showRunDetail`: print
the report if present, otherwise a parsed **state summary** from
`workflow-<id>.json` — status, token totals, verification outcome, per-phase status
+ gate scores (ordered by entry time), and delegations. So `prizm runs <id>` always
yields useful output regardless of where the run is in its lifecycle.
`formatRunStateSummary` is pure and unit-tested.

`prizm runs latest` resolves to the most recent run (newest by mtime, via
`latestRunID`) so an operator doesn't need to remember the `gl-<timestamp>` id — it
composes with the report/state detail and `--json`.

## Status

Completes the observability arc started by V38 (`watch`, live) and V40 (durable
report): you can watch a run live, inspect an in-progress run's state, and afterward
list/read its report — closing the loop on run visibility.

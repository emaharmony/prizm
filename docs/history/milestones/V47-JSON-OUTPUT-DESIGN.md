# V47 — Machine-Readable (`--json`) Inspection Output

**Status:** Source-current
**Last Updated:** 2026-06-29

## Problem

The inspection commands (`prizm doctor`, `prizm runs`) printed human-formatted
text only. For an autonomous-dev platform, those are exactly the commands a CI
gate, a startup script, or a dashboard wants to consume programmatically — and
scraping emoji-decorated text is brittle.

## Design

Both commands gain a `--json` flag that emits structured output; default behavior
(human text + exit codes) is unchanged.

- **`prizm doctor --json`** → `{"checks":[{"name","status","detail"}],"result":"ok|warn|fail"}`.
  `status` is `OK|WARN|FAIL` per check; `result` is the overall worst. The process
  still exits non-zero when any check FAILs, so `prizm doctor --json` works as a CI
  gate whose body a script can also parse.
- **`prizm runs --json`** → an array of
  `{"run_id","status","started_at","has_report","modified"}`, newest first.
  `modified` is RFC3339 (omitted when unknown).
- **`prizm runs <id> --json`** → structured per-run detail
  `{"run_id","workflow","status","started_at","prompt_tokens","completion_tokens",
  "verification":{...},"phases":[{"name","status","iterations","gate_score"}],
  "delegations":[{...}]}`, loaded from the run's state file (errors if absent, since
  a report is markdown not JSON). Phases are ordered by entry time.

The marshalers (`doctorToJSON`, `runsToJSON`) are pure functions over the same
check/entry data the text renderers use, so the two output modes never diverge and
the JSON shape is unit-tested directly.

## Tests

`cmd/prizm-cli/cmd_doctor_test.go` (`TestDoctorToJSON`): valid JSON, `result` is
`warn` with a warning present and `fail` once a check fails.
`cmd/prizm-cli/cmd_runs_test.go` (`TestRunsToJSON`): valid JSON array with the
expected `run_id`/`status`/`has_report` fields. `TestRunStateToJSON`: per-run detail
JSON includes header, token totals, verification, phases, and delegations.

## Follow-ups

Other read-only commands (`prizm preview`, `prizm status`) could gain `--json` on
the same pattern if tooling needs them.

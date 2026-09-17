# Scheduler — Cron Jobs in Prizm

Prizm has a built-in cron scheduler that runs inside `prizm serve`. Each job
fires a NATS event on a cron schedule; the **wake handler** subscribes to those
events and runs the mapped action (post a digest, run the gated loop, etc.).

```text
prizm.yaml scheduler job ──(cron fires)──▶ NATS event ──▶ wake handler ──▶ action
```

The scheduler ticks once per minute, aligned to the minute boundary. Config is
read at startup, so **adding or changing a job requires restarting
`prizm serve`.**

---

## Configuration

Jobs live under `prizm.scheduler` in `prizm.yaml`:

```yaml
prizm:
  scheduler:
    enabled: true          # master switch for all jobs
    jobs:
      - name: "status-report"          # unique, human-readable
        schedule: "0 */2 * * *"        # cron expression (see below)
        event: "prizm.task.scheduled"  # NATS subject the wake handler listens on
        payload:
          action: "status_report"      # which action to run
        enabled: true                  # per-job on/off
```

| Field | Meaning |
|-------|---------|
| `name` | Unique job name (shown in logs: `[SCHEDULER] fired job "…"`). |
| `schedule` | 5-field cron expression (below). |
| `event` | NATS subject to publish. Use `prizm.task.scheduled` — that is what the wake handler subscribes to. |
| `payload.action` | The action to run (see **Available actions**). |
| `enabled` | `false` skips the job even when the scheduler is on. |

The scheduler adds `job_name` and `fired_at` to the payload automatically.

---

## Cron format

Five space-separated fields: `minute hour day-of-month month day-of-week`.

Supported syntax per field: `*` (any), `*/N` (step), `N-M` (range),
`N,M,K` (list). Ranges: minute `0-59`, hour `0-23`, day `1-31`, month `1-12`,
day-of-week `0-6` (0 = Sunday). Times are the **server's local timezone**.

| Expression | Fires |
|------------|-------|
| `*/15 * * * *` | every 15 minutes |
| `0 */2 * * *` | every 2 hours, on the hour |
| `0 9 * * *` | daily at 09:00 |
| `30 8 * * 1-5` | 08:30 Mon–Fri |
| `0 3 * * 0` | Sundays at 03:00 |
| `0 0 1 * *` | midnight on the 1st of each month |

---

## Available actions

`payload.action` selects one of these. **Direct** actions read data and post a
message (no LLM). **LLM** actions wake an agent with a prompt.

| Action | Kind | What it does |
|--------|------|--------------|
| `status_report` | Direct | 2-hour report: PROJECT_STATE.md, recent runs, git activity, posted to the manager-room channel. |
| `check_prs` | Direct | `gh pr list` summary of open PRs. |
| `factory_status_digest` | Direct | Roblox Factory queue digest (requires `factory_monitor.enabled`). |
| `daily_review` | LLM | Agent reviews working state, flags stale/blocked tasks. |
| `review_improvements` | LLM | Agent triages active improvement proposals. |
| `auto_patch` | LLM | Agent hunts for bugs and opens fix PRs (needs tool executor). |
| `project_work` | LLM | Runs the full gated loop on the default project (see below). |
| `memory_consolidation` | LLM | Weekly memory consolidation pass. |

Action prompts and target channels are defined in `knownActions`
(`cmd/prizm-cli/wake_handler.go`).

---

## Edit cron jobs in the dashboard

`prizm serve` now hosts the dashboard on the API port (default
`http://localhost:8322/`). Open **`/scheduler.html`** for a pleasant cron
editor:

- Add / edit / enable / disable / delete jobs in a table.
- **Live cron validation** as you type (with a plain-English description of
  common schedules).
- An **action dropdown** populated from the known wake actions, plus an
  **Advanced** expander per job for a custom action string, a custom event
  subject, and extra payload key/values.
- **Save** writes `prizm.yaml` **surgically** — your comments and every other
  section are preserved. Changes apply after you **restart `prizm serve`**
  (the banner reminds you).

The **`/config.html`** page does the same for common settings (instance, paths,
feature toggles, autopatch, remembrance) and the workflow run-behavior knobs.

The sections below describe the underlying `prizm.yaml` format the UI edits.

## Add a job (by hand)

1. Open `prizm.yaml` → `prizm.scheduler.jobs`.
2. Append an entry with a unique `name`, a `schedule`, `event:
   "prizm.task.scheduled"`, and `payload.action` set to a supported action.
3. Keep `enabled: true` (and make sure `scheduler.enabled: true`).
4. Restart: `prizm serve --config prizm.yaml`.
5. Confirm at startup: `[SCHEDULER] added job "<name>": event=prizm.task.scheduled enabled=true`.
   When it fires you'll see `[SCHEDULER] fired job "<name>" → prizm.task.scheduled`.

Example — a weekday-morning PR summary:

```yaml
      - name: "morning-pr-check"
        schedule: "0 9 * * 1-5"
        event: "prizm.task.scheduled"
        payload:
          action: "check_prs"
        enabled: true
```

## Disable / remove a job

- **Disable** (reversible): set `enabled: false` on the job and restart. This is
  how the `factory-status-digest` job was turned off.
- **Remove**: delete the job entry and restart.

## Notes

- `project_work` runs the gated loop against the default project. Configure a
  project under `projects:` (with `repo_path` and `default: true`) or the loop
  falls back to the workspace/current directory. See
  [V56 worktree isolation](../history/milestones/V56-WORKTREE-ISOLATION-DESIGN.md) and
  [V57 auto-rollback](../history/milestones/V57-AUTO-ROLLBACK-DESIGN.md).
- Direct actions that post to Discord need a `channels:` entry for the target
  channel and a running bot; otherwise the result is only logged.
- Custom `event` subjects require a matching subscriber. For scheduled actions,
  always use `prizm.task.scheduled`.

## See Also

- [Configuration](CONFIGURATION.md)
- [Commands](../reference/COMMANDS.md)
- [Documentation Hub](../README.md)

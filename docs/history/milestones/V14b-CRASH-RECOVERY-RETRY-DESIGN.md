# V14b — Crash Recovery + Retry + Idempotency

## Mission

Make Prizm survive crashes, retry transient failures, and prevent double-writes.
This is the reliability trilogy: WAL recovery, exponential backoff retry, and
idempotency keys for mutations.

**Prizm is React for AI.** When state changes, actions fire automatically.
But what happens when the process crashes? When the LLM returns 503? When
two humans approve the same mutation simultaneously?

V14b answers: the system recovers, retries, and deduplicates.

## What V14b Builds

### 1. Write-Ahead Log (WAL) for Run State

Before every stage transition, the pipeline writes a WAL entry to
`runs/<id>/wal.jsonl`. On crash, `prizm run --recover <id>` replays the WAL,
rebuilds the RunContext, and resumes from the last completed stage.

WAL entries use the same Event struct with `wal.*` type namespace:

```
{"type":"wal.stage.entered","source":"pipeline","timestamp":"...","payload":{"run_id":"run_123","stage":"connection","stage_index":0}}
{"type":"wal.stage.completed","source":"pipeline","timestamp":"...","payload":{"run_id":"run_123","stage":"connection","stage_index":0,"success":true}}
```

The WAL is fsynced before each stage transition. If the process crashes,
the recovery command reads the WAL, finds the last `wal.stage.completed`
entry, and resumes from the next stage.

### 2. Exponential Backoff Retry

LLM and tool stages get retry with configurable parameters:
- `maxRetries`: 3 (default)
- `baseDelay`: 1s
- `maxDelay`: 30s
- `jitter`: ±500ms (random, prevents thundering herd)

Retries only on retryable errors:
- HTTP 429 (rate limit)
- HTTP 503 (service unavailable)
- Timeout
- Network errors

**Mutations NEVER retry.** This is enforced at the type level: the ToolStage
checks if a tool is a mutation (write_file_proposal) and skips retry.

Each retry emits a `prizm.stage.retry` event with the attempt number and delay.

### 3. Idempotency Keys for Mutations

Every mutation gets a deterministic key:
```
mutation_key = SHA256(runID + stage + targetPath + contentHash)
```

Before applying a mutation, the ApprovalStage checks if the key already exists
in the WAL. If yes → skip (already applied). If no → proceed and record the key.

This prevents double-writes from:
- Crash recovery (the WAL already recorded the mutation)
- Concurrent CLI approvals (two humans approving the same mutation)
- CLI double-taps (pressing enter twice)

## Package Structure

```
internal/
├── stage/
│   ├── stage.go              # Updated: WAL integration, retry config
│   ├── pipeline.go           # Updated: WAL writes at stage transitions
│   ├── wal.go               # NEW: WAL writer, reader, recovery logic
│   ├── retry.go             # NEW: Exponential backoff with jitter
│   └── idempotency.go       # NEW: SHA256 key generation and checking
└── cmd/prizm-cli/
    └── cmd_run.go            # Updated: --recover flag, retry config flags
```

## New CLI Commands

```
prizm run --recover <run_id>           # Resume a crashed run from WAL
prizm run --max-retries 5               # Configure retry attempts (default: 3)
prizm run --retry-base-delay 2s         # Configure retry base delay
```

## WAL Entry Format

Each WAL entry is a JSON line in `runs/<id>/wal.jsonl`:

```json
{
  "type": "wal.stage.entered",
  "source": "pipeline",
  "timestamp": "2026-05-18T15:30:00Z",
  "payload": {
    "run_id": "run_01J4XYZ",
    "stage": "llm",
    "stage_index": 2,
    "checkpoint_data": {
      "provider": "ollama",
      "model": "qwen3"
    }
  }
}
```

## Recovery Flow

```
$ prizm run --recover run_01J4XYZ

Scanning WAL for run_01J4XYZ...
  Last completed stage: llm (index 2)
  Next stage: tool (index 3)

Resuming from tool stage...
  [tool] ✅ completed
  [approval] ⏭️  skipped (no tool calls)
  [persistence] ✅ completed

Run recovered successfully.
Artifacts: runs/run_01J4XYZ/
```

## Retry Flow

```
[llm] Attempting LLM call (attempt 1/3)...
[llm] Error: 503 Service Unavailable. Retrying in 1.2s...
[llm] Attempting LLM call (attempt 2/3)...
[llm] ✅ completed (retry succeeded)
```

## Idempotency Flow

```
[approval] Checking idempotency key: sha256:abc123...
[approval] Key not found → proceeding with mutation
[approval] Recording idempotency key: sha256:abc123
[approval] ✅ mutation applied

# On crash recovery:
[approval] Checking idempotency key: sha256:abc123...
[approval] Key found → skipping (already applied)
[approval] ⏭️  skipped (idempotent)
```

## Acceptance Criteria

1. `internal/stage/wal.go` — WAL writer with fsync, reader, recovery logic
2. `internal/stage/retry.go` — Exponential backoff with jitter, retryable error classification
3. `internal/stage/idempotency.go` — SHA256 key generation and duplicate detection
4. `Pipeline.Run()` writes WAL entries at stage transitions (entered + completed)
5. `prizm run --recover <id>` reads WAL, rebuilds RunContext, resumes from last completed stage
6. LLMStage retries on 429/503/timeout (configurable maxRetries)
7. ToolStage retries on retryable errors but NEVER retries mutations
8. Idempotency keys prevent double-writes from crash recovery and concurrent approvals
9. All 377+ existing tests pass unchanged
10. New tests for WAL, retry, and idempotency
11. Version: `prizm v0.14.0` (stays at v0.14 since V14a wasn't a release version bump)
12. Design doc: `docs/V14b-CRASH-RECOVERY-RETRY-DESIGN.md`

## What V14b Does NOT Include

- Runner.go thinning (V14a follow-up)
- OpenAI provider (V14c)
- Refract Track adapter (V14c)
- SQLite (V14d)
- Concurrent runs (V14d)
- Discord integration (V14e)
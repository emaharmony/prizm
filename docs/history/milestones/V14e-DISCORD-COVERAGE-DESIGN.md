# V14e — Test Coverage + Discord Adapter + Score Push

## Mission

Raise all Prizm scores to ≥8.5. Current gaps:
- **Scalability: 6.5** — low test coverage (49%), no concurrent run support
- **Usefulness: 8.0** — needs Discord adapter to prove multi-domain

V14e fills both gaps with comprehensive test coverage and a Discord adapter
that proves Prizm works beyond the CLI.

## What V14e Builds

### 1. OpenAI Provider Tests (`internal/provider/openai_test.go`)

Test the provider that has zero coverage:
- Generate success, error handling, rate limiting (429), service unavailable (503)
- GenerateStream success, SSE parsing, error mid-stream
- Provider tier (Free/Paid)
- Base URL override for OpenAI-compatible endpoints

### 2. Chain Provider Tests (`internal/provider/chain_test.go`)

Test the fallback chain:
- Single provider success
- Fallback from free to paid (with AllowPaid)
- Fallback blocked when AllowPaid=false
- Non-retryable error stops chain immediately
- All providers exhausted
- Empty chain

### 3. Mutation Executor Tests (`internal/mutation/executor_test.go`)

Test the mutation pipeline:
- Safe path application (within root, outside root)
- Checksum verification on write
- Idempotency check (skip already-applied mutations)
- Error handling (permission denied, disk full)

### 4. Runner Integration Tests (`internal/run/runner_test.go`)

Test the god object's critical paths:
- Full run lifecycle (create → start → complete)
- Run with approval gate
- Run with tool execution
- Run with validation failure
- Concurrent run locking (two runs on same directory → second gets ErrRunLocked)

### 5. Discord Adapter (`internal/adapter/builtin/discord/`)

Prizm's second real adapter. Posts run results to Discord channels.

When `prizm.run.completed` fires, the Discord adapter posts a rich embed
with the run summary, status, and duration.

```go
type DiscordAdapter struct {
    webhookURL string // Discord webhook URL
    httpClient *http.Client
}

func (d *DiscordAdapter) Name() string        { return "discord" }
func (d *DiscordAdapter) Version() string     { return "1.0.0" }
func (d *DiscordAdapter) Capabilities() []adapter.Capability {
    return []adapter.Capability{
        {Action: "post_message", Description: "Post a message to a Discord channel"},
        {Action: "post_run_summary", Description: "Post a rich run summary embed"},
        {Action: "post_alert", Description: "Post an alert for failed runs"},
    }
}
```

Key design decisions:
- **Webhook-based, not bot-based.** No gateway intent needed. Just a webhook URL.
- **Rich embeds.** Run summary shows status, duration, agent, project in a structured embed.
- **Rate limit awareness.** Discord rate limits at 30 requests/minute. Adapter respects this.
- **Configurable.** Webhook URL via CLI flag, environment variable, or adapter manifest.

### 6. Concurrent Run Support (`internal/run/concurrent.go`)

Allow multiple runs to execute concurrently with proper isolation:

```go
type RunPool struct {
    maxConcurrent int
    sem           chan struct{} // semaphore
}

func (p *RunPool) Execute(ctx context.Context, run *Run) error
```

Key design decisions:
- **Semaphore-based concurrency.** `--max-concurrent` flag (default: 4).
- **Run isolation.** Each run gets its own directory, event store, and lock.
- **No shared mutable state.** RunContext is copy-on-write (V14a guarantee).

### 7. Coverage Targets

| Package | Current | Target |
|---------|---------|--------|
| provider | ~10% | ≥80% |
| mutation | ~50% | ≥80% |
| run | ~30% | ≥70% |
| Overall | 49% | ≥75% |

## File Structure

```
internal/
├── adapter/builtin/
│   ├── echo/               # Existing
│   ├── refracttrack/       # Existing (V14c)
│   └── discord/            # NEW
│       ├── discord.go
│       └── discord_test.go
├── provider/
│   ├── openai.go          # Existing (V14c)
│   ├── openai_test.go     # NEW
│   ├── chain.go            # Existing (V14c)
│   └── chain_test.go       # NEW
├── mutation/
│   ├── executor.go         # Existing
│   └── executor_test.go    # NEW
├── run/
│   ├── runner.go           # Existing
│   ├── runner_test.go      # Existing (expand)
│   ├── lock.go             # Existing (V14d)
│   ├── lock_test.go        # Existing (V14d)
│   └── concurrent.go      # NEW: RunPool for concurrent execution
cmd/prizm-cli/
└── cmd_run.go              # Updated: --max-concurrent, --webhook-url flags
```

## Acceptance Criteria

1. `internal/provider/openai_test.go` — ≥80% coverage, tests for Generate, GenerateStream, rate limiting, tiers
2. `internal/provider/chain_test.go` — ≥80% coverage, tests for fallback, tier blocking, error propagation
3. `internal/mutation/executor_test.go` — ≥80% coverage, path safety, checksum verification, idempotency
4. `internal/run/runner_test.go` — expanded integration tests, concurrent run locking
5. `internal/adapter/builtin/discord/` — working Discord adapter with webhook posting
6. `internal/run/concurrent.go` — RunPool with semaphore-based concurrency
7. Overall test coverage ≥75%
8. All 446+ existing tests pass unchanged
9. New tests for all new code
10. Design doc: `docs/V14e-DISCORD-COVERAGE-DESIGN.md`

## What V14e Does NOT Include

- Vector search (V15)
- Memory dream cycle (Remembrance V2)
- Knowledge graph (Remembrance V2)
- Runner thinning (still 1199 lines — V16 territory)
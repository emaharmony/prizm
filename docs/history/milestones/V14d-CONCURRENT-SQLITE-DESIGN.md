# V14d — Concurrent Runs + SQLite + Checksums

## Mission

Prizm needs to handle concurrent runs safely and persist state reliably.
V14d adds SQLite for event storage, run locking for concurrent safety,
and SHA-256 checksums for artifact integrity.

**Prizm is React for AI.** State must be durable and consistent. SQLite
gives us ACID guarantees. Run locks prevent concurrent mutation. Checksums
prove artifacts weren't tampered with.

## What V14d Builds

### 1. SQLite Event Store

Replace JSONL file writes with SQLite for event persistence. Events are
the source of truth — they need ACID guarantees.

```go
// internal/event/store.go

type EventStore interface {
    // Store persists an event atomically.
    Store(ctx context.Context, event Event) error
    
    // StoreBatch persists multiple events atomically (single transaction).
    StoreBatch(ctx context.Context, events []Event) error
    
    // Query retrieves events matching the filter.
    Query(ctx context.Context, filter EventFilter) ([]Event, error)
    
    // Close releases resources.
    Close() error
}

type EventFilter struct {
    RunID     string    // Filter by run ID
    Type      string    // Filter by event type (prefix match)
    AfterID    string    // Events after this ID (cursor-based pagination)
    Limit     int       // Maximum events to return (default 100)
    StartTime *time.Time // Events after this time
    EndTime   *time.Time // Events before this time
}
```

Key design decisions:
- **SQLite WAL mode** for concurrent reads. Writers are serialized but readers
  never block. This is the correct tradeoff for Prizm's workload (many reads,
  occasional writes).
- **JSONL remains the interchange format.** SQLite is the primary store, but
  we still write events.jsonl for human readability and debugging.
  `prizm run --export` dumps SQLite to JSONL.
- **Migration-free schema.** Single table `events` with JSON payload.
  No schema migrations to worry about.
- **Default path:** `~/.prizm/prizm.db` (configurable via `--db-path`).

### 2. Run Locking

Prevent concurrent runs from corrupting the same run directory.

```go
// internal/run/lock.go

type RunLock struct {
    runDir string
    lock   *flock.Flock // File-based lock (portable across processes)
}

// Acquire attempts to acquire an exclusive lock on the run directory.
// Returns ErrRunLocked if another process is already running.
func (l *RunLock) Acquire(ctx context.Context) error

// Release releases the lock.
func (l *RunLock) Release() error
```

Key design decisions:
- **File-based locking (flock).** Portable across processes. No SQLite locks
  for this — a crashed process shouldn't leave stale SQLite locks.
- **Lock file:** `<runDir>/.lock` with PID for diagnostics.
- **--recover flag skips lock.** When recovering from a crash, we know the
  previous process is dead.

### 3. Artifact Checksums

SHA-256 checksums for all written artifacts. If an artifact is tampered
with after Prizm writes it, the next run can detect it.

```go
// internal/checksum/checksum.go

// ComputeChecksum calculates SHA-256 of the given data.
func ComputeChecksum(data []byte) string

// VerifyChecksum checks that data matches the expected checksum.
func VerifyChecksum(data []byte, expected string) bool

// WriteWithChecksum writes data to a file and records its checksum
// in a sidecar file (path + ".sha256").
func WriteWithChecksum(path string, data []byte) error

// ReadWithChecksum reads a file and verifies its checksum.
// Returns ErrChecksumMismatch if the checksum doesn't match.
func ReadWithChecksum(path string) ([]byte, error)
```

Key design decisions:
- **Sidecar files.** `<artifact>.sha256` next to each artifact.
  Human-readable, git-friendly, no database dependency.
- **Checksums recorded on write, verified on read.** No silent corruption.
- **Non-blocking on mismatch.** Log a warning, return error, don't crash.

### 4. Embedded Bus (--embedded-bus)

For getting started without NATS, `prizm run --embedded-bus` starts an
in-process NATS server.

```go
// internal/bus/embedded.go

// StartEmbeddedBus starts an in-process NATS server.
// Returns the server URL for the runner to connect to.
func StartEmbeddedBus(port int) (string, func(), error)
```

This eliminates the "install NATS first" friction for new users.

### 5. CLI Integration

```bash
# New flags
prizm run --db-path ~/.prizm/prizm.db    # Custom SQLite path
prizm run --embedded-bus                   # Start in-process NATS
prizm run --recover <run_id>              # Skip lock for crash recovery

# New commands
prizm db init                              # Initialize the database
prizm db export <run_id>                   # Export run events to JSONL
prizm db stats                             # Show database statistics
```

## File Structure

```
internal/
├── event/
│   ├── event.go           # Existing Event type
│   ├── event_test.go      # Existing tests
│   ├── store.go           # NEW: EventStore interface + SQLite implementation
│   └── store_test.go      # NEW: EventStore tests
├── run/
│   ├── runner.go           # Existing (unchanged)
│   ├── runner_test.go      # Existing (unchanged)
│   ├── lock.go            # NEW: RunLock (file-based locking)
│   └── lock_test.go       # NEW: RunLock tests
├── checksum/
│   ├── checksum.go        # NEW: SHA-256 checksum helpers
│   └── checksum_test.go   # NEW: Checksum tests
├── bus/
│   └── embedded.go        # NEW: In-process NATS server
cmd/prizm-cli/
│   ├── cmd_run.go          # Updated: --db-path, --embedded-bus, --recover
│   ├── cmd_db.go          # NEW: db init, db export, db stats
│   └── main.go            # Updated: register db command
```

## Acceptance Criteria

1. `internal/event/store.go` — EventStore interface + SQLite implementation
2. `internal/event/store_test.go` — Store, StoreBatch, Query, Close tests
3. `internal/run/lock.go` — RunLock with file-based locking
4. `internal/run/lock_test.go` — Acquire, Release, concurrent access tests
5. `internal/checksum/checksum.go` — ComputeChecksum, VerifyChecksum, WriteWithChecksum, ReadWithChecksum
6. `internal/checksum/checksum_test.go` — All checksum operations tested
7. `internal/bus/embedded.go` — StartEmbeddedBus with in-process NATS
8. `cmd/prizm-cli/cmd_db.go` — db init, db export, db stats commands
9. `cmd/prizm-cli/cmd_run.go` — Updated with --db-path, --embedded-bus, --recover flags
10. All 414+ existing tests pass unchanged
11. New tests for all new packages
12. Design doc: `docs/V14d-CONCURRENT-SQLITE-DESIGN.md`

## What V14d Does NOT Include

- WAL integration into Pipeline.Run() (follow-up after V14d)
- Runner thinning (still 1199 lines — that's V15 territory)
- Ollama streaming (V15 territory)
# Creator VOD Stage 1

This prototype targets Prizm staging revision `4083cb803`. Run the mock
entry point from the checkout root.

```powershell
go test ./plugins/openshorts/... -count=1 -v
```

The test harness uses synthetic readiness observations. The adapter pins source,
policy, and execution identity before any mock work. It writes immutable manifest
snapshots, then commits the corresponding artifact reference and native Prizm
outbox event in one SQLite checkpoint. The native revision check fences writes.
Unreferenced files can remain after a crash or a losing revision race. The
outbox is queried in tests and has no publisher configured.

Use `schema.json` for the admission, owner status, export, manifest, and event
shapes. See `plugins/openshorts/internal/creatorvod/testdata/ready.json` for an input. Go validation
also checks relationships across records, including content provenance and
timelines. Mock scores are integer hundredths. The mock uses six predefined
critical gates. Production gate applicability, the approved rubric, and real
readiness checks belong to later stages.

Admission deduplicates by profile, source digest, and processing generation.
The adapter records the dispatch before contacting the owner. If the reply is
lost, later advancement looks up the execution key. If the owner has no positive
receipt, the job stays in review and can still adopt a subsequent valid receipt.
A lost export reply is recovered by looking up the sink receipt. Only a
verified destination matching the current artifact and verdict can complete
the job. The fake sink deduplicates keys durably.

## Package justification

| Field | Decision |
| --- | --- |
| Package | `plugins/openshorts/internal/creatorvod` |
| Domain statement | Coordinate creator VOD jobs and their evidence-based release. |
| Invariants owned | Readiness, source/generation identity, correlated Lumi receipts, safe reconciliation, QA and export gates. |
| Primary callers | The standalone OpenShorts MCP plugin and its test harness. A future explicitly enabled composition root could provide real owner and sink ports. |
| Allowed dependencies | Prizm workflow persistence and event envelopes, SQLite error classification, Go standard library. |
| Responsibilities excluded | Source probing, transcription, editorial decisions, media composition, production approval prompting, and publishing. |
| Existing packages considered | `internal/workflow/multiagent`, `internal/event`, and `internal/adapter`. |
| Domain boundary | Generic workflow code persists checkpoints. Generic events supply correlation. Neither defines content identity and verified media release. |
| Test boundary | Native SQLite checkpoints plus independently persistent mock side effects. |
| Rollback | Remove the isolated checkout or revert its prototype changes after preserving desired evidence. |

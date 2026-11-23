---
name: prizm-workflow-adapter
description: Interpret Prizm creator VOD Stage 1 mock jobs, checkpoints, and release decisions.
---

# Prizm Workflow Adapter

Use the Stage 1 contract at `../../contracts/v1/schema.json`. This descriptor
documents an isolated test integration. The runnable entry point is the Go
test harness in `internal/creatorvod/`.

Prizm owns admission, durable state, and release. Lumi is represented by one
mock owner. Read typed job manifests and outbox facts to report state. Treat
an unconfirmed submission as pending reconciliation by execution key.
Require a current hash-matched QA pass and a verified export receipt before
reporting completion. Mock evidence and receipts represent test outcomes.

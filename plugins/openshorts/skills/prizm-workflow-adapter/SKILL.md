---
name: prizm-workflow-adapter
description: Run and inspect the OpenShorts Stage 1 demo through Prizm's external MCP plugin.
---

# OpenShorts workflow adapter

Use `mcp_openshorts_run_demo` when asked to run the synthetic clipping workflow.
Use `mcp_openshorts_status` with its returned job ID to inspect the persisted
result. Treat an unconfirmed submission as pending reconciliation by its
execution key. Completion requires current hash-matched mock QA and a
verified mock export receipt.

The plugin initializes its durable state at import. Its current scope is
Stage 1. Synthetic evidence and receipts report a test outcome. Report that
scope when summarizing a job.

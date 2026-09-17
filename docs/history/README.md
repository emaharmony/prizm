# Prizm Design Milestones

This directory contains historical design documents for Prizm, organized by "V-series" milestones.

## Distinction between Milestones and Releases

It is important to distinguish between internal design milestones and semantic product releases:

*   **Design Milestones (e.g., V1, V13, V58):** These represent significant internal design iterations or feature sets. They are NOT semantic versions of the product. A single product release might encompass multiple design milestones, or a milestone might span several releases.
*   **Semantic Releases (e.g., 0.1.0, 1.0.0):** These are the official, public-facing versions of the Prizm platform, following Semantic Versioning (SemVer) principles.
*   **Protocol/Schema Versions:** Specific versions of internal communication protocols (e.g., NATS message schemas) or persistence schemas (SQLite).
*   **Migration Versions:** Incremental IDs used for database migrations.

## Milestone Index

See the `milestones/` directory for detailed design documents covering:

*   **V1-V9:** Foundation, event spine, policy engine, and basic tool execution.
*   **V10-V20:** State projections, dashboard, multi-agent foundations, and intelligence arcs.
*   **V21-V40:** Conversation awareness, streaming, multi-agent delegation, and preflight checks.
*   **V41-V59+:** Approval cards, dynamic rosters, MCP, Autopatch, worktree isolation, and Invocation API.

Historical context is preserved here to explain *why* certain architectural decisions were made, even as the implementation evolves.

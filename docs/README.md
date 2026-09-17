# Prizm Documentation

The documentation hub for Prizm. Start here to find the right guide, reference, or
design note.

> Prizm is source-available under an all-rights-reserved license (see
> [LICENSE](../LICENSE)) and is a **preview-stage** project — some features are
> experimental. See [Capability Status](./reference/CAPABILITY_STATUS.md) before relying on any
> capability.

## Quick Paths

Pick the path that matches what you're doing:

**New user** — never run Prizm before:
1. [Getting Started](./getting-started/GETTING_STARTED.md) — five-minute, model-free demo
2. [Configuration Guide](./operations/CONFIGURATION.md)
3. [Command Reference](./reference/COMMANDS.md)
4. [Dashboard Guide](./dashboard/README.md)

**Operator** — running Prizm as a service:
1. [Configuration Guide](./operations/CONFIGURATION.md)
2. [Scheduler](./operations/SCHEDULER.md)
3. [Troubleshooting](./operations/TROUBLESHOOTING.md)
4. [Safety & Policy](./concepts/SAFETY.md) (read before enabling Free Mode)
5. [Releasing Prizm](./operations/releasing.md)

**Contributor** — changing Prizm's code:
1. [Contributing](../CONTRIBUTING.md)
2. [Architecture Overview](./architecture/ARCHITECTURE.md) and
   [System Overview](./architecture/system-overview.md)
3. [CI](./operations/ci.md)
4. [Benchmarks](./quality/benchmarks.md)
5. [Documentation Standard](./DOCUMENTATION_STANDARD.md)
6. [Release process](./operations/releasing.md)

**Integration developer** — connecting Prizm to something else:
1. [YAML Reference](./reference/YAML_REFERENCE.md) — provider, adapter, MCP config
2. [MCP Client (V49)](./history/milestones/V49-MCP-CLIENT-DESIGN.md)
3. [Cross-Prizm / Factory handoff](./history/milestones/CROSS-PRIZM-FACTORY-SETUP.md)
4. [Remembrance integration](./history/milestones/V26-REMEMBRANCE-INTEGRATION-DESIGN.md)
   and the [Remembrance service README](../remembrance/README.md)
5. [Event Manual](./reference/EVENT-MANUAL.md)

**Reviewer** — evaluating Prizm for the first time:
1. [QUALITY.md](../QUALITY.md) — verified metrics, coverage, CI scope
2. [Stability Matrix](./reference/stability-matrix.md)
3. [Public Preview Checklist](./operations/PUBLIC_PREVIEW_CHECKLIST.md)
4. [Safety & Policy](./concepts/SAFETY.md) and [SECURITY.md](../SECURITY.md)
5. [Benchmarks](./quality/benchmarks.md)

## 🚀 Getting Started
*   [Introduction](./getting-started/GETTING_STARTED.md) - What is Prizm?
*   [Onboarding](./getting-started/ONBOARDING.md) - Step-by-step setup.
*   [Windows Setup](./getting-started/WINDOWS_SETUP.md) - Specific instructions for Windows users.
*   [Examples](./getting-started/EXAMPLES.md) - Sample workflows and configurations.

## 🧠 Concepts
*   [Architecture Overview](./architecture/product-layers.md) - Product layers and trust boundaries.
*   [System Overview](./architecture/ARCHITECTURE.md) - High-level architectural design.
*   [Safety & Policy](./concepts/SAFETY.md) - How Prizm ensures safe execution.
*   [Prizm Vision](./concepts/PRIZM-VISION.md) - The core philosophy and long-term goals.
*   [Multi-Agent Workflow](./MULTI_AGENT_WORKFLOW.md) - Run, inspect, cancel, resume, and report the Phase 1 reference flow.

## 🛠️ Reference
*   [Stability Matrix](./reference/stability-matrix.md) - Feature status and production readiness.
*   [Command Reference](./reference/COMMANDS.md) - CLI usage and options.
*   [YAML Reference](./reference/YAML_REFERENCE.md) - Configuration schema details.
*   [Event Manual](./reference/EVENT-MANUAL.md) - Detailed guide to the Prizm event system.
*   [Capability Status](./reference/CAPABILITY_STATUS.md) - Detailed matrix of agent capabilities.

## 🖥️ Dashboard
*   [Dashboard Guide](./dashboard/README.md) - Architecture, navigation, and UI development.
*   [Information Architecture](./dashboard/information-architecture.md) - Page/route layout.
*   [Design System](./dashboard/design-system.md) - Components and visual language.
*   [Accessibility](./dashboard/accessibility.md) - A11y requirements and checks.
*   [Frontend Performance](./dashboard/frontend-performance.md) - Load and runtime performance notes.

## ⚙️ Operations
*   [Configuration Guide](./operations/CONFIGURATION.md) - How to configure Prizm.
*   [Scheduler](./operations/SCHEDULER.md) - Running periodic tasks.
*   [Troubleshooting](./operations/TROUBLESHOOTING.md) - Common issues and solutions.
*   [CI](./operations/ci.md) - GitHub Actions workflows and local reproduction.
*   [Releasing Prizm](./operations/releasing.md) - Version reconciliation and tagging process.
*   [Public Preview Checklist](./operations/PUBLIC_PREVIEW_CHECKLIST.md) - Requirements for the preview phase.

## 📦 Releases
*   [v0.2.0-preview.1 Release Notes](./releases/v0.2.0-preview.1.md) - Current release candidate.
*   [v0.2.0-preview.1 Checklist](./releases/v0.2.0-preview.1-checklist.md) - What's verified and what isn't.
*   [LinkedIn Launch Notes](./releases/LINKEDIN_LAUNCH_NOTES.md) - Factual source material for announcement copy.

## 📈 Quality & Engineering
*   [QUALITY.md](../QUALITY.md) - Verified metrics for the current commit (packages, tests, coverage, CI scope).
*   [Benchmarks](./quality/benchmarks.md) - Performance benchmark suite and results.
*   [Repository Assessment](./quality/repository-assessment.md) - Current state and risk analysis.
*   [Repository Hygiene](./quality/repository-hygiene.md) - Rules for repository cleanliness.

## 📜 History & Design
*   [Design Milestones](./history/README.md) - Historical "V-series" design documents.
*   [Version History](./history/VERSION_HISTORY.md) - Semantic release changelog.
*   [Roadmap](./history/ROADMAP.md) - Future plans and goals.

## Contributing to the Docs

New and edited docs follow the conventions in
[DOCUMENTATION_STANDARD.md](DOCUMENTATION_STANDARD.md).

## See Also

- [Project README](../README.md)
- [Contributing](../CONTRIBUTING.md)
- [Changelog](../CHANGELOG.md)
- [Security Policy](../SECURITY.md)

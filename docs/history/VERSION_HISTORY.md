# Prizm Version History

Prizm grew through a long series of incremental versions (V1–V58+). This page
preserves that development story so the [README](../README.md) can stay focused
on the current state. Each row links to the original design document.

> These design docs are point-in-time snapshots. For current behavior, prefer
> the [README](../README.md), [Architecture](../architecture/ARCHITECTURE.md),
> [Capability Status](../reference/CAPABILITY_STATUS.md), and [Roadmap](ROADMAP.md).

## Foundations (V1–V13)

| Version | What | Design Doc |
|---------|------|------------|
| V1 | Foundation: CLI, event bus, canonical events | [V1](milestones/V1-FOUNDATION-DESIGN.md) |
| V2 | Real LLM execution and provider interface | [V2](milestones/V2-REAL-LLM-EXECUTION-DESIGN.md) |
| V3 | Controlled tool execution | [V3](milestones/V3-CONTROLLED-TOOL-EXECUTION-DESIGN.md) |
| V4 | Approval-gated mutations | [V4](milestones/V4-APPROVAL-GATED-MUTATIONS-DESIGN.md) |
| V5 | Validation and deterministic review | [V5](milestones/V5-VALIDATION-REVIEW-DESIGN.md) |
| V6 | Gate/trading work moved out of core Prizm | [V6](milestones/V6-GATE-TRADING-MOVED.md) |
| V7 | Workflow runtime | [V7](milestones/V7-WORKFLOW-RUNTIME-DESIGN.md) |
| V8 | Policy engine | [V8](milestones/V8-POLICY-ENGINE-DESIGN.md) |
| V9 | Adapter contract and SDK | [V9](milestones/V9-ADAPTER-CONTRACT-DESIGN.md) |
| V10 | State projections | [V10](milestones/V10-STATE-PROJECTIONS-DESIGN.md) |
| V11 | Dashboard | [V11](milestones/V11-DASHBOARD-DESIGN.md) |
| V12 | CLI and architecture refactor | [V12](milestones/V12-ARCHITECTURAL-REFACTOR-DESIGN.md) |
| V13 | Multi-agent orchestration | [V13](milestones/V13-MULTI-AGENT-DESIGN.md) |

## Platform Expansion (V14–V34)

| Version | What | Design Doc |
|---------|------|------------|
| V14a-e | Pipeline, streaming, providers, SQLite, Discord coverage | [V14a](milestones/V14a-DECOMPOSE-STREAM-DESIGN.md) |
| V15 | Vector search | [V15](milestones/V15-VECTOR-SEARCH-DESIGN.md) |
| V16 | Intelligence arc | [V16](milestones/V16-INTELLIGENCE-ARC-DESIGN.md) |
| V17 | Performance | [V17](milestones/V17-PERFORMANCE-DESIGN.md) |
| V18 | OpenClaw config transfer | [V18](milestones/V18-OPENCLAW-CONFIG-DESIGN.md) |
| V19 | Smart context injection | [V19](milestones/V19-SMART-CONTEXT-DESIGN.md) |
| V20 | Live orchestrator | [V20](milestones/V20-LIVE-ORCHESTRATOR-DESIGN.md) |
| V21 | Full conversation pipeline | [V21](milestones/V21-FULL-CONVERSATION-DESIGN.md) |
| V22 | Multi-agent delegation | [V22](milestones/V22-MULTI-AGENT-ORCHESTRATION-DESIGN.md) |
| V23 | Platform API, bridge, dashboard, SDK | [V23](milestones/V23-PLATFORM-DESIGN.md) |
| V24 | Visual workflow diagrams | [V24](milestones/V24-VISUAL-WORKFLOW-DESIGN.md) |
| V25 | Visual workflow editor | [V25](milestones/V25-VISUAL-WORKFLOW-EDITOR-DESIGN.md) |
| V26 | Remembrance integration | [V26](milestones/V26-REMEMBRANCE-INTEGRATION-DESIGN.md) |
| V27 | Serve-mode tool executor | [V27](milestones/V27-SERVE-TOOL-EXECUTOR-DESIGN.md) |
| V28 | Project and git tools | [V28](milestones/V28-PROJECT-GIT-TOOLS-DESIGN.md) |
| V29 | Tool guidance and session awareness | [V29](milestones/V29-TOOL-GUIDANCE-SESSION-AWARENESS-DESIGN.md) |
| V30 | Native Ollama tool calling | [V30](milestones/V30-NATIVE-TOOL-CALLING-DESIGN.md) |
| V31 | Native chat streaming gap note | [V31](milestones/V31-CHAT-STREAMING-GAP.md) |
| V32 | Operating environment: state, plans, guard, wake | [V32](milestones/V32-LUMI-OPERATING-ENVIRONMENT.md) |
| V33 | Conversation awareness and channel context | [V33](milestones/V33-CONVERSATION-AWARENESS.md) |
| V34 | OpenAI Responses provider | [V34](milestones/V34-OPENAI-RESPONSES-DESIGN.md) |

## Gated Loop & Observability (V35–V48)

| Version | What | Design Doc |
|---------|------|------------|
| V35 | Objective verification gate in the gated loop | [V35](milestones/V35-VERIFICATION-GATE-DESIGN.md) |
| V37 | Multi-agent delegation wired into the gated loop | [V37](milestones/V37-MULTI-AGENT-DELEGATION-DESIGN.md) |
| V38 | `prizm watch` live run visibility (SSE) | [V38](milestones/V38-LIVE-WATCH-DESIGN.md) |
| V39 | `prizm doctor` preflight health check | [V39](milestones/V39-DOCTOR-PREFLIGHT-DESIGN.md) |
| V40 | Durable `REPORT.md` proof-of-work artifact | [V40](milestones/V40-REPORT-ARTIFACT-DESIGN.md) |
| V41 | Diff preview at the feedback gate | [V41](milestones/V41-DIFF-PREVIEW-DESIGN.md) |
| V42 | Gate-needs-you @-mention notifications | [V42](milestones/V42-GATE-NOTIFICATIONS-DESIGN.md) |
| V43 | `prizm preview` static gated-loop preview | [V43](milestones/V43-WORKFLOW-PREVIEW-DESIGN.md) |
| V44 | Rich Discord approval cards (buttons) | [V44](milestones/V44-APPROVAL-CARDS-DESIGN.md) |
| V45 | De-hardcoded gate personas (config-driven roster) | [V45](milestones/V45-DYNAMIC-ROSTER-DESIGN.md) |
| V46 | `prizm runs` browse past runs & reports | [V46](milestones/V46-RUNS-BROWSER-DESIGN.md) |
| V47 | `--json` inspection output (doctor, runs) | [V47](milestones/V47-JSON-OUTPUT-DESIGN.md) |
| V48 | Actions on flows in the visual workflow editor | [V48](milestones/V48-EDGE-ACTIONS-DESIGN.md) |

## Advanced / Experimental (V49–V58)

| Version | What | Design Doc |
|---------|------|------------|
| V49 | MCP client foundation (consume external MCP tool servers) | [V49](milestones/V49-MCP-CLIENT-DESIGN.md) |
| V50 | Self-patching PR mode (autopatch opens pull requests) | [V50](milestones/V50-AUTOPATCH-PR-DESIGN.md) |
| V51 | Issue-discovery scanner + `prizm scan` (self-directed autopatch) | [V50](milestones/V50-AUTOPATCH-PR-DESIGN.md) |
| V52 | `prizm config` validate + summarize prizm.yaml | [V52](milestones/V39-DOCTOR-PREFLIGHT-DESIGN.md) |
| V54 | Skill-use capabilities (Claude Code / OpenClaw SKILL.md) | [V54](milestones/V54-SKILLS-DESIGN.md) |
| V55 | Config wizard + OpenClaw→prizm.yaml import | [V55](milestones/V55-CONFIG-WIZARD-DESIGN.md) |
| V56 | Gated-loop worktree isolation | [V56](milestones/V56-WORKTREE-ISOLATION-DESIGN.md) |
| V57 | Auto-rollback for failed loops + per-phase token budgets | [V57](milestones/V57-AUTO-ROLLBACK-DESIGN.md) |
| V58 | Full autonomy: generic sub-agent worker (bounded tool-loop, worktree isolation, capability routing) | [V58](milestones/V58-FULL-AUTONOMY-DESIGN.md) |

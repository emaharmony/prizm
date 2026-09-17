# Prizm Product Layers

This document defines the architectural layers of Prizm to clarify system boundaries and trust levels.

## Prizm Core
The essential runtime engine. Without these, Prizm does not function as an event-native, policy-governed system.

| Package / Subsystem | Layer | Stability | Owner | Required | Trust Boundary |
|---|---|---|---|---|---|
| `internal/bus` | Core | Stable | Runtime | Yes | Internal |
| `internal/event` | Core | Stable | Runtime | Yes | Internal |
| `internal/run` | Core | Stable | Execution | Yes | Internal |
| `internal/workflow` | Core | Stable | Execution | Yes | Internal |
| `internal/safety` | Core | Stable | Security | Yes | Strict enforcement |
| `internal/policy` | Core | Stable | Security | Yes | Strict enforcement |
| `internal/agent` | Core | Stable | Orchestration | Yes | Managed |
| `internal/orchestrator`| Core | Stable | Orchestration | Yes | Managed |
| `internal/task` | Core | Stable | State | Yes | Persistence |

## Prizm Platform
Services and interfaces that make the core operational and accessible.

| Package / Subsystem | Layer | Stability | Owner | Required | Trust Boundary |
|---|---|---|---|---|---|
| `internal/api` | Platform | Preview | Platform | Yes (Service mode) | Network Ingress |
| `internal/dashboard` | Platform | Preview | DX | Optional | Read-only / User |
| `internal/router` | Platform | Stable | Orchestration | Yes | Internal |
| `internal/scheduler` | Platform | Preview | Platform | Optional | Timer-driven |
| `internal/session` | Platform | Stable | State | Yes | Persistence |
| `internal/provider` | Platform | Stable | Integration | Yes | Model Provider |
| `internal/invocation` | Platform | Experimental | Platform | Optional | Network Ingress |

## Prizm Integrations
Adapters for external platforms and specialized autonomous capabilities.

| Package / Subsystem | Layer | Stability | Owner | Required | Trust Boundary |
|---|---|---|---|---|---|
| `adapters/discord` | Integration | Preview | Integrations | Optional | Discord API |
| `internal/mcp` | Integration | Experimental | Integrations | Optional | External Host |
| `internal/autopatch` | Integration | Experimental | Autonomous | Optional | Local Worktree |
| `internal/subagent` | Integration | Experimental | Autonomous | Optional | Prizm Delegation |
| `remembrance` | Integration | Experimental | Memory | Optional | RPC / Persistence |
| `adapters/firecrawl` | Integration | Experimental | Integrations | Optional | External Web |
| `adapters/roblox` | Integration | Experimental | Integrations | Optional | External API |

## Layer Definitions

### Prizm Core
*   **Definition:** The minimal set of components required to boot Prizm and execute a task.
*   **Guarantees:** High stability, strong backward compatibility, rigorous testing.
*   **Mutation Safety:** Primary enforcement point for policy and safety.

### Prizm Platform
*   **Definition:** The operational surface of Prizm. APIs, UIs, and management services.
*   **Guarantees:** Stable interfaces, functional parity across releases.
*   **Responsibility:** Orchestrating core components for multi-user or service-mode use.

### Prizm Integrations
*   **Definition:** Extensions that connect Prizm to the outside world or add high-level agent behaviors.
*   **Guarantees:** Varies (see Stability Level). May change rapidly.
*   **Responsibility:** Adapting external protocols to the Prizm event-native core.

# AGENTS.md

## Known Agents

This file defines the agents in this Prizm environment and how they interact.

---

## Current Roster

| Agent        | Role             | Status |
| ------------ | ---------------- | ------ |
| [Agent Name] | [Role] (primary) | Active |

---

## Delegation Rules

1. The primary agent routes tasks and owns coordination.
2. Specialist agents handle domain-specific work delegated by the primary.
3. The primary never delegates approval decisions — those go to the user.
4. Inter-agent communication happens through NATS event subjects.

---

## Adding New Agents

Add agents to the roster table above, then define their delegation contract:

```text
## [Agent Name]

- **Delegates from:** [Parent agent]
- **Task types:** [What tasks this agent handles]
- **Returns:** [What it sends back]
- **Autonomy:** [What it can do without asking]
```

---

## Prizm Package Boundary Rules

When contributing code to Prizm, every agent MUST read and follow
[Package Boundaries](../docs/architecture/PACKAGE_BOUNDARIES.md).

Packages represent bounded domains, not helper categories. Code belongs with
the domain that owns its invariants. Every architectural PR that introduces a
new package MUST include the package justification required by that document.

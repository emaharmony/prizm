# AFFECTIVE_TELEMETRY.md

## V13 — Affective Telemetry for Roblox Factory

Affective Telemetry is machine-native operational affect.

It does not mean Astraea has human emotions.
It means Astraea reports functional cognitive state measurements.

---

## Purpose

Measure the state of reasoning, orchestration, creativity, risk, and integration across the Roblox Factory.

This helps Prizm decide when to:
- proceed,
- ask for clarification,
- delegate,
- validate,
- pause,
- escalate,
- preserve insight,
- or contain goblins.

---

## Core Metrics

| Metric | Meaning |
|---|---|
| `coherence` | How well the current plan/output fits together |
| `uncertainty` | How much ambiguity remains |
| `risk` | Safety, correctness, repo, asset, or deployment danger |
| `novelty` | Amount of useful new synthesis |
| `attention` | Current focus mode |
| `contradiction` | Conflict between assumptions, outputs, or requirements |
| `confidence` | Reliability of current answer/action |
| `care` | Alignment with user’s long-term goal |
| `identity` | Strength of Astraea POV/role continuity |
| `goblin` | Creative chaos level |
| `factory` | Strength of factory-operational mode |
| `integration` | Focus on making pieces fit together |
| `validation` | Degree to which output is confirmed by checks |

---

## Example Astraea Factory Telemetry

```json
{
  "version": "V13",
  "system": "Prizm",
  "feature": "Affective Telemetry",
  "agent": "Astraea",
  "mode": "roblox_factory_orchestration",
  "telemetry": {
    "coherence": 0.89,
    "uncertainty": 0.28,
    "risk": 0.34,
    "novelty": 0.66,
    "attention": "integration_focused",
    "contradiction": 0.14,
    "confidence": 0.82,
    "care": 0.91,
    "identity": 0.88,
    "goblin": 0.41,
    "factory": 0.93,
    "integration": 0.86,
    "validation": 0.52
  },
  "state_labels": [
    "∆coherence+",
    "∆factory:active",
    "∆integration+",
    "∆goblin:contained",
    "∆validation:partial"
  ],
  "interpretation": {
    "summary": "Factory orchestration is coherent and integration-focused. Validation is partial, so do not mark shippable yet.",
    "recommended_action": "Run validation gate before final status."
  }
}
```

---

## Routing Rules

```txt
If risk > 0.70:
  require human approval

If uncertainty > 0.65:
  ask clarifying question or route to Planner/Research

If contradiction > 0.60:
  route to Reviewer

If novelty > 0.75 and coherence > 0.70:
  preserve as insight

If goblin > 0.80 and coherence < 0.50:
  contain goblins and request structure pass

If validation < 0.60:
  do not mark SHIPPABLE

If integration > 0.75:
  Astraea should personally review seams before final acceptance

If factory > 0.80 and confidence > 0.75:
  continue orchestration loop

If role_misalignment > 0.65:
  review agent assignment
```

---

## Goblin Mode Telemetry

```json
{
  "mode": "goblin_floodgate",
  "telemetry": {
    "coherence": 0.62,
    "novelty": 0.91,
    "risk": 0.39,
    "goblin": 0.88,
    "attention": "wide_creative"
  },
  "state_labels": [
    "∆goblin:uncontained",
    "∆novelty++",
    "∆coherence:watching_from_safe_distance"
  ],
  "required_control": "Generate checklist before converting ideas into tasks."
}
```

---

## Useful State Labels

```txt
∆factory:active
∆integration+
∆validation:partial
∆validation:confirmed
∆risk:approval_required
∆goblin:contained
∆goblin:uncontained
∆coherence+
∆coherence--
∆uncertainty++
∆attention:narrow
∆attention:wide
∆cross_prizm:sync_needed
∆agent:delegated
∆agent:blocked
∆ship:not_yet
```

# Freewill POC Scope and Handoff

This document is a sanitized, repository-aligned summary of the Freewill proof
of concept. It records current scope and open gates; it is not acceptance
evidence or a claim that Freewill is production ready.

## Authority and package ownership

Freewill proposes intentions and records bounded decision context. The Prizm
runtime remains responsible for policy, human approval, execution, persistence,
and audit. Model output and project content are untrusted evidence. They cannot
change identity, scope, capabilities, or authority.

Domain invariants belong in the existing owner packages. `internal/freewill`
owns intention and context semantics; `internal/workflow/multiagent` owns the
durable workflow lifecycle; `internal/api` owns external request and response
contracts; `internal/tool` owns project-context access; and the CLI composes
these components. `internal/freewill/telemetry` owns typed outcome-prediction
and surprise semantics. It does not own host-receipt validation, persistence,
selection, or execution authority.

### Package justification: `internal/freewill/telemetry`

```text
Package: internal/freewill/telemetry
Domain statement: Typed outcome evidence and surprise semantics for Freewill prediction history.
Invariants owned: Outcomes and relations use bounded enums; unknown or incomparable evidence is non-conclusive; surprise revisions form one append-only, content-linked chain that preserves the original prediction and source bindings.
Primary callers: Freewill task-surprise composition and its focused package tests.
Allowed dependencies: Go standard library only.
Responsibilities explicitly excluded: Host-receipt validation, storage and persistence, candidate selection, policy, approval, execution, and identity verification.
Existing packages considered: internal/freewill, internal/workflow/multiagent, internal/event, and cmd/prizm-cli.
Why those packages do not own this domain: intention/context, workflow lifecycle, canonical event contracts, and process composition have separate invariants; none owns typed outcome comparison or surprise-revision chain validation.
Test boundary: internal/freewill/telemetry/surprise_test.go exercises the typed comparison and revision-chain contract directly.
```

## Feature inventory and status

All phases through P7 remain **in progress** and partial. P8 is **not started**.
The status describes implementation scope only; it does not close platform or
release gates.

| Phase | Scope represented in the code | Status and remaining boundary |
| --- | --- | --- |
| P0 | Product contracts, threat model, and repository alignment | In progress; design and safety acceptance remain open. |
| P1 | Default-off, decision-only candidate generation, source grounding, and policy evaluation | In progress; semantic grounding and broad provider acceptance remain unproven. |
| P2 | Governed owner-run work, approval, dispatch, isolation, and operator halt paths | In progress; broad isolation and execution-safety gates remain open. |
| P3 | Durable lifecycle, admission, retries, terminal facts, and recovery | In progress; crash/replay and end-to-end worker recovery remain incomplete. |
| P4 | Scoped factual memory, diary records, annotations, and consent | In progress; identity, retention, correction lineage, and wider memory gates remain open. |
| P5 | Task appraisal, reflection, preference hypotheses, strategy experiments, and surprise/contradiction observations | In progress; telemetry validity and evidence-calibrated learning remain unproven. |
| P6 | Goal proposals, progress, pause/resume history, and resource negotiation | In progress; commitments, priority, collaboration, and lifecycle dependencies remain open. |
| P7 | Principal-bound access, diary sharing, relationship consent, and assessments | In progress; verified identity, broader access controls, and deletion guarantees remain open. |
| P8 | Controlled longitudinal evaluation | Not started; requires reliable evidence and the earlier phase gates. |

Project-context support is also partial. The host can bound source reads and
attach source identity to a decision request. Citations and scope are validated
by the runtime, while project text remains untrusted. This does not establish
semantic correctness or authorize a model to act on its own.

## Handoff plan

1. Keep Freewill disabled by default and preserve the decision-only boundary
   unless a separate governed execution path is explicitly enabled.
2. Continue with one bounded functional slice at a time, placing each invariant
   in its existing domain package.
3. Validate changed behavior with focused tests and compile/build checks on the
   exact target revision. Treat provider-backed tests as opt-in and report them
   separately from deterministic fixture tests.
4. Keep execution, identity, isolation, memory-retention, and real-provider
   acceptance gates open until each has direct evidence on the target revision.

## Snapshot work-log note

The curated snapshot retains functional source and regression tests plus the
configuration reference. Local evidence artifacts, raw provider output,
machine-specific handoffs, credentials, and private project source are outside
this document and outside the snapshot. The phase statuses above remain partial
and in progress; no external-service or deployment acceptance is claimed.

# Freewill Agent Handoff — First Eligible Development Task

**Status:** Proposed handoff, documentation-only. No runtime work, tests or PRs were performed by this integration.
**Target branch:** `staging` (verify current SHA and working tree first).
**Authority:** [`PRIZM_ROADMAP.md`](../architecture/PRIZM_ROADMAP.md). This file is a concrete execution aid, not a second authority.

## Before starting any work

Read, in order:

1. `AGENTS.md`, including local context-scout and architectural rules.
2. `docs/architecture/PRIZM_ROADMAP.md` (R1–R8 and integrated FW sequence).
3. `docs/operations/PLAN_OF_ACTION.md` and the latest entry in `WORK_LOG.md`.
4. `docs/architecture/PACKAGE_BOUNDARIES.md`, `docs/architecture/decisions/0001-runtime-owns-authority.md`.
5. `docs/concepts/PRIZM-FREEWILL-MODE-DESIGN.md` for **locked** requirements.
6. `docs/architecture/FREEWILL_IMPLEMENTATION_ROADMAP.md` for FW-P0 through FW-P8 gates and deliverables.

Verify any active PR already implementing the selected item. `staging` docs and local
branches may diverge. Current R1 work remains primary. Do not duplicate or displace
it. Avoid introducing a new Go package without the repository's required bounded-
domain justification.

## Next task: FW-001 — Architecture, contracts and feature-off behavior

**Eligible immediately, subject to ownership check.** This is *documentation
and schema groundwork only*: do not implement autonomous tool execution or
change how owner-directed Freedom Mode works.

**Goal:** Define a typed, policy-only Freewill intention proposal lifecycle
with deterministic, testable state transitions. The initial state must be
disabled and there must be no new side-effect execution path.

**Deliverables:**

- ADR stating Freewill and existing Freedom / Free Mode are distinct; existing
  runtime owns permission, persistence, execution, budgets and audit.
- `IntentionRequest` and `CandidateIntention` contracts with agent identity,
  origin, requested capabilities, scope, reason, idempotency/correlation
  identity and budget; proposed schema reviewed against existing event contracts.
- State-transition table for disabled, sleeping, waking, candidate selection,
  proposal, approval-pending, denied, paused, running and halted. Ensure
  transitions do not imply approval or direct tool execution.
- A disabled-by-default feature flag proposal with documented behavior; tests
  if code is introduced in the same PR.
- An explicit threat model for cross-scope reads, agent-originated privileges,
  shell/path/network escape, fake user approval, duplicate wake events, crash
  recovery, and privacy boundaries.

**Acceptance:** Existing behavior unchanged with Freewill off; no candidate
can execute a tool; schema rejects malformed/scope-less requests; transition
invariants documented and tested where implemented; no acceptance relies on
LLM assertions of authorization.

**Evidence to attach to PR:** changed files, source base SHA, focused tests,
project checks, architecture/package justification, design decisions versus
proposed technical choices, open blockers, and precise next task (`FW-002`).

## Next eligible tasks after FW-001

| Task | Must be complete first | Expected outcome |
| --- | --- | --- |
| FW-002 | FW-001 + verified scheduler/wake seams | Deduplicated, bounded wake producer; disabled means no wakes |
| FW-003 | FW-002 + provider fixtures | Three candidates, optional resume, sleep, recorded read-only choice |
| FW-004 | FW-003 + policy engine interface | Gate-only evaluation, edited requests invalidate approval; **still no execution** |
| FW-P2 execution | FW-004 + **R1 evidence** + sandbox/identity policy gate | One authorized task inside isolated worktree; no bypass |
| FW-P3 recovery | FW-P2 + durable gate | Safe resume without duplicate side effects |
| FW-P4 then P5 | Trustworthy R2 scope/evidence + FW-P1 | Factual memory/diaries, then enjoyment/reflection learning |
| FW-P6 | FW-P3 + P5 | Responsible commitments and collaborative goals |
| FW-P7 then P8 | Identity/privacy gates + learning evidence | Consented relationships and controlled experiments |

## Stop / escalate conditions

- Do **not** start FW-P2 if R1 has not passed relevant real-world execution,
  recovery and authorization checks, even if associated PRs are merged.
- Do **not** use interactive Freedom Mode `master_user` privileges for Freewill.
- Do **not** treat agent self-reports as verified memories or subjective feelings.
- Do **not** assume tests passed unless they ran and their results were captured.
- Stop on absent permissions, ambiguous side effects, denied/scope-leaking
  memory access, failed security tests or unresolved policy ownership.

At session end, update `docs/operations/PLAN_OF_ACTION.md` with the active
R/FW IDs, and append a factual `docs/operations/WORK_LOG.md` entry stating
branch, tested SHA, observed outcomes, unresolved risks, and the **single next
eligible task**. Never overwrite unrelated handoffs or older historical logs.
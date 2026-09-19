# Adapter interaction runtime

Prizm owns the durable decision loop. Integrations expose an environment through
the `internal/adapter` contract and keep their domain state opaque to the core.
The optional interaction extensions provide observations, state-dependent legal
actions, fail-closed validation, neutralization, and readiness checks.

An interaction run follows this bounded sequence:

```text
Observe → restore checkpoint → select goal/lane → enumerate legal actions
→ policy/approval → validate → execute → verify → persist → continue or pause
```

`internal/workflow/multiagent.InteractionScheduler` runs the sequence. Its
strategic, tactical, and reflex phases are data-driven and each phase has a
cadence, interrupt triggers, verification requirements, and action budget. A
failed action receives only the configured number of retries. Every failure,
timeout, approval pause, and verification result calls the adapter's optional
`Neutralize` hook before the run advances or fails.

The scheduler records observations, decisions, validation, execution, outcome,
verification, neutralization, and recovery events in `InteractionRunRecord`.
`MemoryInteractionRunStore` is useful for embedding and deterministic tests;
`JSONInteractionRunStore` supplies a small durable checkpoint for restart and
pause/resume tests. The canonical graph runtime accepts an optional scheduler
through `DurableRuntimeOptions.Interaction`. For authored graph nodes that
include an `execution` policy, the runtime derives the lane, cadence,
interruption triggers, verification checks, action budget, retry budget, and
role deadline from that node before invoking the role runner. Scheduler events
are attached to the graph runtime's durable event outbox, so observations and
outcomes are inspectable with the rest of the run history. The CLI composition
root enables this path by setting `PRIZM_INTERACTION_ADAPTER` to a registered
adapter name; inspection and legacy runs remain unchanged when it is unset.

## Reflection and project learning

Authored graphs can opt into a typed `reflector` role with a reflection policy.
The durable runtime invokes it at configured milestones such as terminal
outcomes, failures, verification failures, interruptions, and recovery. The
reflector returns strict JSON containing a verdict, confidence, evidence,
failure class, an optional lesson candidate, and an optional replan request.
Replanning is advisory and bounded to two passes; it returns through the
configured strategic role rather than allowing the reflector to select an
arbitrary graph transition.

Project memory writeback is opt-in with `PRIZM_REFLECTION_MEMORY=1`. Candidates
are passed through the existing memory gate and a deterministic project-scope
policy before they reach `MemoryStore`; gate or storage failures reject the
candidate without failing the main run.

Adapters remain compatible with the original five-method `Adapter` interface.
They opt into interaction behavior incrementally through `ObservationProvider`,
`LegalActionProvider`, `ActionValidator`, `Neutralizer`, or the richer
`StructuredInteractionAdapter` interface. Policy, approval, and persistence
authority stay in Prizm; physical or external-system safety stays in the
adapter.

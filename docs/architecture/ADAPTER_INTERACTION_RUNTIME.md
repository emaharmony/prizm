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
pause/resume tests. Production composition should connect the store and event
sink to Prizm's existing durable run and event infrastructure.

Adapters remain compatible with the original five-method `Adapter` interface.
They opt into interaction behavior incrementally through `ObservationProvider`,
`LegalActionProvider`, `ActionValidator`, `Neutralizer`, or the richer
`StructuredInteractionAdapter` interface. Policy, approval, and persistence
authority stay in Prizm; physical or external-system safety stays in the
adapter.

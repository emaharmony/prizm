# Registry and Versioning

`DefinitionStore` (`internal/workflow/multiagent/definition_store.go`) is an
immutable, versioned registry for compiled `WorkflowDefinition`s, backed by
a SQLite table your `prism` process owns (`multiagent_definitions.db` by
default — see [Running and inspecting workflows](running-and-inspecting.md)
for the exact flags).

## Immutability guarantees

- `multiagent_definitions(workflow_id, version)` is the table's primary key.
  There is no `UPDATE` path anywhere in this store — every row is immutable
  by construction, not by convention. Overwriting an existing version is a
  schema-level impossibility, not just a discouraged operation.
- `Register` computes the next version as `MAX(version) + 1` for the
  `workflow_id`, **inside the same transaction as the insert** — the version
  number is always a property of what is already durably stored, never
  caller-supplied.
- Registering a definition whose compiled fingerprint already matches a
  previously-registered version for the same `workflow_id` does **not**
  create a redundant new version — it returns the existing row plus the
  sentinel `ErrDefinitionUnchanged` (`errors.Is`), not an error a caller
  needs to treat as failure.
- A **starting run resolves `(workflowID, version|"latest")` once**, at
  start, and stamps the resolved `(workflowID, version, fingerprint)` into
  `RunState` permanently. **Resume always reopens the exact pinned
  `(workflowID, version)` — never "latest"** — even if a newer version has
  since been registered for the same `workflow_id`. This is the concrete
  mechanism that satisfies "updating a definition must not alter
  active/historical runs": a run's behavior is fixed the moment it starts,
  regardless of what gets registered afterward.

## Fingerprinting

Every compiled graph carries a SHA-256 hex `Fingerprint()`
(`internal/checksum.ComputeChecksum`), computed from a deterministic,
canonically-ordered encoding of the graph's nodes/edges/loops/budgets — not
by ranging a Go map (whose iteration order is randomized per-process).
Compiling the same definition 100 times in a row produces the identical
fingerprint every time (this is directly tested,
`compiler_test.go`'s fingerprint-determinism test). `Register` reuses
`graph.Fingerprint()` as-is rather than recomputing it a second way.

## Storage model: recompile-on-read

`DefinitionStore` stores only `definition_json` — the canonical, authored
`WorkflowDefinition` — never a serialized `CompiledGraph`. `Get`/`Latest`/
`GetByFingerprint` all recompile the stored definition on every read via
`Compile`, then **defensively re-check** that the recompiled graph's own
`Fingerprint()` still matches the fingerprint the row was stored under. This
trades a small amount of CPU on every read (validating and rebuilding a
graph that is, realistically, at most a few hundred nodes/edges) for
avoiding an entirely separate "rehydrate a `CompiledGraph` from stored JSON"
code path that would need to be proven equivalent to `Compile` itself — see
`definition_store.go`'s top-of-file comment for the full rationale.

## API surface

`internal/api/multiagent_definitions.go` exposes the registry under
`/api/v1/multiagent/definitions` (a distinct prefix from the existing
`/api/v1/multiagent/runs`):

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/api/v1/multiagent/definitions/validate` | Validate-only — loads and compiles the request body, persists nothing. |
| `POST` | `/api/v1/multiagent/definitions` | Register a new version (or resolve to the existing one via `ErrDefinitionUnchanged`). |
| `GET` | `/api/v1/multiagent/definitions` | List every `workflow_id` with at least one registered version. |
| `GET` | `/api/v1/multiagent/definitions/{id}/versions` | List every registered version's cheap summary. |
| `GET` | `/api/v1/multiagent/definitions/{id}/versions/{n}` | The full `WorkflowDefinition` for one version. |
| `GET` | `/api/v1/multiagent/definitions/{id}/versions/{n}/compiled` | The compiled graph view (fingerprint, nodes, edges, loops, budgets). |
| `GET` | `/api/v1/multiagent/definitions/{id}/versions/{n}/fingerprint` | Just the fingerprint string. |
| `POST` | `/api/v1/multiagent/definitions/{id}/versions/{n}/run` | Start a pinned run against exactly `(id, n)` — never `"latest"`. |

**Duplicate-fingerprint registration returns `200` with `"registered":
false`, not an error.** The request was well-formed and its intent — "make
sure this definition is registered" — was already satisfied by an existing
version, so a 4xx/5xx would be misleading. Check the `registered` field, not
the HTTP status, to tell "a new version was created" apart from "this exact
definition was already registered."

Every mutating route (`POST`/`PUT`/`DELETE`/`PATCH`) is gated by Prism's
existing bearer-token auth middleware whenever a token is configured — see
[Security review](security.md) for how this claim was independently
re-verified against `internal/api/server.go` for this PR, not just trusted
from an earlier PR's report.

`api.Config.DefinitionStore == nil` disables every route above with a `503`,
matching the existing `MultiAgentRuns` zero-value-disables convention — no
running SQLite store means the routes simply aren't available, not a
partially-broken surface.

## CLI equivalents

See [Running and inspecting workflows](running-and-inspecting.md) for
`prism graph run`'s `--workflow`/`--version` (registry-backed) vs. `--file`
(local-dev, validates+compiles+registers+runs in one step) modes.

# Loops and Cycles

A correction loop — a role sending work back to an earlier role — is just an
edge whose destination can eventually lead back to its own source. The
validator's cycle rule (`validateCycles` in
`internal/workflow/multiagent/validator.go`) finds every cycle using
Tarjan's strongly-connected-components (SCC) algorithm and requires a
bounded `loop:` policy on every edge inside a cyclic component. Read this
page before authoring any workflow with a loop back to an earlier role — the
rule's actual scope surprises most people the first time.

## The rule that surprises people: it's SCC-wide, not edge-wide

You might expect the loop-policy requirement to apply only to the
intuitively "backwards" edge — the one that sends work back. It does not.
**Every edge inside the same strongly-connected component needs a `loop:`
policy, including edges that only ever move a run forward.**

Take Planner → Developer → Tester → Reviewer, with two correction loops:
Tester can send work back to Developer (`tests_failed`), and Reviewer can
also send work back to Developer (`changes_requested`). Once both of those
back-edges exist, Developer, Tester, and Reviewer are all reachable from
each other — they form **one** strongly-connected component together. That
means the two purely-forward edges, Developer → Tester
(`implementation_ready`) and Tester → Reviewer (`tests_passed`), are
**also** members of that cyclic component, even though a human would never
call "the developer finished, so we move on to testing" a loop. The
validator requires a `loop:` policy on those two edges as well — omitting
one produces `cycle.unbounded-loop-edge`, a hard error, not a warning.

This is a deliberate design decision
([ADR 0002](../architecture/decisions/0002-graph-defined-workflows.md)):
Tarjan's algorithm finds every cycle in linear time, where enumerating every
*simple* cycle individually (to distinguish "this specific back-edge" from
"an edge that merely happens to sit in the same component") would be
combinatorially worse for no real benefit — the actual invariant being
enforced ("can this edge participate in unbounded repetition") is a
property of the whole component, not of one edge in isolation.

### What to do about the forward edges

The two shipped templates that have a real correction loop
([`templates/software-delivery.yaml`](../../internal/workflow/multiagent/templates/software-delivery.yaml),
[`templates/security-review.yaml`](../../internal/workflow/multiagent/templates/security-review.yaml))
both handle this the same way: give the forward-progress edges a generous
`maxTraversals` (999 and 99 respectively) with `onExhausted: fail`. They are
not meant to ever actually bind — the real bound on how many times a role
can be re-entered comes from that role's own `maxVisits` and from the
genuine correction loop's own tight limit. A generous forward-edge limit is
simply large enough that the workflow's `spec.budgets.maxTransitions`
ceiling will always be hit first if something has actually gone wrong.

```yaml
# software-delivery.yaml — forward edge inside the cycle:
- id: dev-to-tester
  from: developer
  to: tester
  when: { outcome: implementation_ready }
  loop:
    maxTraversals: 999   # generous: not meant to bind in practice
    onExhausted: fail

# ...the genuine correction loop, tightly bounded:
- id: tester-to-dev
  from: tester
  to: developer
  when: { outcome: tests_failed }
  loop:
    maxTraversals: 2     # real bound: at most 2 corrections
    onExhausted: fail
```

## `loop:` policy fields

| Field | Meaning |
| --- | --- |
| `maxTraversals` | Must be a positive integer — **never `-1`** (unlike every other budget dimension in this schema, a loop must always be able to terminate; `budget.unsafe-unlimited-loop` rejects `-1` or `0` here even outside a cycle). |
| `onExhausted` | `"fail"` or `"route_to"`. See the important caveat below. |
| `routeTo` | Required when `onExhausted: route_to`. Must name a known node, and must NOT be inside the same cycle this loop is meant to escape (`cycle.invalid-exhaustion-route` if it is). |

### `onExhausted: route_to` is validated but not yet runtime-enforced

The schema accepts `onExhausted: route_to` and the validator checks it
(`routeTo` must resolve to a known node and must escape the cycle), but as
of this release **the supervisor's runtime loop-exhaustion check does not
actually consult `onExhausted`/`routeTo` at all** — every loop exhaustion,
regardless of the declared `onExhausted` value, ends the run with a
budget-exhausted status identical to `onExhausted: fail`. This was found
during this PR's security/hardening review; see
[Current limitations](limitations.md#loop-onexhausted-route_to-is-not-runtime-enforced)
and [Security review](security.md#finding-looponexhausted-route_to-is-schema-validated-but-not-runtime-enforced)
for the full finding. Neither shipped template relies on `route_to` actually
firing at runtime today — author it if you like (it costs nothing and the
validator will keep it honest), but do not depend on the run actually
landing at `routeTo` until this is implemented.

## When you don't need any of this

If your workflow has no cycle at all — a strict pipeline where a rejection
routes to its own terminal state instead of looping back — none of this
applies. [`templates/documentation-change.yaml`](../../internal/workflow/multiagent/templates/documentation-change.yaml)
is exactly that: Writer → Technical Reviewer → Editor → Published, where a
"needs revision" outcome from either the reviewer or the editor routes to a
separate `needs-revision` terminal rather than back to the writer. No edge
in that workflow needs a `loop:` policy, because there is no cycle for
Tarjan to find. Reach for a bounded loop only when your workflow actually
needs automated back-and-forth correction; a plain DAG is simpler, and
simpler is usually right.

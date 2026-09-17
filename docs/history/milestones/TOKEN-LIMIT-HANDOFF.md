# Token Limit Work Notes

Branch: `token-limit`

## Goal

Find token-related defaults and autonomous run paths that allowed project work to run without a finite token limit, replace accidental unlimited behavior with bounded defaults, preserve an explicit unlimited opt-out, and verify the behavior with tests.

## Completed

- `read_project` has a default aggregate output budget of `50_000` estimated tokens, accepts optional `max_tokens`, and reports aggregate/per-file token metadata.
- Workflow `global.max_total_tokens` semantics are explicit: `-1` = unlimited, `0` or omitted = `DefaultRunTokenCeiling` (`2_000_000`), positive = explicit cap, below `-1` = invalid.
- `v2.NewEngine` normalizes token budgets and defensively rejects invalid negatives for direct callers.
- Workflow state records `max_total_tokens`, and phase state records `max_tokens`, so reports/API/CLI can show ceiling and remaining budget after a run.
- Budget-killed runs end as `budget_exhausted`, and `workflow.completed` carries the terminal status instead of masking budget stops as normal completion.
- Providers that return zero token usage are estimated by prompt/response length so budget enforcement still works.
- Project `token_budget` supports the same `-1` / `0` / positive semantics and overrides the workflow cap.
- Delegated task packets carry `max_tokens`; sub-agent loops honor the parent remaining budget, support `-1`, stop before more tool work once exhausted, and return usage plus prior partial artifacts on budget errors.
- Delegated sub-agent prompt/completion tokens roll up into the parent workflow budget.
- V36 wake tool loops use the resolved project/default/unlimited ceiling instead of a hardcoded run cap and estimate zero-usage provider responses.
- `prizm context show` / run context injection no longer default to unbounded context: `0` resolves to `4000`, `-1` is explicit no truncation, and `< -1` is rejected.
- Cost event aggregation moved into `internal/cost`; `GET /api/v1/costs`, `prizm cost`, and REPORT artifacts expose token totals, ceilings, remaining budget, status, and estimated cost when event logs include cost data.
- `prizm cost` can now fall back to workflow state for token/budget fields even when an events file is missing.
- Default constants are disambiguated with compatibility aliases: `DefaultRunTokenCeiling`, `DefaultSubAgentTokenBudget`, and `DefaultRunResponseTokens`.
- YAML/docs/dashboard labels now document `-1 = unlimited`, `0 = default` for run/project token budgets.
- `.claude/commands/prizm-loop.md` was checked; it adds loop discipline only: finish dirty work, update docs/state, run build/vet/test, commit.

## Regression Coverage

Added or updated tests for:

- omitted/zero workflow token caps defaulting to `2_000_000`;
- `max_total_tokens = -1` unlimited and invalid negatives rejected on load;
- project `token_budget = -1` accepted and `< -1` rejected;
- budget-killed runs ending as `budget_exhausted` with a single global budget event;
- provider zero-usage responses still tripping budgets via estimates;
- delegated token usage rolling up to the parent;
- delegated packets receiving the parent remaining budget and blocking when no budget remains;
- sub-agent packet budget override, unlimited override, over-budget final answers, and partial artifacts/usage on budget errors;
- wake-loop resolved ceiling and unlimited behavior;
- context command budget sentinel/default handling;
- `read_project` aggregate token truncation;
- `REPORT.md` token budget section;
- real `/api/v1/costs` token/budget fields;
- `prizm cost` budget/remaining display and workflow-state fallback.

## Verification

Focused gate:

```powershell
go test ./internal/workflow/v2 ./internal/subagent ./cmd/prizm-cli ./internal/orchestrator ./internal/runtrack ./internal/tool ./internal/api ./internal/cost
```

Final gates:

```powershell
go build ./...
go vet ./...
go test -p 1 ./...
```

Result: all commands passed on `token-limit`.

## Unrelated Working Tree Items

Do not stage unrelated local settings unless intentionally part of a separate change:

- `.claude/settings.local.json`

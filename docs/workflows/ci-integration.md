# CI Integration

`prism graph validate` and `prism graph test` are both plain CLI commands
with meaningful exit codes and no running-service dependency — they load,
validate, compile, and (for `test`) run scripted scenarios entirely
in-process. That makes them safe to run in any CI system without standing
up an agent runtime, a database, or network access.

## Exit codes

| Command | Exit `0` when | Exit non-zero when |
| --- | --- | --- |
| `prism graph validate <file\|dir>...` | Every file has zero error-severity diagnostics (warnings never fail it). | Any file has at least one error-severity diagnostic, or a path/read failure. |
| `prism graph test <file\|dir>...` | Every scenario in every scenario file passed. | Any scenario failed an assertion, or any scenario file failed to load/compile its referenced workflow. |

Both accept a directory and walk it recursively — but **point them at
separate directory trees** if you keep workflow definitions and scenario
fixtures in different places (which you should — see
[Testing workflows](testing.md#why-scenario-fixtures-live-in-a-separate-directory-from-workflow-definitions)
for why mixing the two file kinds under one scanned path breaks both
commands).

## A minimal CI recipe

```bash
prism graph validate ./workflows
prism graph test ./workflow-scenarios
```

Both commands' `--json` flag gives you structured output
(`[]Diagnostic`/`[]graphTestOutcome`, grouped by file for `validate`) if
your CI wants to parse results rather than just check the exit code.

## GitHub Actions example

This repository's own CI (`.github/workflows/ci.yml`) is plain GitHub
Actions with pinned-SHA third-party actions — see
[CI](../operations/ci.md) for the full picture. A project consuming Prism's
CLI as a dependency (not this repository itself) would add a step shaped
like this:

```yaml
jobs:
  validate-workflows:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.26.x"
      - name: Build prism-cli
        run: go build -o prism-cli ./cmd/prism-cli
      - name: Validate workflow graphs
        run: ./prism-cli graph validate ./workflows
      - name: Test workflow graphs
        run: ./prism-cli graph test ./workflow-scenarios
```

## How this repository itself covers its shipped templates

The three shipped templates under
[`internal/workflow/multiagent/templates/`](../../internal/workflow/multiagent/templates/)
and their scenario fixtures under
[`internal/workflow/multiagent/testdata/template-scenarios/`](../../internal/workflow/multiagent/testdata/template-scenarios/)
are covered two ways, both already exercised by this repository's existing
`test-linux`/`test-windows` CI jobs (`go test ./...` — no separate CI job
was added for this):

1. **`template_compat_test.go`** — a Go test that loads, validates,
   compiles, and runs every scenario for all three templates, asserting
   zero errors and every scenario passing. This is what actually runs in
   CI today.
2. **The CLI commands themselves**, for local iteration and as the
   documented, reproducible way to check the same thing outside `go test`:

   ```text
   prism graph validate internal/workflow/multiagent/templates
   prism graph test internal/workflow/multiagent/testdata/template-scenarios
   ```

   (Not `prism graph test internal/workflow/multiagent/templates` — that
   directory holds only workflow definitions, no scenario fixtures; see the
   directory-layout note linked above.)

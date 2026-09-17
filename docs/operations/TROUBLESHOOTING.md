# Prizm Troubleshooting

Common setup and runtime issues for new users, with concrete commands. Run all
commands from the repository root (the folder with `go.mod`).

## Go command issues

Ensure Go 1.26+ is installed:

```bash
go version
```

If modules fail to resolve, fetch them explicitly:

```bash
go mod download
```

## Running from the wrong directory

Go build/run and workflow discovery are relative to the repo root. If commands
fail with missing packages or missing workflows, `cd` to the folder containing
`go.mod` and retry.

## `package cmd/prizm-cli is not in std`

Use a relative package path with the leading `./`:

```bash
go run ./cmd/prizm-cli
```

not:

```bash
go run cmd/prizm-cli
```

## Workflow not found

Workflows load from `examples/workflows/`. List registered names and run from
the repo root:

```bash
go run ./cmd/prizm-cli workflow list
```

## Config not found

`prizm.yaml` is not required for the echo demo. For serve/chat, create it and
pass `--config`:

```bash
cp prizm.yaml.example prizm.yaml
go run ./cmd/prizm-cli config --config prizm.yaml
```

## YAML parse error

Validate and summarize your config:

```bash
go run ./cmd/prizm-cli config --config prizm.yaml
```

Check indentation (spaces, not tabs), quoting of values with special characters,
and that list items align.

## Unknown step type

A workflow step's `type` must be a supported type (e.g. `tool.execute`,
`dispatch.run`, `delegate`). See the
[YAML Reference](../reference/YAML_REFERENCE.md#workflow-yaml).

## Policy denied action

Some actions are denied by design (e.g. `run_command` shell execution). Inspect
the rules:

```bash
go run ./cmd/prizm-cli policy list
```

The `reason` on the matching rule explains why. See
[`policies/default.yaml`](../../policies/default.yaml).

## Tool validator blocked action

Even when policy allows an action, local validators enforce input safety (e.g.
workspace path checks for `read_file`/`write`). Ensure paths fall under the
configured `read_roots` / `write_roots` in `prizm.yaml`.

## Artifacts not written

Runs write to the run directory (default `./runs`, or `--run-dir`). Confirm the
directory is writable and check the run id echoed by the command. See
`examples/runs/sample-run/` for the expected artifact set.

## Dashboard not loading

The dashboard is served by `prizm serve` on the API port (`prizm.port + 1`,
default `8322`). Confirm serve mode is running and open
`http://localhost:8322/`. `prizm dashboard` starts a separate read-only view.

## Provider / model not configured

The echo demo needs no model. For `prizm run` with a real model, pass a provider
and model, or configure agents in `prizm.yaml`:

```bash
go run ./cmd/prizm-cli run --task "hello" --provider ollama --model llama3.2
```

## API key missing

API-backed providers (OpenAI, Anthropic, Gemini) require credentials via
environment variables (e.g. `OPENAI_API_KEY`). Do not inline keys in YAML.

## Ollama not running

Start Ollama and confirm it is reachable (default `http://localhost:11434`).
Override with `--ollama-url` or the `OLLAMA_BASE_URL` env var.

## Remembrance unavailable

Remembrance is optional and disabled by default. If enabled, start the Python
service and check health:

```bash
go run ./cmd/prizm-cli remembrance health
```

## MCP connection failed

Probe the server before enabling it in `mcp_servers`:

```bash
go run ./cmd/prizm-cli mcp probe <name>
```

## Tests failing

Run the suite with race detection and no cache:

```bash
go test ./... -count=1 -race
```

Run a single package for faster iteration, e.g.:

```bash
go test ./internal/checksum/ -v -count=1
```

## Windows-specific notes

- Use `.\prizm-current.exe` style paths in PowerShell; chain commands with `;`,
  not `&&`.
- Use forward slashes or escaped backslashes in YAML paths (e.g. `D:/projects`).
- See [docs/WINDOWS_SETUP.md](../getting-started/WINDOWS_SETUP.md) for a full walkthrough.

## macOS / Linux notes

- Prefer `go build -o prizm ./cmd/prizm-cli` and run `./prizm`.
- Paths are case-sensitive on Linux; match file names exactly.

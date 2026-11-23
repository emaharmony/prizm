# OpenShorts plugin for Prizm

Import this plugin to register OpenShorts as an external MCP server and
initialize its SQLite databases and manifest directory. Prizm starts the
plugin executable and discovers its tools when it loads your configuration.

Build from the Prizm checkout on Windows.

```powershell
go build -o .\openshorts-prizm.exe ./plugins/openshorts
.\openshorts-prizm.exe import --config C:\path\to\prizm.yaml
prizm mcp probe openshorts --config C:\path\to\prizm.yaml
```

On macOS or Linux, build with `go build -o ./openshorts-prizm ./plugins/openshorts`
and use `./openshorts-prizm import --config /path/to/prizm.yaml`.

Keep the executable at its imported path. State defaults to
`.prizm/plugins/openshorts` alongside the configuration. Use `--state-dir`
during import to choose another directory. Re-importing the same settings
leaves the configuration and stored jobs intact. Startup and tool calls
continue to use Prizm's existing MCP approval policy.

| Registered tool | What it does |
| --- | --- |
| `mcp_openshorts_guide` | Return the bundled workflow instructions. |
| `mcp_openshorts_run_demo` | Run or resume a synthetic clipping job with mock QA and export receipts. |
| `mcp_openshorts_status` | Read a durable demo job by ID. |

Import prepares the plugin for Stage 1. A job starts when you explicitly call
`run_demo`. Use the optional `generation_id` to run a new synthetic job. The
mock result carries `mode: mock`. Real VOD intake, rendering, and export
require the later stages.

To disable the plugin, set `enabled: false` on its `mcp_servers` entry and
reload Prizm. Removing that entry unregisters it. The state directory can
be retained for review.

Run `go test ./plugins/openshorts/...` for import, MCP registration, and
recovery coverage. See the [contract](contracts/v1/README.md) for the workflow
records and package boundaries.

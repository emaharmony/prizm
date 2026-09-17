# Prizm Windows Setup

These steps are for running Prizm from this repo on Windows with PowerShell.

## 1. Install Prerequisites

Install:

- Go 1.26 or newer
- Git for Windows
- Python 3.11 or newer, only if you want the Remembrance memory service
- Ollama, if you want local Ollama models
- Docker Desktop, optional

Check the main tools:

```powershell
go version
git --version
python --version
```

If Go reports telemetry or cache access errors, run the commands in a normal
PowerShell session outside a restricted shell and disable telemetry for the
session:

```powershell
$env:GOTELEMETRY = "off"
```

## 2. Clone and Build

```powershell
git clone https://github.com/emaharmony/prizm.git
cd prizm
$env:GOTELEMETRY = "off"
go build -o .\prizm-current.exe .\cmd\prizm-cli
go build -o .\prizm-bus-current.exe .\cmd\prizm-bus
```

Nothing is checked into the repository as a prebuilt binary — `*.exe` and the
root `prizm`/`prizm-bus`/`prizm-agent` binaries are gitignored. Always build
from source for a fresh setup.

Verify the CLI:

```powershell
.\prizm-current.exe version
.\prizm-current.exe --help
```

## 3. Run Tests

Fast verification:

```powershell
$env:GOTELEMETRY = "off"
go test .\internal\orchestrator .\internal\provider\ollama .\cmd\prizm-cli -run Test -count=1
```

Full test suite:

```powershell
$env:GOTELEMETRY = "off"
go test .\... -count=1
```

Race tests are useful but slower:

```powershell
$env:GOTELEMETRY = "off"
go test .\... -count=1 -race
```

## 4. Configure Prizm

Copy the example config:

```powershell
Copy-Item .\prizm.yaml.example .\prizm.yaml
```

Edit `prizm.yaml` before running `serve`.

Important Windows notes:

- `data_dir: "~/.prizm/data"` is not expanded by the current Go config loader.
  Use a repo-relative or absolute Windows path instead.
- `workspace` should be explicit on Windows. The fallback uses `$HOME`, which is
  not always set in Windows shells.
- `${DISCORD_BOT_TOKEN}` in YAML is not expanded by the current config loader.
  Put the real token in the file or remove the Discord channel while testing.

Minimal local config without Discord:

```yaml
prizm:
  nats_url: ""
  data_dir: ".\\.prizm\\data"
  workspace: "D:\\_projects_\\prizm"
  ollama_url: "http://localhost:11434"
  log_level: "info"

agents:
  - id: lumi
    role: lead
    provider: ollama
    model: "llama3.2"
    primary: true
    context: []
    capabilities: [plan, route, review, report]

channels: []
actions: []

sessions:
  idle_timeout_minutes: 30
  daily_reset_hour: 4
  max_context_messages: 100
  compaction_strategy: "truncate"

remembrance:
  enabled: false
  url: "http://localhost:18790"
```

For OpenAI, Anthropic, or Gemini agents, set the matching environment variable:

```powershell
$env:OPENAI_API_KEY = "..."
$env:ANTHROPIC_API_KEY = "..."
$env:GEMINI_API_KEY = "..."
```

## 5. Run Prizm Serve Mode

`serve` starts its own embedded NATS bus when `prizm.nats_url` is empty.

```powershell
.\prizm-current.exe serve --config .\prizm.yaml
```

Default URLs:

- Health: `http://localhost:8321/health`
- API/status: `http://localhost:8322/api/v1/status`

Stop it with `Ctrl+C`.

## 6. Run One-Shot CLI Mode

`prizm run` is different from `serve`: it expects a NATS bus at
`nats://localhost:4222`.

Start the bus in one PowerShell window:

```powershell
.\prizm-bus-current.exe
```

Then run a dry-run in another PowerShell window:

```powershell
.\prizm-current.exe run --task "Windows setup smoke test" --dry-run-prompt
```

Run with Ollama:

```powershell
ollama serve
ollama pull llama3.2
.\prizm-current.exe run --task "Explain Prizm in one paragraph" --provider ollama --model llama3.2
```

## 7. Optional: Remembrance Memory Service

Install and initialize Remembrance:

```powershell
cd .\remembrance
python -m venv .venv
.\.venv\Scripts\Activate.ps1
python -m pip install --upgrade pip
pip install -e .
ollama pull nomic-embed-text
remembrance init
remembrance ingest --file .\memory_seed\framework.jsonl
```

Start the API server:

```powershell
uvicorn remembrance.app:app --host 127.0.0.1 --port 18790
```

Then enable it in the repo root `prizm.yaml`:

```yaml
remembrance:
  enabled: true
  url: "http://localhost:18790"
```

## 8. Optional: Dashboard

The standalone dashboard command reads run artifacts and policies:

```powershell
.\prizm-current.exe dashboard --port 8080 --run-dir .\runs --policy-dir .\policies
```

Open `http://localhost:8080`.

In `serve` mode, the live API is on port `8322` by default.

## Common Issues

`failed to connect to NATS`

Start `.\prizm-bus-current.exe` before using `prizm run`, or use `prizm serve`
for embedded NATS.

`OPENAI_API_KEY environment variable not set`

Your config has an OpenAI agent. Set `$env:OPENAI_API_KEY` or switch the agent
provider/model to Ollama.

Discord connects with the literal value `${DISCORD_BOT_TOKEN}`

The config loader does not expand env vars inside YAML. Replace the token value
or remove the Discord channel for local testing.

Data created under a folder named `~`

The config loader does not expand `~`. Change `data_dir` to `.\\.prizm\\data`
or an absolute path.

## See Also

- [Getting Started](GETTING_STARTED.md)
- [Troubleshooting](../operations/TROUBLESHOOTING.md)
- [Documentation Hub](../README.md)

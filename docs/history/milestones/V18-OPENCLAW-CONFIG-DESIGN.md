# V18 — OpenClaw Config Transfer

**Status:** Implemented
**Date:** 2026-05-19

## Mission

Prizm should read LLM provider configuration from OpenClaw's `openclaw.json`, so you configure providers once and both systems use them. No more duplicating API keys, base URLs, and model lists.

## What Changed

### 1. OpenClaw Config Loader (`internal/provider/openclaw.go`)

New function `LoadFromOpenClaw(path)` that:

1. Reads `~/.openclaw/openclaw.json` (or a specified path)
2. Parses the `models.providers` section
3. For each provider, creates the appropriate Prizm `Provider`:
   - `api: "ollama"` → `ollama.New(baseUrl)`
   - `api: "openai"` → `openai.New(apiKey)` with optional `baseUrl` override
   - `api: "anthropic"` → `anthropic.New(apiKey)` with optional `baseUrl` override
   - `api: "gemini"` → `gemini.New(apiKey)`
4. Returns a `ProviderRegistry` mapping model IDs to providers
5. Merges model metadata (context window, cost) into Prizm's pricing table

### 2. Provider Registry (`internal/provider/registry.go`)

New struct `ProviderRegistry` that maps model IDs to their Provider instances:

```go
type ProviderRegistry struct {
    providers map[string]provider.Provider  // model ID → provider
    models    map[string]ModelInfo           // model ID → metadata
    chains    map[string]*ChainProvider      // name → chain
}
```

Methods:
- `Get(modelID string) (provider.Provider, error)` — look up provider by model ID
- `ListModels() []string` — list available model IDs
- `ModelInfo(modelID string) (ModelInfo, error)` — get context window, cost, capabilities
- `Register(modelID string, p provider.Provider, info ModelInfo)` — add a provider
- `NewChain(name string, providers ...provider.Provider) *ChainProvider` — create a chain

### 3. CLI Integration

New `prizm run` flags:
- `--config <path>` — path to `openclaw.json` (default: `~/.openclaw/openclaw.json`)
- `--from-config` — load provider configuration from OpenClaw config file

When `--from-config` is set:
1. Load `openclaw.json`
2. Build `ProviderRegistry` from all configured providers
3. `--model` selects from the registry (e.g., `glm-5.3-flash:cloud`, `gpt-4o`)
4. `--provider` is optional — inferred from config if `--model` is unique
5. If `--provider` is specified, it filters to that provider's models

### 4. Model Metadata

```go
type ModelInfo struct {
    ID            string    `json:"id"`
    Name          string    `json:"name,omitempty"`
    ContextWindow int       `json:"context_window"`
    InputTypes    []string  `json:"input_types"`
    Reasoning     bool      `json:"reasoning"`
    Cost          ModelCost `json:"cost"`
}

type ModelCost struct {
    InputPer1K  float64 `json:"input_per_1k"`
    OutputPer1K float64 `json:"output_per_1k"`
    CacheRead   float64 `json:"cache_read_per_1k"`
    CacheWrite  float64 `json:"cache_write_per_1k"`
}
```

Model metadata flows from OpenClaw config → ProviderRegistry → CostTracker (V16). This means `prizm cost` now shows accurate costs even for custom/Ollama models.

### 5. Config Discovery

Prizm searches for OpenClaw config in this order:
1. `--config` flag path (explicit)
2. `OPENCLAW_CONFIG` environment variable
3. `~/.openclaw/openclaw.json` (default)
4. Fallback: manual `--provider` / `--model` flags (existing behavior)

## Design Decisions

1. **No OpenClaw dependency** — Prizm reads a JSON file, it doesn't import OpenClaw packages. The config format is stable and well-known.
2. **Merge, don't replace** — OpenClaw config supplements manual flags. If you specify `--provider openai --model gpt-4o`, that takes precedence.
3. **API keys from config** — OpenClaw stores API keys in `openclaw.json`. Prizm reads them. No environment variable duplication needed.
4. **Cost metadata flows through** — OpenClaw's `cost` per model feeds directly into Prizm's V16 `CostTracker`. Accurate per-model pricing without manual entry.
5. **Backward compatible** — All existing `prizm run --provider mock --model mock-model` calls continue to work. `--from-config` is opt-in.

## Packages

| Package | Purpose | Files |
|---------|---------|-------|
| `internal/provider/openclaw.go` | OpenClaw config loader | New |
| `internal/provider/registry.go` | Provider registry (model → provider) | New |
| `cmd/prizm-cli/cmd_run.go` | `--from-config` and `--config` flags | Modified |

## Test Coverage

- OpenClaw config parsing (all provider types)
- Provider registry lookup by model ID
- Config discovery order (flag > env > default)
- Merge behavior (config + manual flags)
- Missing config file graceful fallback
- API key extraction from config
- Model metadata → CostTracker integration

## What's NOT in V18

- **OpenClaw channel/agent config** — Only models/providers are transferred, not Discord/Signal channels or agent definitions
- **Hot reload** — Config is read at startup. For hot reload, use SIGHUP or a future V.
- **OpenClaw gateway integration** — Prizm doesn't connect to the OpenClaw gateway daemon; it just reads the config file
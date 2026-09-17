# V2 — Real LLM Execution

## Mission

Replace the V1 placeholder agent with real LLM execution. Introduce a pluggable
provider interface, a prompt builder, a mock provider for testing, and an Ollama
provider for production. Wire the full LLM lifecycle into the runner with
comprehensive event types and CLI flags.

**Prizm owns the lifecycle. The model only generates text.**

## What Changed

### Provider Interface (`internal/provider`)
- `Provider` interface with single method: `Generate(ctx, req) → (res, err)`
- `GenerateRequest`: RunID, CorrelationID, Agent, Project, Task, Prompt, Model, Temperature, MaxTokens
- `GenerateResponse`: Text, Model, Provider, LatencyMS, PromptTokens, OutputTokens, Raw metadata
- `MockProvider` — returns deterministic output for testing
- `FailingMockProvider` — returns errors for failure path testing
- `OllamaProvider` — POST `/api/generate` with configurable base URL, context timeout support
- Providers are stateless — Prizm handles lifecycle, events, and retries

### Prompt Builder (`internal/prompt`)
- `BuildPrompt(task, project, context)` — assembles the prompt with template + injected context
- `WritePrompt(runDir, prompt)` — persists `prompt.md` artifact
- `WriteOutput(runDir, output)` — persists `output.md` artifact
- Memory context is injected into the prompt (optional, from Remembrance)

### V2 Event Types
- `prizm.llm.requested` — prompt is assembled, about to call provider
- `prizm.llm.completed` — text generated successfully
- `prizm.llm.failed` — provider returned error or timed out
- `prizm.agent.failed` — added for provider failure flow (parent: `llm.failed`)
- `prizm.context.injected` — memory context was successfully merged into prompt
- `prizm.context.requested` — memory context was requested
- `prizm.context.failed` — memory context request failed
- `prizm.output.written` — output artifact persisted

### Runner Rewrite (`internal/run`)
- `Runner.Run()` rewritten to use `Provider` interface instead of placeholder agent
- Full V2 event emission at every lifecycle point
- `DryRunPrompt` mode: build prompt + artifacts, skip LLM call
- V2 `Summary` fields: Provider, Model, OutputPath, PromptPath, MemoryStatus, LLMLatencyMs, LLMError
- `ProviderName` fallback: defaults to `"mock"` when `Provider` is nil

### CLI Flags (`cmd/prizm-cli`)
- `--provider` (mock | ollama)
- `--model` (LLM model name, e.g., `llama3.2`)
- `--temperature` (float, default 0.7)
- `--max-tokens` (int, default 4096)
- `--timeout` (duration string, e.g., `120s`)
- `--ollama-url` (Ollama base URL, default `http://localhost:11434`)
- `--dry-run-prompt` (build artifacts, skip LLM execution)
- Version bumped to `v0.2.0`

### Parent Chain Fixes
- `agent.completed` parent → `llm.completed` (was `agent.started`)
- `output.written` parent → `agent.completed`
- `task.completed` parent → `agent.completed`
- `agent.failed` parent → `llm.failed` (was `llm.requested`)

Correct causal chain:
```
task.created → task.started → agent.started → llm.requested →
  llm.completed → agent.completed → output.written → task.completed
```

## Key Packages/Files

| Package / File | Purpose |
|---|---|
| `internal/provider/provider.go` | Provider interface + request/response types |
| `internal/provider/mock.go` | Deterministic mock for testing |
| `internal/provider/ollama.go` | Ollama HTTP client for production |
| `internal/prompt/builder.go` | Prompt assembly + artifact persistence |
| `internal/event/event.go` | Extended with V2 event types |
| `internal/run/runner.go` | Rewritten with full LLM lifecycle |
| `cmd/prizm-cli/main.go` | New CLI flags for provider, model, etc. |

## Design Decisions

1. **Provider is an interface, not a framework** — Prizm doesn't dictate provider
   implementation. Any backend that implements `Generate()` works. This keeps the
   core runner provider-agnostic.

2. **Prizm owns the lifecycle, the model only generates** — The provider interface
   is narrow by design. Providers don't manage events, approvals, tool calls, or
   memory. Prizm handles everything around the `Generate()` call.

3. **Mock provider for deterministic testing** — Real LLM calls are non-deterministic
   and slow. The mock provider returns guaranteed output, making tests fast and
   reproducible. The `FailingMockProvider` exercises failure paths.

4. **Ollama as the first real provider** — Ollama is self-hosted, free, and doesn't
   require API keys. The provider uses `httptest` for unit tests (success, timeout,
   error cases). Future providers (OpenAI, Anthropic, Gemini) follow the same pattern.

5. **Dry-run mode** — `--dry-run-prompt` builds the full prompt and artifacts
   without calling the LLM. This is essential for debugging prompt assembly and
   testing the pipeline end-to-end without LLM latency or cost.

6. **Parent chain as causal chain** — Every event must correctly trace back to its
   cause. The parent chain fix ensures events form a valid DAG. Tests assert the
   full causal chain, not just individual parent IDs.

7. **Prompt persistence** — `prompt.md` and `output.md` are persisted alongside
   the event log. This makes every run fully reproducible: given the prompt, any
   LLM provider (or human) can produce the same reasoning artifacts.

## Test Coverage

- **46 tests total** (25 V1 + 7 prompt builder + 5 provider + 9 V2 runner tests)
- Prompt builder: valid task, empty project, context injection, markdown template
- Provider: mock success, failing mock, Ollama success/timeout/error (httptest)
- Runner: DryRunPrompt, LLM request/completed/failed events, output write, memory context injection/request/failed, agent failed lifecycle
- Parent chain assertions for full causal chain

## Artifacts per Run

```
runs/<run_id>/
├── events.jsonl          # Line-delimited event log (V2 events included)
├── summary.json          # V2 fields: provider, model, latency, tokens
├── prompt.md             # Assembled prompt (always persisted)
└── output.md             # LLM output (if successful)
```

## Provider Architecture

```
Runner.Run()
  → prompt.BuildPrompt(task, project, context)
  → emit(prizm.llm.requested)
  → emit(prizm.context.requested)       [optional]
  → RemembranceClient.GetContext()      [optional]
  → emit(prizm.context.injected/failed) [optional]
  → provider.Generate(ctx, request)
  → emit(prizm.llm.completed | prizm.llm.failed)
  → prompt.WriteOutput(runDir, output)
  → emit(prizm.output.written)
```

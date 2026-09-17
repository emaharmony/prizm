# V34 - OpenAI Responses Provider

## Mission

Add an OpenAI Responses API provider without removing the existing OpenAI chat-completions provider.

## What Changed

### New Provider Name

Serve mode supports:

```yaml
provider: openai_responses
model: "gpt-5.1"
```

The existing `openai` provider remains available.

### Provider Implementation

`internal/provider/openai/responses.go` implements `provider.Provider`.

It:

- Calls the OpenAI Responses API.
- Uses `OPENAI_API_KEY`.
- Collects output text from Responses output items.
- Reports provider name as `openai_responses`.
- Uses the shared provider response shape.

### Serve Integration

`cmd/prizm-cli/cmd_serve.go` creates the Responses provider when an agent config uses `provider: openai_responses`.

Unsupported provider errors list the currently supported serve-mode providers:

- `ollama`
- `openai`
- `openai_responses`
- `anthropic`
- `gemini`

### Config Example

`prizm.yaml.example` includes a commented API-backed agent example.

```yaml
# - id: openai-reviewer
#   role: reviewer
#   provider: openai_responses
#   model: "gpt-5.1"
#   context:
#     - agents
#   capabilities:
#     - review
#     - summarize
```

## Public Interfaces

Environment:

```powershell
$env:OPENAI_API_KEY = "..."
```

Agent config:

```yaml
agents:
  - id: reviewer
    role: reviewer
    provider: openai_responses
    model: "gpt-5.1"
    capabilities: [review, summarize]
```

## Limitations

- This provider is currently synchronous.
- Native tool calling is not documented as part of this provider.
- ChatGPT web subscriptions and OpenAI API billing are separate; an API key with billing access is required.

## Testing

Tests cover:

- Successful Responses API generation.
- Output text collection.
- Provider name reporting.
- Retryable rate-limit behavior.

## Status

Implemented as an additive provider option.

# V14c — Real Providers + Refract Track Adapter + Deployment

## Mission

Make Prizm actually useful. Right now it only has mock + ollama providers and
an echo adapter. V14c adds an OpenAI-compatible provider, the first real adapter
(Refract Track), and a deployment story.

**Prizm is React for AI.** When state changes, actions fire automatically.
Refract Track is the proof: when `task.completed` fires, Refract Track
auto-logs progress. No manual tracking. The model doesn't do it — the event
system does.

## What V14c Builds

### 1. OpenAI-Compatible Provider

A provider that calls OpenAI's chat completion API using raw HTTP (no SDK).
This gives Prizm access to GPT-4, GPT-3.5, and any OpenAI-compatible endpoint.

```go
// internal/provider/openai.go

type OpenAIProvider struct {
    apiKey     string
    baseURL    string    // defaults to https://api.openai.com/v1
    httpClient *http.Client
    models     []string  // available models
}

func NewOpenAIProvider(apiKey string) *OpenAIProvider
func (p *OpenAIProvider) Name() string
func (p *OpenAIProvider) Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error)
func (p *OpenAIProvider) GenerateStream(ctx context.Context, req GenerateRequest) (<-chan TokenChunk, error)
```

Key design decisions:
- **Raw HTTP, no SDK.** The OpenAI Go SDK is 2MB. We need ~200 lines.
- **Provider chaining.** `--fallback-provider openai --fallback-model gpt-4` for
  automatic fallback when Ollama is unavailable.
- **Tier-based paid guard.** `--allow-paid-fallback` flag. Without it, the
  fallback chain skips paid providers.
- **Streaming support.** Implements `StreamingProvider` using SSE (Server-Sent Events).

### 2. Provider Chaining

```go
// internal/provider/chain.go

type ChainProvider struct {
    providers       []Provider  // ordered list of providers to try
    allowPaid       bool        // whether to include paid providers in the chain
}

func (c *ChainProvider) Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error) {
    for _, p := range c.providers {
        if !c.allowPaid && p.Tier() == TierPaid {
            continue
        }
        resp, err := p.Generate(ctx, req)
        if err == nil {
            return resp, nil
        }
        if !IsRetryable(err) {
            return resp, err  // non-retryable error, don't try next provider
        }
        // retryable error, try next provider
    }
    return GenerateResponse{}, fmt.Errorf("all providers failed")
}
```

### 3. Refract Track Adapter (First Real Adapter)

Refract Track auto-logs project progress when tasks complete. It subscribes
to `prizm.task.completed` events and writes progress entries to JSONL files.

```go
// internal/adapter/builtin/refracttrack/refracttrack.go

type RefractTrackAdapter struct {
    storagePath string  // ~/.prizm/refract-track/<project>/
}

func (a *RefractTrackAdapter) Name() string        // "refract-track"
func (a *RefractTrackAdapter) Version() string     // "1.0.0"
func (a *RefractTrackAdapter) Capabilities() []adapter.Capability {
    return []adapter.Capability{
        {Action: "log_progress", Description: "Auto-log task progress"},
        {Action: "query_status", Description: "Query project status"},
        {Action: "list_projects", Description: "List tracked projects"},
    }
}
func (a *RefractTrackAdapter) Execute(ctx context.Context, input map[string]any) (map[string]any, error)
func (a *RefractTrackAdapter) Health(ctx context.Context) (*adapter.Health, error)
```

**Auto-logging from events:**
When `prizm.task.completed` fires, Refract Track automatically creates a
progress entry with the task description, status, agent, and timestamp.
No manual action needed. That's the thesis in action.

### 4. Deployment Story

```makefile
# Makefile

.PHONY: dev test build

dev:                           ## Run in development mode
	go run ./cmd/prizm-cli run --task "hello" --project dev --agent lumi

test:                          ## Run all tests
	go test ./... -count=1 -race

test-coverage:                 ## Run tests with coverage
	go test ./... -coverprofile=coverage.out
	go tool cover -html=coverage.out

build:                         ## Build binary
	CGO_ENABLED=0 go build -o prizm ./cmd/prizm-cli/

build-all:                     ## Cross-compile for all platforms
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o prizm-linux-amd64 ./cmd/prizm-cli/
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -o prizm-darwin-arm64 ./cmd/prizm-cli/
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o prizm-windows-amd64.exe ./cmd/prizm-cli/

lint:                          ## Run linters
	go vet ./...
```

```dockerfile
# Dockerfile

FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o prizm ./cmd/prizm-cli/

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=builder /app/prizm /usr/local/bin/prizm
ENTRYPOINT ["prizm"]
CMD ["--help"]
```

```yaml
# docker-compose.yaml

version: '3.8'
services:
  prizm:
    build: .
    ports:
      - "8080:8080"
    volumes:
      - ./runs:/app/runs
      - ./policies:/app/policies
    environment:
      - PRIZM_PORT=8080
      - PRIZM_RUN_DIR=/app/runs
      - PRIZM_POLICY_DIR=/app/policies
```

### 5. `--embedded-bus` Mode

For getting started without NATS, `prizm run --embedded-bus` starts an in-process
NATS server. This eliminates the "install NATS first" friction for new users.

```go
// internal/bus/embedded.go

func StartEmbeddedBus(port int) (string, error) {
    // Start an in-process NATS server
    // Return the URL for the runner to connect to
}
```

## File Structure

```
internal/
├── provider/
│   ├── provider.go          # Existing Provider interface
│   ├── streaming.go         # Existing StreamingProvider interface
│   ├── mock.go              # Existing mock provider
│   ├── ollama.go            # Existing Ollama provider
│   ├── openai.go           # NEW: OpenAI-compatible provider (raw HTTP)
│   └── chain.go            # NEW: Provider chaining with tier-based fallback
├── adapter/builtin/
│   ├── echo/               # Existing echo adapter
│   └── refracttrack/       # NEW: Refract Track adapter
│       └── refracttrack.go
├── bus/
│   └── embedded.go         # NEW: In-process NATS server for --embedded-bus
├── stage/
│   └── (existing files)    # No changes to stage package
cmd/prizm-cli/
│   └── cmd_run.go          # Updated: --fallback-provider, --allow-paid-fallback
Makefile                      # NEW: dev, test, build, lint targets
Dockerfile                    # NEW: Multi-stage build
docker-compose.yaml           # NEW: One-command start
```

## Acceptance Criteria

1. `internal/provider/openai.go` — OpenAI-compatible provider with Generate + GenerateStream
2. `internal/provider/chain.go` — Provider chaining with tier-based fallback
3. `internal/adapter/builtin/refracttrack/` — Auto-logging adapter with log_progress, query_status, list_projects
4. Refract Track auto-logs from `prizm.task.completed` events
5. `Makefile` with dev, test, build, build-all, lint targets
6. `Dockerfile` with multi-stage build, CGO_ENABLED=0
7. `docker-compose.yaml` with prizm service
8. `--embedded-bus` flag for in-process NATS server
9. All 393+ existing tests pass unchanged
10. New tests for OpenAI provider, chain provider, Refract Track adapter
11. Design doc: `docs/V14c-PROVIDERS-REFRACT-TRACK-DESIGN.md`

## What V14c Does NOT Include

- WAL integration into Pipeline.Run() (V14a follow-up)
- SQLite (V14d)
- Concurrent runs (V14d)
- Discord integration (V14e)
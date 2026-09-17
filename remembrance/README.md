# Remembrance — Semantic Memory for Agentic Workflows

> *What context should this agent receive before doing this task?*

Remembrance is a framework-native memory system for AI agents. It provides semantic retrieval, context injection, and event-driven memory ingestion — built from the ground up for the Prizm event-driven agent platform.

## Architecture

```
Go Core (Prizm)                    Python (Remembrance)
├── Agent Runtime                  ├── Event Consumer
├── Task Runner                    ├── Memory Ingest Pipeline
├── Native Event Manager           ├── Embedding Provider (Ollama)
├── Context Middleware ──HTTP──────► Context Builder
├── Remembrance Client             ├── Vector Store (LanceDB)
└── Permission Layer               ├── Metadata Store (SQLite)
                                   ├── Hybrid Retriever
                                   ├── Memory Evaluator
                                   └── API / CLI
```

## Quick Start

### Install

```bash
cd remembrance
pip install -e .
ollama pull nomic-embed-text
```

### Initialize

```bash
remembrance init
```

### Ingest Seed Memories

```bash
remembrance ingest --file memory_seed/framework.jsonl
```

### Search

```bash
remembrance search "How does context injection work?" --project framework
```

### Build Context

```bash
remembrance build-context "Implement the event consumer" --project framework --out context.md
```

### Start API Server

```bash
uvicorn remembrance.app:app --host 127.0.0.1 --port 18790
```

## API Endpoints

| Method | Path | Description |
|--------|------|-------------|
| POST | `/v1/memory/ingest` | Ingest a new memory |
| POST | `/v1/memory/search` | Search for relevant memories |
| POST | `/v1/context/build` | Build a context pack for an agent task |
| GET | `/v1/health` | Health check |

## Hybrid Ranking

Search results are ranked by:

```
final_score = vector_similarity * 0.65
            + keyword_score * 0.15
            + importance_score * 0.10
            + project_match * 0.05
            + recency_score * 0.05
```

## Go Integration

The Go client (`go/remembrance.go`) provides a `RemembranceClient` interface:

```go
client := prizm.NewRemembranceClient("http://localhost:18790")
context, err := client.BuildContext(ctx, prizm.BuildContextRequest{
    ProjectID: "my-project",
    AgentID:   "lumi",
    Task:      "Implement the event consumer",
    MaxTokens:  2500,
})
// context.ContextMarkdown is ready for injection into agent prompt
```

## V1 Stack

- **Python 3.11+** with Pydantic, FastAPI, Typer
- **LanceDB** for vector storage (local, embedded, no server needed)
- **SQLite** for metadata, audit logs, memory events
- **Ollama nomic-embed-text** for embeddings (768 dimensions)
- **Go HTTP client** for Prizm integration

## Security (V1)

- No raw SQL exposed to agents
- No schema changes from APIs
- No hard deletes (soft delete via status='deleted')
- No shell execution from memory content
- All writes logged to audit_log
- API binds to localhost by default

## License

Part of the Prizm project.
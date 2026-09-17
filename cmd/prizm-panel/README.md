# prizm-panel — desktop pet

A small native desktop window that connects to a running Prizm instance and shows a
friendly **ASCII pet** whose mood tracks the agent: sleeping when idle, thinking
while it works, happy on success, worried when a human is needed. It's the
non-developer-facing face of `prizm watch` — the creature and a plain-language
caption lead, with the phase/token detail shown below as secondary stats.

It is **read-only**: it consumes the same SSE event stream `prizm watch` uses
(`GET /api/v1/events/stream`) and never affects a run. Local-only by default.

## Why it's a separate module

The GUI uses [Fyne](https://fyne.io), which requires **cgo + a C compiler**. To keep
the pure-Go `prizm` CLI unaffected, this lives in its own Go module
(`cmd/prizm-panel/go.mod`). The root module's `go build ./...` and CI never compile
it. The reusable, GUI-free logic lives back in the main module and is unit-tested
there:

- `internal/tracker` — the live workflow model + ASCII stats rendering (shared with
  `prizm watch`).
- `internal/petart` — the pet's moods, frames, and captions.

## Prerequisites

- A C compiler on `PATH`. On Windows, [WinLibs](https://winlibs.com) /
  MinGW-w64 gcc works:

  ```powershell
  winget install -e --id BrechtSanders.WinLibs.POSIX.UCRT
  ```

  (You may need to open a new shell so `gcc` is on `PATH`.)

## Build

From the repo root:

```bash
make build-panel
```

or directly:

```bash
cd cmd/prizm-panel
CGO_ENABLED=1 go build -o ../../prizm-panel .
```

## Run

Start Prizm, then launch the panel:

```bash
prizm serve          # in one terminal (API defaults to 127.0.0.1:8322)
prizm panel          # launches the pet window (finds the co-located prizm-panel binary)
```

`prizm panel` forwards all flags to the panel (e.g. `prizm panel --port 8322`) and,
if the binary isn't built yet, tells you how to build it. You can also run the
binary directly: `./prizm-panel`.

### Flags

| Flag        | Default            | Meaning                                        |
|-------------|--------------------|------------------------------------------------|
| `--host`    | `127.0.0.1`        | API host                                       |
| `--port`    | `8322`             | API port (this is the `prizm serve` port `+ 1`) |
| `--url`     | *(derived)*        | Full base URL; overrides `--host`/`--port`     |
| `--token`   | *(empty)*          | Bearer token (only needed for non-loopback binds) |
| `--subject` | `prizm.workflow.>` | NATS subject filter for the event stream       |

The panel reconnects automatically with backoff, so it survives `prizm serve`
restarting — the pet drops to **sleeping / "Can't reach Prizm — retrying…"** and
wakes back up when the daemon returns.

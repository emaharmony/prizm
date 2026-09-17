# Roblox Game-Dev Agent Team

A small dev studio of Prizm agents that designs and builds Roblox games. Roster,
models, tooling, and the external handoffs are described here. Personas/
personality are a later pass — `prizm-workspace/AGENTS.md` holds the shared
roster charter for now.

## Roster

| Agent | Role | Model (provider) | Why this model |
|---|---|---|---|
| `astraea` | Orchestrator | `claude-opus-4-8` (**claude_code**) | strongest planning/delegation; no per-token bill (subscription) |
| `scout` | Researcher | `qwen3.5:9b` (ollama, local) + `llama3.2-vision:11b` (vision) | runs often; local/free; vision for captioning refs |
| `muse` | Game Planner | `nemotron-3-ultra:cloud` (ollama) | strong ideation + rubric reasoning, parallel to the orchestrator |
| `atlas` | Factory Master | `deepseek-v4-pro:cloud` (ollama) | stronger interpretation of Factory build/validation results |
| `chisel` | Asset Maker | `qwen3.5:9b` (ollama, local) | high-volume Blender tool-calling; local avoids metering blowout |
| `forge` | Coder (Prizm Go) | `nemotron-3-ultra:cloud` (ollama) | powers autopatch / Prizm self-maintenance (not Roblox work) |

The registry is keyed by model, so `scout` + `chisel` share one `qwen3.5:9b`
registration. Only `astraea` (the primary agent) may propose code writes;
everyone else is read-only on the codebase.

## Production pipeline

```text
idea ──► rubric score ──► plan ──► references ──► assets ──► Factory build ──► validate ──► integrate
 muse       (remote)      muse      scout        chisel        atlas          atlas       astraea
```

## Reference-image tools (the Researcher)

Native Prizm tools in `internal/tool/image_tools.go`, auto-approved (read-only
w.r.t. code; they write only into safe reference/output folders):

| Tool | What it does | Config (env) |
|---|---|---|
| `collect_reference_images` | Scout reference collector: probes local Codex image output, then falls back to configured image/web search and downloads images | `PRIZM_IMAGE_SEARCH_URL`, `PRIZM_IMAGE_SEARCH_KEY`, fallback `PRIZM_WEBSEARCH_URL`/`PRIZM_WEBSEARCH_KEY`; optional Codex CLI |
| `fetch_image` | Download an image URL into `references/` or a safe `output_dir` | uses `PRIZM_IMAGE_DIR`, default `<workspace>/references` |
| `generate_image` | Text->image via a generic endpoint, saved to `references/` or a safe `output_dir` | `PRIZM_IMAGEGEN_URL`, `PRIZM_IMAGEGEN_KEY` |
| `analyze_image` | Vision-caption a path/URL for the reference brief | `PRIZM_VISION_MODEL` (default `llama3.2-vision:11b`), `PRIZM_VISION_URL`/`OLLAMA_BASE_URL` |

Each degrades to a clear "not configured" result when its endpoint/model is
unset. `collect_reference_images` treats Codex as optional: it only uses the
CLI when the executable resolves, `codex login status` succeeds, and
`codex exec` returns image base64 or image URLs in the expected JSON contract.
If Codex is missing, closed, unauthenticated, blocked, or returns no usable
image data, Scout falls back to the configured search endpoint. All image save
locations are checked against the workspace/write roots; delegators may pass a
safe `output_dir`.

Setup:

```bash
ollama pull llama3.2-vision:11b      # vision model for analyze_image
export PRIZM_IMAGE_SEARCH_URL=...    # preferred JSON image-search endpoint
export PRIZM_WEBSEARCH_URL=...       # fallback JSON search endpoint (Brave/Serper/SearXNG)
export PRIZM_IMAGEGEN_URL=...        # optional: a text->image endpoint
```

> **Vision model / Ollama version:** `llama3.2-vision` uses the `mllama`
> architecture, which requires a **recent Ollama** (older builds fail with
> `unknown model architecture: 'mllama'`). If `analyze_image` returns that
> error, either upgrade Ollama, or use a broadly-compatible VLM instead:
> `ollama pull llava` then `export PRIZM_VISION_MODEL=llava`. The default is
> `llama3.2-vision:11b`; override it anytime with `PRIZM_VISION_MODEL`.
>
## Asset pipeline (the Asset Maker → Blender → Factory)

`chisel` drives Blender through MCP (`mcp_blender_*` tools), building models from
`scout`'s reference brief, then hands exports to the Roblox Factory.

Config in `prizm.yaml`:

```yaml
mcp_servers:
  - name: "blender"
    command: "uvx"
    args: ["blender-mcp"]
    enabled: true
mcp_auto_approve: true
```

Requires **`uv`/`uvx` installed** and **Blender running with the MCP addon**
(ahujasid/blender-mcp). Without them, serve logs `MCP blender: error: ...` and
continues (the rest of the team is unaffected). Adjust `command`/`args` to your
Blender MCP install.

## Rubric handshake (the Game Planner → remote Prizm)

`muse` sends ideas to a **second Prizm instance holding the rubric** over the
cross-Prizm bridge. Config in `prizm.yaml` under `bridge.target_profiles`:

```yaml
- name: "rubric"
  instance_id: "REPLACE_WITH_REMOTE_INSTANCE_ID"
  adapter: "generic"
  capabilities: ["plan", "review", "report"]
```

`muse` uses `send_cross_message` (`message_type: task_request` or
`validation_request`) with `request.expected_output: validation_report` and a
`definition_of_done` drawn from the rubric; the remote replies with the
structured `task_response` (status, confidence, validation, recommended next
action) defined in `CROSS_PRIZM_PROTOCOL.md`.

**Prerequisites:** both Prizms share one NATS broker (`bridge.mode: shared_nats`)
and the same `PRIZM_BRIDGE_SECRET`; set the real remote `instance_id` above.

## Factory handoff (the Factory Master → Roblox Factory)

`atlas` packages `task_request`s to the `factory` target_profile via
`send_cross_message`; the coordinator writes the Factory queue
(`<factory-root>/inbox/*.json`). Defaults to report-only unless
`bridge.factory.approval_mode: implementation` (+ `run_codex: true`). See
`docs/PUDGYPOWER-FACTORY-RUNBOOK.md`.

## Execution model (now vs. later)

- **Real autonomous execution today:** `atlas` (Factory queue), `chisel`
  (Blender MCP), and the rubric round-trip (cross-Prizm) run against genuine
  external runners.
- **Orchestrator-routed today:** `scout` and `muse` run either as agents you
  chat with directly, or as roles `astraea` adopts inside the gated loop by
  calling their tools — until the generic sub-agent worker lands (deferred).
- Every agent is independently addressable now: `prizm chat --agent scout`.

## Caveat

The serve-mode tool suite (research/image/MCP tools) currently initializes when
a messaging channel is configured. The production `prizm.yaml` has a Discord
channel, so the tools register. (A future cleanup can decouple tool setup from
channels.)

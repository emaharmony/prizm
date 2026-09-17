# Cross-Prizm Factory Setup

This setup connects two Prizm environments over a shared NATS broker without relying on Discord as the agent-to-agent transport. Discord is a command and observer surface only.

## Roles

- `lumi-ceo`: Mac-side CEO Prizm.
- `astraea-manager`: Windows-side manager Prizm.
- `forge`: Windows-side coder/Factory support agent.
- NATS broker: Windows host, default `nats://127.0.0.1:4222`.
- Factory target: `D:/Projects/Roblox/eggventura`.

## Required Environment

Set these on both Prizm environments:

```powershell
$env:PRIZM_BRIDGE_SECRET="use-a-long-random-shared-secret"
```

Set this on the Windows Prizm if Discord is enabled:

```powershell
$env:DISCORD_BOT_TOKEN="..."
```

Set this only for agents using `provider: openai_responses` or `provider: openai`:

```powershell
$env:OPENAI_API_KEY="..."
```

OpenAI ChatGPT subscriptions and OpenAI API platform billing are separate. Prizm can use OpenAI API models through `OPENAI_API_KEY`; it cannot spend a ChatGPT web subscription directly as an API model entitlement. Official references:

- <https://help.openai.com/en/articles/9039756-managing-billing-settings-on-chatgpt-web-and-platform>
- <https://platform.openai.com/docs/api-reference/responses/create>

## Protocol Subjects

Only these signed protocol subjects should be enabled:

- `prizm.cross.context_sync`
- `prizm.cross.task_request`
- `prizm.cross.status_request`
- `prizm.cross.validation_request`
- `prizm.cross.task_response`
- `prizm.cross.task_accept`
- `prizm.cross.task_reject`
- `prizm.cross.clarification`
- `prizm.cross.task_progress`
- `prizm.cross.task_result`
- `prizm.cross.task_cancel`

Each message includes `from`, `to`, `message_type`, `correlation_id`, timestamp, nonce, and an HMAC-SHA256 signature. Messages not addressed to the local `prizm.instance_id` are ignored.

## Delegation Flow

Use target profiles instead of hardcoded project logic:

```yaml
bridge:
  leader_instance: "lumi-ceo"
  confidence_threshold: 0.75
  max_clarification_rounds: 1
  target_profiles:
    - name: "generic"
      instance_id: "astraea-manager"
      adapter: "generic"
      capabilities: ["plan", "review", "report"]
    - name: "factory"
      instance_id: "astraea-manager"
      adapter: "factory"
      capabilities: ["roblox_factory", "validation", "report"]
    - name: "codex"
      instance_id: "astraea-manager"
      adapter: "codex"
      capabilities: ["code", "test", "review", "report"]
```

The receiver rates confidence from the signed request. If confidence is below `confidence_threshold`, it sends one `clarification_request` by default. After the clarification budget is exhausted, the task needs human input instead of entering an infinite loop.

Accepted work returns a structured report contract:

- `status`
- `summary`
- `confidence`
- `artifacts`
- `blockers`
- `next_recommended_action`
- `needs_human_input`
- `thread_id`
- `correlation_id`

## Discord Commands

The current Discord adapter receives message events, so Prizm supports message-style command shims:

```text
/prizm delegate target:factory task:run a Factory smoke check and report artifacts
/prizm delegate target:generic task:review the project state and return blockers
/prizm delegate target:codex task:implement the requested code change and report tests
/prizm status target:generic task:cross-corr-...
/prizm stop target:generic task:cross-corr-...
```

These commands publish signed NATS messages. They do not make one bot talk to another bot in Discord.

Keep `listen_to_agents: []` for Discord-connected agents. `prizm serve` now ignores Discord messages authored by bots, which prevents startup greeting and acknowledgment loops.

## Codex Subscription Worker

The `codex` target profile uses the local OpenAI Codex CLI, not `OPENAI_API_KEY`. Run this once on the machine that will execute tasks:

```powershell
codex login
codex login status
```

Then enable the worker:

```yaml
codex:
  enabled: true
  executable: ""          # empty = codex.cmd on Windows, codex elsewhere
  model: ""               # empty = Codex CLI default/profile model
  profile: ""
  workspace: ""
  sandbox: "workspace-write"
  approval_policy: "on-request"
  timeout_minutes: 30
  max_concurrency: 1
  capture_diff: true
  extra_args: []
```

Local delegation also works when Codex is enabled:

```text
[DELEGATE: codex | code] Implement the scoped code change and run relevant tests.
```

Codex outputs are stored under the Prizm data directory in `codex/<task-id>/`, including the prompt, stdout, stderr, final message, and optional git diff artifacts.

## Factory Handoff

The Windows Prizm accepts signed `task_request` messages addressed to `astraea-manager`. With the current config it writes:

- Request markdown: `D:/_projects_/roblox-factory/prompts/prizm-<correlation>.md`
- Queue task JSON: `D:/_projects_/roblox-factory/inbox/prizm-<correlation>.json`

Current milestone defaults:

- `approval_mode: report_only`
- `run_codex: false`
- `vision_review: none`
- `playtest_mode: none`

Implementation and Codex execution stay off until explicitly enabled.

## OpenAI Model Option

Use this per-agent config when API billing is configured:

```yaml
provider: openai_responses
model: "gpt-5.1"
```

The existing `provider: openai` path remains available for Chat Completions-compatible behavior.

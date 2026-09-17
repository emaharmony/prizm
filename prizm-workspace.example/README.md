# prizm-workspace (Example)

This directory contains example templates for a Prizm agent workspace. Copy this folder to `prizm-workspace/` and customize the files for your agent.

```bash
cp -r prizm-workspace.example prizm-workspace
```

The `prizm-workspace/` directory is **gitignored** — it contains your agent's local identity, user profile, and operational context. It is never committed to the repository.

---

## Files

| File | Purpose |
|------|---------|
| `SOUL.md` | Agent personality, tone, philosophy, and behavioral vows |
| `IDENTITY.md` | Agent identity card — name, role, capabilities, boundaries |
| `ORCHESTRATOR.md` | Orchestration logic, workflows, decision framework |
| `USER.md` | Creator/user profile, preferences, schedule, platforms |
| `AGENTS.md` | Known agents roster and delegation rules |

---

## How It Works

Prizm's context builder reads these files and injects them into the agent's system prompt. Files are loaded based on the `context` list in `prizm.yaml`:

```yaml
agents:
  - id: my-agent
    context:
      - soul        # loads SOUL.md
      - identity    # loads IDENTITY.md
      - user        # loads USER.md
      - agents      # loads AGENTS.md
```

`SOUL.md` is always injected at highest priority (never truncated). Other files are injected based on available token budget.

---

## Customization

Each file is a Markdown document. Edit them freely to define your agent's personality, role, and operational rules. The templates below contain placeholder sections — fill in what applies and remove what doesn't.

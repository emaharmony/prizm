# V54 — Skill-Use Capabilities (Claude Code / OpenClaw Skills)

**Status:** Source-current (complete)
**Last Updated:** 2026-06-30
**Rating: ~0/10 → 8.75/10**

## Goal

Let Prizm agents discover and use **skills** — the `SKILL.md` "Agent Skills"
convention used by Claude Code and OpenClaw. A skill is a named capability
(frontmatter `name` + `description` + a markdown instruction body, optional
`allowed-tools`, plus bundled scripts/resources) that the model invokes when
relevant; invoking it loads the skill's instructions into context to steer
behavior, and bundled scripts run through Prizm's normal tool/policy path.

## Starting rating: ~0/10

Prizm had **no skill concept** before this change — zero references in the Go
codebase; OpenClaw config covers only providers/models; the only `SKILL.md` files
present were unrelated Python venv artifacts. So the ability to use Claude
Code / OpenClaw skills was effectively nonexistent.

## Design (to reach 8.75+)

A self-contained `internal/skill` package that reuses Prizm's existing substrate
(tool registry, prompt builder, policy) with no gated-loop core change.

### This iteration (foundation) — `internal/skill`

- **`Skill`** — `{Name, Description, Body, AllowedTools, Dir, Source}`; `Info()`
  is the cheap name+description listing view (so skills can be advertised to the
  model without loading every body).
- **`Parse(content, dir, source)`** — splits the `---`-delimited YAML frontmatter
  from the markdown body (CRLF-safe; unterminated frontmatter degrades to all-body;
  name falls back to the directory base name). Frontmatter maps `name`,
  `description`, `allowed-tools`.
- **`Registry`** — thread-safe Register/Resolve/List/Len; first registration wins
  on a name collision (trusted dirs load first).
- **`LoadDir(root, source)`** — discovers `<root>/<name>/SKILL.md` (the Claude Code
  layout) plus a `SKILL.md` placed directly in root; missing root is not an error;
  per-skill parse errors are collected without aborting the rest.
- **`DefaultSkillDirs` / `LoadDefault(root)`** — scans, in precedence order,
  `.claude/skills` (source `claude`), `.openclaw/skills` (`openclaw`), and `skills/`
  (`workspace`), so Claude Code skills win collisions.
- **`Prompt()`** — renders a skill's invocation payload (heading, description,
  allowed tools, skill dir, body) for injection when the skill is used.

### `use_skill` tool + prompt advertising (shipped — iteration 2)

- **`tool.UseSkillTool`** (`internal/tool/skill_tool.go`) — backed by a
  `*skill.Registry`; `Execute({name})` resolves the skill and returns its
  `Prompt()` instructions (plus name/source/dir). Unknown name → failed result
  listing available skills; nil/empty registry handled. `RegisterSkillTool` adds it
  to the tool registry only when ≥1 skill exists.
- **Policy:** `use_skill` is auto-approved (read-only — returns instructions only).
  Scripts a skill bundles still run through the mutation tools' own approval gates.
- **`skill.PromptSuffix(infos)`** — renders the "Available skills (invoke with
  use_skill …)" block (name + truncated description) for the system prompt; "" when
  none.

### Serve wiring (shipped — iteration 3)

At `prizm serve` startup (where the tool registry is built), after MCP
registration: `skillReg = skill.NewRegistry(); skillReg.LoadDefault(workspaceRoot)`
discovers the workspace's skills, `tool.RegisterSkillTool(toolReg, skillReg)` adds
`use_skill` (only if ≥1 skill), and `wakeHandler.SetSkills(skillReg)` makes the
gated-loop system prompt append `skill.PromptSuffix(...)`. So the model sees the
available skills and can invoke them; skill-bundled scripts run through the normal
(already policy-gated) mutation tools. The load count is logged at startup.

### Inspection: `prizm skills` + doctor (shipped — iteration 4)

- **`prizm skills [--root .] [--json]`** lists discovered skills (name, source,
  description); **`prizm skills show <name>`** prints a skill's full instruction
  payload. Same discovery `prizm serve` uses, so it shows exactly what agents can
  invoke. Verified end-to-end against a real on-disk `.claude/skills/<name>/SKILL.md`.
- **`prizm doctor`** gained a `skills` check reporting the discovered count
  (informational/OK; WARN if a skills dir failed to parse).
- Added to the grouped help under a "Skills" section.

## Final rating: 8.75/10

| Dimension | Assessment |
|-----------|------------|
| Architectural | Self-contained `internal/skill` package; integrates via the existing tool registry, policy engine, and prompt builder; no gated-loop core change. Same clean injectable pattern as MCP. |
| Logical | Correct SKILL.md frontmatter parsing (CRLF/unterminated/name-fallback); both Claude Code and OpenClaw layouts; precedence + dedup; read-only `use_skill` with scripts still gated. |
| Computational | Cheap listing (name+description) vs. lazy body load on invoke; bounded prompt advertising; discovery is a one-time startup scan. |

End-to-end: **discover → advertise → invoke → policy-gated**, inspectable via CLI
and doctor, for both Claude Code (`.claude/skills`) and OpenClaw (`.openclaw/skills`)
skills. Up from ~0 (no skill concept existed).

### Follow-ups (optional polish, not needed for 8.75)

- Bundled-resource path scoping (a `read_file` against a skill's dir).
- Hot-reload of skills without a serve restart.
- Per-skill `allowed-tools` enforcement (today advisory in the prompt).

## Tests

`internal/skill/skill_test.go`: frontmatter parsing (full / name-fallback /
no-frontmatter / CRLF / unterminated), `Prompt` rendering, registry
register/resolve/list/dedup, `LoadDir` (incl. missing root + non-skill dirs), and
`LoadDefault` precedence (claude wins over workspace). New package → suite 70→71,
all green.

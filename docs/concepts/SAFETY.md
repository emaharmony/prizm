# Prizm Safety Model

Prizm is designed so that the framework — not the model — controls what actually
happens. Models generate outputs and request actions; the runtime decides
whether those actions run. This page explains the human-in-the-loop model and
the boundaries you should rely on.

> See also: [SECURITY.md](../../SECURITY.md) for reporting and known risk areas,
> and [Capability Status](../reference/CAPABILITY_STATUS.md) for stable vs experimental
> surface.

## Core Principles

- **The framework controls the lifecycle.** Policy, approvals, and validators
  gate every action. A clever prompt cannot talk the runtime into a denied
  action.
- **Policy is not the same as validation.** The policy engine decides
  *permission* (allow / deny / requires approval) deterministically, with no LLM
  involved. Local validators separately enforce *input safety* (paths,
  allowlists, sizes). Both must pass.
- **LLMs cannot approve their own mutations — with one explicit, opt-in
  exception.** By default, file mutations require an approval gate resolved
  by a human (or an explicitly configured reviewer), never by the acting
  model. **Free Mode** (see below) is a deliberate, owner-only bypass of this
  gate for a single trusted operator; it does not apply to any other user or
  channel.
- **Local-first and explicit.** External integrations are opt-in and disabled by
  default. The HTTP API binds to loopback unless you set an auth token.

## Secrets and Artifacts

- **Never commit secrets.** Do not commit `.env`, API keys, tokens, broker or
  Discord credentials. Prefer environment variables
  (e.g. `${DISCORD_BOT_TOKEN}`, `api.auth_token_env`, `bridge.secret_env`).
- **Run artifacts may contain sensitive data.** Files under `runs/<run_id>/`
  (prompts, outputs, event logs) can include private context. They are
  gitignored by default; only sanitized examples under `examples/runs/` are
  tracked. Do not commit real run artifacts.

## Free Mode (Owner-Authorized Mutation Mode)

Free Mode — sometimes called "trusted-owner mutation mode" — is an
**experimental, opt-in, single-user** bypass of the approval-gate workflow,
introduced for interactive Discord use. It is not a general auto-approve
switch: it is scoped to one configured operator and one channel setting at a
time.

**Who can enable it:** only whoever controls `prizm.yaml`. Free Mode requires
two things to be true simultaneously for a given message:
1. The Discord channel's `channel_roles` entry has `mode: free`.
2. The message sender's Discord user ID matches `shell.master_user_id`
   exactly.

**Default state:** off. `mode` defaults to `gated` and `shell.master_user_id`
defaults to empty — an empty `master_user_id` means Free Mode can never
activate, even on a channel configured with `mode: free`.

**Who else can trigger it:** nobody. Every other Discord user in a
free-mode-configured channel is explicitly routed back to gated behavior;
there is no remote or cross-Prizm path into Free Mode. It is Discord-only —
the HTTP API, CLI, and scheduled/autonomous work do not go through this
switch.

**Scope of the bypass:** while active, mutation tools that would otherwise
require approval (`write_file`, `write_file_proposal`, `create_directory`,
`create_directory_proposal`, `git_add`, `git_commit`, `git_push`,
`git_checkout`, `create_pr`, and the shell tool) are auto-approved for the
duration of that one message. The bypass is reset immediately after the
message is handled — it is not a standing session state.

**What still applies:**
- File and directory writes are independently re-validated against the
  configured workspace root and write roots at the tool implementation
  level, *separately* from the approval-gate bypass. A free-mode write
  cannot escape those roots even though it skips the approval prompt.
- The shell tool's hard blocklist (catastrophic patterns such as
  `rm -rf /`, `mkfs`, raw disk writes, fork bombs) is always enforced and
  cannot be disabled by configuration or tier.
- **`project.default_branch` is an enforced protection.** `GitCommitTool`
  and `GitPushTool` (`internal/tool/git.go`) refuse to commit or push while
  the repo's current branch — or, for push, an explicitly targeted branch
  param — equals the configured protected branch (default `"main"`). This
  check is in the tool itself, not the approval layer, so it applies
  unconditionally — including in Free Mode. `git_checkout` is unaffected,
  since checking out the branch is not itself a mutation; the check fires
  on the subsequent commit/push. Note the scope limit: this is a single
  global branch name resolved from the default project
  (`Config.ProtectedBranch()`), not a per-project setting — in multi-project
  deployments with differing `default_branch` values, only the default
  project's branch name is protected across every repo any git tool
  touches.
- Every free-mode action still emits audit events
  (`prizm.free.action`, `prizm.mutation.applied`, etc.) to the normal event
  stream — nothing about Free Mode is silent.

**What does NOT still apply — read this before enabling:**
- **The shell tool's working directory is not path-contained.** Unlike file
  tools, the shell tool does not restrict `cwd` to the workspace or write
  roots — only the hard blocklist and the channel's configured
  `shell_policy` command-pattern tier restrict what runs. A free-mode
  channel with no explicit `shell_policy` defaults to `tier_3` (effectively
  unrestricted command patterns). Treat Free Mode's shell access as
  "whatever the OS user running Prizm can do," not "sandboxed to this
  repository."

**How to disable it:** set `mode: gated` (or omit `mode`) on every channel
role, and/or leave `shell.master_user_id` empty. There is no separate global
kill switch beyond removing these two config values.

**Why it is not recommended for shared or exposed deployments:** Free Mode
was built for a single owner's personal, interactive Discord channel. If
`shell.master_user_id` is set to an account other than the deployment
owner's, or a free-mode channel is exposed to a Discord server with
untrusted members, the master-user check is the only thing standing between
"chat interface" and "mostly unrestricted shell access with workspace-scoped
but no directory-scoped shell execution." Do not enable it on multi-tenant
or publicly-joinable deployments.

## Self-Patching / Autopatch

Autopatch is **experimental** and disabled by default.

- Prefer `propose` mode, which produces a patch artifact for review and never
  touches your repository.
- `pr` mode opens a pull request via the `gh` CLI — it should not auto-merge or
  auto-apply without human review.
- Keep `require_clean_worktree: true` and do not run autopatch on sensitive
  repositories without explicit consent.
- Fixes are authored in isolated git worktrees and gated by validation profiles.

## Explicitly Out of Scope

- **Live trading / financial execution** is not supported. Trading-style
  adapters are report-only examples, never live order execution.
- **Unattended high-risk automation** and **enterprise multi-user deployment**
  are not production-ready.

## No False Guarantees

Prizm reduces risk through layered controls; it does not make automation
"fully safe" or "fully autonomous." Review experimental features before enabling
them, and keep a human in the loop for any mutation or high-impact action.

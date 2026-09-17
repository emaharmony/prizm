# LinkedIn Launch — Source Material

This is factual source material for whoever writes the actual LinkedIn post
and any accompanying repository presentation — **not the post itself**. Pull
from it; don't publish it verbatim.

## One-Sentence Description

Prizm is a Go event-native AI agent runtime where the framework — not the
model — owns lifecycle, policy, approvals, budgets, validation, and
persistence, and agents operate strictly inside those boundaries.

## Central Message

> Most agent systems begin with the model and add control later. Prizm
> begins with the runtime: lifecycle, policy, approvals, tools, budgets,
> validation, persistence, and observability are owned by the framework.

## Three Possible Hooks

1. **The control-first framing.** "Every agent framework promises
   guardrails. Most bolt them onto the model after the fact. Prizm starts
   from the other end: a deterministic policy engine decides what's allowed
   before any model output can act — the model never approves its own
   mutations."
2. **The verification-gate angle.** "An agent that writes code and never
   runs the tests isn't autonomous, it's unsupervised. Prizm's gated dev
   loop won't let a phase complete until an allowlisted build/test profile
   actually passes — and feeds the real failure back to the model instead
   of trusting its own report."
3. **The honest-preview angle.** "We're publishing Prizm in public preview
   with the actual numbers: verified test counts, known Windows-only test
   gaps, and one safety feature (Free Mode) documented in enough detail to
   tell you exactly what it does and doesn't protect against. No inflated
   claims."

## Five Strongest Capabilities

1. Deterministic, model-independent policy engine gating every tool call.
2. Verified gated dev loop — build/test verification enforced before a
   workflow phase can complete, not just requested.
3. Multi-agent delegation with worktree-isolated, bounded sub-agents.
4. Local-first architecture — embedded NATS + SQLite, no external broker or
   database required for the core golden-path demo.
5. A same-origin dashboard with live token-usage tracking, a run browser,
   and a visual workflow editor.

## Strongest Quality Evidence

- 1669 Go tests across 85 packages, verified on this commit, 0 failures.
- Clean `go vet` and staticcheck (0 findings).
- Coverage gate passes: 55.4% aggregate, every safety-critical package
  (policy, approval, mutation, validation, guard, tool authority) at 80%+.
- 0 broken internal documentation links (163 found and fixed in this pass —
  worth mentioning as evidence of the audit, not hiding).
- All three CI workflows pin third-party GitHub Actions to immutable commit
  SHAs.
- A five-minute, model-free demo anyone can run without an API key.

Note: the coverage number above was verified locally on Windows, not yet
from a Linux CI run (the gate isn't wired into CI yet) — say "coverage gate
passes with these numbers" rather than "CI-verified coverage" until it is.

## Known Limitations (state plainly if asked, don't hide)

- Public preview, not production-ready, source-available (not open source).
- Windows CI runs a reduced test subset; full-suite verification is
  Linux-only.
- Free Mode (owner-authorized mutation bypass) exists and is documented in
  detail — including what it does *not* protect (shell working directory,
  unenforced branch protection). Don't let launch copy imply it's sandboxed.
- No macOS CI; macOS is build-verified only.

## Recommended Screenshots

- Dashboard live overview (`http://localhost:8322/` after `prizm serve`) —
  **not yet captured**, needed before publishing.
- Terminal output of the five-minute echo-workflow demo.
- The execution-lifecycle diagram from the README.

## Demo Sequence (for a video or GIF)

1. `git clone` + `go build`.
2. `go run ./cmd/prizm-cli workflow run demo.echo_tool` — no API key, no
   config file needed.
3. Open `runs/<run_id>/events.jsonl` to show the event trail.
4. `prizm serve` → open the dashboard → show the run appearing live.
5. (Optional, more advanced) show an approval gate pausing a mutation and a
   human approving it via the dashboard or Discord button.

## Terminology to Use

- "Public preview," "source-available," "local-first," "policy-gated,"
  "approval-gated," "owner-authorized mutation mode" (or "Free Mode" with
  a one-line explanation of the scoping).
- "Not production-ready" stated plainly, not hedged.

## Terminology to Avoid

- "Open source" (it isn't — source-available, all-rights-reserved).
- "Enterprise-ready," "production platform," "stable platform."
- "Fully autonomous" or "safe for unattended deployment" without
  qualification.
- "Unrestricted mode," "god mode," or anything implying Free Mode removes
  *all* controls — it removes the approval gate for one user; several
  controls (path containment on writes, the shell hard blocklist, audit
  events) still apply.
- Do not describe the shell tool as "sandboxed" — its working directory is
  not path-contained.

## Repository

`https://github.com/emaharmony/prizm`

## Candidate Version

`v0.2.0-preview.1` (prepared, not yet tagged as of this writing)

# V41 — Diff Preview at the Feedback Gate

**Status:** Source-current
**Last Updated:** 2026-06-29

## Problem

At FEEDBACK_POST the human approver was shown task descriptions and the
verification result, but not *what actually changed*. Approving (or requesting
changes on) code you can't see is the weakest part of the human-in-the-loop UX.
`FormatReviewPackage` already has a "Changes (git diff --stat)" block, but the
engine passed an empty diff (the v2 core has no git access by design).

## Design

The diff is gathered in the **notifier** (`startWorkflowFeedbackNotifier`), the
presentation layer that reposts `feedback.requested` to Discord — so the hardened
loop core is untouched. The pause event already carries `repo_path`, which is all
that's needed.

`diffPreview(repoPath, runGit)`:

1. Finds a base ref by trying `merge-base HEAD <ref>` for
   `origin/HEAD, origin/main, origin/master, main, master` in order.
2. Runs `git diff --stat <base>...HEAD` — the changes this run introduced on its
   feature branch.
3. Falls back to `git show --stat --oneline HEAD` (last commit) when no base ref
   exists.
4. Bounds the output to 1500 chars and returns "" on any failure (graceful
   degradation — a missing/odd git state never breaks the notification).

For `phase == "FEEDBACK_POST"`, the notifier appends the preview to the Discord
message (only when non-empty and not already present), under a
**Changes (git diff --stat)** code block.

`runGit` is injected, so the ref-resolution / fallback / truncation logic is unit
tested without a real repo; `execGit` is the production `git -C <repo> …` runner.

## Tests

`cmd/prizm-cli/workflow_feedback_notifier_test.go`: merge-base path (diffs
`base...HEAD`), fallback to last commit when no base ref, empty on all-fail / no
repo path, and truncation of oversized output.

## Follow-ups (UX roadmap)

Remaining brainstormed items: dry-run/plan preview, rich Discord approval cards
(buttons), and gate-needs-you notifications.

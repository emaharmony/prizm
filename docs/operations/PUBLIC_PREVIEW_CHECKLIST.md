# Prizm Public Preview Checklist

A realistic checklist for presenting Prizm as a credible public-preview project.
This tracks presentation and hygiene, not production readiness.

## Documentation

- [x] README is current and concise (status, golden path, capability matrix)
- [x] Version history moved out of README to `docs/history/VERSION_HISTORY.md`
- [x] Getting Started, Configuration, YAML Reference, Commands, Examples,
      Troubleshooting docs exist (under `docs/getting-started/`,
      `docs/operations/`, `docs/reference/`)
- [x] Architecture doc exists (`docs/architecture/ARCHITECTURE.md`)
- [x] Safety doc exists (`docs/concepts/SAFETY.md`)
- [x] Capability status matrix exists (`docs/reference/CAPABILITY_STATUS.md`)
- [x] Roadmap grouped into completed / current / experimental / planned

## Consistency & Claims

- [x] License language consistent (source-available / all-rights-reserved)
- [x] Prizm is not called "open source"
- [x] No production-readiness claims
- [x] Stable vs experimental boundaries are explicit

## Repo Hygiene

- [x] No IDE folders tracked (`.idea/`, `.vscode/`)
- [x] No local workspace state tracked (`prizm-workspace/`)
- [x] No generated `runs/` artifacts tracked (only sanitized `examples/runs/`)
- [x] Scratch/temp helpers untracked and gitignored
- [x] No secrets committed
- [x] `.gitignore` protects future local/generated clutter

## Demo & Tests

- [x] Golden-path demo works (echo workflow → events → artifacts)
- [ ] `go test ./...` passes on the target machine — as of commit `9a21215`
      it does **not** cleanly pass on Windows: `TestShellTool_Timeout` and
      `TestShellTool_Cwd` in `internal/tool` fail there (pre-existing,
      Windows-only; this is why CI's `test-windows` job excludes that
      package). Linux passes fully — see [QUALITY.md](../../QUALITY.md).
- [ ] Dashboard screenshots / GIFs added (see `docs/assets/`)
- [ ] Tag a release candidate (`v0.2.0-preview.1` is prepared on
      `VERSION`/`CHANGELOG.md` but **not tagged**) — do not tag
      automatically; tag when the owner decides

> Unchecked items are follow-ups a maintainer should complete/verify locally.
> Test runs and release tagging are intentionally left to the owner.

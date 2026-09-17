# Releasing Prizm

Prizm has not yet established a tagged release in the verified checkout. The
maintainer must reconcile the CLI, changelog, SDK, and Remembrance versions
before the first artifact release.

## Checklist

1. Choose one semantic version and update the CLI version source, changelog,
   Python package metadata, and compatibility notes intentionally.
2. Confirm `git status` is clean and CI is green on the release commit.
3. Run formatting, vet, build, tests, full race, Python tests/Ruff, docs checks,
   quality report, benchmarks, doctor, and the echo workflow.
4. Review the stability matrix and call out experimental surfaces.
5. Create an annotated `vX.Y.Z` tag from the verified commit and push it.
6. The release workflow builds checksummed Linux amd64, macOS arm64, and
   Windows amd64 CLI archives and attaches them to the GitHub release.
7. Download each artifact, verify SHA-256, run `prizm version`, `--help`, and
   `doctor --json`, then publish release notes.

## Rollback

Do not move or delete a published tag. Mark a broken release as withdrawn,
publish a patch release from the last known-good commit, retain artifacts for
audit, and document impact and migration steps in the changelog. Runtime state
or schema rollback requires a separately tested migration plan.

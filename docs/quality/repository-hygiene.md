# Repository Hygiene Report

This document records the repository hygiene pass performed on 2026-07-09.

## Removed Items

*   **Runtime State:**
    *   Verified that `runs/` (except `runs/.gitkeep`) is correctly ignored in `.gitignore`.
    *   Verified that `prizm-data/` and `prizm-workspace/` are correctly ignored.
*   **One-off Utilities:**
    *   Removed `scripts/loop/` and moved its contents to `tools/`.

## Moved Items

*   **Developer Tools:**
    *   Moved `scripts/loop/digest.ps1` and `scripts/loop/digest.sh` to `tools/`.

## Intentionally Tracked Items

*   **Examples:**
    *   `examples/runs/sample-run/`: Sanitized run artifacts for documentation and testing.
    *   `examples/workflows/`: Reference workflow configurations.
*   **Policy:**
    *   `policies/`: Core policy definition files.

## Now Ignored

*   `.junie/`: Session artifacts.
*   `/.output.txt`: Temporary terminal output capture.
*   `/~`: Temporary directory artifact.
*   `.claude/`, `.codex/`, `.gemini/`: LLM assistant local state.

## Git Configuration Improvements

*   **`.gitignore`:** Hardened to ensure no local binaries (`*.exe`) or runtime databases (`*.db`) from the root or `.prizm/` are committed.
*   **`.gitattributes`:** Added to enforce consistent line endings (`LF` for Go/Shell, `CRLF` for PowerShell/Batch) and mark binaries.

## Migration Notes for Contributors

*   Please use `tools/` for any reusable developer scripts.
*   The root directory should remain clean of binaries and runtime data. Use `go build -o bin/` if you want to keep binaries in a specific ignored folder.

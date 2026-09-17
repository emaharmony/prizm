# Continuous Integration

Prizm uses GitHub Actions for continuous integration to ensure code quality and cross-platform compatibility.

All third-party actions across all three workflows (`ci.yml`, `release.yml`,
`external-links.yml`) are pinned to immutable commit SHAs, not mutable
version tags — each pin is annotated with the tag it corresponds to
(e.g. `actions/checkout@34e11487... # v4`) so it stays legible while
remaining tamper-resistant.

## Workflows

### 1. CI (`.github/workflows/ci.yml`)
The main workflow runs on every push and pull request.

#### Jobs:
*   **lint-linux:**
    *   Runs `go vet` and `staticcheck`.
    *   Ensures code adheres to standard Go idioms and avoids common pitfalls.
*   **test-linux:**
    *   Runs full Go test suite (`go test ./...`).
    *   Runs race detector on Core packages.
    *   Generates and uploads coverage reports.
*   **test-windows:**
    *   Verifies build on Windows.
    *   Runs basic safety, config, and run tests.
    *   Executes `prizm doctor` smoke test to verify CLI initialization.
*   **test-python:**
    *   Installs dependencies and runs `pytest` for the Remembrance service.

## Local Reproduction

You can reproduce CI checks locally:

### Linux / macOS
```bash
go vet ./...
go build ./...
go test ./... -count=1
go test -race ./internal/bus/... ./internal/event/... [etc] -count=1
```

### Windows (PowerShell)
```powershell
go build ./...
go test ./internal/safety/... ./internal/config/... ./internal/run/... -count=1
go run ./cmd/prizm-cli doctor
```

### Python
```bash
cd remembrance
pip install -r requirements.txt
pytest tests/
```

## Quality Reporting
The `scripts/quality-report.ps1` script gathers metrics and generates `QUALITY.md`. It should be run before significant releases or after major architecture changes.

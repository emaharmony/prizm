param(
    [double]$MinimumAggregate = 55.0,
    [double]$MinimumCritical = 80.0,
    [string]$Output = "coverage-gate.md"
)

$ErrorActionPreference = "Stop"
$root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$resultDir = Join-Path $root "test-results/coverage"
New-Item -ItemType Directory -Force -Path $resultDir | Out-Null
Push-Location $root
try {
    function Total-Coverage([string]$profile) {
        $line = (& go tool cover "-func=$profile" | Select-Object -Last 1)
        if ($line -notmatch '([0-9]+(?:\.[0-9]+)?)%') { throw "cannot parse coverage total: $line" }
        return [double]$Matches[1]
    }

    $covdataDir = Join-Path $resultDir "covdata"
    New-Item -ItemType Directory -Force -Path $covdataDir | Out-Null
    Get-ChildItem -LiteralPath $covdataDir -File | Remove-Item -Force

    & go test '-coverpkg=./...' ./... -count=1 -args "-test.gocoverdir=$covdataDir"
    if ($LASTEXITCODE -ne 0) { throw "instrumented test suite failed" }

    $coverageBinary = Join-Path $resultDir "prizm-cover.exe"
    & go build '-cover' '-coverpkg=./...' "-o=$coverageBinary" ./cmd/prizm-cli
    if ($LASTEXITCODE -ne 0) { throw "instrumented CLI build failed" }

    function Invoke-Smoke([string[]]$Arguments) {
        $previousPreference = $ErrorActionPreference
        $ErrorActionPreference = "Continue"
        & $coverageBinary @Arguments 2>&1 | Out-Null
        $exitCode = $LASTEXITCODE
        $ErrorActionPreference = $previousPreference
        if ($exitCode -ne 0) { throw "CLI coverage smoke failed: $($Arguments -join ' ')" }
    }
    $env:GOCOVERDIR = $covdataDir
    try {
        Invoke-Smoke @('--help')
        Invoke-Smoke @('version')
        Invoke-Smoke @('config', '--config', 'prizm.yaml.example', '--json')
        Invoke-Smoke @('doctor', '--config', 'prizm.yaml.example', '--json')
        Invoke-Smoke @('preview', '--workflow', 'examples/workflows/gated-loop.yaml')
        Invoke-Smoke @('context', 'show', '--workspace-root', 'prizm-workspace.example', '--context', 'soul')
        Invoke-Smoke @('workflow', 'list')
        Invoke-Smoke @('workflow', 'show', 'demo.echo_tool')
        Invoke-Smoke @('workflow', 'run', 'demo.echo_tool', '--run-dir', 'test-results/coverage/cli-runs')
        Invoke-Smoke @('agent', 'list')
        Invoke-Smoke @('agent', 'show', 'planner')
        Invoke-Smoke @('tool', 'list')
        Invoke-Smoke @('validation', 'list')
        Invoke-Smoke @('validation', 'run', 'echo_test', '--project', '.', '--run-dir', 'test-results/coverage/validation-runs')
        Invoke-Smoke @('policy', 'list')
        Invoke-Smoke @('adapter', 'list')
        Invoke-Smoke @('adapter', 'show', 'echo')
        Invoke-Smoke @('adapter', 'health', 'echo')
        Invoke-Smoke @('projection', 'list')
        Invoke-Smoke @('skills', '--json')
        Invoke-Smoke @('mcp', '--config', 'prizm.yaml.example', '--json')
        Invoke-Smoke @('scan', '--json')
        Invoke-Smoke @('search', '--query', 'coverage', '--provider', 'mock')
        Invoke-Smoke @('approval', 'list')
        Invoke-Smoke @('status', '--config', 'prizm.yaml.example')

        $sampleDir = Join-Path $resultDir 'sample-data/run_01JR0SAMPLE0'
        New-Item -ItemType Directory -Force -Path $sampleDir | Out-Null
        Copy-Item 'examples/runs/sample-run/*' $sampleDir -Force
        Invoke-Smoke @('cost', '-data', 'test-results/coverage/sample-data', 'run_01JR0SAMPLE0')
        Invoke-Smoke @('trace', '-data', 'test-results/coverage/sample-data', 'run_01JR0SAMPLE0')
    } finally {
        Remove-Item Env:GOCOVERDIR -ErrorAction SilentlyContinue
    }

    $aggregateProfile = Join-Path $root "coverage.out"
    & go tool covdata textfmt "-i=$covdataDir" "-o=$aggregateProfile"
    if ($LASTEXITCODE -ne 0) { throw "coverage data conversion failed" }
    $aggregate = Total-Coverage $aggregateProfile

    $criticalPackages = @('safety', 'policy', 'approval', 'mutation', 'validation', 'guard')
    $rows = [System.Collections.Generic.List[string]]::new()
    $failed = $aggregate -lt $MinimumAggregate
    foreach ($package in $criticalPackages) {
        $profile = Join-Path $resultDir "$package.out"
        & go test "./internal/$package" "-coverprofile=$profile" -count=1 *> $null
        if ($LASTEXITCODE -ne 0) { throw "coverage test failed for internal/$package" }
        $coverage = Total-Coverage $profile
        if ($coverage -lt $MinimumCritical) { $failed = $true }
        $rows.Add("| internal/$package | $coverage% | $MinimumCritical% | $(if ($coverage -ge $MinimumCritical) {'PASS'} else {'FAIL'}) |")
    }

    $toolProfile = Join-Path $resultDir "tool.out"
    & go test ./internal/tool "-coverprofile=$toolProfile" -count=1 *> $null
    if ($LASTEXITCODE -ne 0) { throw "coverage test failed for internal/tool" }
    $authorityFiles = @('/executor.go:', '/policy.go:', '/registry.go:', '/paths.go:', '/path_resolve.go:')
    $statements = 0
    $covered = 0
    foreach ($line in Get-Content $toolProfile | Select-Object -Skip 1) {
        if ($authorityFiles | Where-Object { $line.Contains($_) }) {
            $parts = $line -split ' '
            $count = [int]$parts[1]
            $statements += $count
            if ([int]$parts[2] -gt 0) { $covered += $count }
        }
    }
    if ($statements -eq 0) { throw "tool authority coverage matched no statements" }
    $toolAuthority = [math]::Round(100.0 * $covered / $statements, 1)
    if ($toolAuthority -lt $MinimumCritical) { $failed = $true }
    $rows.Add("| internal/tool authority files | $toolAuthority% | $MinimumCritical% | $(if ($toolAuthority -ge $MinimumCritical) {'PASS'} else {'FAIL'}) |")

    $report = @(
        '# Coverage gate',
        '',
        "Aggregate: **$aggregate%** (required: $MinimumAggregate%) - $(if ($aggregate -ge $MinimumAggregate) {'PASS'} else {'FAIL'})",
        '',
        '| Surface | Coverage | Required | Status |',
        '|---|---:|---:|---|'
    ) + $rows
    Set-Content -LiteralPath $Output -Value ($report -join [Environment]::NewLine) -Encoding utf8
    $report
    if ($failed) { exit 1 }
} finally {
    Pop-Location
}

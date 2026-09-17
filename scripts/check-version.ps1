$ErrorActionPreference = "Stop"
$root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
Push-Location $root
try {
    $version = (Get-Content VERSION -Raw).Trim()
    # Pre-1.0 SemVer with an optional prerelease suffix (e.g. 0.2.0-preview.1),
    # matching the tag pattern release.yml already accepts.
    if ($version -notmatch '^0\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$') {
        throw "VERSION is not pre-1.0 SemVer: $version"
    }

    $checks = @(
        @('internal/version/version.go', "var Version = `"$version`""),
        @('sdk/pyproject.toml', "version = `"$version`""),
        @('remembrance/pyproject.toml', "version = `"$version`""),
        @('sdk/prizm/__init__.py', "__version__ = `"$version`""),
        @('remembrance/src/remembrance/__init__.py', "__version__ = `"$version`""),
        @('CHANGELOG.md', "## [$version]")
    )
    foreach ($check in $checks) {
        if (-not (Select-String -LiteralPath $check[0] -SimpleMatch $check[1] -Quiet)) {
            throw "version $version missing from $($check[0])"
        }
    }

    $actual = (& go run ./cmd/prizm-cli version).Trim()
    if ($actual -ne "prizm v$version") { throw "CLI version mismatch: $actual" }
    "version contract: v$version"
} finally {
    Pop-Location
}

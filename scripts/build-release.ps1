param([string]$Version = "")

$ErrorActionPreference = "Stop"
$root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
Push-Location $root
try {
    if (-not $Version) { $Version = (Get-Content VERSION -Raw).Trim() }
    if ($Version -notmatch '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$') {
        throw "invalid SemVer: $Version"
    }

    $module = "github.com/emaharmony/prizm"
    $ldflags = "-s -w -X $module/internal/version.Version=$Version"
    $dist = Join-Path $root "dist"
    if (Test-Path $dist) { Remove-Item -Recurse -Force $dist }
    New-Item -ItemType Directory -Path $dist | Out-Null

    function Build-Archive([string]$GoOS, [string]$GoArch, [string]$Extension, [string]$Format) {
        $package = "prizm-$Version-$GoOS-$GoArch"
        $stage = Join-Path $dist $package
        New-Item -ItemType Directory -Path $stage | Out-Null
        $previousOS, $previousArch, $previousCGO = $env:GOOS, $env:GOARCH, $env:CGO_ENABLED
        try {
            $env:GOOS, $env:GOARCH, $env:CGO_ENABLED = $GoOS, $GoArch, "0"
            & go build -trimpath -ldflags $ldflags -o (Join-Path $stage "prizm$Extension") ./cmd/prizm-cli
            if ($LASTEXITCODE -ne 0) { throw "build failed for $GoOS/$GoArch" }
        } finally {
            $env:GOOS, $env:GOARCH, $env:CGO_ENABLED = $previousOS, $previousArch, $previousCGO
        }
        Copy-Item README.md, LICENSE, VERSION $stage
        if ($Format -eq "zip") {
            Compress-Archive -Path $stage -DestinationPath (Join-Path $dist "$package.zip")
        } else {
            & tar -C $dist -czf (Join-Path $dist "$package.tar.gz") $package
            if ($LASTEXITCODE -ne 0) { throw "archive failed for $package" }
        }
        Remove-Item -Recurse -Force $stage
    }

    Build-Archive linux amd64 "" tar
    Build-Archive darwin arm64 "" tar
    Build-Archive windows amd64 ".exe" zip

    $archives = Get-ChildItem $dist -File | Sort-Object Name
    $lines = foreach ($archive in $archives) {
        $hash = (Get-FileHash -Algorithm SHA256 $archive.FullName).Hash.ToLowerInvariant()
        "$hash  $($archive.Name)"
    }
    Set-Content -Encoding ascii (Join-Path $dist "SHA256SUMS") $lines
    foreach ($archive in $archives) {
        $expected = ($lines | Where-Object { $_ -like "*  $($archive.Name)" }).Split(' ')[0]
        $actual = (Get-FileHash -Algorithm SHA256 $archive.FullName).Hash.ToLowerInvariant()
        if ($actual -ne $expected) { throw "checksum mismatch: $($archive.Name)" }
    }
    "release artifacts built for v$Version"
} finally {
    Pop-Location
}

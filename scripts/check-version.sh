#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

version=$(tr -d '[:space:]' < VERSION)
# Pre-1.0 SemVer with an optional prerelease suffix (e.g. 0.2.0-preview.1),
# matching the tag pattern release.yml already accepts.
[[ $version =~ ^0\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || { echo "VERSION is not pre-1.0 SemVer: $version" >&2; exit 1; }

grep -Fq "var Version = \"$version\"" internal/version/version.go
grep -Fq "version = \"$version\"" sdk/pyproject.toml
grep -Fq "version = \"$version\"" remembrance/pyproject.toml
grep -Fq "__version__ = \"$version\"" sdk/prizm/__init__.py
grep -Fq "__version__ = \"$version\"" remembrance/src/remembrance/__init__.py
grep -Fq "## [$version]" CHANGELOG.md

actual=$(go run ./cmd/prizm-cli version)
[[ $actual == "prizm v$version" ]] || { echo "CLI version mismatch: $actual" >&2; exit 1; }
echo "version contract: v$version"

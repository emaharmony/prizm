#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

version=${1:-$(tr -d '[:space:]' < VERSION)}
[[ $version =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$ ]] || {
  echo "invalid SemVer: $version" >&2
  exit 1
}

module=github.com/emaharmony/prizm
ldflags="-s -w -X ${module}/internal/version.Version=${version}"
rm -rf dist
mkdir -p dist

build_archive() {
  local goos=$1 goarch=$2 extension=$3 archive=$4
  local package="prizm-${version}-${goos}-${goarch}"
  local stage="dist/${package}"
  mkdir -p "$stage"
  CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch go build -trimpath -ldflags "$ldflags" -o "$stage/prizm${extension}" ./cmd/prizm-cli
  cp README.md LICENSE VERSION "$stage/"
  if [[ $archive == zip ]]; then
    (cd dist && zip -qr "${package}.zip" "$package")
  else
    tar -C dist -czf "dist/${package}.tar.gz" "$package"
  fi
  rm -rf "$stage"
}

build_archive linux amd64 "" tar
build_archive darwin arm64 "" tar
build_archive windows amd64 .exe zip

(cd dist && sha256sum prizm-* > SHA256SUMS && sha256sum --check SHA256SUMS)
echo "release artifacts built for v${version}"

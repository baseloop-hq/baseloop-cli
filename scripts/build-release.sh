#!/usr/bin/env bash
# Build release archives expected by scripts/install.sh.

set -euo pipefail

VERSION="${1:-}"
[[ -n "$VERSION" ]] || { echo "Usage: scripts/build-release.sh VERSION" >&2; exit 1; }
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || { echo "Invalid semver: $VERSION" >&2; exit 1; }

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
DIST="${ROOT}/dist"
mkdir -p "$DIST"
rm -f "$DIST"/baseloop_"$VERSION"_*.tar.gz "$DIST"/baseloop_"$VERSION"_*.zip "$DIST"/checksums.txt

build_one() {
  local os="$1"
  local arch="$2"
  local platform="${os}_${arch}"
  local work="${DIST}/${platform}"
  mkdir -p "$work"
  local binary="baseloop"
  if [[ "$os" == "windows" ]]; then
    binary="baseloop.exe"
  fi
  echo "Building ${platform}"
  GOOS="$os" GOARCH="$arch" go build \
    -ldflags "-s -w -X github.com/baseloop-hq/baseloop-cli/internal/version.Version=${VERSION}" \
    -o "${work}/${binary}" ./cmd/baseloop
  if [[ "$os" == "windows" ]]; then
    command -v zip >/dev/null 2>&1 || { echo "zip is required to build Windows release artifacts" >&2; exit 1; }
    (cd "$work" && zip -q "${DIST}/baseloop_${VERSION}_${platform}.zip" "$binary")
  else
    (cd "$work" && tar -czf "${DIST}/baseloop_${VERSION}_${platform}.tar.gz" "$binary")
  fi
  rm -rf "$work"
}

cd "$ROOT"
build_one darwin amd64
build_one darwin arm64
build_one linux amd64
build_one linux arm64
build_one windows amd64
build_one windows arm64

(cd "$DIST" && shasum -a 256 baseloop_"$VERSION"_*.tar.gz baseloop_"$VERSION"_*.zip > checksums.txt)
echo "Wrote ${DIST}"

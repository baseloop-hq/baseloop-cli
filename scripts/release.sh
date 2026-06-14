#!/usr/bin/env bash
# Usage: scripts/release.sh VERSION [--dry-run]

set -euo pipefail

VERSION="${1:-}"
DRY_RUN=false
if [[ "$*" == *"--dry-run"* ]]; then
  DRY_RUN=true
fi

[[ -n "$VERSION" ]] || { echo "Usage: scripts/release.sh VERSION [--dry-run]" >&2; exit 1; }
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || { echo "Invalid semver: $VERSION" >&2; exit 1; }

git diff --quiet || { echo "Working tree is dirty" >&2; exit 1; }
git diff --cached --quiet || { echo "Index is dirty" >&2; exit 1; }

make release-check
scripts/build-release.sh "$VERSION"

if [[ "$DRY_RUN" == "true" ]]; then
  echo "Dry run complete. Release artifacts are in dist/."
  exit 0
fi

git tag -a "v${VERSION}" -m "Release v${VERSION}"
git push origin "v${VERSION}"
echo "Release tag pushed: v${VERSION}"

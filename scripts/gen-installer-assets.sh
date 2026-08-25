#!/usr/bin/env bash
# gen-installer-assets.sh - Produce the pinned installer scripts published as
# release assets (install-cli, install-cli.ps1).
#
# The app install routes point at the latest release's copies, so a hosted
# install always runs a script that was reviewed with that release and is
# pinned to it — never the tip of main (DISTRIBUTION.md forbids production
# installs from depending on the main branch). The pin is a *default*: a
# user-set BASELOOP_VERSION still overrides it, exactly as the app-route
# contract requires.
#
# Usage: scripts/gen-installer-assets.sh <version-without-v> [dist-dir]

set -euo pipefail

VERSION="${1:?usage: gen-installer-assets.sh <version-without-v> [dist-dir]}"
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
DIST="${2:-${ROOT}/dist}"

if ! [[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "error: version '$VERSION' is not semver (expected e.g. 1.2.3 or 1.2.3-rc.1)" >&2
  exit 1
fi

mkdir -p "$DIST"

# Stamp exactly one occurrence of the placeholder; zero or several means the
# script drifted and the release must fail loudly, not ship an unpinned
# installer that silently follows "latest".
stamp() {
  local src="$1" out="$2" pattern="$3" replacement="$4"
  cp "$src" "$out"
  if ! PATTERN="$pattern" REPLACEMENT="$replacement" perl -0pi -e \
    'BEGIN { $ok = 0 } $ok += s/\Q$ENV{PATTERN}\E/$ENV{REPLACEMENT}/g; END { exit($ok == 1 ? 0 : 1) }' \
    "$out"; then
    echo "error: pinned-version placeholder not found exactly once in ${out} (looked for: ${pattern})" >&2
    exit 1
  fi
}

stamp "${ROOT}/scripts/install.sh" "${DIST}/install-cli" \
  'PINNED_DEFAULT_VERSION=""' "PINNED_DEFAULT_VERSION=\"${VERSION}\""
chmod +x "${DIST}/install-cli"

stamp "${ROOT}/scripts/install.ps1" "${DIST}/install-cli.ps1" \
  "\$PinnedDefaultVersion = ''" "\$PinnedDefaultVersion = '${VERSION}'"

echo "Wrote ${DIST}/install-cli and ${DIST}/install-cli.ps1 pinned to ${VERSION}"

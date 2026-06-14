#!/usr/bin/env bash
# install-dev.sh - Prepare a local install-cli script for end-to-end installer tests.

set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
DIST="${ROOT}/dist"
BASE_URL="${1:-file://${DIST}}"

mkdir -p "$DIST"
cp "${ROOT}/scripts/install.sh" "${DIST}/install-cli"

if ! BASE_URL="$BASE_URL" perl -0pi -e 'BEGIN { $ok = 0 } $ok += s#local base_url="https://github\.com/\$\{REPO\}/releases/download/v\$\{version\}"#"local base_url=\"$ENV{BASE_URL}\""#eg; END { exit($ok ? 0 : 1) }' "${DIST}/install-cli"; then
  echo "Could not patch checksum release URL in ${DIST}/install-cli" >&2
  exit 1
fi

if ! BASE_URL="$BASE_URL" perl -0pi -e 'BEGIN { $ok = 0 } $ok += s#url="https://github\.com/\$\{REPO\}/releases/download/v\$\{version\}/\$\{archive\}"#"url=\"$ENV{BASE_URL}/\${archive}\""#eg; END { exit($ok ? 0 : 1) }' "${DIST}/install-cli"; then
  echo "Could not patch archive release URL in ${DIST}/install-cli" >&2
  exit 1
fi

chmod +x "${DIST}/install-cli"
echo "Wrote ${DIST}/install-cli using release base ${BASE_URL}"

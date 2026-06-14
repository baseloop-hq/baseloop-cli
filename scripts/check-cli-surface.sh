#!/usr/bin/env bash
# Validate and snapshot the machine-readable command catalog.

set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
OUT="${ROOT}/docs/cli-surface.json"
TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT

cd "$ROOT"
mkdir -p "$(dirname "$OUT")"
go run ./cmd/baseloop commands --json > "$TMP"

if command -v jq >/dev/null 2>&1; then
  jq '.data' "$TMP" > "$OUT"
else
  cp "$TMP" "$OUT"
fi

echo "Wrote ${OUT}"

# Drift gate: the snapshot is committed, so an uncommitted change here means
# the CLI surface changed without the snapshot being reviewed. Output-contract
# stability is what makes background auto-update safe to swap binaries under
# live agent sessions, so drift must fail loudly, not regenerate silently.
# Without jq the regenerated file has a different shape (full envelope), so the
# comparison would false-positive; CI always has jq, which is the gate that counts.
if command -v jq >/dev/null 2>&1; then
  if ! git -C "$ROOT" diff --exit-code -- "$OUT"; then
    echo "error: docs/cli-surface.json changed; review the surface change and commit the updated snapshot." >&2
    exit 1
  fi
else
  echo "warning: jq not found; skipping cli-surface drift check (CI enforces it)." >&2
fi

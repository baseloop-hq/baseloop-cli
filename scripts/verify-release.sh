#!/usr/bin/env bash
# verify-release.sh - Prove a published GitHub release is complete and
# installable before anyone points users at it.
#
# scripts/release.sh tags and pushes; GitHub Actions builds and publishes.
# Nothing in that chain checks the result. This script does: it waits for the
# release workflow, then verifies the release's flags, assets, checksums, the
# pinned installer stamps, and (on macOS/Linux) that the published installer
# actually installs the version it claims, into a throwaway home.
#
# Usage: scripts/verify-release.sh VERSION [--repo owner/name] [--wait SECONDS]
#                                          [--skip-install]
#
#   VERSION         Semver without the v prefix (1.2.3 or 1.2.3-rc.1)
#   --repo          GitHub repo (default: the current repo per gh)
#   --wait          Max seconds to wait for the workflow run (default 900)
#   --skip-install  Skip the sandboxed install of the published installer
#
# Exit status is 0 only when every check passes.

set -euo pipefail

VERSION=""
REPO=""
WAIT_SECONDS=900
SKIP_INSTALL=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --repo) [[ $# -ge 2 ]] || { echo "--repo requires owner/name" >&2; exit 2; }; REPO="$2"; shift 2 ;;
    --wait) [[ $# -ge 2 ]] || { echo "--wait requires a number of seconds" >&2; exit 2; }; WAIT_SECONDS="$2"; shift 2 ;;
    --skip-install) SKIP_INSTALL=1; shift ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    -*) echo "Unknown option: $1" >&2; exit 2 ;;
    *) VERSION="$1"; shift ;;
  esac
done

[[ -n "$VERSION" ]] || { echo "Usage: scripts/verify-release.sh VERSION [--repo owner/name] [--wait SECONDS] [--skip-install]" >&2; exit 2; }
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || { echo "Invalid semver: $VERSION" >&2; exit 2; }
command -v gh >/dev/null || { echo "gh CLI is required (brew install gh)" >&2; exit 2; }
command -v jq >/dev/null || { echo "jq is required (brew install jq)" >&2; exit 2; }

TAG="v${VERSION}"
if [[ -z "$REPO" ]]; then
  REPO=$(gh repo view --json nameWithOwner -q .nameWithOwner)
fi
IS_PRERELEASE=0
[[ "$VERSION" == *-* ]] && IS_PRERELEASE=1

PASS=0
FAIL=0
FAILURES=()
ok()   { PASS=$((PASS + 1)); printf '  ok    %s\n' "$1"; }
fail() { FAIL=$((FAIL + 1)); FAILURES+=("$1"); printf '  FAIL  %s\n' "$1"; }
note() { printf '        %s\n' "$1"; }

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

echo "Verifying ${REPO} release ${TAG}"

# 1. The release workflow run for this tag must finish successfully. Tag
#    pushes report the tag name as the run's branch, which is what --branch
#    filters on.
echo "Workflow run"
deadline=$((SECONDS + WAIT_SECONDS))
run_json=""
while :; do
  # A lookup error must not abort the script under set -e: a 404 (no such
  # repo or workflow) is a definitive failure, anything else is retried
  # until the deadline like a run that has not appeared yet.
  if ! run_json=$(gh run list --repo "$REPO" --workflow release.yml --branch "$TAG" --json databaseId,status,conclusion,url --limit 1 2>"$TMP/run-list.err"); then
    if grep -q 'HTTP 404' "$TMP/run-list.err"; then
      fail "release workflow lookup failed: $(head -n 1 "$TMP/run-list.err")"
      run_status="error"
      break
    fi
    note "workflow lookup failed ($(head -n 1 "$TMP/run-list.err")); retrying..."
    run_json='[]'
  fi
  run_status=$(jq -r '.[0].status // empty' <<<"$run_json")
  if [[ "$run_status" == "completed" ]]; then
    break
  fi
  if (( SECONDS >= deadline )); then
    break
  fi
  if [[ -z "$run_status" ]]; then
    note "no run for ${TAG} yet; waiting..."
  else
    note "run is ${run_status}; waiting..."
  fi
  sleep 15
done
run_url=$(jq -r '.[0].url // empty' <<<"$run_json")
run_conclusion=$(jq -r '.[0].conclusion // empty' <<<"$run_json")
if [[ "$run_status" == "error" ]]; then
  : # already recorded above
elif [[ "$run_status" == "completed" && "$run_conclusion" == "success" ]]; then
  ok "release workflow succeeded (${run_url})"
elif [[ -z "$run_status" ]]; then
  fail "no release workflow run found for ${TAG} (was the tag pushed to ${REPO}?)"
else
  fail "release workflow status=${run_status} conclusion=${run_conclusion:-none} (${run_url})"
fi

# 2. Release metadata: exists, not a draft, prerelease flag matches the tag.
echo "Release"
if ! release_json=$(gh release view "$TAG" --repo "$REPO" --json tagName,isDraft,isPrerelease,url,assets 2>/dev/null); then
  fail "release ${TAG} does not exist"
  release_json='{}'
fi
if [[ "$(jq -r '.tagName // empty' <<<"$release_json")" == "$TAG" ]]; then
  ok "release exists ($(jq -r .url <<<"$release_json"))"
  if [[ "$(jq -r .isDraft <<<"$release_json")" == "false" ]]; then
    ok "release is published, not a draft"
  else
    fail "release is still a draft"
  fi
  actual_pre=$(jq -r .isPrerelease <<<"$release_json")
  if [[ "$IS_PRERELEASE" == "1" && "$actual_pre" == "true" ]]; then
    ok "marked as prerelease (hyphenated tag), so 'latest' will not move"
  elif [[ "$IS_PRERELEASE" == "0" && "$actual_pre" == "false" ]]; then
    ok "marked as a full release"
  else
    fail "prerelease flag is ${actual_pre}, expected $([[ "$IS_PRERELEASE" == "1" ]] && echo true || echo false) for ${TAG}"
  fi
fi

# 3. 'latest' must point at a final release and never at an rc: the
#    installer and `baseloop upgrade` both follow it.
#    A failed lookup proves nothing either way, so it is a failure of its
#    own rather than an empty tag that happens to differ from the rc.
if latest_tag=$(gh api "repos/${REPO}/releases/latest" -q .tag_name 2>/dev/null) && [[ -n "$latest_tag" ]]; then
  if [[ "$IS_PRERELEASE" == "1" ]]; then
    if [[ "$latest_tag" != "$TAG" ]]; then
      ok "'latest' still points at ${latest_tag}, not the rc"
    else
      fail "'latest' points at the rc ${TAG}; users would be upgraded onto a release candidate"
    fi
  else
    if [[ "$latest_tag" == "$TAG" ]]; then
      ok "'latest' points at ${TAG}"
    else
      fail "'latest' points at ${latest_tag}, expected ${TAG}"
    fi
  fi
else
  fail "could not resolve the latest release (gh api repos/${REPO}/releases/latest), so where 'latest' points is unproven"
fi

# 4. Every asset the installers and upgrade path expect must be present.
echo "Assets"
expected_assets=(
  "baseloop_${VERSION}_darwin_amd64.tar.gz"
  "baseloop_${VERSION}_darwin_arm64.tar.gz"
  "baseloop_${VERSION}_linux_amd64.tar.gz"
  "baseloop_${VERSION}_linux_arm64.tar.gz"
  "baseloop_${VERSION}_windows_amd64.zip"
  "baseloop_${VERSION}_windows_arm64.zip"
  "checksums.txt"
  "install-cli"
  "install-cli.ps1"
)
present_assets=$(jq -r '.assets[]?.name' <<<"$release_json")
missing=0
for asset in "${expected_assets[@]}"; do
  if grep -qxF "$asset" <<<"$present_assets"; then
    ok "asset ${asset}"
  else
    fail "missing asset ${asset}"
    missing=1
  fi
done

# 5. Downloaded content: installer stamps and the checksum of this
#    platform's archive.
echo "Content"
download() {
  gh release download "$TAG" --repo "$REPO" --pattern "$1" --dir "$TMP" --clobber >/dev/null 2>&1
}
# A stamp counts only as a whole, uncommented assignment line, and there must
# be exactly one: gen-installer-assets.sh replaces the placeholder once, so a
# commented copy or a duplicate means the script drifted. CR is stripped so a
# CRLF-checked-out .ps1 still matches.
count_stamp_lines() {
  tr -d '\r' <"$1" | grep -cxF "$2" || true
}
if grep -qxF "install-cli" <<<"$present_assets"; then
  if ! download "install-cli"; then
    fail "could not download install-cli"
  else
    stamps=$(count_stamp_lines "$TMP/install-cli" "PINNED_DEFAULT_VERSION=\"${VERSION}\"")
    if [[ "$stamps" == "1" ]]; then
      ok "install-cli is pinned to ${VERSION}"
    else
      fail "install-cli must have exactly one active PINNED_DEFAULT_VERSION=\"${VERSION}\" line, found ${stamps} (first assignment: $(grep -m1 -E '^PINNED_DEFAULT_VERSION=' "$TMP/install-cli" || echo 'none'))"
    fi
  fi
fi
if grep -qxF "install-cli.ps1" <<<"$present_assets"; then
  if ! download "install-cli.ps1"; then
    fail "could not download install-cli.ps1"
  else
    stamps=$(count_stamp_lines "$TMP/install-cli.ps1" "\$PinnedDefaultVersion = '${VERSION}'")
    if [[ "$stamps" == "1" ]]; then
      ok "install-cli.ps1 is pinned to ${VERSION}"
    else
      fail "install-cli.ps1 must have exactly one active \$PinnedDefaultVersion = '${VERSION}' line, found ${stamps}"
    fi
  fi
fi

case "$(uname -s)-$(uname -m)" in
  Darwin-arm64) platform="darwin_arm64" ;;
  Darwin-x86_64) platform="darwin_amd64" ;;
  Linux-x86_64|Linux-amd64) platform="linux_amd64" ;;
  Linux-aarch64|Linux-arm64) platform="linux_arm64" ;;
  *) platform="" ;;
esac
archive="baseloop_${VERSION}_${platform}.tar.gz"
if [[ -n "$platform" ]] && grep -qxF "$archive" <<<"$present_assets" && grep -qxF "checksums.txt" <<<"$present_assets"; then
  if download "checksums.txt" && download "$archive"; then
    expected_sha=$(awk -v f="$archive" '$2 == f {print $1}' "$TMP/checksums.txt")
    if command -v sha256sum >/dev/null; then
      actual_sha=$(sha256sum "$TMP/$archive" | awk '{print $1}')
    else
      actual_sha=$(shasum -a 256 "$TMP/$archive" | awk '{print $1}')
    fi
    if [[ -n "$expected_sha" && "$expected_sha" == "$actual_sha" ]]; then
      ok "${archive} matches checksums.txt"
    else
      fail "${archive} checksum mismatch (checksums.txt: ${expected_sha:-absent}, actual: ${actual_sha})"
    fi
  else
    fail "could not download ${archive} or checksums.txt"
  fi
fi

# 6. The published installer, run exactly as a user would (no
#    BASELOOP_VERSION override, so the stamped default is what resolves),
#    must install a binary that reports this version. Everything lands in a
#    throwaway home so the real machine is untouched.
echo "Install"
if [[ "$SKIP_INSTALL" == "1" ]]; then
  note "skipped (--skip-install)"
elif [[ -z "$platform" ]]; then
  note "skipped (unsupported host platform for the Unix installer)"
elif [[ ! -f "$TMP/install-cli" ]]; then
  fail "cannot exercise the installer: install-cli was not downloaded"
else
  sandbox="$TMP/sandbox"
  mkdir -p "$sandbox/home" "$sandbox/bin"
  install_log="$TMP/install.log"
  # HOME alone is not a sandbox: the installer honors ZDOTDIR for the zsh rc
  # file it writes the PATH block to, and XDG_* for config/state dirs. Every
  # one of those must point inside the sandbox or the run edits real dotfiles.
  # BASELOOP_REPO pins the sandbox install to the repository under test, so a
  # fork or mirror is verified against its own archives rather than the
  # upstream ones the installer would otherwise default to.
  if HOME="$sandbox/home" \
     BASELOOP_REPO="$REPO" \
     ZDOTDIR="$sandbox/home" \
     XDG_CONFIG_HOME="$sandbox/home/.config" \
     XDG_STATE_HOME="$sandbox/home/.local/state" \
     XDG_CACHE_HOME="$sandbox/home/.cache" \
     XDG_DATA_HOME="$sandbox/home/.local/share" \
     CODEX_HOME="$sandbox/home/.codex" \
     BASELOOP_BIN_DIR="$sandbox/bin" \
     BASELOOP_STATE="$sandbox/state" \
     BASELOOP_CONFIG="$sandbox/config.json" \
     BASELOOP_SKIP_SETUP=1 \
     BASELOOP_SKIP_AUTH=1 \
     BASELOOP_SKIP_AGENT_PERMISSIONS=1 \
     BASELOOP_NO_UPDATE_CHECK=1 \
     NO_COLOR=1 \
     bash "$TMP/install-cli" </dev/null >"$install_log" 2>&1; then
    ok "published installer completed in a sandbox"
  else
    fail "published installer failed (see below)"
    sed 's/^/        | /' "$install_log" | tail -20
  fi
  installed="$sandbox/bin/baseloop"
  if [[ -x "$installed" ]]; then
    # `baseloop --version` prints "baseloop X.Y.Z"; compare the version
    # field exactly so an rc binary can never satisfy a final-release check.
    reported=$("$installed" --version 2>&1 || true)
    reported_version=$(printf '%s\n' "$reported" | head -n 1 | awk '{print $NF}')
    if [[ "$reported_version" == "$VERSION" ]]; then
      ok "installed binary reports ${VERSION} (${reported})"
    else
      fail "installed binary reports '${reported}', expected version ${VERSION}"
    fi
    if HOME="$sandbox/home" BASELOOP_STATE="$sandbox/state" BASELOOP_CONFIG="$sandbox/config.json" BASELOOP_NO_UPDATE_CHECK=1 \
       "$installed" commands --json >/dev/null 2>&1; then
      ok "installed binary runs (commands --json)"
    else
      fail "installed binary cannot run commands --json"
    fi
    if grep -qsE '"install_policy":[[:space:]]*"managed"' "$sandbox/state/manifest.json"; then
      ok "install receipt recorded as managed (stamped default, not a user pin)"
    else
      fail "install receipt missing or not 'managed' in ${sandbox}/state/manifest.json"
    fi
  elif [[ "$SKIP_INSTALL" != "1" ]]; then
    fail "no binary at ${installed} after install"
  fi
fi

echo
echo "Result: ${PASS} passed, ${FAIL} failed"
if (( FAIL > 0 )); then
  for f in "${FAILURES[@]}"; do
    printf '  - %s\n' "$f"
  done
  exit 1
fi

#!/usr/bin/env bash
# uninstall.sh - Remove the Baseloop CLI and everything its installer added.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/baseloop-hq/baseloop-cli/main/scripts/uninstall.sh | bash
#
# Flags (passed through to `baseloop uninstall`):
#   --dry-run   Show what would be removed without removing anything.
#   --purge     Also remove the config file and stored auth token.
#
# Options via environment:
#   BASELOOP_BIN_DIR          Directory the binary was installed to.

set -euo pipefail

BIN_DIR="${BASELOOP_BIN_DIR:-}"
PATH_MARKER="# Added by Baseloop CLI installer"
PATH_BEGIN_MARKER="# >>> Baseloop CLI installer >>>"
PATH_END_MARKER="# <<< Baseloop CLI installer <<<"
REMOVAL_FAILED=0

if [[ -z "${NO_COLOR:-}" ]] && [[ -t 1 ]]; then
  bold() { printf '\033[1m%s\033[0m' "$1"; }
  dim() { printf '\033[2m%s\033[0m' "$1"; }
  green() { printf '\033[32m%s\033[0m' "$1"; }
  yellow() { printf '\033[33m%s\033[0m' "$1"; }
else
  bold() { printf '%s' "$1"; }
  dim() { printf '%s' "$1"; }
  green() { printf '%s' "$1"; }
  yellow() { printf '%s' "$1"; }
fi

info() { echo "  $(green "OK") $1"; }
step() { echo "  -> $1"; }
warn() { echo "  $(yellow "Heads up:") $1"; }
error() {
  echo "  Could not finish: $1" >&2
  exit 1
}

DRY_RUN=0
PURGE=0
for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    --purge) PURGE=1 ;;
  esac
done

find_binary() {
  local candidate
  if [[ -n "$BIN_DIR" ]] && [[ -x "${BIN_DIR}/baseloop" ]]; then
    echo "${BIN_DIR}/baseloop"
    return 0
  fi
  for candidate in "$HOME/.local/bin/baseloop" "$HOME/bin/baseloop"; do
    if [[ -x "$candidate" ]]; then
      echo "$candidate"
      return 0
    fi
  done
  if command -v baseloop >/dev/null 2>&1; then
    command -v baseloop
    return 0
  fi
  return 1
}

binary_is_safe_to_remove() {
  local binary="$1" candidate
  if [[ -n "$BIN_DIR" && "$binary" == "${BIN_DIR}/baseloop" ]]; then
    return 0
  fi
  for candidate in "$HOME/.local/bin/baseloop" "$HOME/bin/baseloop"; do
    if [[ "$binary" == "$candidate" ]]; then
      return 0
    fi
  done
  return 1
}

state_dir() {
  if [[ -n "${BASELOOP_STATE:-}" ]]; then
    echo "$BASELOOP_STATE"
  elif [[ -n "${XDG_STATE_HOME:-}" ]]; then
    echo "${XDG_STATE_HOME}/baseloop"
  else
    echo "${HOME}/.local/state/baseloop"
  fi
}

config_path() {
  if [[ -n "${BASELOOP_CONFIG:-}" ]]; then
    echo "$BASELOOP_CONFIG"
  elif [[ -n "${XDG_CONFIG_HOME:-}" ]]; then
    echo "${XDG_CONFIG_HOME}/baseloop/config.json"
  else
    echo "${HOME}/.config/baseloop/config.json"
  fi
}

# One dir per supported agent. The Codex dir honors CODEX_HOME the same way
# the CLI binary does; removal is ownership-gated, so listing a dir that does
# not exist (or is not ours) is harmless.
baseloop_entry_skill_dirs() {
  echo "${HOME}/.claude/skills/baseloop"
  echo "${CODEX_HOME:-${HOME}/.codex}/skills/baseloop"
}

is_baseloop_entry_skill_dir() {
	local dir="$1"
	local skill_file="${dir}/SKILL.md"
	local marker_file="${dir}/.baseloop.sha256"
	local expected actual
	[[ -f "$skill_file" ]] || return 1
	[[ -f "$marker_file" ]] || return 1
	expected=$(tr -d '[:space:]' <"$marker_file")
	actual=$(sha256_file "$skill_file") || return 1
	[[ -n "$expected" && "$expected" == "$actual" ]]
}

sha256_file() {
	local path="$1"
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$path" | awk '{print $1}'
		return 0
	fi
	if command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$path" | awk '{print $1}'
		return 0
	fi
	return 1
}

path_exists_for_removal() {
  [[ -e "$1" || -L "$1" ]]
}

remove_path() {
  local path="$1"
  path_exists_for_removal "$path" || return 0
  if [[ "$DRY_RUN" -eq 1 ]]; then
    info "Would remove ${path}"
    return 0
  fi
  if rm -rf "$path"; then
    info "Removed ${path}"
  else
    REMOVAL_FAILED=1
    warn "Could not remove ${path}; delete it manually."
  fi
}

strip_path_marker() {
  local file="$1"
  [[ -f "$file" ]] || return 0
  if ! grep -qFx "$PATH_MARKER" "$file" 2>/dev/null && ! grep -qFx "$PATH_BEGIN_MARKER" "$file" 2>/dev/null; then
    return 0
  fi

  if [[ "$DRY_RUN" -eq 1 ]]; then
    info "Would remove PATH entry from ${file}"
    return 0
  fi

  local tmp skip_next in_block line
  local -a out=()
  tmp=$(mktemp "${TMPDIR:-/tmp}/baseloop-uninstall.XXXXXX")
  skip_next=0
  in_block=0
  while IFS= read -r line || [[ -n "$line" ]]; do
    if [[ "$line" == "$PATH_BEGIN_MARKER" ]]; then
      local out_len
      out_len=${#out[@]}
      if [[ "$out_len" -gt 0 && -z "${out[$((out_len - 1))]//[[:space:]]/}" ]]; then
        unset "out[$((out_len - 1))]"
      fi
      in_block=1
      continue
    fi
    if [[ "$in_block" -eq 1 ]]; then
      if [[ "$line" == "$PATH_END_MARKER" ]]; then
        in_block=0
      fi
      continue
    fi
    if [[ "$skip_next" -eq 1 ]]; then
      skip_next=0
      if [[ "$line" == export\ PATH=\"*:\$PATH\" ]]; then
        continue
      fi
    fi
    if [[ "$line" == "$PATH_MARKER" ]]; then
      local out_len
      out_len=${#out[@]}
      if [[ "$out_len" -gt 0 && -z "${out[$((out_len - 1))]//[[:space:]]/}" ]]; then
        unset "out[$((out_len - 1))]"
      fi
      skip_next=1
      continue
    fi
    out+=("$line")
  done <"$file"
  if [[ "$in_block" -eq 1 ]]; then
    rm -f "$tmp"
    REMOVAL_FAILED=1
    warn "Could not update ${file}; the Baseloop PATH block is missing its end marker."
    return 0
  fi
  printf '%s\n' "${out[@]}" >"$tmp"
  if mv "$tmp" "$file"; then
    info "Removed PATH entry from ${file}"
  else
    rm -f "$tmp"
    REMOVAL_FAILED=1
    warn "Could not update ${file}; remove the Baseloop PATH block manually."
  fi
}

path_rc_files() {
  if [[ -n "${ZDOTDIR:-}" && "$ZDOTDIR" != "$HOME" ]]; then
    echo "${ZDOTDIR}/.zshrc"
  fi
  echo "$HOME/.zshrc"
  echo "$HOME/.zshenv"
  echo "$HOME/.bashrc"
  echo "$HOME/.bash_profile"
  echo "$HOME/.bash_login"
  echo "$HOME/.profile"
}

remove_known_files_directly() {
  step "Removing Baseloop files we know about"

  local state config
  state=$(state_dir)
  config=$(config_path)

  local paths=(
  )
  local entry_skill
  while IFS= read -r entry_skill; do
    if is_baseloop_entry_skill_dir "$entry_skill"; then
      paths+=("$entry_skill")
    fi
  done < <(baseloop_entry_skill_dirs)
  if [[ -z "${BASELOOP_STATE:-}" ]]; then
    paths+=("$state")
  else
    paths+=("${state}/manifest.json")
  fi
  if [[ -n "$BIN_DIR" ]]; then
    paths+=("${BIN_DIR}/baseloop")
  fi
  if [[ "$PURGE" -eq 1 ]]; then
    if [[ "$(basename "$config")" == "config.json" && ! -d "$config" ]]; then
      paths+=("$config")
    else
      REMOVAL_FAILED=1
      warn "Refusing to remove unsafe config path ${config}; expected a config.json file."
    fi
  fi

  local path
  for path in "${paths[@]}"; do
    remove_path "$path"
  done

  if [[ "$PURGE" -eq 1 && "$DRY_RUN" -eq 0 ]]; then
    if [[ "$(basename "$config")" == "config.json" ]]; then
      rmdir "$(dirname "$config")" 2>/dev/null || true
    fi
  fi
  if [[ -n "${BASELOOP_STATE:-}" && "$DRY_RUN" -eq 0 ]]; then
    rmdir "$state" 2>/dev/null || true
  fi

  while IFS= read -r path; do
    strip_path_marker "$path"
  done < <(path_rc_files)
}

main() {
  echo ""
  echo "  $(bold "Baseloop")"
  echo "  $(dim "Uninstall Baseloop shortcuts")"
  echo ""
  echo "  This removes Baseloop from this computer. Your AI assistants and workspace data stay untouched."
  if [[ "$PURGE" -eq 0 ]]; then
    echo "  Your Baseloop sign-in is kept, so reinstalling later is easy."
  else
    echo "  Purge mode is on, so your local Baseloop sign-in will be removed too."
  fi
  echo ""

  local binary
  if binary=$(find_binary); then
    step "Using ${binary}"
    # --keep-binary: this script removes the binary itself so the flow is the
    # same on every platform (Windows cannot delete a running executable).
    "$binary" uninstall --keep-binary "$@" || error "baseloop uninstall reported issues; fix them and rerun uninstall."
  else
    warn "Could not find the baseloop command, so checking known install locations."
    remove_known_files_directly
  fi

  if [[ "$DRY_RUN" -eq 1 ]]; then
    echo ""
    info "Preview complete. Nothing was removed. Re-run without --dry-run when you're ready."
    return 0
  fi

  # Remove the binary last.
  if [[ -n "${binary:-}" ]] && path_exists_for_removal "$binary"; then
    if ! binary_is_safe_to_remove "$binary"; then
      warn "Leaving ${binary}; it is outside Baseloop's user install directories."
    elif rm -f "$binary"; then
      info "Removed ${binary}"
    else
      REMOVAL_FAILED=1
      warn "Could not remove ${binary}; delete it manually when no terminal is using it."
    fi
  fi

  if [[ "$REMOVAL_FAILED" -ne 0 ]]; then
    error "Some files could not be removed. Delete the paths listed above and rerun uninstall."
  fi

  echo ""
  green "Baseloop has been uninstalled."
  echo
  echo "  Open a new terminal so the baseloop command disappears there too."
  echo "  If Baseloop commands still appear in Claude or Codex, restart the app or refresh its command picker."
  echo ""
}

main "$@"

#!/usr/bin/env bash
# install.sh - Install Baseloop CLI.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/baseloop-hq/baseloop-cli/main/scripts/install.sh | bash
#
# Flags:
#   --dry-run    Preview every step without downloading or changing anything
#   --no-color   Disable colored output
#   -h, --help   Show help and exit
#
# Options via environment:
#   BASELOOP_REPO           GitHub repo, default baseloop-hq/baseloop-cli
#   BASELOOP_BIN_DIR        Install directory
#   BASELOOP_VERSION        Version without v prefix, default latest
#   BASELOOP_API_URL        API URL used for auth bootstrap
#   BASELOOP_SKIP_SETUP     Set to 1 to skip agent (Claude/Codex) setup
#   BASELOOP_SKIP_AUTH      Set to 1 to skip auth bootstrap
#   BASELOOP_AUTO_UPDATE    Set to 1 to enable background self-updates

set -euo pipefail

REPO="${BASELOOP_REPO:-baseloop-hq/baseloop-cli}"
BIN_DIR="${BASELOOP_BIN_DIR:-}"
VERSION="${BASELOOP_VERSION:-}"
CURL_SCHANNEL_FALLBACK_FLAG=""
CURL_LAST_ERROR=""
CURL_FALLBACK_NOTED=0
DRY_RUN=0
AUTHENTICATED=0

print_help() {
  cat <<'EOF'
Baseloop CLI installer

Usage:
  curl -fsSL https://raw.githubusercontent.com/baseloop-hq/baseloop-cli/main/scripts/install.sh | bash
  ./install.sh [options]

  To pass flags through a piped install, use bash -s --, e.g.:
  curl -fsSL https://raw.githubusercontent.com/baseloop-hq/baseloop-cli/main/scripts/install.sh | bash -s -- --dry-run

Options:
  --dry-run     Preview what the installer would do without changing anything.
                No download, no PATH edits, no files written.
  --no-color    Disable colored output.
  -h, --help    Show this help and exit.

Common environment variables:
  BASELOOP_BIN_DIR        Install directory (default: ~/.local/bin or ~/bin)
  BASELOOP_VERSION        Version to install without the v prefix (default: latest)
  BASELOOP_API_URL        API URL used for auth bootstrap
  BASELOOP_SKIP_SETUP     Set to 1 to skip agent (Claude/Codex) setup
  BASELOOP_SKIP_AUTH      Set to 1 to skip the auth bootstrap
  BASELOOP_AUTO_UPDATE    Set to 1 to enable background self-updates
  BASELOOP_FORCE_COLOR    Set to 1 to force colored output (e.g. for previews)

Change your mind later? Run: baseloop uninstall
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run) DRY_RUN=1; shift ;;
    --no-color) NO_COLOR=1; shift ;;
    -h|--help) print_help; exit 0 ;;
    *) echo "Unknown option: $1" >&2; echo "Try --help for usage." >&2; exit 2 ;;
  esac
done

# Color is on for a real TTY (and when NO_COLOR is unset). BASELOOP_FORCE_COLOR=1
# forces it on for previews/screenshots; --no-color (parsed above) sets NO_COLOR.
if { [[ -z "${NO_COLOR:-}" ]] && [[ -t 1 ]]; } || [[ "${BASELOOP_FORCE_COLOR:-}" == "1" ]]; then
  USE_TTY=1
  C_RESET=$'\033[0m'; C_BOLD=$'\033[1m'; C_DIM=$'\033[2m'
  C_GREEN=$'\033[32m'; C_RED=$'\033[31m'; C_YELLOW=$'\033[33m'; C_CYAN=$'\033[36m'
  C_ORANGE=$'\033[38;2;255;79;0m'
else
  USE_TTY=0
  C_RESET=''; C_BOLD=''; C_DIM=''
  C_GREEN=''; C_RED=''; C_YELLOW=''; C_CYAN=''; C_ORANGE=''
fi

bold() { printf '%s%s%s' "$C_BOLD" "$1" "$C_RESET"; }
green() { printf '%s%s%s' "$C_GREEN" "$1" "$C_RESET"; }
red() { printf '%s%s%s' "$C_RED" "$1" "$C_RESET"; }

# Status glyphs degrade to ASCII when color/Unicode aren't in play.
if [[ "$USE_TTY" == "1" ]]; then
  G_OK="✓"; G_ARROW="→"; G_WARN="⚠"; G_ERR="✗"; G_DOT="•"
else
  G_OK="OK"; G_ARROW="->"; G_WARN="!"; G_ERR="ERROR"; G_DOT="*"
fi

info() { printf '  %s%s%s %s\n' "$C_GREEN" "$G_OK" "$C_RESET" "$1"; }
warn() { printf '  %s%s%s %s\n' "$C_YELLOW" "$G_WARN" "$C_RESET" "$1"; }
step() { printf '    %s%s%s %s\n' "$C_DIM" "$G_ARROW" "$C_RESET" "$1"; }
detail() { printf '    %s%s%s\n' "$C_DIM" "$1" "$C_RESET"; }
error() {
  printf '  %s%s%s %s\n' "$C_RED" "$G_ERR" "$C_RESET" "$1" >&2
  exit 1
}

# Spinner for long, otherwise-silent operations. Animates in place on a TTY;
# no-ops elsewhere (the surrounding step lines carry the information instead).
SPINNER_PID=""
spinner_start() {
  [[ "$USE_TTY" == "1" ]] || return 0
  local msg="$1" frames='⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏'
  (
    local i=0
    while true; do
      i=$(((i + 1) % ${#frames}))
      printf '\r    %s%s%s %s' "$C_CYAN" "${frames:$i:1}" "$C_RESET" "$msg"
      sleep 0.08
    done
  ) &
  SPINNER_PID=$!
}
spinner_stop() {
  [[ -n "$SPINNER_PID" ]] || return 0
  kill "$SPINNER_PID" 2>/dev/null || true
  wait "$SPINNER_PID" 2>/dev/null || true
  SPINNER_PID=""
  [[ "$USE_TTY" == "1" ]] && printf '\r\033[K'
  return 0
}
# Never let a spinner leak past an error or Ctrl-C.
trap 'spinner_stop' EXIT INT TERM

show_banner() {
  local cols
  cols=$(tput cols 2>/dev/null || true)
  if [[ -z "$cols" ]]; then
    cols=$(stty size 2>/dev/null | awk '{print $2}' || true)
  fi
  [[ -z "$cols" ]] && cols=80

  echo ""

  if [[ "$cols" -ge 70 ]]; then
    # BASELOOP wordmark (figlet larry3d). Quoted heredoc keeps the backslashes
    # and quotes literal; read -r preserves them. Works on bash 3.2 (no mapfile).
    local line
    while IFS= read -r line; do
      printf '  %s%s%s%s\n' "$C_ORANGE" "$C_BOLD" "$line" "$C_RESET"
    done <<'WORDMARK'
 ____     ______  ____    ____    __       _____   _____   ____
/\  _`\  /\  _  \/\  _`\ /\  _`\ /\ \     /\  __`\/\  __`\/\  _`\
\ \ \L\ \\ \ \L\ \ \,\L\_\ \ \L\_\ \ \    \ \ \/\ \ \ \/\ \ \ \L\ \
 \ \  _ <'\ \  __ \/_\__ \\ \  _\L\ \ \  __\ \ \ \ \ \ \ \ \ \ ,__/
  \ \ \L\ \\ \ \/\ \/\ \L\ \ \ \L\ \ \ \L\ \\ \ \_\ \ \ \_\ \ \ \/
   \ \____/ \ \_\ \_\ `\____\ \____/\ \____/ \ \_____\ \_____\ \_\
    \/___/   \/_/\/_/\/_____/\/___/  \/___/   \/_____/\/_____/\/_/
WORDMARK
  else
    printf '  %s%sBASELOOP%s\n' "$C_ORANGE" "$C_BOLD" "$C_RESET"
  fi
  printf '\n  %sBring Baseloop workflows into your AI assistant%s\n\n' "$C_DIM" "$C_RESET"
}

# A short, friendly "here's exactly what happens" intro. The point is trust:
# tell people up front that this needs no admin rights and is reversible, so a
# piped-to-shell install doesn't feel like running something sketchy.
show_welcome() {
  printf "  %sLet's get you set up.%s This usually takes less than a minute.\n\n" "$C_BOLD" "$C_RESET"
  printf "  What this will do:\n\n"
  printf '    %s%s%s download the official Baseloop CLI and verify it\n' "$C_CYAN" "$G_DOT" "$C_RESET"
  printf '    %s%s%s place it in your home folder, no admin password needed\n' "$C_CYAN" "$G_DOT" "$C_RESET"
  printf '    %s%s%s add Baseloop shortcuts to your AI assistant\n' "$C_CYAN" "$G_DOT" "$C_RESET"
  printf '    %s%s%s keep it reversible: %sbaseloop uninstall%s removes it later\n' "$C_CYAN" "$G_DOT" "$C_RESET" "$C_GREEN" "$C_RESET"
  echo ""
  if [[ "$DRY_RUN" == "1" ]]; then
    printf '  %s%s dry run, this is just a preview, nothing will be installed%s\n' "$C_DIM" "$G_ARROW" "$C_RESET"
  fi
}

clean_shell_path() {
  local shell_flag="$1" marker="$2"
  local shell_bin seed_path user_name log_name output
  local -a shell_env
  shell_bin="${SHELL:-/bin/sh}"
  [[ -x "$shell_bin" ]] || shell_bin="/bin/sh"
  seed_path="/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
  user_name="${USER:-$(id -un 2>/dev/null || true)}"
  log_name="${LOGNAME:-$user_name}"
  shell_env=(
    "HOME=$HOME"
    "USER=$user_name"
    "LOGNAME=$log_name"
    "SHELL=$shell_bin"
    "PATH=$seed_path"
  )
  if [[ -n "${ZDOTDIR:-}" ]]; then
    shell_env+=("ZDOTDIR=$ZDOTDIR")
  fi

  output=$(env -i \
    "${shell_env[@]}" \
    "$shell_bin" "$shell_flag" "printf '\n${marker}%s' \"\$PATH\"" </dev/null 2>/dev/null) || return 1
  [[ "$output" == *"$marker"* ]] || return 1
  printf '%s' "${output##*${marker}}"
}

configured_shell_path() {
  local shell_name shell_path os_name
  shell_name="${SHELL##*/}"
  shell_name="${shell_name#-}"
  case "$shell_name" in
    zsh)
      if shell_path=$(clean_shell_path "-lic" "__BASELOOP_LOGIN_INTERACTIVE_PATH__"); then
        printf '%s' "$shell_path"
        return 0
      fi
      if shell_path=$(clean_shell_path "-ic" "__BASELOOP_INTERACTIVE_PATH__"); then
        printf '%s' "$shell_path"
        return 0
      fi
      ;;
    bash)
      os_name=$(uname -s 2>/dev/null || true)
      if [[ "$os_name" != "Darwin" ]]; then
        if shell_path=$(clean_shell_path "-ic" "__BASELOOP_INTERACTIVE_PATH__"); then
          printf '%s' "$shell_path"
          return 0
        fi
        if shell_path=$(clean_shell_path "-lc" "__BASELOOP_LOGIN_PATH__"); then
          printf '%s' "$shell_path"
          return 0
        fi
      else
        if shell_path=$(clean_shell_path "-lic" "__BASELOOP_LOGIN_INTERACTIVE_PATH__"); then
          printf '%s' "$shell_path"
          return 0
        fi
        if shell_path=$(clean_shell_path "-lc" "__BASELOOP_LOGIN_PATH__"); then
          printf '%s' "$shell_path"
          return 0
        fi
      fi
      ;;
  esac

  clean_shell_path "-lc" "__BASELOOP_LOGIN_PATH__"
}

configured_shell_contains_dir() {
  local dir="$1" shell_path
  shell_path=$(configured_shell_path) || return 1
  [[ ":$shell_path:" == *":$dir:"* ]]
}

bin_dir_from_path() {
  local path_value="$1" entry first_candidate="" selected=""
  local old_ifs="$IFS" restore_glob=0
  case "$-" in
    *f*) ;;
    *) set -f; restore_glob=1 ;;
  esac
  IFS=:
  for entry in $path_value; do
    case "$entry" in
      "$HOME/.local/bin"|"$HOME/bin")
        if [[ -z "$first_candidate" ]]; then
          first_candidate="$entry"
        fi
        if [[ -x "$entry/baseloop" ]]; then
          selected="$entry"
          break
        fi
        ;;
    esac
  done
  IFS="$old_ifs"
  if [[ "$restore_glob" -eq 1 ]]; then
    set +f
  fi
  if [[ -n "$selected" ]]; then
    echo "$selected"
    return 0
  fi
  if [[ -n "$first_candidate" ]]; then
    echo "$first_candidate"
    return 0
  fi
  return 1
}

configured_shell_bin_dir() {
  local shell_path
  shell_path=$(configured_shell_path) || return 1
  bin_dir_from_path "$shell_path"
}

shell_rc_path() {
  local os_name detected_shell shell_name
  shell_name="${SHELL##*/}"
  shell_name="${shell_name#-}"
  if [[ "$shell_name" == "zsh" && -n "${ZDOTDIR:-}" ]]; then
    echo "$ZDOTDIR/.zshrc"
    return 0
  fi

  os_name=$(uname -s 2>/dev/null || true)
  detected_shell=$(detect_invoking_shell)
  case "$detected_shell" in
    zsh)
      echo "${ZDOTDIR:-$HOME}/.zshrc"
      ;;
    bash)
      if [[ "$os_name" == "Darwin" ]]; then
        # macOS Terminal starts bash as a login shell. Bash reads the first
        # existing login file in this order; do not create .bash_profile when
        # the user already relies on .bash_login or .profile.
        if [[ -e "$HOME/.bash_profile" ]]; then
          echo "$HOME/.bash_profile"
        elif [[ -e "$HOME/.bash_login" ]]; then
          echo "$HOME/.bash_login"
        elif [[ -e "$HOME/.profile" ]]; then
          echo "$HOME/.profile"
        else
          echo "$HOME/.bash_profile"
        fi
      else
        echo "$HOME/.bashrc"
      fi
      ;;
    *)
      echo "$HOME/.profile"
      ;;
  esac
}

shell_has_dir() {
  local dir="$1"
  configured_shell_contains_dir "$dir" && return 0
  local shell_rc path_line
  shell_rc=$(shell_rc_path)
  path_line="export PATH=\"${dir}:\$PATH\""
  [[ -f "$shell_rc" ]] && grep -qFx "$path_line" "$shell_rc" 2>/dev/null
}

path_begin_marker="# >>> Baseloop CLI installer >>>"
path_end_marker="# <<< Baseloop CLI installer <<<"
# Marker written by older installers; migrated to the begin/end block on rerun.
legacy_path_marker="# Added by Baseloop CLI installer"

default_bin_dir() {
  local platform="$1"
  local dir

  if dir=$(configured_shell_bin_dir); then
    echo "$dir"
    return 0
  fi

  if [[ "$platform" == windows_* ]]; then
    echo "$HOME/bin"
  else
    echo "$HOME/.local/bin"
  fi
}

detect_platform() {
  local os arch

  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  case "$os" in
    darwin) os="darwin" ;;
    linux) os="linux" ;;
    mingw*|msys*|cygwin*) os="windows" ;;
    *) error "Unsupported OS: $os" ;;
  esac

  arch=$(uname -m)
  case "$arch" in
    x86_64|amd64) arch="amd64" ;;
    aarch64|arm64) arch="arm64" ;;
    *) error "Unsupported architecture: $arch" ;;
  esac

  # If Terminal is running under Rosetta on an Apple Silicon Mac, uname reports
  # x86_64 even though the native arm64 binary is the better install target.
  if [[ "$os" == "darwin" && "$arch" == "amd64" ]]; then
    if [[ "$(sysctl -n sysctl.proc_translated 2>/dev/null || true)" == "1" ]]; then
      arch="arm64"
    fi
  fi

  echo "${os}_${arch}"
}

platform_label() {
  case "$1" in
    darwin_arm64) echo "macOS on Apple Silicon" ;;
    darwin_amd64) echo "macOS on Intel" ;;
    linux_arm64) echo "Linux on ARM64" ;;
    linux_amd64) echo "Linux on Intel/AMD" ;;
    windows_arm64) echo "Windows on ARM64" ;;
    windows_amd64) echo "Windows on Intel/AMD" ;;
    *) echo "$1" ;;
  esac
}

detect_invoking_shell() {
  local parent shell_name

  parent=$(ps -p "${PPID:-}" -o comm= 2>/dev/null | awk '{print $1}' || true)
  parent="${parent##*/}"
  parent="${parent#-}"
  case "$parent" in
    bash|zsh)
      echo "$parent"
      return 0
      ;;
  esac

  shell_name="${SHELL##*/}"
  shell_name="${shell_name#-}"
  case "$shell_name" in
    bash|zsh)
      echo "$shell_name"
      return 0
      ;;
  esac

  echo "sh"
}

detect_curl_fallback() {
  local version_output help_output

  version_output=$(curl --version 2>/dev/null || true)
  if [[ "$version_output" != *[Ss]channel* ]]; then
    return 0
  fi

  help_output=$(curl --help all 2>/dev/null || true)
  if [[ "$help_output" == *"--ssl-revoke-best-effort"* ]]; then
    CURL_SCHANNEL_FALLBACK_FLAG="--ssl-revoke-best-effort"
  elif [[ "$help_output" == *"--ssl-no-revoke"* ]]; then
    CURL_SCHANNEL_FALLBACK_FLAG="--ssl-no-revoke"
  fi
}

curl_run() {
  local err_file status err
  err_file=$(mktemp "${TMPDIR:-/tmp}/baseloop-curl.XXXXXX")

  if curl --show-error "$@" 2>"$err_file"; then
    rm -f "$err_file"
    CURL_LAST_ERROR=""
    return 0
  fi
  status=$?

  err=$(<"$err_file")
  rm -f "$err_file"

  if [[ $status -ne 0 ]] && [[ -n "$CURL_SCHANNEL_FALLBACK_FLAG" ]] && [[ "$err" == *"CRYPT_E_NO_REVOCATION_CHECK"* ]]; then
    if [[ $CURL_FALLBACK_NOTED -eq 0 ]]; then
      step "Windows certificate revocation checks are unavailable; retrying curl with ${CURL_SCHANNEL_FALLBACK_FLAG}" >&2
      CURL_FALLBACK_NOTED=1
    fi

    err_file=$(mktemp "${TMPDIR:-/tmp}/baseloop-curl.XXXXXX")
    if curl --show-error "$CURL_SCHANNEL_FALLBACK_FLAG" "$@" 2>"$err_file"; then
      rm -f "$err_file"
      CURL_LAST_ERROR=""
      return 0
    fi
    status=$?

    err=$(<"$err_file")
    rm -f "$err_file"
  fi

  CURL_LAST_ERROR="$err"
  return "$status"
}

find_sha256_cmd() {
  if command -v sha256sum >/dev/null 2>&1; then
    echo "sha256sum"
  elif command -v shasum >/dev/null 2>&1; then
    echo "shasum -a 256"
  else
    error "No SHA256 tool found. Install sha256sum or shasum."
  fi
}

get_latest_version() {
  local url version api_json

  if url=$(curl_run -fsSL -o /dev/null -w '%{url_effective}' "https://github.com/${REPO}/releases/latest"); then
    version="${url##*/}"
    version="${version#v}"
    if [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
      echo "$version"
      return 0
    fi
  fi

  if api_json=$(curl_run -fsSL -H 'Accept: application/vnd.github+json' -H 'User-Agent: baseloop-cli-installer' "https://api.github.com/repos/${REPO}/releases/latest"); then
    if [[ $api_json =~ \"tag_name\"[[:space:]]*:[[:space:]]*\"v?([^\"]+)\" ]]; then
      version="${BASH_REMATCH[1]}"
      if [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
        echo "$version"
        return 0
      fi
    fi
  fi

  error "Could not determine latest version.${CURL_LAST_ERROR:+ curl said: ${CURL_LAST_ERROR}}"
}

archive_ext() {
  local platform="$1"
  if [[ "$platform" == windows_* ]]; then
    echo "zip"
  else
    echo "tar.gz"
  fi
}

binary_name() {
  local platform="$1"
  if [[ "$platform" == windows_* ]]; then
    echo "baseloop.exe"
  else
    echo "baseloop"
  fi
}

verify_checksums() {
  local version="$1"
  local tmp_dir="$2"
  local archive_name="$3"
  local base_url="https://github.com/${REPO}/releases/download/v${version}"
  local expected actual checksum

  if ! curl_run -fsSL "${base_url}/checksums.txt" -o "${tmp_dir}/checksums.txt"; then
    error "Failed to download checksums.txt${CURL_LAST_ERROR:+ (${CURL_LAST_ERROR})}"
  fi

  checksum=$(find_sha256_cmd)
  expected=$(awk -v f="$archive_name" '$2 == f || $2 == ("*" f) {print $1; exit}' "${tmp_dir}/checksums.txt")
  actual=$(cd "$tmp_dir" && $checksum "$archive_name" | awk '{print $1}')
  [[ -n "$expected" && "$expected" == "$actual" ]] || error "Checksum verification failed for $archive_name"

}

download_binary() {
  local version="$1"
  local platform="$2"
  local tmp_dir="$3"
  local ext archive url binary

  ext=$(archive_ext "$platform")
  archive="baseloop_${version}_${platform}.${ext}"
  url="https://github.com/${REPO}/releases/download/v${version}/${archive}"
  binary=$(binary_name "$platform")

  if [[ "$DRY_RUN" == "1" ]]; then
    step "would download the Baseloop build for $(platform_label "$platform")"
    step "would verify it before installing"
    info "would install Baseloop on this computer"
    return 0
  fi

  spinner_start "fetching ${archive}"
  if ! curl_run -fsSL "$url" -o "${tmp_dir}/${archive}"; then
    spinner_stop
    error "Failed to download from $url${CURL_LAST_ERROR:+ (${CURL_LAST_ERROR})}"
  fi
  spinner_stop

  verify_checksums "$version" "$tmp_dir" "$archive"

  if [[ "$ext" == "zip" ]]; then
    command -v unzip >/dev/null 2>&1 || error "unzip is required for Windows archives"
    unzip -q "${tmp_dir}/${archive}" -d "$tmp_dir"
  else
    command -v tar >/dev/null 2>&1 || error "tar is required"
    tar -xzf "${tmp_dir}/${archive}" -C "$tmp_dir"
  fi

  [[ -f "${tmp_dir}/${binary}" ]] || error "Binary not found in archive"

  mkdir -p "$BIN_DIR"
  mv "${tmp_dir}/${binary}" "${BIN_DIR}/${binary}"
  chmod +x "${BIN_DIR}/${binary}"

  info "Baseloop downloaded and installed"
}

setup_path() {
  if shell_has_dir "$BIN_DIR"; then
    info "baseloop command already available"
    return 0
  fi

  local shell_rc path_line activate_hint tmp
  shell_rc=$(shell_rc_path)

  path_line="export PATH=\"${BIN_DIR}:\$PATH\""
  activate_hint="Open a new terminal, or run: ${path_line}"

  if [[ "$DRY_RUN" == "1" ]]; then
    info "would add the baseloop command"
    return 0
  fi

  mkdir -p "$(dirname "$shell_rc")"

  if [[ -f "$shell_rc" ]] && grep -qF "$path_begin_marker" "$shell_rc" 2>/dev/null; then
    if ! grep -qF "$path_end_marker" "$shell_rc" 2>/dev/null; then
      error "Found an incomplete Baseloop PATH block in ${shell_rc}. Remove it and rerun the installer."
    fi
    tmp=$(mktemp "${TMPDIR:-/tmp}/baseloop-profile.XXXXXX")
    awk -v begin="$path_begin_marker" -v end="$path_end_marker" -v line="$path_line" '
      $0 == begin {
        if (!replaced) {
          print begin
          print line
          print end
          replaced = 1
        }
        in_block = 1
        next
      }
      in_block {
        if ($0 == end) {
          in_block = 0
        }
        next
      }
      { print }
      END {
        if (in_block) {
          exit 1
        }
      }
    ' "$shell_rc" >"$tmp" || {
      rm -f "$tmp"
      error "Could not update the Baseloop PATH block in ${shell_rc}."
    }
    mv "$tmp" "$shell_rc"
    info "Updated the baseloop command"
    detail "updated ${shell_rc}"
    detail "$activate_hint"
    return 0
  fi

  if [[ -f "$shell_rc" ]] && grep -qFx "$legacy_path_marker" "$shell_rc" 2>/dev/null; then
    tmp=$(mktemp "${TMPDIR:-/tmp}/baseloop-profile.XXXXXX")
    awk -v legacy="$legacy_path_marker" '
      skip_export {
        skip_export = 0
        if ($0 ~ /^export PATH=".*:\$PATH"$/) {
          next
        }
      }
      $0 == legacy {
        skip_export = 1
        next
      }
      { print }
    ' "$shell_rc" >"$tmp" || {
      rm -f "$tmp"
      error "Could not migrate the Baseloop PATH entry in ${shell_rc}."
    }
    {
      echo ""
      echo "$path_begin_marker"
      echo "$path_line"
      echo "$path_end_marker"
    } >>"$tmp"
    mv "$tmp" "$shell_rc"
    info "Updated the baseloop command"
    detail "updated ${shell_rc}"
    detail "$activate_hint"
    return 0
  fi

  if [[ -f "$shell_rc" ]] && grep -qFx "$path_line" "$shell_rc" 2>/dev/null; then
    info "baseloop command already added"
    detail "already configured in ${shell_rc}"
    detail "$activate_hint"
    return 0
  fi

  {
    echo ""
    echo "$path_begin_marker"
    echo "$path_line"
    echo "$path_end_marker"
  } >>"$shell_rc"

  info "Added the baseloop command"
  detail "updated ${shell_rc}"
  detail "$activate_hint"
}

verify_install() {
  local platform="$1"
  local binary

  binary=$(binary_name "$platform")

  if [[ "$DRY_RUN" == "1" ]]; then
    info "would check that Baseloop opens"
    return 0
  fi

  if "$BIN_DIR/$binary" --version >/dev/null 2>&1; then
    info "Baseloop opens correctly"
    return 0
  fi

  error "Installation failed; baseloop is not working"
}

setup_agents() {
  local binary="$1"

  if [[ "${BASELOOP_SKIP_SETUP:-}" == "1" ]]; then
    step "skipping agent setup (BASELOOP_SKIP_SETUP=1)"
    return 0
  fi

  if [[ "$DRY_RUN" == "1" ]]; then
    step "would install the Baseloop entry skills and plugins for Claude and Codex"
    info "would add Baseloop agent setup"
    return 0
  fi

  # Capture instead of stream: on success the binary's summary line duplicates
  # the info below, but on failure its error and hint must reach the user.
  local setup_out
  if setup_out="$("$binary" setup skills 2>&1)"; then
    info "Baseloop agent setup added"
    return 0
  fi

  warn "Baseloop agent setup was not added"
  printf '    %sRetry after fixing the agent named below with:%s %sbaseloop setup skills%s\n' "$C_DIM" "$C_RESET" "$C_GREEN" "$C_RESET"
  [[ -n "$setup_out" ]] && printf '%s\n' "$setup_out" | sed 's/^/      /'
  exit 1
}

enable_auto_update() {
  local binary="$1"

  # Opt-in fleet hook: BASELOOP_AUTO_UPDATE=1 at install time turns on
  # background self-updates for this machine. Off by default; the CLI then
  # upgrades itself after ordinary commands when a new release exists.
  if [[ "${BASELOOP_AUTO_UPDATE:-}" != "1" ]]; then
    return 0
  fi

  if [[ "$DRY_RUN" == "1" ]]; then
    step "would enable background auto-update"
    return 0
  fi

  if [[ -n "${BASELOOP_REPO:-}" && "${BASELOOP_REPO}" != "baseloop-hq/baseloop-cli" ]]; then
    warn "BASELOOP_REPO is set: automatic updates only trust the official repo, so this install will show update notices instead of self-updating"
  fi

  # Best-effort: a failed enable must not fail the install.
  if "$binary" setup auto-update on >/dev/null 2>&1; then
    info "background auto-update enabled"
  else
    warn "could not enable auto-update; run: baseloop setup auto-update on"
  fi
}

bootstrap_auth() {
  local binary="$1"
  local answer
  local -a auth_args
  local workflow_prompt_file=""
  auth_args=(auth login)

  if [[ "${BASELOOP_SKIP_AUTH:-}" == "1" ]]; then
    info "Sign-in skipped for this install"
    return 0
  fi

  if [[ "$DRY_RUN" == "1" ]]; then
    step "would open a browser to connect your Baseloop account"
    return 0
  fi

  if [[ ! -t 1 ]]; then
    info "Sign-in skipped for now"
    return 0
  fi

  if [[ ! -r /dev/tty ]]; then
    info "Sign-in skipped for now"
    return 0
  fi

  while true; do
    printf '  %sDo you already have a Baseloop account?%s [y/N] ' "$C_BOLD" "$C_RESET" >/dev/tty
    if ! IFS= read -r answer </dev/tty; then
      info "Sign-in skipped for now"
      return 0
    fi

    case "$answer" in
      [Yy]|[Yy][Ee][Ss])
        break
        ;;
      ""|[Nn]|[Nn][Oo])
        auth_args=(auth login --signup)
        detail "No problem, we'll open Baseloop so you can create one and connect this CLI."
        break
        ;;
      *)
        warn "Please answer y or n"
        ;;
    esac
  done

  if [[ -n "${BASELOOP_API_URL:-}" ]]; then
    auth_args+=(--api-url "$BASELOOP_API_URL")
  fi

  if [[ " ${auth_args[*]} " == *" --signup "* ]]; then
    local state_dir
    state_dir="${BASELOOP_STATE:-${XDG_STATE_HOME:-$HOME/.local/state}/baseloop}"
    mkdir -p "$state_dir"
    rm -f "$state_dir/workflow-prompt"
    workflow_prompt_file="$state_dir/workflow-prompt-$$-$RANDOM"
    rm -f "$workflow_prompt_file"
  fi

  # Re-render auth output so the browser fallback link stays visible without
  # breaking the installer's visual hierarchy.
  if BASELOOP_WORKFLOW_PROMPT_FILE="$workflow_prompt_file" "$binary" "${auth_args[@]}" </dev/null 2>&1 | while IFS= read -r line; do
    case "$line" in
      Authenticated.*)
        ;;
      "Opening Baseloop login in your browser...")
        detail "A browser window should open."
        ;;
      "Closed the window by accident? Use this link:")
        detail "If the browser did not open, copy this link:"
        ;;
      http://*|https://*)
        printf '      %s%s%s\n' "$C_CYAN" "$line" "$C_RESET"
        ;;
      "")
        ;;
      *)
        detail "$line"
        ;;
    esac
  done; then
    info "Connected your Baseloop account"
    AUTHENTICATED=1
    run_pending_workflow "$workflow_prompt_file" "$binary"
  else
    warn "Sign-in did not complete"
    printf '    %sYou can run it anytime:%s %sbaseloop auth login%s\n' "$C_DIM" "$C_RESET" "$C_GREEN" "$C_RESET"
  fi
}

# During signup the CLI's auth login runs inside our render pipeline, so it
# cannot launch an interactive agent itself: it parks the workflow prompt
# picked in the browser in its state dir (see runWorkflowPrompt in the CLI)
# and we launch it here, where we own the terminal.
run_pending_workflow() {
  local prompt_file prompt display_prompt binary agent_bin tty_in
  prompt_file="${1:-}"
  binary="${2:-baseloop}"
  # Only launch the per-session prompt file this run asked the CLI to write.
  # Falling back to the shared state-dir file could replay a stale prompt
  # parked by an unrelated earlier signup.
  [[ -n "$prompt_file" ]] || return 0
  [[ -f "$prompt_file" ]] || return 0
  prompt="$(cat "$prompt_file")"
  rm -f "$prompt_file"
  [[ -n "$prompt" ]] || return 0
  case "$prompt" in
    -*)
      # A flag-shaped prompt would be parsed by the agent CLI as an option,
      # not a prompt. Never launch one.
      return 0
      ;;
  esac
  # For human display only: strip control characters (defuses ANSI-escape
  # smuggling from a browser-supplied string) but keep the text readable —
  # %q shell-escaping is wrong here because nothing re-parses this as shell.
  display_prompt="$(printf '%s' "$prompt" | tr -d '\000-\037\177')"

  if command -v claude >/dev/null 2>&1; then
    agent_bin="claude"
  elif command -v codex >/dev/null 2>&1; then
    agent_bin="codex"
  else
    info "Claude Code is not installed, so the workflow was not started."
    detail "Install Claude Code (or Codex), then paste this workflow prompt:"
    printf '      %s%s%s\n' "$C_CYAN" "$display_prompt" "$C_RESET"
    return 0
  fi

  printf '\n  %sWorkflow received%s\n' "$C_BOLD" "$C_RESET"
  printf '      %s\n\n' "$display_prompt"
  printf '  %sPress Enter to run it with %s%s (Ctrl-C to skip): ' "$C_BOLD" "$agent_bin" "$C_RESET" >/dev/tty
  if ! IFS= read -r _ </dev/tty; then
    printf '\n    %sSkipped. Run it yourself anytime:%s\n' "$C_DIM" "$C_RESET"
    printf '    %sStart %s, then paste this workflow prompt:%s\n' "$C_DIM" "$agent_bin" "$C_RESET"
    printf '      %s%s%s\n' "$C_CYAN" "$display_prompt" "$C_RESET"
    return 0
  fi

  # Hand the agent the real pty device backing our stdout, not /dev/tty:
  # Bun-based CLIs (Claude Code) crash on the /dev/tty alias device because
  # macOS refuses to register it with kqueue.
  #
  # BASELOOP_AGENT_HOME: the dev harness (make dev-install) runs this whole
  # installer under a scratch HOME; the agent must still see the user's real
  # HOME or it treats the machine as unconfigured (fresh onboarding, no auth).
  # Unset in production installs, where HOME is already the real one.
  local agent_home="${BASELOOP_AGENT_HOME:-$HOME}"

  # The agent session must still reach the Baseloop CLI and the credentials
  # this installer just created, even though its HOME differs under the dev
  # harness: pin the config file path (resolved against OUR home, where auth
  # login stored the token and API URL) and put the installed binary's
  # directory on PATH. Both are no-ops in production, where every path is
  # already the real one.
  local cli_config_path="${BASELOOP_CONFIG:-${XDG_CONFIG_HOME:-$HOME/.config}/baseloop/config.json}"
  local resolved_bin bin_dir agent_path
  resolved_bin="$(command -v "$binary" 2>/dev/null || printf '%s' "$binary")"
  agent_path="$PATH"
  case "$resolved_bin" in
    */*)
      bin_dir="$(cd "$(dirname "$resolved_bin")" 2>/dev/null && pwd || true)"
      [[ -n "$bin_dir" ]] && agent_path="$bin_dir:$PATH"
      ;;
  esac

  # The workflow prompt invokes the /baseloop entry skill. Setup may have been
  # skipped (BASELOOP_SKIP_SETUP, or the dev harness installing skills into a
  # scratch HOME), in which case the agent would answer "Unknown command:
  # /baseloop" — self-heal by installing the skills into the agent's HOME.
  local skill_marker="$agent_home/.claude/skills/baseloop/SKILL.md"
  if [[ "$agent_bin" == "codex" ]]; then
    skill_marker="$agent_home/.codex/skills/baseloop/SKILL.md"
  fi
  if [[ ! -f "$skill_marker" ]]; then
    detail "Installing the Baseloop skill for $agent_bin first..."
    HOME="$agent_home" BASELOOP_CONFIG="$cli_config_path" PATH="$agent_path" "$resolved_bin" setup skills >/dev/null 2>&1 || true
  fi

  tty_in="$(tty 0<&1 2>/dev/null || true)"
  if [[ -n "$tty_in" && "$tty_in" != "not a tty" && -r "$tty_in" ]]; then
    if ! HOME="$agent_home" BASELOOP_CONFIG="$cli_config_path" PATH="$agent_path" "$agent_bin" "$prompt" <"$tty_in"; then
      warn "$agent_bin exited before the workflow completed"
      detail "Start $agent_bin, then paste this workflow prompt:"
      printf '      %s%s%s\n' "$C_CYAN" "$display_prompt" "$C_RESET"
    fi
  else
    if ! HOME="$agent_home" BASELOOP_CONFIG="$cli_config_path" PATH="$agent_path" "$agent_bin" "$prompt"; then
      warn "$agent_bin exited before the workflow completed"
      detail "Start $agent_bin, then paste this workflow prompt:"
      printf '      %s%s%s\n' "$C_CYAN" "$display_prompt" "$C_RESET"
    fi
  fi
}

print_success() {
  if [[ "$DRY_RUN" == "1" ]]; then
    printf '\n  %s%s%s Dry run complete, nothing was changed on your machine.\n' "$C_GREEN" "$G_OK" "$C_RESET"
    printf '     Run the same command without %s--dry-run%s when you'\''re ready.\n\n' "$C_BOLD" "$C_RESET"
    return 0
  fi

  printf '\n  %s🎉  You'\''re all set!%s Baseloop is ready to go.\n\n' "$C_GREEN$C_BOLD" "$C_RESET"

  printf '  %sNext step%s\n' "$C_BOLD" "$C_RESET"
  if [[ "$AUTHENTICATED" != "1" ]]; then
    printf '    Sign in to your Baseloop account first:\n'
    printf '    %sbaseloop auth login%s\n\n' "$C_CYAN" "$C_RESET"
    printf '    Then open your AI assistant and type:\n'
  else
    printf '    Open your AI assistant and type:\n'
  fi
  printf '    %s/baseloop list my Baseloop workspaces%s\n' "$C_CYAN" "$C_RESET"
  echo ""

  printf '  %sUsing Claude Cowork (desktop app)?%s Skills work via a plugin there, setup takes a minute:\n' "$C_BOLD" "$C_RESET"
  printf '    %shttps://github.com/baseloop-hq/baseloop-gtm-plugin%s\n' "$C_CYAN" "$C_RESET"
  echo ""

  printf '  %sChanged your mind? Baseloop can be removed later with the uninstaller.%s\n\n' "$C_DIM" "$C_RESET"
  printf "  Enjoy! 👋\n\n"
}

main() {
  show_banner
  show_welcome

  local platform version binary
  platform=$(detect_platform)
  if [[ "$DRY_RUN" != "1" ]]; then
    command -v curl >/dev/null 2>&1 || error "curl is required"
    detect_curl_fallback
  fi
  detail "detected $(platform_label "$platform")"

  if [[ -z "$BIN_DIR" ]]; then
    BIN_DIR=$(default_bin_dir "$platform")
  fi
  binary=$(binary_name "$platform")

  if [[ -n "$VERSION" ]]; then
    version="$VERSION"
    [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || error "Invalid version '${version}'. Expected semver, for example 1.2.3 or 1.2.3-rc.1."
  elif [[ "$DRY_RUN" == "1" ]]; then
    # Stay fully offline for previews; the real run resolves this from GitHub.
    version="<latest>"
  else
    version=$(get_latest_version)
  fi

  if [[ "$DRY_RUN" == "1" ]]; then
    tmp_dir="(dry-run)"
  else
    tmp_dir=$(mktemp -d)
    cleanup() {
      spinner_stop
      rm -rf "$tmp_dir"
    }
    trap cleanup EXIT
  fi

  download_binary "$version" "$platform" "$tmp_dir"
  setup_path
  verify_install "$platform"
  setup_agents "${BIN_DIR}/${binary}"
  enable_auto_update "${BIN_DIR}/${binary}"
  bootstrap_auth "${BIN_DIR}/${binary}"

  print_success
}

main "$@"

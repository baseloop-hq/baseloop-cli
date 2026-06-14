# Baseloop CLI Install Guide

You are a coding agent installing the Baseloop CLI for your user. Follow the steps in order. Run the commands yourself; only hand a command to the user when a step says to.

## Step 1: Confirm an Agent CLI

Baseloop installs an integration path per supported agent: a local `baseloop` entry skill plus the `baseloop-gtm` plugin from the Baseloop marketplace. The setup command writes the entry skills and shells out to each agent CLI it finds on PATH:

```bash
# Claude Code
claude plugin marketplace add baseloop-hq/baseloop-gtm-plugin
claude plugin install baseloop-gtm

# Codex
codex plugin marketplace add baseloop-hq/baseloop-gtm-plugin
codex plugin add baseloop-gtm@baseloop-gtm-plugin
```

Before installing, confirm at least one agent CLI is on PATH:

```bash
claude --version
codex --version
```

A missing agent is fine; setup skips it with a note. If neither Claude Code nor Codex is present, continue with the install anyway: the CLI works standalone, and `setup skills` still writes the Claude entry skill and exits 0. After installing, tell the user no agent integration was configured and ask whether they want to install Claude Code or Codex, then re-run `baseloop setup skills`.

## Step 2: Install

Pick the command for the current platform and run it.

Unix/macOS:

```bash
curl -fsSL https://app.baseloop.io/install-cli | bash
```

Windows PowerShell:

```powershell
irm https://app.baseloop.io/install-cli.ps1 | iex
```

Pre-release fallback, only while the `app.baseloop.io` install routes do not exist yet — fetch the install script from GitHub (the script itself comes from `main`, but it still installs the latest approved release binary):

```bash
curl -fsSL https://raw.githubusercontent.com/baseloop-hq/baseloop-cli/main/scripts/install.sh | bash
```

```powershell
irm https://raw.githubusercontent.com/baseloop-hq/baseloop-cli/main/scripts/install.ps1 | iex
```

This downloads the latest approved binary, verifies it against `checksums.txt`, installs it into `BASELOOP_BIN_DIR`, `~/bin`, or `~/.local/bin`, adds that directory to PATH when possible, and installs Baseloop agent setup for every agent CLI found on PATH. It does not install or configure MCP.

The default install needs no configuration. Check [Non-Default Installs](#non-default-installs) first only if your user asked for a pinned version, a custom location, or CI.

## Step 3: Verify

Run:

```bash
baseloop --version
baseloop doctor --json
```

Read the doctor output and branch:

- `baseloop` not found: your shell has a stale PATH. Source the rc file the installer printed, or start a new shell, then retry.
- `claude_entry_skill: false` or `claude_plugin: false`: run `baseloop setup skills`, then re-run `baseloop doctor --json`.
- `codex_entry_skill: false` or `codex_plugin: false`: advisory only; run `baseloop setup skills` to fix, but a false value never fails doctor.
- `api_health: false`: the API is unreachable from this machine. Stop and report it to your user; this is not fixable from your side.
- `token: false`: expected on a fresh install. Continue to Step 4.

Agent plugin checks are conditional: `claude_plugin` appears only when `claude` is on PATH, and the `codex_*` checks appear only when `codex` is on PATH. Their absence means that agent is not installed on this machine; it is not a failure, and JSON consumers must not treat a missing check name as one. A Codex-only machine with a healthy install reports doctor success without any `claude_plugin` check.

## Step 4: Authenticate

Login opens a browser, so you cannot do it for the user. Ask them to run:

```bash
baseloop auth login
```

Then confirm with:

```bash
baseloop auth status --json
```

If the user wants a non-interactive setup instead, have them provide a token and use `baseloop auth login --token "$BASELOOP_TOKEN"`. The token is a Clerk OAuth access token, sent as `Authorization: Bearer`; to obtain one, sign in interactively once on any machine and run `baseloop auth token`.

## Step 5: Confirm and Report

Run:

```bash
baseloop tools list --json
```

If it returns a tool catalog, the install is complete. Tell your user: the CLI version installed, where the binary lives, which agent plugins were installed (Claude, Codex, or both), and whether they are signed in.

## Failure Handling

- If checksum verification fails during install: stop. Do not use the downloaded binary. Report it to your user.
- If the installer fails partway, it is safe to re-run; the installer is idempotent for the same version.
- For anything `baseloop doctor --json` cannot explain, capture its output and the installer output and show both to your user.

## Non-Default Installs

Apply these env vars to the Step 2 install only when your user asked for them. With the piped install, put them on the `bash` side (`curl ... | BASELOOP_VERSION=0.1.0 bash`), or export them first; a prefix on `curl` never reaches the script. In PowerShell, set `$env:` variables before `irm ... | iex`.

- **Pin a version:** `BASELOOP_VERSION=0.1.0` (no `v` prefix). Defaults to the latest release.
- **Custom install location:** `BASELOOP_BIN_DIR=/some/dir`. Defaults to `~/bin` or `~/.local/bin`.
- **Skip the login prompt:** `BASELOOP_SKIP_AUTH=1` (binary + agent setup only, never blocks on auth; the installer also skips login on its own when no interactive terminal is present).
- **CI:** `BASELOOP_SKIP_SETUP=1 BASELOOP_SKIP_AUTH=1`, and provide `BASELOOP_TOKEN` separately.
- **Background auto-update:** `BASELOOP_AUTO_UPDATE=1` enables opt-in self-updates on the installed machine (the installer runs `baseloop setup auto-update on`).

The full env var reference, including fork and private-repo overrides, is in [DISTRIBUTION.md](DISTRIBUTION.md).

## Upgrade

To update an existing install, run:

```bash
baseloop upgrade
```

It downloads the latest release, verifies it against the release's `checksums.txt`, swaps the binary in place, and refreshes agent setup for Claude and Codex. `baseloop doctor` reports an advisory `cli_version` check when a newer release exists.

## Uninstall

Only when your user asks. Preview first, then remove:

```bash
baseloop uninstall --dry-run   # preview removals
baseloop uninstall             # remove PATH marker and install state; keep auth
baseloop uninstall --purge     # also remove ~/.config/baseloop
```

If the binary is not on PATH, use the script instead:

```bash
curl -fsSL https://raw.githubusercontent.com/baseloop-hq/baseloop-cli/main/scripts/uninstall.sh | bash
```

```powershell
irm https://raw.githubusercontent.com/baseloop-hq/baseloop-cli/main/scripts/uninstall.ps1 | iex
```

Uninstall removes the installer's PATH entry, the install manifest, the CLI-owned Claude and Codex entry skills (only when their content still matches what setup wrote), and the binary. It does not uninstall the Claude or Codex plugins; each agent's plugin manager owns that lifecycle. Auth and config are kept unless `--purge` is passed, so a reinstall stays signed in. On Windows, prefer `scripts/uninstall.ps1` so the installer-recorded User PATH entry is removed.

---

Maintainers: the distribution model, app route contract, auth bootstrap policy, and installer internals live in [DISTRIBUTION.md](DISTRIBUTION.md).

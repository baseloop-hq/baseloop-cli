# Distribution Model

This document is for maintainers of the Baseloop CLI release pipeline and the app install routes. The agent-facing install runbook is [install.md](install.md).

## Release Artifacts

Use GitHub Releases for versioned binaries and checksums:

- `baseloop_<version>_darwin_amd64.tar.gz`
- `baseloop_<version>_darwin_arm64.tar.gz`
- `baseloop_<version>_linux_amd64.tar.gz`
- `baseloop_<version>_linux_arm64.tar.gz`
- `baseloop_<version>_windows_amd64.zip`
- `baseloop_<version>_windows_arm64.zip`
- `checksums.txt`

Agents and users treat the app route as canonical and GitHub as the binary artifact store.

## App Route Contract

| Route | Behavior |
| --- | --- |
| `GET /install-cli` | Returns the Unix/macOS installer script. |
| `GET /install-cli.ps1` | Returns the Windows PowerShell installer script. |

The app routes should serve installer scripts pinned to the latest approved GitHub release. During pre-release testing they may point at raw GitHub `main`, but production should resolve pinned versions so a broken commit cannot break installs. Do not make the installer depend on the GitHub `main` branch in production.

The app-served installer should embed:

- `BASELOOP_BASE_URL`, for example `https://app.baseloop.io`.
- `BASELOOP_API_URL`, for example `https://api-v2.baseloop.io/v1/cli`.
- `BASELOOP_REPO`, for example `baseloop-hq/baseloop-cli`.
- `BASELOOP_VERSION`, defaulted (not overridden) to the approved CLI release version, so a user-set `BASELOOP_VERSION` still wins.

## Installer Behavior

The installer:

- Downloads the latest approved `baseloop` binary for the current platform.
- Verifies the archive against `checksums.txt`.
- Installs the binary into `BASELOOP_BIN_DIR`, `~/bin`, or `~/.local/bin`.
- Adds the install directory to PATH when possible.
- Runs `baseloop setup skills` unless `BASELOOP_SKIP_SETUP=1`, which writes the `baseloop` entry skills, adds the Baseloop marketplace, and installs the `baseloop-gtm` plugin for every supported agent CLI found on PATH (Claude Code, Codex). The Claude entry skill is written unconditionally; the entire Codex leg is gated on the `codex` binary so setup never creates `~/.codex` for software that is not installed. A missing agent CLI degrades to a note with exit 0; a genuine failure with the CLI present exits 1 and names the failing agent.
- Records installer-owned local state in an install manifest at `~/.local/state/baseloop/manifest.json` (or `$XDG_STATE_HOME/baseloop/`).
- Does not install or configure MCP.

The plugins are installed through each agent's plugin manager:

```bash
# Claude Code
claude plugin marketplace add baseloop-hq/baseloop-gtm-plugin
claude plugin install baseloop-gtm

# Codex (no `plugin update` subcommand; refresh is `marketplace upgrade` + re-add)
codex plugin marketplace add baseloop-hq/baseloop-gtm-plugin
codex plugin add baseloop-gtm@baseloop-gtm-plugin
```

Codex installed-state detection reads `config.toml` under `$CODEX_HOME` (default `~/.codex`) for the `baseloop-gtm@baseloop-gtm-plugin` plugin table; the same root is used for the Codex entry skill so the CLI writes where Codex reads.

## Updates

Installs are one-shot, so staying in sync rests on three pieces:

- Ordinary commands print a one-line "new release available, run baseloop upgrade" notice to stderr (the npm/Homebrew pattern), so the update signal reaches every machine that uses the CLI at all. Stderr keeps `--json`/`--agent` stdout parseable. Update-lifecycle and introspection commands (`upgrade`, `doctor`, `uninstall`, `version`, `commands`, help) are excluded.
- `baseloop doctor` emits an advisory `cli_version` check when a newer release exists. The release lookup behind both signals is throttled to once per 24 hours via `version-check.json` in the state directory (failed lookups are cached too, so offline machines never pay a network timeout per run). Both signals are absent for `dev` builds and when `BASELOOP_NO_UPDATE_CHECK` is set.
- `baseloop upgrade` resolves the newest non-prerelease GitHub release for the platform, verifies the archive against the release's `checksums.txt` (**mandatory**; a release without a fetchable checksum entry fails the upgrade), swaps the binary in place with same-directory renames (Windows-safe: the running executable is renamed aside, never deleted), then re-runs `setup skills` **via the new binary** so the entry skills, plugin marketplaces, and plugin metadata refresh for both agents. It honors `BASELOOP_REPO` and `BASELOOP_SKIP_SETUP` like the installers.

A release must always publish `checksums.txt` covering the binary archives, or `baseloop upgrade` will refuse it.

### Background auto-update (opt-in)

With `auto_update` enabled (`baseloop setup auto-update on`, or `BASELOOP_AUTO_UPDATE=1`), an ordinary command that learns about a newer release from the 24h-cached version check spawns `baseloop upgrade --background` as a detached process and exits immediately — the command itself is never delayed. The child takes an exclusive lock in the state dir (`upgrade.lock`), runs the same checksum-verified download and rename-aside swap as a manual upgrade, refreshes the Claude plugin via the new binary, and logs to `auto-update.log` in the state dir. Failures are recorded and surfaced on the next command (retrying at most every 24 hours, three attempts per release, then dormant until the next release); `baseloop doctor` shows the full auto-update state including why an enabled machine is not updating (CI, endpoint overrides, package-manager-owned binaries, unwritable install dirs — all of which fall back to the plain update notice). Default is off: releases are checksum-verified but not yet signed, so executing downloaded binaries unattended is consent the operator gives explicitly. Flipping the default to on is deliberately deferred until releases are signed.

## Environment Variable Reference

| Variable | Purpose |
| --- | --- |
| `BASELOOP_REPO` | GitHub repo to download releases from. Defaults to `baseloop-hq/baseloop-cli`. |
| `BASELOOP_VERSION` | Version to install without `v`, for example `0.1.0`. Defaults to latest release. |
| `BASELOOP_BIN_DIR` | Install location. Defaults to `~/bin` or `~/.local/bin` on Unix, and `~/bin` on Windows. |
| `BASELOOP_SKIP_SETUP` | Set to `1` to skip automatic agent (Claude/Codex) setup. |
| `BASELOOP_SKIP_AUTH` | Set to `1` to skip post-install auth bootstrap. |
| `BASELOOP_STATE` | Override the install state/manifest directory. Defaults to `~/.local/state/baseloop`. |
| `BASELOOP_NO_UPDATE_CHECK` | Set to any value to suppress all update signals: the ordinary-command update notice, background auto-update, and doctor's `cli_version` / `auto_update` advisories. |
| `BASELOOP_AUTO_UPDATE` | Set to `1`/`true` (or `0`/`false`) to override the `auto_update` config key. When effective, ordinary commands spawn a detached `baseloop upgrade` in the background when a newer release is known. The installers run `baseloop setup auto-update on` when this is `1` at install time. |
| `BASELOOP_UPGRADE_CHILD` | Internal. Marks the detached upgrade child and its subprocesses so they never spawn further upgrades or print update notices. Do not set by hand. |
| `BASELOOP_RELEASES_API_URL` | Override the GitHub releases endpoint `baseloop upgrade` and the version check query (tests, mirrors). Setting it (or a non-default `BASELOOP_REPO`) disables background auto-update: the automatic path only trusts the canonical endpoint. |

## Local Installer Testing

Use `make dev-install` to exercise the Unix installer end to end without touching the production installer contract or GitHub releases. It builds local release archives, generates `dist/install-cli` via `scripts/install-dev.sh`, and runs the same `curl file://... | bash` flow into `/tmp/baseloop-dev-home` and `/tmp/baseloop-dev-bin`.

By default `make dev-install` also installs or refreshes Baseloop agent setup and runs the post-install auth bootstrap so the full sign-in experience can be tested locally. Use `make dev-install DEV_SKIP_SETUP=1` to skip agent setup, or `make dev-install DEV_SKIP_AUTH=1` for fast unattended installer tests.

## Auth Bootstrap Policy

The installer may attempt OAuth after binary and agent setup, but it must be skippable (`BASELOOP_SKIP_AUTH=1`).

Recommended behavior:

- Human terminal install, including `curl | bash`: run `baseloop auth login` after setup unless `BASELOOP_SKIP_AUTH=1`.
- Fully non-interactive install with no terminal output: do not block on auth; print `baseloop auth login` as the next step.
- Agent-managed install: set `BASELOOP_SKIP_AUTH=1` and only install binary + plugin.
- CI install: set `BASELOOP_SKIP_SETUP=1 BASELOOP_SKIP_AUTH=1` and provide `BASELOOP_TOKEN` separately.

## Uninstall Internals

What uninstall removes:

- The installer's Unix PATH line, stripped from the shell profile by its marker comment. On Windows, `scripts/uninstall.ps1` removes the installer-recorded User PATH entry.
- The install manifest / state directory (`BASELOOP_STATE`, default `~/.local/state/baseloop`).
- The binary (removed by the uninstall scripts; a running binary cannot delete itself on Windows).
- The CLI-owned entry skills at `~/.claude/skills/baseloop` and `$CODEX_HOME/skills/baseloop` (default `~/.codex/skills/baseloop`), each removed only when its content still matches what `baseloop setup skills` wrote for that agent. The binary checks the sha256 marker with a per-agent content fallback when the marker is missing; the no-binary fallback scripts (`uninstall.sh`, `uninstall.ps1`) check the marker only, so a dir missing its marker survives script uninstall. The agent's parent skills directory is removed only when it is a real, empty directory (a symlinked skills dir is left alone); `~/.codex` itself is never touched because Codex keeps `config.toml` there.
- The Claude and Codex plugins themselves are owned by each agent's plugin manager and are not removed by Baseloop uninstall.

Config and the stored auth token are kept unless `--purge` is passed, so a reinstall stays signed in.

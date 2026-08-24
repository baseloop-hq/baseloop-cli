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
- `install-cli` / `install-cli.ps1` — the installer scripts with the release's version stamped as the pinned default (`scripts/gen-installer-assets.sh`)

A tag containing a hyphen (for example `v1.2.3-rc.1`) publishes as a GitHub prerelease. GitHub's `latest` pointer, the install scripts, and `baseloop upgrade` all skip prereleases, so an rc can never reach the fleet by accident.

Agents and users treat the app route as canonical and GitHub as the binary artifact store.

## App Route Contract

| Route | Behavior |
| --- | --- |
| `GET /install-cli` | Returns the Unix/macOS installer script. |
| `GET /install-cli.ps1` | Returns the Windows PowerShell installer script. |

The app routes must serve installer scripts pinned to the latest approved GitHub release: point them at the latest release's `install-cli` / `install-cli.ps1` assets (`https://github.com/baseloop-hq/baseloop-cli/releases/latest/download/install-cli`), which carry the release's version as their stamped `PINNED_DEFAULT_VERSION` default. During pre-release testing they may point at raw GitHub `main`, but production must not: a broken commit on `main` would break every install, and an unpinned script silently follows `latest`. Do not make the installer depend on the GitHub `main` branch in production.

The app-served installer should embed:

- `BASELOOP_BASE_URL`, for example `https://app.baseloop.io`.
- `BASELOOP_API_URL`, for example `https://api-v2.baseloop.io/v1/cli`.
- `BASELOOP_REPO`, for example `baseloop-hq/baseloop-cli`.
- `BASELOOP_VERSION`, defaulted (not overridden) to the approved CLI release version, so a user-set `BASELOOP_VERSION` still wins. The release assets implement this as the stamped `PINNED_DEFAULT_VERSION` script default; a user-set `BASELOOP_VERSION` additionally records the install as `pinned` in its receipt, while the stamped default stays `managed`.

## Installer Behavior

The installer:

- Downloads the latest approved `baseloop` binary for the current platform.
- Verifies the archive against `checksums.txt`.
- Installs the binary into `BASELOOP_BIN_DIR`, `~/bin`, or `~/.local/bin`.
- Adds the install directory to PATH when possible.
- Runs `baseloop setup skills` unless `BASELOOP_SKIP_SETUP=1`, which writes the `baseloop` entry skills, adds the Baseloop marketplace, and installs the `baseloop-gtm` plugin for every supported agent CLI found on PATH (Claude Code, Codex). The Claude entry skill is written unconditionally; the entire Codex leg is gated on the `codex` binary so setup never creates `~/.codex` for software that is not installed. A missing agent CLI degrades to a note with exit 0; a genuine failure with the CLI present exits 1 and names the failing agent.
- Records installer-owned local state in an install manifest at `~/.local/state/baseloop/manifest.json` (or `$XDG_STATE_HOME/baseloop/`). Both installers ask the binary to write its own receipt via the hidden `baseloop setup receipt --policy <pinned|managed>` (`pinned` when the operator set `BASELOOP_VERSION`, `managed` otherwise); a pinned install gets no update notices, never background-updates, and doctor's `cli_version` advisory stays `ok` (with the newer release named in the hint) so agent workflows never upgrade it as a side effect. A successful manual `baseloop upgrade` releases the pin back to `managed` — including when it lands on the version already installed — while a background update can never release a pin. Receipt writing is best-effort: it must not fail an otherwise working install.
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
- `baseloop upgrade` resolves the newest non-prerelease GitHub release for the platform, verifies the archive against the release's `checksums.txt` (**mandatory**; a release without a fetchable checksum entry fails the upgrade), then **executes the extracted binary** (`version`, bounded by a 5s timeout and a 16KB output cap, staged next to the install target so a noexec temp directory cannot break upgrades) and refuses the swap unless it reports exactly the release's own version on one strict `baseloop <version>` line — the checksum proves integrity against `checksums.txt`, the identity check proves those bytes are actually the release the tag names (a wrong asset attached to a release, a re-published artifact under a reused tag). Only then does it swap the binary in place with same-directory renames (Windows-safe: the running executable is renamed aside, never deleted) and re-run `setup skills` **via the new binary** so the entry skills, plugin marketplaces, and plugin metadata refresh for both agents. It honors `BASELOOP_REPO` and `BASELOOP_SKIP_SETUP` like the installers.

A release must always publish `checksums.txt` covering the binary archives, or `baseloop upgrade` will refuse it.

### Background auto-update (opt-in)

With `auto_update` enabled (`baseloop setup auto-update on`, or `BASELOOP_AUTO_UPDATE=1`), an ordinary command that learns about a newer release from the 24h-cached version check spawns `baseloop upgrade --background` as a detached process and exits immediately — the command itself is never delayed. The child takes an exclusive lock in the state dir (`upgrade.lock`), runs the same checksum-verified download and rename-aside swap as a manual upgrade, refreshes the Claude plugin via the new binary, and logs to `auto-update.log` in the state dir. Failures are recorded and surfaced on the next command (retrying at most every 24 hours, three attempts per release, then dormant until the next release); `baseloop doctor` shows the full auto-update state including why an enabled machine is not updating (CI, endpoint overrides, package-manager-owned binaries, unwritable install dirs — all of which fall back to the plain update notice). Default is off: releases are checksum-verified but not yet signed, so executing downloaded binaries unattended is consent the operator gives explicitly. Flipping the default to on is deliberately deferred until releases are signed.

## Environment Variable Reference

| Variable | Purpose |
| --- | --- |
| `BASELOOP_REPO` | GitHub repo to download releases from. Defaults to `baseloop-hq/baseloop-cli`. |
| `BASELOOP_VERSION` | Version to install without `v`, for example `0.1.0`. Defaults to latest release. |
| `BASELOOP_BIN_DIR` | Install location. Defaults to `~/bin` or `~/.local/bin` on Unix, and `~/bin` on Windows. |
| `BASELOOP_SKIP_SETUP` | Set to `1` to skip automatic agent (Claude/Codex) setup, including the agent permission prompt. |
| `BASELOOP_SKIP_AGENT_PERMISSIONS` | Set to `1` to skip only the agent permission prompt (see below). |
| `BASELOOP_SKIP_AUTH` | Set to `1` to skip post-install auth bootstrap. |
| `BASELOOP_STATE` | Override the install state/manifest directory. Defaults to `~/.local/state/baseloop`. |
| `BASELOOP_NO_UPDATE_CHECK` | Set to any value to suppress all update signals: the ordinary-command update notice, background auto-update, and doctor's `cli_version` / `auto_update` advisories. |
| `BASELOOP_ORG_ID` | Default organization for org-scoped commands when no `--org-id` flag is given. Scoped per shell, so parallel agent sessions never fight over a shared default; an explicit flag still wins. |
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

## Agent Permission Prompt

After agent setup, an interactive install on a machine with `~/.claude` or `~/.codex` asks once: "Let agents run baseloop commands without asking each time? [y/N]". Yes grants every agent found: `Bash(baseloop:*)` is added to `permissions.allow` in `~/.claude/settings.json` for Claude Code, together with the PATH-prefixed `Bash(export PATH=$HOME/.local/bin:$HOME/bin:$PATH && baseloop *)` the install.md runbook relies on when an agent's shell predates the PATH change (the runbook's older bare spelling `Bash(baseloop *)` is recognized as already granted), and `prefix_rule(pattern=["baseloop"], decision="allow")` is appended to `~/.codex/rules/default.rules` (the execpolicy file Codex itself records "always allow" prefixes in; `$CODEX_HOME` is honored) for Codex. The default answer is no: the grant widens what an agent may do unattended, so it is only ever written on an explicit yes. The edit is made by the hidden `baseloop setup agent-permissions` subcommand (additive, atomic rename, follows a symlinked file, keeps a `.baseloop-backup` copy), which refuses to touch a malformed Claude settings file, a `permissions` block of an unexpected shape, or a Codex rules file that already decides `baseloop` as something other than allow. `--check` reports the state without writing. Non-interactive installs, `BASELOOP_SKIP_SETUP=1`, and `BASELOOP_SKIP_AGENT_PERMISSIONS=1` skip the prompt silently; a declined prompt prints the subcommand as the way to opt in later. The subcommand stays out of the command catalog on purpose so agents are not steered into granting their own permissions.

## Uninstall Internals

What uninstall removes:

- The installer's Unix PATH line, stripped from the shell profile by its marker comment. On Windows, `scripts/uninstall.ps1` removes the installer-recorded User PATH entry.
- The install manifest / state directory (`BASELOOP_STATE`, default `~/.local/state/baseloop`).
- The binary (removed by the uninstall scripts; a running binary cannot delete itself on Windows).
- The CLI-owned entry skills at `~/.claude/skills/baseloop` and `$CODEX_HOME/skills/baseloop` (default `~/.codex/skills/baseloop`), each removed only when its content still matches what `baseloop setup skills` wrote for that agent. The binary checks the sha256 marker with a per-agent content fallback when the marker is missing; the no-binary fallback scripts (`uninstall.sh`, `uninstall.ps1`) check the marker only, so a dir missing its marker survives script uninstall. The agent's parent skills directory is removed only when it is a real, empty directory (a symlinked skills dir is left alone); `~/.codex` itself is never touched because Codex keeps `config.toml` there.
- The Claude and Codex plugins themselves are owned by each agent's plugin manager and are not removed by Baseloop uninstall.

Config and the stored auth token are kept unless `--purge` is passed, so a reinstall stays signed in.

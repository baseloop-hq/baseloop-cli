# Baseloop CLI

`baseloop` is the official command-line interface for Baseloop. Run enrichment, manage workspaces, tables, and rows, and drive Baseloop tools from your terminal or through a coding agent. It is designed to replace MCP as the default agent transport while keeping MCP available as a compatibility path.

- Works standalone or with Claude Code and Codex
- JSON envelope output for easy scripting and agent navigation
- Clerk OAuth authentication with automatic token refresh
- Installs the Baseloop entry skills and the GTM plugin from the Baseloop marketplace for Claude and Codex
- Clean uninstall for the CLI, PATH entry, and install state

## Quick Start

**macOS / Linux / WSL2**

```bash
curl -fsSL https://app.baseloop.io/install-cli | bash
```

**Windows (PowerShell)**

```powershell
irm https://app.baseloop.io/install-cli.ps1 | iex
```

That's it. The installer downloads the verified binary, installs the Baseloop entry skills and plugins for every agent CLI it finds (Claude Code, Codex), adds `baseloop` to your PATH, and signs you in (when run in an interactive terminal and `BASELOOP_SKIP_AUTH` is not set).

<details>
<summary>Other installation methods</summary>

**Install from GitHub `main` (before the app routes exist):**

```bash
curl -fsSL https://raw.githubusercontent.com/baseloop-hq/baseloop-cli/main/scripts/install.sh | bash
```

```powershell
irm https://raw.githubusercontent.com/baseloop-hq/baseloop-cli/main/scripts/install.ps1 | iex
```

**Private or renamed repo** — override the GitHub source:

```bash
BASELOOP_REPO=your-org/baseloop-cli \
  curl -fsSL https://raw.githubusercontent.com/your-org/baseloop-cli/main/scripts/install.sh | bash
```

**GitHub Releases** — download a versioned binary and `checksums.txt` from
[Releases](https://github.com/baseloop-hq/baseloop-cli/releases), verify the SHA-256, and place it on your PATH.

**Build from source:**

```bash
git clone https://github.com/baseloop-hq/baseloop-cli
cd baseloop-cli
make build
./bin/baseloop commands --json
```

See [install.md](install.md) for the agent install runbook (give it to your coding agent), and [DISTRIBUTION.md](DISTRIBUTION.md) for the release pipeline and installer internals.

</details>

## Usage

```bash
baseloop auth login                                  # Sign in with OAuth
baseloop me                                          # Show the authenticated user and org
baseloop tools list                                  # Discover available platform tools
baseloop tools describe list_workspaces --json       # Fetch one tool's full schema
baseloop tools call list_workspaces --input '{}'     # Run a tool
baseloop tools call list_tables --input '{"workspaceId":"<id>"}'
baseloop doctor                                      # Check install, auth, and API health
baseloop upgrade                                     # Update the binary and refresh agent setup
baseloop commands                                    # Print the full command catalog
```

### Output Formats

Every command supports two machine-readable modes:

```bash
baseloop tools list --json                       # Compact catalog envelope
baseloop tools describe list_workspaces --agent  # Raw single-tool schema/detail
```

### JSON Envelope

```json
{
  "ok": true,
  "data": { },
  "summary": "Human-readable one-liner",
  "meta": { }
}
```

Errors return `ok: false` with a structured `error` object (`code`, `message`, `hint`) and a non-zero exit code, so scripts and agents can branch on the failure.

## Authentication

OAuth authorization-code flow with PKCE and automatic token refresh. The first login opens your browser:

```bash
baseloop auth login              # Authenticate with Baseloop
baseloop auth status --json      # Show auth state and config path
baseloop auth token              # Print the access token for scripts
baseloop auth logout             # Clear stored credentials
```

Tokens, the refresh token, and OAuth client metadata are stored in `~/.config/baseloop/config.json` with `0600` permissions.

**Token override** for CI, local development, and agent shells:

```bash
export BASELOOP_TOKEN="<token>"   # Overrides stored auth
baseloop auth login --token "$BASELOOP_TOKEN"
```

**Custom API URL** for local platform development:

```bash
baseloop auth login --api-url http://localhost:8911/v1/cli
baseloop doctor --api-url http://localhost:8911/v1/cli --json
```

The default API URL is `https://api-v2.baseloop.io/v1/cli`. The CLI talks to the platform's `/v1/cli` surface (`health`, `me`, `tools`, `tools/:toolName`).

## AI Agent Integration

`baseloop` integrates with Claude Code and Codex. Setup writes a `baseloop` entry skill per agent, adds the Baseloop plugin marketplace, and installs the `baseloop-gtm` plugin in every agent CLI it finds on PATH; missing agents are skipped with a note:

**Install agent setup:**

```bash
baseloop setup skills
```

Equivalent agent commands:

```bash
# Claude Code
claude plugin marketplace add baseloop-hq/baseloop-gtm-plugin
claude plugin install baseloop-gtm

# Codex
codex plugin marketplace add baseloop-hq/baseloop-gtm-plugin
codex plugin add baseloop-gtm@baseloop-gtm-plugin
```

The Claude entry skill installs unconditionally (the Claude desktop app reads it without the CLI); the Codex entry skill installs only when `codex` is on PATH, so setup never creates `~/.codex` for software that is not installed.

**Agent discovery:** every command supports `--agent --help` for structured help, and `baseloop commands --json` returns the full catalog.

## Configuration

```text
~/.config/baseloop/                # Auth and preferences (XDG_CONFIG_HOME)
└── config.json                    #   OAuth tokens + client metadata (0600)

~/.local/state/baseloop/           # Install state (XDG_STATE_HOME)
└── manifest.json                  #   What the CLI installed, for clean uninstall

~/.claude/skills/baseloop/         # CLI-owned Claude entry skill (sha256 ownership marker)
~/.claude/plugins/                 # Claude Code plugin state managed by Claude

~/.codex/skills/baseloop/          # CLI-owned Codex entry skill (CODEX_HOME overrides ~/.codex)
~/.codex/config.toml               # Codex plugin/marketplace state managed by Codex (CODEX_HOME overrides ~/.codex)
```

## Uninstall

Remove the binary, the installer's PATH entry, and install state:

```bash
curl -fsSL https://raw.githubusercontent.com/baseloop-hq/baseloop-cli/main/scripts/uninstall.sh | bash
```

```powershell
irm https://raw.githubusercontent.com/baseloop-hq/baseloop-cli/main/scripts/uninstall.ps1 | iex
```

Or directly through the binary when it is on PATH:

```bash
baseloop uninstall --dry-run     # Preview what would be removed
baseloop uninstall               # Remove Unix PATH marker and state; keep auth
baseloop uninstall --purge       # Also remove ~/.config/baseloop (config + token)
```

`baseloop uninstall` strips Unix PATH lines by their marker comment, removes Baseloop install state, and removes the CLI-owned Claude and Codex entry skills (only when their content still matches what setup wrote). It does not uninstall the Claude or Codex plugins; each agent's plugin manager owns that lifecycle. The uninstall scripts also delete the binary; on Windows, `uninstall.ps1` removes the installer-recorded User PATH entry because a running binary cannot fully uninstall itself there. Auth is kept by default so a reinstall stays signed in.

## Troubleshooting

```bash
baseloop doctor          # Check install, auth, API connectivity, entry skill, and plugin state
baseloop doctor --json   # Machine-readable health report
```

- `token: false` — run `baseloop auth login`.
- `api_health: false` — the configured API URL is unreachable; check `--api-url`.
- Entry skill or plugin checks failing — run `baseloop setup skills`.
- `codex_*` checks are advisory and appear only when `codex` is on PATH; their absence means Codex is not installed, not that something failed. `claude_plugin` likewise appears only when `claude` is on PATH.
- `cli_version` advisory not ok — a newer release exists; run `baseloop upgrade` to update the binary and refresh agent setup. Set `BASELOOP_NO_UPDATE_CHECK=1` to silence the check.
- `auto_update` advisory — shows whether background auto-update is enabled, the last auto-update failure if any, and what is blocking updates on this machine.

## Auto-Update (opt-in)

```bash
baseloop setup auto-update on    # enable background self-updates
baseloop setup auto-update      # show the current state
baseloop setup auto-update off   # back to notices only
```

Enabled, the CLI updates itself in the background: when a command notices a newer release (checked at most once per 24h), it spawns a detached upgrade and exits at full speed — you are never more than one command behind. The swap is checksum-verified and atomic; a failed update keeps the old binary working and tells you once on the next command instead of retrying forever. It stays out of the way by design: never in CI, never on Homebrew/Nix-managed binaries, never when the release endpoint env overrides are set, and never without the opt-in. `BASELOOP_AUTO_UPDATE=1` at install time enables it fleet-wide via the installer.

## Development

```bash
make build           # Build the binary into bin/
make test            # Run Go tests
make smoke           # Build + exercise core commands
make fmt             # gofmt
make release-check   # fmt + test + smoke + CLI surface check
```

## Release

```bash
scripts/build-release.sh 0.1.0   # Build local release artifacts
make release-check               # Run the release checks
```

Tagging `vX.Y.Z` triggers the GitHub release workflow.

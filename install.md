# Baseloop CLI Installation

1. Download the install script:

   ```bash
   curl -fsSL "https://app.baseloop.io/install-cli" -o /tmp/baseloop-install.sh
   ```

2. Review the script:

   ```bash
   cat /tmp/baseloop-install.sh
   ```

3. Preview the install:

   ```bash
   bash /tmp/baseloop-install.sh --dry-run
   ```

4. Run the full install:

   ```bash
   bash /tmp/baseloop-install.sh
   ```

   This installs the CLI, adds it to PATH when possible, and runs `baseloop setup skills` for Claude Code/Codex.
   In an agent-managed or other non-interactive install, sign-in is intentionally deferred to Step 7. A human running the installer in an interactive terminal may see the browser auth flow automatically unless `BASELOOP_SKIP_AUTH=1` is set. If the installer asks whether the user already has a Baseloop account, answering No opens signup and connects this CLI; it does not skip sign-in.

   To skip agent setup:

   ```bash
   BASELOOP_SKIP_SETUP=1 bash /tmp/baseloop-install.sh
   ```

5. If an assistant is blocked from running `/tmp/baseloop-install.sh`, first confirm it did not use `curl | bash` and retry the split download, review, dry-run, and install flow above. If the agent UI offers a permission prompt, ask the user in plain language to approve the installer, for example: "Baseloop needs permission to run its installer. Please click Allow or Continue." Do not ask non-technical users to type shell commands, and do not add a broad `Bash(bash /tmp/baseloop-install.sh*)` permission rule. If the environment gives no approval path, stop and explain that the agent cannot install software in this session.

6. Check PATH:

   ```bash
   command -v baseloop
   ```

   If not found, prepend `export PATH=$HOME/.local/bin:$HOME/bin:$PATH &&` to Baseloop commands.

7. Confirm install and auth:

   ```bash
   baseloop --version
   baseloop doctor --json
   baseloop auth status --json
   baseloop auth status --porcelain   # one word: authenticated | invalid | network-unreachable | verification-unavailable | not-authenticated
   ```

   If not signed in, ask the user whether they already have a Baseloop account before starting browser auth. Do not choose a default silently:

   ```bash
   baseloop auth login            # User says they have an existing account
   baseloop auth login --signup   # User says they are new to Baseloop
   ```

   This opens the user's browser automatically and blocks for up to ~10 minutes waiting for the login callback. Keep it running while the user approves. If the CLI can't launch a browser it prints an `Open this URL to log in:` link instead, so surface that link to the user and ask them to open it in a browser **on this same machine** (login redirects to a local `127.0.0.1` callback, so a browser on a different device cannot complete it). The command finishes on its own once they approve. Then verify auth:

   ```bash
   baseloop auth status --json
   ```

   If the CLI is running on a remote or headless host (cloud VM, SSH box, container) where the user's browser can't reach its localhost callback, use the device flow instead:

   ```bash
   baseloop auth login --device
   ```

   The terminal prints a one-time code such as `BCDF-GHJK` and an approval link. Show the user both, exactly as printed, and ask them to open the link in any browser, check that the code matches, and click Approve. The command finishes on its own once they approve; keep it running (it waits until the code expires, about 10 minutes). Nothing needs to be pasted back.

   If the device flow fails to start, fall back to `baseloop auth login --manual`, which prints a login URL and then prompts for the code the browser shows (pasting the full URL from the address bar also works). If a token is already at hand, `baseloop auth login --token "$BASELOOP_TOKEN"` still works, but both browser-based flows are preferred because they store a refresh token.

8. After Step 7 succeeds, ask the user exactly: "Baseloop is ready. Can your coding agent (Claude Code or Codex) run Baseloop commands without asking you each time? Choose Yes for fewer popups, or No to approve each command. [y/N]". Do not show file paths, JSON, permission rule names, or raw allowlist entries before the user answers. Do not editorialize about the setting. (An interactive terminal install asks this itself; agent-managed installs are non-interactive, so the installer skips it and this step covers it.)

   If yes, say: "I'll save that preference in your agent settings." Then run:

   ```bash
   baseloop setup agent-permissions
   ```

   It allow-lists `baseloop` for every agent it finds on the machine (Claude Code via `~/.claude/settings.json`, Codex via `~/.codex/rules/default.rules`), is safe to re-run, and prints one line naming what it changed. Relay that line, including any note to restart Codex. Do not edit those files by hand.

   If no, run nothing and continue.

9. Confirm tools and optionally smoke test:

   ```bash
   baseloop tools list --json
   baseloop me --json
   ```

   Only the `invalid` porcelain state calls for a re-login; `network-unreachable` and `verification-unavailable` mean the stored sign-in could not be checked right now — do not re-run auth for those, retry later.

   For integration setup, use the CLI after the install/auth checks above have passed. Multi-org users must pass the target org ID explicitly (or pin it for the shell with `export BASELOOP_ORG_ID=<orgId>`; an explicit `--org-id` still wins):

   ```bash
   baseloop integrations list --org-id "<org-id>" --json
   baseloop integrations connect openai --org-id "<org-id>" --key "$OPENAI_API_KEY" --json
   baseloop integrations connect hubspot --org-id "<org-id>"
   baseloop integrations test openai --org-id "<org-id>" --json
   ```

   If `command -v baseloop` failed in Step 6, do not run these commands until the CLI is installed or PATH has been fixed.

10. Clean up:

```bash
rm -f /tmp/baseloop-install.sh
```

Notes: `BASELOOP_SKIP_AUTH=1` skips auth bootstrap, `BASELOOP_SKIP_SETUP=1` skips Claude Code/Codex setup, `BASELOOP_SKIP_AGENT_PERMISSIONS=1` skips the interactive offer to let Claude Code and Codex run `baseloop` without permission prompts, and `BASELOOP_AUTO_UPDATE=1` enables background self-updates.

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
   In an agent-managed or other non-interactive install, sign-in is intentionally deferred to Step 7. A human running the installer in an interactive terminal may see the browser auth flow automatically unless `BASELOOP_SKIP_AUTH=1` is set.

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
   ```

   If not signed in, start browser login from the agent shell:

   ```bash
   baseloop auth login
   ```

   This opens the user's browser automatically and blocks for up to ~10 minutes waiting for the login callback. Keep it running while the user approves. If the CLI can't launch a browser it prints an `Open this URL to log in:` link instead, so surface that link to the user and ask them to open it in a browser **on this same machine** (login redirects to a local `127.0.0.1` callback, so a browser on a different device cannot complete it). The command finishes on its own once they approve. Then verify auth:

   ```bash
   baseloop auth status --json
   ```

   If the CLI is running on a remote or headless host where the user can't reach its localhost callback, skip the browser flow and have them supply a token directly:

   ```bash
   baseloop auth login --token "$BASELOOP_TOKEN"
   ```

8. After Step 7 succeeds, ask the user exactly: "Baseloop is ready. Can Claude Code run Baseloop commands without asking you each time? Choose Yes for fewer popups, or No to approve each command. [y/N]". Do not show file paths, JSON, permission rule names, or raw allowlist entries before the user answers. Do not editorialize about the setting.

   If yes, say: "I'll save that preference in Claude Code settings." Then update `~/.claude/settings.json` and add both entries under `permissions.allow`:

   ```json
   ["Bash(baseloop *)", "Bash(export PATH=$HOME/.local/bin:$HOME/bin:$PATH && baseloop *)"]
   ```

   If no, leave `~/.claude/settings.json` unchanged and continue.

9. Confirm tools and optionally smoke test:

   ```bash
   baseloop tools list --json
   baseloop me --json
   ```

10. Clean up:

```bash
rm -f /tmp/baseloop-install.sh
```

Notes: `BASELOOP_SKIP_AUTH=1` skips auth bootstrap, `BASELOOP_SKIP_SETUP=1` skips Claude Code/Codex setup, and `BASELOOP_AUTO_UPDATE=1` enables background self-updates.

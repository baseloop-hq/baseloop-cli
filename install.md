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
   This installs the CLI, adds it to PATH when possible, runs `baseloop setup skills` for Claude Code/Codex, and may open a browser for auth. To skip agent setup:
   ```bash
   BASELOOP_SKIP_SETUP=1 bash /tmp/baseloop-install.sh
   ```

5. If an assistant is blocked from running `/tmp/baseloop-install.sh`, do not add a `Bash(bash /tmp/baseloop-install.sh*)` permission rule. Ask the user to run Steps 3-4 manually, then continue only after they report back.

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
   If not signed in:
   ```bash
   baseloop auth login
   baseloop auth status --json
   ```

8. After Step 7 succeeds, ask: "Allow Claude Code to run common Baseloop commands without permission prompts? [y/N]". If yes, update `~/.claude/settings.json` and add both entries under `permissions.allow`:
   ```json
   [
     "Bash(baseloop *)",
     "Bash(export PATH=$HOME/.local/bin:$HOME/bin:$PATH && baseloop *)"
   ]
   ```
   If no, leave `~/.claude/settings.json` unchanged.

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

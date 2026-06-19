# Baseloop CLI Installation

You are a coding agent installing the Baseloop CLI for your user. Follow these steps in order. Download the installer to a local temp file first; do not pipe `curl` directly into a shell.

## macOS / Linux / WSL2

1. Create an unpredictable temp path and download the install script:

   ```bash
   BASELOOP_INSTALLER="$(mktemp "${TMPDIR:-/tmp}/baseloop-install.XXXXXX")"
   curl -fsSL "https://app.baseloop.io/install-cli" -o "$BASELOOP_INSTALLER"
   ```

2. Review the full script before running it:

   ```bash
   cat "$BASELOOP_INSTALLER"
   ```

3. Generate the exact installer command from the temp path:

   ```bash
   BASELOOP_INSTALL_CMD="$(printf 'bash %q' "$BASELOOP_INSTALLER")"
   printf '%s\n' "$BASELOOP_INSTALL_CMD"
   ```

   If running under Claude Code auto mode, ask: "Allow Claude to run the reviewed Baseloop installer command? [y/N]". If yes, add the exact printed command as a temporary permission entry under `permissions.allow` by wrapping it as `"Bash(<printed command>)"`. If no, leave `~/.claude/settings.json` unchanged and continue.

4. Tell the user: "The installer may open a browser tab to connect your Baseloop account. Please sign in and approve access when it opens." Then run the exact printed command from Step 3.

   Do not run `bash "$BASELOOP_INSTALLER"` under Claude Code auto mode; the permission matcher may not expand shell variables before checking the allowed command.

   If Claude Code auto-mode blocks execution because the script was downloaded from an external domain, print an expanded command:

   ```bash
   printf '%s\n' "$BASELOOP_INSTALL_CMD"
   ```

   Then stop and tell the user: "Claude reviewed the full installer but auto-mode blocked executing a downloaded script. Please run this exact command in your terminal: `<printed command>`. Tell me when it finishes so I can continue verification." Do not try to work around the denial.

5. If you added the temporary installer permission in Step 3, remove `"Bash(<printed command>)"` from `~/.claude/settings.json` after the installer finishes. Then remove the reviewed temp file:

   ```bash
   rm -f "$BASELOOP_INSTALLER"
   ```

6. Check if `baseloop` is in PATH:

   ```bash
   command -v baseloop
   ```

   If found, use bare `baseloop` commands. If not found, prepend this PATH export to Baseloop commands:

   ```bash
   export PATH=$HOME/.local/bin:$HOME/bin:$PATH && baseloop ...
   ```

7. Confirm the install:

   ```bash
   baseloop --version
   baseloop doctor --json
   ```

   If `baseloop` was not found in Step 6, use the PATH-prefixed form:

   ```bash
   export PATH=$HOME/.local/bin:$HOME/bin:$PATH && baseloop --version
   export PATH=$HOME/.local/bin:$HOME/bin:$PATH && baseloop doctor --json
   ```

8. Confirm auth:

   ```bash
   baseloop auth status --json
   ```

   If the user is not signed in, tell them: "Baseloop login will open a browser tab. Please sign in and approve access when it opens." Then run:

   ```bash
   baseloop auth login
   baseloop auth status --json
   ```

9. Ask: "Allow Claude to run common Baseloop setup commands without permission prompts? [y/N]". If yes, update `~/.claude/settings.json` and add BOTH permission entries under `permissions.allow`: `"Bash(baseloop *)"` and `"Bash(export PATH=$HOME/.local/bin:$HOME/bin:$PATH && baseloop *)"`.

   If no, leave `~/.claude/settings.json` unchanged.

10. Confirm Baseloop tools are available:

   ```bash
   baseloop tools list --json
   ```

11. Ask the user if they want to run a quick smoke test. If yes, run a read-only command such as:

   ```bash
   baseloop me --json
   ```

## Windows PowerShell

1. Download the install script:

   ```powershell
   iwr "https://app.baseloop.io/install-cli.ps1" -OutFile "$env:TEMP\baseloop-install.ps1"
   ```

2. Review the full script:

   ```powershell
   Get-Content "$env:TEMP\baseloop-install.ps1"
   ```

3. Tell the user: "The installer may open a browser tab to connect your Baseloop account. Please sign in and approve access when it opens." Then run:

   ```powershell
   powershell -ExecutionPolicy Bypass -File "$env:TEMP\baseloop-install.ps1"
   ```

   If Claude Code auto-mode blocks execution because the script was downloaded from an external domain, stop and tell the user to run the same PowerShell command in their terminal, then continue verification after they confirm it finished. Do not try to work around the denial.

4. Confirm with:

   ```powershell
   baseloop --version
   baseloop doctor --json
   baseloop auth status --json
   ```

## Notes

- The installer downloads the verified Baseloop binary, installs agent setup for Claude Code and Codex when their CLIs are on PATH, and adds the install directory to PATH when possible.
- If agent setup is missing or stale, run `baseloop setup skills`, then re-run `baseloop doctor --json`.
- If API health fails in `baseloop doctor --json`, stop and report the doctor output to the user.
- For non-default installs, set env vars before running the local script. Common options are `BASELOOP_VERSION`, `BASELOOP_BIN_DIR`, `BASELOOP_SKIP_AUTH=1`, `BASELOOP_SKIP_SETUP=1`, and `BASELOOP_AUTO_UPDATE=1`.

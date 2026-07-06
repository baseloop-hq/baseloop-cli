package cli

const baseloopClaudeSkill = `---
name: baseloop
description: Start here for any Baseloop task. Routes workflow requests (plan, build, diagnose, review) to the baseloop-gtm skills, and uses the Baseloop CLI to inspect and operate workspaces, tables, fields, rows, runs, presets, views, integrations, and action metadata without MCP. Prefer this skill for Baseloop work from Claude or shell scripts.
user-invocable: true
argument-hint: "[Baseloop task]"
---

# Baseloop CLI Agent Skill

## Routing

Match the user's intent first. If a ` + "`baseloop-gtm`" + ` skill fits, defer to it instead of working from this skill.

| The user wants to | Defer to |
| --- | --- |
| Design a workflow from a goal, without creating anything | ` + "`baseloop-gtm-plan`" + ` |
| Build a planned workflow step by step | ` + "`baseloop-gtm-build`" + ` |
| Fix a field error, unexpected output, or data is not flowing | ` + "`baseloop-gtm-diagnose`" + ` |
| Audit an existing workflow for pitfalls (read-only) | ` + "`baseloop-gtm-review`" + ` |
| Anything else: inspect or operate workspaces, tables, fields, rows, runs, presets, views, or integrations | this skill, below |

If the matching ` + "`baseloop-gtm`" + ` skill is not installed, run ` + "`baseloop setup skills`" + ` to install or refresh agent setup, or continue with the CLI below.

## CLI Usage

Use the ` + "`baseloop`" + ` binary for Baseloop operations. The CLI replaces MCP for agent workflows; MCP is only a compatibility fallback when the CLI is missing or unauthenticated.

## Required Checks

1. Run ` + "`baseloop doctor --json`" + ` before multi-step Baseloop work. If the ` + "`cli_version`" + ` advisory reports ` + "`ok: false`" + `, run ` + "`baseloop upgrade`" + ` first; it updates the binary and refreshes agent skills and plugins. Doctor emits agent plugin checks (` + "`claude_plugin`" + `, ` + "`codex_entry_skill`" + `, ` + "`codex_plugin`" + `) only when that agent's CLI is on PATH; a missing check name means that agent is not installed, not a failure.
2. Use ` + "`baseloop auth status --json`" + ` when auth state is unclear.
3. Use ` + "`baseloop integrations list --json`" + ` before configuring actions that need provider credentials.
4. Use ` + "`baseloop tools list --json`" + ` to discover tool names.
5. Use ` + "`baseloop tools describe <tool_name> --json`" + ` to fetch one tool's full schema before calling it.
6. Use ` + "`baseloop tools call <tool_name> --input '<json>' --json`" + ` to execute a tool.
7. Use ` + "`baseloop --agent --help`" + ` and ` + "`baseloop <command> --agent --help`" + ` to inspect command shape.
8. For multi-org users, pass ` + "`organizationId`" + ` for org-scoped tools when the tool schema marks it required.
9. Inspect tool ` + "`safety.destructive`" + `, ` + "`safety.cost`" + `, and ` + "`safety.idempotent`" + ` before running write, destructive, or high-cost operations.
10. Do not parse human output in automation. Use ` + "`--json`" + ` or ` + "`--agent`" + `.

## Integrations

Use ` + "`baseloop integrations`" + ` for CLI-only provider setup. This is separate from MCP tools so secrets and browser handoffs stay in the terminal.

When working from a terminal, try the CLI integration flow first. Do not tell the user to log in to ` + "`app.baseloop.io`" + ` or navigate to the web app just to connect a provider unless the CLI flow fails and the error explicitly requires dashboard recovery.

| Provider type | Command shape |
| --- | --- |
| API-key providers, such as OpenAI or Vidu | ` + "`baseloop integrations connect <provider> --key '<api-key>' --json`" + ` |
| OAuth providers, such as HubSpot, Pipedrive, Salesforce, Slack, or Attio | ` + "`baseloop integrations connect <provider>`" + ` |
| Hosted-auth providers, currently LinkedIn via Unipile | ` + "`baseloop integrations connect linkedin`" + ` |

When an agent or script needs a browser URL without opening a browser, use ` + "`--json`" + ` for the start-flow payload or ` + "`--no-browser`" + ` for human-readable output. Do not ask users to paste raw API keys into chat. Prefer that the user runs the connect command in their own terminal, or use a user-provided environment variable in a local shell.

## Output Modes

| Goal | Command shape |
| --- | --- |
| Full machine-readable envelope | ` + "`baseloop <command> --json`" + ` |
| Raw data for an agent | ` + "`baseloop <command> --agent`" + ` |
| Human status | ` + "`baseloop <command>`" + ` |

## Common Commands

` + "```bash" + `
baseloop doctor --json
baseloop upgrade --json
baseloop --agent --help
baseloop auth login
baseloop auth status --json
baseloop me --json
baseloop integrations list --json
baseloop integrations connect openai --key "$OPENAI_API_KEY" --json
baseloop integrations connect hubspot
baseloop integrations connect linkedin
baseloop integrations test openai --json
baseloop tools list --json
baseloop tools describe list_workspaces --json
baseloop tools call list_workspaces --input '{}' --json
baseloop tools call list_tables --input '{"workspaceId":"<workspace-id>"}' --json
baseloop tools call get_table_schema --input '{"tableId":"<table-id>"}' --json
baseloop tools call create_field --input '{"tableId":"<table-id>","name":"Example","type":"text"}' --json
baseloop tools call run_field --input '{"fieldId":"<field-id>"}' --json
baseloop tools call wait_for_run --input '{"runId":"<run-id>"}' --json
` + "```" + `

## Setup

Install or refresh this entry skill and the Claude plugin with:

` + "```bash" + `
baseloop setup skills
` + "```" + `

The installer also attempts safe non-interactive setup.
`

// baseloopCodexSkill is the Codex flavor of the entry skill. It carries the
// same routing and CLI contract as baseloopClaudeSkill with two deliberate
// differences: the frontmatter matches Codex's skill loader (no
// user-invocable field), and the wording stays agent-neutral so it never
// claims Claude-specific behavior to a Codex session.
const baseloopCodexSkill = `---
name: baseloop
description: Start here for any Baseloop task. Routes workflow requests (plan, build, diagnose, review) to the baseloop-gtm skills, and uses the Baseloop CLI to inspect and operate workspaces, tables, fields, rows, runs, presets, views, integrations, and action metadata without MCP. Prefer this skill for Baseloop work from a coding agent or shell scripts.
argument-hint: "[Baseloop task]"
---

# Baseloop CLI Agent Skill

## Routing

Match the user's intent first. If a ` + "`baseloop-gtm`" + ` skill fits, defer to it instead of working from this skill.

| The user wants to | Defer to |
| --- | --- |
| Design a workflow from a goal, without creating anything | ` + "`baseloop-gtm-plan`" + ` |
| Build a planned workflow step by step | ` + "`baseloop-gtm-build`" + ` |
| Fix a field error, unexpected output, or data is not flowing | ` + "`baseloop-gtm-diagnose`" + ` |
| Audit an existing workflow for pitfalls (read-only) | ` + "`baseloop-gtm-review`" + ` |
| Anything else: inspect or operate workspaces, tables, fields, rows, runs, presets, views, or integrations | this skill, below |

If the matching ` + "`baseloop-gtm`" + ` skill is not installed, run ` + "`baseloop setup skills`" + ` to install or refresh agent setup, or continue with the CLI below.

## CLI Usage

Use the ` + "`baseloop`" + ` binary for Baseloop operations. The CLI replaces MCP for agent workflows; MCP is only a compatibility fallback when the CLI is missing or unauthenticated.

## Required Checks

1. Run ` + "`baseloop doctor --json`" + ` before multi-step Baseloop work. If the ` + "`cli_version`" + ` advisory reports ` + "`ok: false`" + `, run ` + "`baseloop upgrade`" + ` first; it updates the binary and refreshes agent skills and plugins. Doctor emits agent plugin checks (` + "`claude_plugin`" + `, ` + "`codex_entry_skill`" + `, ` + "`codex_plugin`" + `) only when that agent's CLI is on PATH; a missing check name means that agent is not installed, not a failure.
2. Use ` + "`baseloop auth status --json`" + ` when auth state is unclear.
3. Use ` + "`baseloop integrations list --json`" + ` before configuring actions that need provider credentials.
4. Use ` + "`baseloop tools list --json`" + ` to discover tool names.
5. Use ` + "`baseloop tools describe <tool_name> --json`" + ` to fetch one tool's full schema before calling it.
6. Use ` + "`baseloop tools call <tool_name> --input '<json>' --json`" + ` to execute a tool.
7. Use ` + "`baseloop --agent --help`" + ` and ` + "`baseloop <command> --agent --help`" + ` to inspect command shape.
8. For multi-org users, pass ` + "`organizationId`" + ` for org-scoped tools when the tool schema marks it required.
9. Inspect tool ` + "`safety.destructive`" + `, ` + "`safety.cost`" + `, and ` + "`safety.idempotent`" + ` before running write, destructive, or high-cost operations.
10. Do not parse human output in automation. Use ` + "`--json`" + ` or ` + "`--agent`" + `.

## Integrations

Use ` + "`baseloop integrations`" + ` for CLI-only provider setup. This is separate from MCP tools so secrets and browser handoffs stay in the terminal.

When working from a terminal, try the CLI integration flow first. Do not tell the user to log in to ` + "`app.baseloop.io`" + ` or navigate to the web app just to connect a provider unless the CLI flow fails and the error explicitly requires dashboard recovery.

| Provider type | Command shape |
| --- | --- |
| API-key providers, such as OpenAI or Vidu | ` + "`baseloop integrations connect <provider> --key '<api-key>' --json`" + ` |
| OAuth providers, such as HubSpot, Pipedrive, Salesforce, Slack, or Attio | ` + "`baseloop integrations connect <provider>`" + ` |
| Hosted-auth providers, currently LinkedIn via Unipile | ` + "`baseloop integrations connect linkedin`" + ` |

When an agent or script needs a browser URL without opening a browser, use ` + "`--json`" + ` for the start-flow payload or ` + "`--no-browser`" + ` for human-readable output. Do not ask users to paste raw API keys into chat. Prefer that the user runs the connect command in their own terminal, or use a user-provided environment variable in a local shell.

## Output Modes

| Goal | Command shape |
| --- | --- |
| Full machine-readable envelope | ` + "`baseloop <command> --json`" + ` |
| Raw data for an agent | ` + "`baseloop <command> --agent`" + ` |
| Human status | ` + "`baseloop <command>`" + ` |

## Common Commands

` + "```bash" + `
baseloop doctor --json
baseloop upgrade --json
baseloop --agent --help
baseloop auth login
baseloop auth status --json
baseloop me --json
baseloop integrations list --json
baseloop integrations connect openai --key "$OPENAI_API_KEY" --json
baseloop integrations connect hubspot
baseloop integrations connect linkedin
baseloop integrations test openai --json
baseloop tools list --json
baseloop tools describe list_workspaces --json
baseloop tools call list_workspaces --input '{}' --json
baseloop tools call list_tables --input '{"workspaceId":"<workspace-id>"}' --json
baseloop tools call get_table_schema --input '{"tableId":"<table-id>"}' --json
baseloop tools call create_field --input '{"tableId":"<table-id>","name":"Example","type":"text"}' --json
baseloop tools call run_field --input '{"fieldId":"<field-id>"}' --json
baseloop tools call wait_for_run --input '{"runId":"<run-id>"}' --json
` + "```" + `

## Setup

Install or refresh this entry skill and the Baseloop GTM plugin with:

` + "```bash" + `
baseloop setup skills
` + "```" + `

The installer also attempts safe non-interactive setup.
`

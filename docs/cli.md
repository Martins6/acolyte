# CLI reference

**Intent.** Every `acolyte` command, what it is for, and where to read more. Run
`acolyte <command> --help` for the exact flags of the installed version.

## Agents

| Command | Description |
| ------- | ----------- |
| `acolyte new --name <n> --workspace-folder <p> --config '<json>'` | Scaffold, validate, and register an agent ([config reference](README.md#creating-an-agent)). |
| `acolyte config <agent> get\|set\|edit [value]` | Read or update an agent's runtime capabilities ([`orchestrator/config.md`](orchestrator/config.md)). |
| `acolyte status [name\|workspace]` | List agents, or show one agent's channels, allowed users, last message, jobs, and activity. |
| `acolyte delete <name\|workspace>` | Remove an agent from the registry; the workspace folder is kept. |
| `acolyte attach [--agent-name <n>] [--session-id <id>]` | Attach the OpenCode TUI to the agent session ([`orchestrator/daemon.md`](orchestrator/daemon.md)). |
| `acolyte inspect --session-id <id>` | Merge OpenCode history with communication routing events (`--no-opencode`, `--no-comm`, `--ndjson`, `--limit`). |
| `acolyte run "<message>"` | Run one headless agent turn and print the reply (`--continue`, `--session-id`, `--json`, `--timeout`, `--verbose`). |

## Scheduling and messaging

| Command | Description |
| ------- | ----------- |
| `acolyte start [--log-file <path>]` | Start the multi-agent daemon ([`orchestrator/daemon.md`](orchestrator/daemon.md)). |
| `acolyte cron ...` | Manage scheduled prompt, shell, and routine jobs ([`orchestrator/cron.md`](orchestrator/cron.md)). |
| `acolyte cron list\|view\|rm\|routine-list\|tick` | Inspect or run the cron service. |
| `acolyte notify-user "<text>" [--media <path>]` | Notify the user only, never the agent; one optional image. |
| `acolyte inbox "<text>"` | Deliver a synthetic external event to an agent session. |

## Memory

| Command | Description |
| ------- | ----------- |
| `acolyte memory export --out <zip>` | Export a portable memory trace (`--session-id`, `--no-opencode`, `--sanitize`, `--force`, `--json`) ([`memory/memory-export/opencode-export.md`](memory/memory-export/opencode-export.md)). |
| `acolyte memory import <zip> [--mode trace\|sessions\|all]` | Import a trace bundle (`--dry-run`, `--force`, `--json`). |

## Service and maintenance

| Command | Description |
| ------- | ----------- |
| `acolyte install-service [--user\|--system\|--print]` | Install the daemon as a crash-restarting service ([`orchestrator/autostart.md`](orchestrator/autostart.md)). |
| `acolyte update [--check]` | Check for and apply releases ([`updating.md`](updating.md)). |
| `acolyte version [--short]` | Print the binary version. |

## Targeting an agent

For `run`, `attach`, `inbox`, `notify-user`, `memory export`, `memory import`, and `cron` job
creation, the target is `--agent-name <name>` **xor** `--workspace-folder <path>`. With neither
flag the command falls back to `$ACOLYTE_WORKSPACE_PATH` and then the current directory, which must
be a registered agent workspace.

The other commands take a target differently:

- `config`, `status`, and `delete` take the agent name or workspace path as a positional argument;
  `status` with no argument lists every agent. `new` takes `--name` and `--workspace-folder` and
  does not accept `--agent-name`.
- `inspect` scans every registered agent for the session when neither target flag is passed; with a
  target flag, the session must be mapped to that agent.
- `cron list`, `cron routine-list`, and `cron tick` are global. `cron view` and `cron rm` scan every
  agent when no target flag is passed, and report ambiguity when several agents use the same title.

`--session-id` is checked against the target agent's session mappings by `run`, `inbox`, and
`cron`, and by `inspect` when a target is given, so an event cannot land in another agent's session.
`notify-user --session-id` is attribution-only and skips that check.

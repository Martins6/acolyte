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

Flag-targeted commands (`run`, `inspect`, `attach`, `inbox`, `notify-user`, `cron`, and `memory
export`/`import`) accept `--agent-name <name>` **xor** `--workspace-folder <path>`. When neither is
passed they fall back to `$ACOLYTE_WORKSPACE_PATH` and then the current directory, which must be a
registered agent workspace.

Other commands take the target differently: `config`, `status`, and `delete` take an agent name or
workspace path as a positional argument, while `new` requires both `--name` and `--workspace-folder`.

An explicit `--session-id` must already be mapped to the target agent; sessions are global in
OpenCode, so this check keeps an inbox or cron event inside the selected agent.

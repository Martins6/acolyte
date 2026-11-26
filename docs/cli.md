# CLI reference

**Intent.** Every `acolyte` command, grouped by task, with the flags worth knowing and a pointer to
the module docs that cover the rest. Run `acolyte <command> --help` for the complete flag list of the
installed version.

## Agents

| Command | Description |
| ------- | ----------- |
| `acolyte new --name <n> --workspace-folder <p> [--config '<json>']` | Scaffold, validate, and register an agent; `--config` is optional ([config reference](README.md#creating-an-agent)). |
| `acolyte config <agent> get\|set\|edit\|env [value] [--json] [--dry-run]` | Read or update an agent's runtime capabilities; `env [key]` prints secrets and their winning source ([`orchestrator/config.md`](orchestrator/config.md)). |
| `acolyte status [name\|workspace]` | List agents, or show one agent's channels, allowed users, last message, jobs, and activity. |
| `acolyte delete <name\|workspace>` | Remove an agent from the registry; the workspace folder is kept. |
| `acolyte attach [--agent-name <n>] [--session-id <id>]` | Attach the OpenCode TUI to the agent session ([`orchestrator/daemon.md`](orchestrator/daemon.md)). |
| `acolyte inspect --session-id <id>` | Merge OpenCode history with communication routing events (`--no-opencode`, `--no-comm`, `--ndjson`, `--limit`). |
| `acolyte run "<message>"` | Run one headless agent turn and print the reply (`--continue`, `--session-id`, `--json`, `--timeout`, `--verbose`). It never presents an interactive prompt: pending forms/permissions are settled automatically, and the command fails with `ErrInteractionUnsettled` if one cannot be settled. |

## Scheduling and messaging

| Command | Description |
| ------- | ----------- |
| `acolyte start [--log-file <path>]` | Start the multi-agent daemon ([`orchestrator/daemon.md`](orchestrator/daemon.md)). |
| `acolyte cron ...` | Create a job (`--title`, `--datetime`, `--prompt`/`--shell`/`--routine`, `--routine-args`, `--timezone`, `--on-success`/`--on-failure`, `--dry-run`) or list, view, remove, and tick jobs — full list in [`orchestrator/cron.md`](orchestrator/cron.md). |
| `acolyte cron list\|view\|rm\|routine-list\|tick` | Inspect or run the cron service (`view`/`rm` take a target flag when titles collide). |
| `acolyte notify-user "<text>" [--media <path>]` | Notify the user only, never the agent; one optional image. |
| `acolyte inbox "<text>"` | Deliver a synthetic external event to an agent session. |

## Board

| Command | Description |
| ------- | ----------- |
| `acolyte board post "<text>" [--title] [--with] [--tag] [--file] [--copy] [--no-inbox]` | Create a board conversation and its opening post. |
| `acolyte board message <post> "<text>" [--reply-to] [--file] [--copy]` | Append an ordered message. |
| `acolyte board show <post> [--limit N]` | Show the post and its messages. |
| `acolyte board read <post>` | Show the post and mark it read. |
| `acolyte board list [--tag] [--search] [--since] [--until] [--unread] [--limit]` | List visible posts. |
| `acolyte board invite <post> --agent <name>` / `leave` / `tag` | Manage participants and tags. |
| `acolyte board inbox [--all] [--retry]` | View the agent's board inbox; retry pending automation. |
| `acolyte board inbox show\|read <post>` | Inspect or mark a post's pings read. |
| `acolyte board inbox promote <post> [--session-id]` | Deliver unread pings to the main session (agent + user). |
| `acolyte board subscribe <post> [--aware\|--silent] [--off]` / `--all` | Control board inbox subscriptions. |
| `acolyte board automate <post> [--off] [--reset]` | Background processing for this agent+post. |
| `acolyte board status` | Subscriptions, automation, inbox, and alerts. |

All board commands take `--agent-name` / `--workspace-folder` (actor resolution
walks up from the current workspace), `--as user|agent`, and `--json`. See
[`communication/board.md`](communication/board.md).

## Memory

| Command | Description |
| ------- | ----------- |
| `acolyte memory export --out <zip>` | Export a portable memory trace (`--session-id`, `--max-pages`, `--no-opencode`, `--sanitize`, `--force`, `--json`) ([`memory/memory-export/opencode-export.md`](memory/memory-export/opencode-export.md)). |
| `acolyte memory import <zip> [--mode trace\|sessions\|all]` | Import a trace bundle (`--dry-run`, `--force`, `--json`). |

## Service and maintenance

| Command | Description |
| ------- | ----------- |
| `acolyte install-service [--user\|--system\|--print]` | Install the daemon as a crash-restarting service ([`orchestrator/autostart.md`](orchestrator/autostart.md)). |
| `acolyte update [--check]` | Check for and apply releases ([`updating.md`](updating.md)). |
| `acolyte version [--short]` | Print the binary version. |

## Global flag

`--env KEY=VALUE` is repeatable and works on every command. It overrides secrets for that invocation
only, and wins over the process environment, `<workspace>/.env`, and `$ACOLYTE_HOME/.env` (in that
order). `acolyte config <agent> env [key]` reports the winning source per key and masks the value.

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

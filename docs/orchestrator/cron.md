# Orchestrator / cron service

**Intent.** A shared scheduler **any module registers jobs with**, and a single place —
`acolyte cron list` — to see every scheduled job regardless of who registered it.

## Job model (owner × kind)
- **`owner`** — who registered it (`user` | `memory-learn` | `heartbeat` | `comm` | …). Attribution +
  grouping only; does not affect execution.
- **`kind`** — what runs when it fires: **`prompt` | `shell` | `routine`**.

| kind | runs the model? | authored by | what runs | output |
| ---- | --------------- | ----------- | --------- | ------ |
| `prompt` | yes | user | `--prompt <text>` run through the agent | delivered to the user |
| `shell`  | no  | user | `--shell <path>` run with **`$SHELL`** (never `sh`) | routed (mail/notify) |
| `routine`| yes | module | `--routine <name>` + args (a registered routine) | internal / by routine |

- **`$SHELL`** — shell jobs run the script with the user's `$SHELL` (e.g. zsh), never hardcoded `sh`.
- **Target** — `--agent-name <name>` **xor** `--workspace-folder <path>` (if neither, use
  `$ACOLYTE_WORKSPACE_PATH`, then infer from a valid pwd); optional `--session-id <id>` (default =
  latest session).
- **Actions are mutually exclusive**: `--prompt` | `--shell` | `--routine`.

## Routines (`routine` kind)
Named, headless behaviors registered by modules (`dream`, `heartbeat`) with an argument schema. See
the [routines catalog](../README.md#routines) and `acolyte cron routine-list`.

## CLI
```bash
acolyte cron --title <t> --datetime <cron> (--prompt <text> | --shell <path> | --routine <name>) [--agent-name <n> | --workspace-folder <p>] [--session-id <id>]
acolyte cron list              # markdown table: title | datetime | prompt | shell | agent-name | session-id
acolyte cron view <title>      # full JSON of one job
acolyte cron routine-list      # JSON catalog of routines + args
acolyte cron rm <title>        # remove a job
```

`acolyte cron list` shows every job with its `owner`/`kind`, so capability-owned jobs (dream/heartbeat)
are visible alongside user tasks. Routine jobs show `routine:<name>` in the prompt column.

## Data model
`orch_cron_jobs(id, title, owner, kind, spec, prompt, shell_path, routine, routine_args,
agent_target, session_id, timezone, enabled, last_run_at, next_run_at, created_at, meta)`.

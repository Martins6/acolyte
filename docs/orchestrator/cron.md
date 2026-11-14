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
| `shell`  | no  | user | `--shell <path>` run with **`$SHELL`** (never `sh`) | routed (`inbox` / `notify-user` / `log`; default success `inbox`, failure `notify-user`) |
| `routine`| yes | module | `--routine <name>` + args (a registered routine) | internal / by routine |

- **`$SHELL`** — shell jobs run the script with the user's `$SHELL` (e.g. zsh), never hardcoded `sh`.
  Installed services bake `$SHELL` from the installing shell into the unit (see
  [autostart](autostart.md)).
- **Target** — `--agent-name <name>` **xor** `--workspace-folder <path>` (if neither, use
  `$ACOLYTE_WORKSPACE_PATH`, then infer from a valid pwd); optional `--session-id <id>` targets that
  session for `prompt`/`shell` jobs. Routine jobs reject `--session-id` and always run in
  `cron:<job ID>`.
- **Actions are mutually exclusive**: `--prompt` | `--shell` | `--routine`.

## Routines (`routine` kind)
Named, headless behaviors registered by modules (`dream`, `heartbeat`) with an argument schema. See
the [routines catalog](../README.md#routines) and `acolyte cron routine-list`.

- **`dream`** — reviews recent sessions and conservatively writes persona/skill memory. Direct write by
  default; `--dry-run` makes the prompt proposal-only and audits any changed paths in `mem_dreams`.
  Both phases run under a session-scoped permission ruleset: analysis is read-only; the writer may
  edit only `.acolyte/*.md` (not `HEARTBEAT.md`) and `.agents/skills/<id>/SKILL.md`.
- **`heartbeat`** — prompt-guided: reads `HEARTBEAT.md` + the latest non-cron session and instructs the
  agent to reply `NOTIFY: <message>` only when something is genuinely interesting; otherwise
  `NO_ACTION`. Its turn is fully read-only; the routine delivers the notification itself.

Routine turns apply their rules with `PATCH /api/session/{id}` before the turn and restore the
previous session rules afterwards. OpenCode evaluates session rules after the agent's rules
(last match wins), so this is an enforced tool boundary rather than prompt guidance.

Routine output returns empty so cron does not double-notify; heartbeat delivery is performed by the
routine through the communication sink. Routine jobs default both success and failure routing to
`log` (internal); an explicit `--on-success`/`--on-failure` can still choose `inbox` or
`notify-user`. Routine jobs always run in `cron:<job ID>` and reject `--session-id`, even if a
stored row carries one.

## CLI
```bash
acolyte cron --title <t> --datetime <cron> (--prompt <text> | --shell <path> | --routine <name>) \
  [--routine-args '<json>'] [--dry-run] [--agent-name <n> | --workspace-folder <p>] [--session-id <id>] \
  [--timezone <IANA>] [--on-success inbox|notify-user|log] [--on-failure inbox|notify-user|log]
acolyte cron list              # markdown table: title | datetime | agent | owner | kind | prompt | shell | session
acolyte cron view <title>      # full JSON of one job (target-aware when titles collide)
acolyte cron routine-list      # JSON catalog of routines + args
acolyte cron rm <title>        # remove a job (target-aware when titles collide)
acolyte cron tick              # run one due-job pass (testing/debugging)
```

`acolyte cron list` shows every job with its `owner`/`kind`, so capability-owned jobs (dream/heartbeat)
are visible alongside user tasks. Routine jobs show `routine:<name>` in the prompt column; prompt jobs
show `prompt:<text>`.

## Delivery

Cron output is routed through the target agent's Communication adapter via the durable outbox:
`notify-user` queues a user-only message, and `inbox` runs a synthetic turn and queues the reply.
Turns always run and persist even when no channel can deliver; delivery failures are recorded in
`comm_tg_outbox`/`orch_logs`.

If the outbox enqueue itself fails, the final text/chat/session is persisted in
`orch_cron_routes` before the job run is recorded. The cron tick retries those intents with bounded
backoff and idempotent envelope IDs, so a transient store error does not lose the output.

## Data model
`orch_cron_jobs(id, title, owner, kind, spec, prompt, shell_path, routine, routine_args,
agent_target, session_id, timezone, enabled, last_run_at, next_run_at, created_at, meta)`.

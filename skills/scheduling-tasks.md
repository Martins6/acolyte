# Scheduling Tasks Skill

## Overview

Acolyte runs a shared cron service for every registered agent. A job is one of:

- **`shell`** — run a script with the agent's `$SHELL` in the agent workspace.
- **`prompt`** — run a prompt through the agent's OpenCode session.
- **`routine`** — run a registered routine (`dream`, `heartbeat`, …).

Jobs are owner-tagged, stored in the agent's `.acolyte.sqlite.db`, and visible through
`acolyte cron list`.

## When to Use This Skill

Use scheduled jobs for:

- Daily reports (weather, news digests, system health)
- Periodic backups or cleanup tasks
- Scheduled notifications or reminders
- Recurring agent prompts or routine maintenance

One-time “remind me in 30 minutes” scheduling is not supported by the current cron parser; use
`acolyte inbox` for an immediate event, or ask the user to choose a recurring schedule.

## Commands

```bash
# Shell job: --shell takes a script path, not an inline command.
acolyte cron --title "disk-check" \
  --datetime "0 */4 * * *" \
  --shell scripts/disk-check.sh \
  --agent-name valet \
  --on-success notify-user --on-failure notify-user

# Prompt job: runs through the agent.
acolyte cron --title "morning-brief" \
  --datetime "0 9 * * *" \
  --prompt "Summarize today's calendar and inbox." \
  --agent-name valet

# Routine job.
acolyte cron --title "heartbeat" \
  --datetime "@every 60m" \
  --routine heartbeat --routine-args '{"interval":60}' \
  --agent-name valet

# Inspect/manage.
acolyte cron list
acolyte cron view morning-brief
acolyte cron rm morning-brief
acolyte cron routine-list
acolyte cron tick                 # drain due jobs now (testing/debugging)
```

## Schedule Expressions

Acolyte uses standard cron expressions plus descriptors:

- `0 9 * * *` — daily at 09:00
- `0 * * * *` — every hour
- `*/15 * * * *` — every 15 minutes
- `0 9 * * 1-5` — weekdays at 09:00
- `@every 1h` — every hour
- `@every 72h` — every three days

Set the timezone per job with `--timezone America/Sao_Paulo`. If omitted, the job uses the
agent's `schedule.timezone` setting (seeded by `acolyte new`), then the process local timezone.

## Flags

| Flag | Description |
| ---- | ----------- |
| `--title` | Unique job title (per agent). |
| `--datetime` | Cron expression or `@every` descriptor. |
| `--shell <path>` | Script path, resolved relative to the agent workspace. |
| `--prompt <text>` | Prompt for a prompt job. |
| `--routine <name>` | Registered routine name. |
| `--routine-args '<json>'` | Validated routine arguments. |
| `--agent-name` / `--workspace-folder` | Target agent (mutually exclusive; defaults to `$ACOLYTE_WORKSPACE_PATH`/pwd). |
| `--session-id` | Optional OpenCode session (must belong to the target agent). |
| `--timezone` | IANA timezone. |
| `--on-success` | `inbox`, `notify-user`, or `log` (default: shell `inbox`, others `notify-user`). |
| `--on-failure` | `inbox`, `notify-user`, or `log` (default: `notify-user`). |

## Output Handling

- **`inbox`** — runs a synthetic turn in the agent session; the reply is delivered through the
  agent's channel. This is the default for successful shell jobs.
- **`notify-user`** — queues a user-only message; never runs the agent.
- **`log`** — writes the output to `orch_logs` only.

Cron output is delivered by the agent's Communication adapter through a durable outbox. If no
channel is available, the turn still runs and persists; delivery is retried or logged.

## User Preference Questions

When setting up a job, ask:

1. **Timing** — “How often should this run?” (cron expression or `@every`).
2. **Target** — “Which agent/workspace should own this job?”
3. **Output** — “Should success go to the agent inbox, notify you directly, or just log?”
4. **Script path** — for shell jobs, confirm the script exists in the workspace and is executable.
5. **Verification** — show the command/job before creating it and offer `acolyte cron tick` as a
   manual test.

## Examples

### Daily Weather Report

```bash
acolyte cron --title "weather" --datetime "0 7 * * *" \
  --prompt "Fetch the weather for my configured city and summarize it." \
  --agent-name valet --on-success notify-user
```

### Disk Space Monitoring

Create `scripts/disk-check.sh` in the workspace:

```bash
#!/bin/sh
df -h | awk '$5+0 > 90 {print "Disk at " $5}'
```

Then:

```bash
acolyte cron --title "disk-check" --datetime "0 */4 * * *" \
  --shell scripts/disk-check.sh --agent-name valet \
  --on-success notify-user --on-failure notify-user
```

### Backup Script

```bash
acolyte cron --title "backup" --datetime "0 0 * * *" \
  --shell scripts/backup.sh --agent-name valet \
  --on-success log --on-failure notify-user
```

## Important Notes

- The Acolyte daemon must be running for jobs to execute.
- Shell jobs require a script file; the body is not passed inline.
- Use absolute paths or workspace-relative script paths.
- Test scripts manually before scheduling.
- Job output and errors are visible in `orch_logs` and through
  `acolyte inspect --session-id <id>`.
- Runtime capability edits are available through `acolyte config <agent> get|set|edit`; `acolyte new`
  preserves existing jobs/settings on adoption.

## Skill Usage

When a user mentions “schedule something”, “set up a cron job”, “run this periodically”, or “set up
a task to run automatically”:

1. Understand the task and timing.
2. Ask the output/target preferences.
3. Create the job with `acolyte cron`.
4. Test with `acolyte cron tick` when practical.
5. Confirm the job with `acolyte cron list`/`view`.

# Logging

The daemon logs to standard output/error.

- **systemd:** those streams go to journald. `journalctl --user -u acolyte -f` for a user unit, or
  `journalctl -u acolyte -f` for a system unit. Rotation is handled by journald.
- **launchd:** `acolyte install-service` passes `--log-file $ACOLYTE_HOME/logs/daemon.log`, which
  rotates at 10 MiB and keeps three backups; crash output is written to `daemon.err.log` in the same
  directory.
- **No service manager:** `acolyte start --log-file <path>` uses the same rotating writer.

See [`orchestrator/autostart.md`](orchestrator/autostart.md) for the full service setup and the
environment baked into each unit.

## Per-agent audit events

Cron runs, delivery failures, and no-channel inbox replies are stored in the `orch_logs` table in
`<workspace>/.acolyte.sqlite.db`. Turn activity adds one row per
`session.tool.called`/`session.tool.success`/`session.tool.failed` event (source
`opencode:<agent>`), carrying only event, session, assistant-message, and tool-call IDs — never tool
input/results, prompts, or text deltas.

`acolyte inspect --session-id <id>` prints the conversation plus routing history for one session,
which is usually easier than reading the table directly.

# Orchestrator / daemon

**Intent.** `acolyte start` runs one daemon for every registered agent. The daemon owns the shared
OpenCode client, the refcounted per-agent store pool, the cron service, and one Communication
adapter per configured agent.

## Lifecycle

1. Ensure the OpenCode background service is reachable (`opencode service`).
2. Open the registry and the store pool.
3. Start the Communication daemon:
   - refresh the registry every 30s,
   - resolve each enabled agent's channels, allowed users, and token,
   - start/stop/restart adapters when the workspace or config changes,
   - reject duplicate token fingerprints (one poller per bot token).
4. Start cron with the same store pool and the daemon as its sink.
5. Shutdown order: cron → daemon → store pool → registry.

## Turn routing

Inbound Telegram updates become Envelopes. The daemon resolves the agent from the adapter, maps
`telegram:<chat_id>` to an OpenCode session in `orch_sessions`, runs a real user turn, persists the
session, and sends the reply through the agent's adapter. If an adapter is restarting or disabled,
the reply is still queued in that agent's durable outbox for later delivery.

Cron prompt/routine jobs and `acolyte inbox` admit **synthetic** events. Turns always run and
persist, even when no channel can deliver the reply; delivery is a separate, durable outbox step.
`acolyte notify-user` is user-only and therefore requires a registered chat.

`acolyte run` is the headless, channel-free path: it runs a real user turn, maps the session to
`cli:<agent>`, and never starts an adapter or queues a delivery. `--continue` reuses that CLI
session; the reply is printed directly (plain or `--json`).

## Sessions and inspection

OpenCode owns the conversation history. Acolyte stores only routing/delivery metadata plus the
OpenCode session/message IDs needed to join the two.

- `acolyte inspect --session-id <id>` reads the session from OpenCode and the matching
  `comm_tg_inbox`/`comm_tg_outbox` rows, then prints a timestamp-ordered JSON event stream with
  message-ID links.
- `acolyte attach` runs `opencode attach <service-url> --dir <workspace> [--session <id>]` with the
  service credentials in the environment. Running `opencode` directly starts a separate server and
  will not see Acolyte's sessions.

## Database locations

| Store | Location |
| ----- | -------- |
| Per-agent Acolyte state | `<workspace>/.acolyte.sqlite.db` |
| Registry | `$ACOLYTE_HOME/registry.sqlite.db` |
| OpenCode history | `~/.local/share/opencode/opencode.db` (or `OPENCODE_DB`) |

## Runtime config

Capabilities are baked by `acolyte new` and can be changed in place with
`acolyte config <agent> get|set|edit`: `description`, `engine.model`, `engine.agent`,
`communication.channels`, `communication.telegram.allowed_users`, `schedule.timezone`,
`schedule.heartbeat_minutes`, and `memory-load` / `memory-access` / `memory-learn` /
`memory-export`. Every `set` validates before writing, supports `--dry-run`, and updates the same
artifacts `acolyte new` baked. See [`config.md`](config.md). New modes/channels are still deferred;
`acolyte new` preserves existing settings on adoption.

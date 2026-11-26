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

## Service restarts

The OpenCode background service can restart independently of the daemon (for example while the
OpenCode CLI updates). While it drains and boots it answers `503` with a `service_stopping` or
`service_starting` code and `Retry-After`; the shared client waits and replays those calls. After a
transport failure it re-discovers the service URL and password, so a restart on a new port does not
strand the daemon. Transport failures are replayed only for idempotent calls (`GET`, permission
updates, `wait`, and agent/model switches), so a prompt can never be admitted twice.

## Turn routing

Inbound Telegram updates become Envelopes. The daemon resolves the agent from the adapter, maps
`telegram:<chat_id>` to an OpenCode session in `orch_sessions`, runs a real user turn, persists the
session, and sends the reply through the agent's adapter. If an adapter is restarting or disabled,
the reply is still queued in that agent's durable outbox for later delivery.

Cron prompt/routine jobs and `acolyte inbox` admit **synthetic** events. Turns always run and
persist, even when no channel can deliver the reply; delivery is a separate, durable outbox step.
`acolyte notify-user` is user-only and therefore requires a registered chat.

`/new` is handled before the model: inside the per-thread lock the router deletes the
`telegram:<chat_id>` → session mapping in `orch_sessions` and replies `New session started.` with no
model turn, so the next message starts a fresh OpenCode session.

`acolyte run` is the headless, channel-free path: it runs a real user turn, maps the session to
`cli:<agent>`, and never starts an adapter or queues a delivery. `--continue` reuses that CLI
session; the reply is printed directly (plain or `--json`).

## Turn event activity

Both turn paths subscribe to the OpenCode event stream for the duration of a turn. A best-effort
recorder writes one `orch_logs` row per `session.tool.called`/`session.tool.success`/
`session.tool.failed` event: `info` for called/succeeded, `warn` for failed, with source
`opencode:<agent>`. The meta JSON only carries the event, session, assistant-message, and
tool-call IDs; tool input/results, prompts, and text/reasoning deltas are never stored. Trace export
reads `orch_logs`, so tool activity is available to memory/learning feeds. Recording failures are
ignored and never fail or stall a turn.

## Headless interaction settlement

Turn activity recording is separate from the headless invariant. Every turn pre-sweeps pending
OpenCode forms/permissions for the session and its descendants, polls every second while it runs, and
sweeps once more after collection. Question forms are cancelled with the non-interactive question
message, other forms get a generic headless message, and permission requests are rejected. A turn
that cannot settle a pending interaction fails with `ErrInteractionUnsettled` instead of waiting.
Settlement is independent of the best-effort event stream: a missed event, a stale form from a
crashed turn, or a dropped stream cannot leave the turn blocked. Background subagents that outlive
root idle are settled by the next turn's pre-sweep. See
[`docs/opencode/v2-contract.md#headless-turn-invariant`](../opencode/v2-contract.md#headless-turn-invariant).

## Sessions and inspection

OpenCode owns the conversation history. Acolyte stores only routing/delivery metadata plus the
OpenCode session/message IDs needed to join the two.

- `acolyte inspect --session-id <id>` reads the session from OpenCode and the matching
  `comm_tg_inbox`/`comm_tg_outbox` rows, then prints a timestamp-ordered JSON event stream with
  message-ID links. Outbound media rows include the `media_path` attribution when available.
- `acolyte attach` runs `opencode attach <service-url> --dir <workspace> [--session <id>]` with the
  service credentials in the environment. Running `opencode` directly starts a separate server and
  will not see Acolyte's sessions. Attach is **debug-only and not concurrency-safe**: it does not take
  the per-session turn lock, so the headless settler can cancel a form or permission a human is
  answering. Do not attach while Acolyte is running a turn on that session.

## Database locations

| Store | Location |
| ----- | -------- |
| Per-agent Acolyte state | `<workspace>/.acolyte.sqlite.db` |
| Registry | `$ACOLYTE_HOME/registry.sqlite.db` |
| OpenCode history | `~/.local/share/opencode/opencode.db` (or `OPENCODE_DB`) |

## Runtime config

Capabilities are baked by `acolyte new` and can be changed in place with
`acolyte config <agent> get|set|edit` (channels, allowed users, schedule, engine, memory keys; see
[`config.md`](config.md)). `acolyte config <agent> env [KEY]` shows which secret layer wins
(`--env`, process env, `.env-workspace`, `.env-global`) without printing the value.
`acolyte new` still preserves existing settings/jobs when it adopts a
folder. New modes/channels remain deferred in [#113](https://github.com/Martins6/acolyte/issues/113).

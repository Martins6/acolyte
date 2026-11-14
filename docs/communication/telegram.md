# Communication / Telegram

**Module:** communication · **Channel:** `telegram`

## Intent

Telegram adapter: parse Telegram updates into Envelopes (inbound) and deliver Envelopes as
Telegram messages (outbound). One adapter runs per agent, bound to that agent's bot token and
workspace.

## Inbound

- Receives Telegram updates (messages, photos, documents, audio, voice, video).
- Checks the sender against `telegram.allowed_users` (numeric ID or `@username`, case-insensitive).
  Unauthorized senders receive a short refusal and are not admitted.
- Records `tg_update_id` for dedup and stores the inbound envelope as a pending replay payload.
- Hands each Envelope to the Orchestrator's inbound sink. Only a successful admission (or an
  already-linked OpenCode message) clears the payload and marks the row processed. Failed/aborted
  admissions stay pending and are replayed on start and every 30s.
- Resolves `chat_id` in `comm_tg_chats` and records the last-seen time.
- **Media:** downloads the file into `<workspace>/media/inbound/<type>/`, reusing the existing path when
  the Telegram `file_id` was seen before. If a download fails, the pending row keeps the raw update
  and media is retried by replay. The Envelope carries the absolute path and a prompt of the form
  `File located at: <path>\n\nUser message: <caption>`.
- Hands each Envelope to the Orchestrator's inbound sink.

## Outbound

- `Send(Envelope)` resolves the chat from the Envelope thread/`to` and enqueues a row in
  `comm_tg_outbox`.
- The adapter attempts delivery immediately and again from a 5s retry loop.
- Outbound media is one image per message in v0: `notify-user --media <path>` validates and copies
  the file under `media/outbound/images/`, then sends it with `sendPhoto` and an optional caption.
  JPEG, PNG, and WebP are accepted up to 10 MiB; captions are capped at 1024 characters.
- Long text is split by UTF-16 units while preserving rune boundaries (Telegram's limit is 4096
  UTF-16 units).
- Chunk progress is persisted, so a retry resumes after the last delivered chunk.
- Transient failures back off (5s, 15s, 1m, … up to 15m) for at most 5 attempts, then the row is
  marked `failed`. Successful/failed rows clear their text; only routing/delivery metadata remains.
- Bot tokens are redacted from logged/persisted errors.

### Delivery semantics

v0 keeps image delivery and the turn's reply as **two messages**:

- `send_image` / `acolyte notify-user --media` queues the photo immediately when the tool or
  command runs, sending the explicit caption when one is provided.
- The agent's final assistant text is delivered as a separate message.

This is deliberately immediate and deterministic: it works from the CLI runner, routines, and any
harness that does not go through the router's reply path, and it does not depend on the turn
finishing. Merging the two would require suppressing the tool-side send and holding the image until
the turn ends.

To merge later, extend `TurnResult` with images extracted from `send_image` tool calls and send one
photo whose caption is the final reply. See `FUTURE-IDEAS.md` → Communication → *Merged image +
final text*.

## State (`.acolyte.sqlite.db`, prefix `comm_tg_`)

| Table | Holds |
| ----- | ----- |
| `comm_tg_chats` | chat_id, username, allowed, first/last seen |
| `comm_tg_inbox` | unique tg_update_id, chat_id, envelope_id, media_path, pending replay payload/reply, processed_at, attempts, OpenCode session/message IDs |
| `comm_tg_outbox` | envelope_id, chat_id, text/caption (pending only), media_path, reply_to, chunk progress, attempts, delivery, tg_message_id, source message |
| `comm_tg_media` | tg_file_id, file_path, type, chat_id, saved_at |
| `comm_tg_settings` | non-secret channel settings (`channels`, `telegram.allowed_users`) |

## Testing

`internal/comm/telegram/telegramtest` is an in-process Telegram Bot API
emulator. It serves `getMe`, `getUpdates`, `sendMessage`, `getFile` and file
downloads over a real HTTP listener and exposes helpers to queue updates,
inspect sent messages, register files and inject API failures. Tests point the
adapter at it with `telegram.Options.ServerURL`; the emulator is never used by
production code.

Run the hermetic suite with:

```bash
go test -race -count=1 -v -run EmulatedTelegram ./internal/comm/telegram/... ./internal/daemon/
```

The live E2E suite also starts the real daemon against the emulator and a real
OpenCode service (`e2e/telegram_test.go`, build tag `e2e`).

For a local Bot API server, set `ACOLYTE_TELEGRAM_API_URL`. Because the bot
token is sent there, plain `http` is accepted only for loopback hosts; use
`https` for anything remote.

## Config

Non-secret settings live in `comm_tg_settings`, seeded by `acolyte new`:

- `channels` — JSON array; defaults to `["telegram"]` when absent.
- `telegram.allowed_users` — JSON array of numeric IDs or `@usernames`.

The bot **token** is a secret read from the layered `.env` files:
`<workspace>/.env` overrides `$ACOLYTE_HOME/.env`; the process environment overrides both. Neither
file is ever committed or scaffolded. See `SPEC.md` §4.

Runtime config mutation is available without re-running `acolyte new`:

```bash
acolyte config valet set communication.channels '["telegram"]'
acolyte config valet set communication.telegram.allowed_users '@alice,@bob'
```

See `docs/orchestrator/config.md` for validation, `--dry-run`, and the full key list.

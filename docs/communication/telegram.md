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
- **Media:** downloads the file into `<workspace>/downloads/<type>/`, reusing the existing path when
  the Telegram `file_id` was seen before. The Envelope carries the absolute path and a prompt of the
  form `File located at: <path>\n\nUser message: <caption>`.
- Hands each Envelope to the Orchestrator's inbound sink.

## Outbound

- `Send(Envelope)` resolves the chat from the Envelope thread/`to` and enqueues a row in
  `comm_tg_outbox`.
- The adapter attempts delivery immediately and again from a 5s retry loop.
- Long text is split by UTF-16 units while preserving rune boundaries (Telegram's limit is 4096
  UTF-16 units).
- Chunk progress is persisted, so a retry resumes after the last delivered chunk.
- Transient failures back off (5s, 15s, 1m, … up to 15m) for at most 5 attempts, then the row is
  marked `failed`. Successful/failed rows clear their text; only routing/delivery metadata remains.
- Bot tokens are redacted from logged/persisted errors.

## State (`.acolyte.sqlite.db`, prefix `comm_tg_`)

| Table | Holds |
| ----- | ----- |
| `comm_tg_chats` | chat_id, username, allowed, first/last seen |
| `comm_tg_inbox` | unique tg_update_id, chat_id, envelope_id, media_path, pending replay payload, processed_at, attempts, OpenCode session/message IDs |
| `comm_tg_outbox` | envelope_id, chat_id, text (pending only), reply_to, chunk progress, attempts, delivery, tg_message_id, source message |
| `comm_tg_media` | tg_file_id, file_path, type, chat_id, saved_at |
| `comm_tg_settings` | non-secret channel settings (`channels`, `telegram.allowed_users`) |

## Config

Non-secret settings live in `comm_tg_settings`, seeded by `acolyte new`:

- `channels` — JSON array; defaults to `["telegram"]` when absent.
- `telegram.allowed_users` — JSON array of numeric IDs or `@usernames`.

The bot **token** is a secret read from the layered `.env` files:
`<workspace>/.env` overrides `$ACOLYTE_HOME/.env`; the process environment overrides both. Neither
file is ever committed or scaffolded. See `SPEC.md` §4.

Runtime config mutation (changing allowed users/channels without re-running `acolyte new`) is not
exposed yet; see `FUTURE-IDEAS.md`.

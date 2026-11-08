# Communication / Telegram

**Module:** communication · **Channel:** `telegram`

## Intent
Telegram adapter: parse Telegram updates into Envelopes (inbound) and deliver Envelopes as Telegram
messages (outbound). Reuses the Telegram plumbing already in Acolyte.

## Inbound
- Receives Telegram updates (messages, photos, …), parses them into `Envelope{channel:"telegram"}`.
- Resolves `chat_id` ↔ allowed user; records `tg_update_id` for dedup.
- **Media:** downloads the file into `<workspace>/downloads/<type>/`, then emits an Envelope whose
  `content.media` holds the resolved absolute path (the model reads files, not blobs).
- Hands each Envelope to the Orchestrator's inbound sink.

## Outbound
- `Send(Envelope)` posts the text/media to the target chat; records `tg_message_id` + delivery status
  for retries and reply-threading.

## State (`.acolyte.sqlite.db`, prefix `comm_tg_`)
| Table | Holds |
| ----- | ----- |
| `comm_tg_chats` | chat_id, username, allowed, resolved_at |
| `comm_tg_inbox` | tg_update_id, chat_id, envelope_id, received_at, media_path |
| `comm_tg_outbox` | envelope_id, chat_id, tg_message_id, delivery, sent_at |
| `comm_tg_media` | file_path, tg_file_id, type, chat_id, saved_at |

## Config
Non-secret settings (e.g. allowed users) are stored in `.acolyte.sqlite.db` under
`comm_tg_settings`. The bot **token** is a secret read from the layered `.env` files:
`<workspace>/.env` overrides `$ACOLYTE_HOME/.env`; the process environment overrides both. Neither
file is ever committed or scaffolded. See `SPEC.md` §4.

# Communication

**Intent.** Communication moves messages between the agent and the outside world: it parses inbound
channel payloads into **Envelopes** for the Orchestrator, and delivers outbound Envelopes to the
right channel. Telegram is the v0 channel.

## The contract (Communication ⇄ Orchestrator)

```go
type InboundSink func(Envelope)                 // Comm → Orch: parsed inbound message

type Communication interface {
    Name() string                                            // "telegram"
    Start(ctx context.Context, sink InboundSink) error       // begin delivering inbound
    Send(Envelope) error                                     // deliver outbound
    Stop() error
}
```

## Per-agent adapters

Each registered agent has its own adapter instance and its own bot token. The daemon reads the
token from `<workspace>/.env`, falling back to `$ACOLYTE_HOME/.env` and then the process
environment. **Two agents cannot share a token** (Telegram allows one long-poller per bot); the
daemon starts one adapter per token and warns about the rest.

An adapter is started only when:

- `communication.channels` contains `telegram` (missing setting defaults to `["telegram"]`),
- `telegram.allowed_users` is non-empty,
- a token is resolvable.

## What Communication owns

- Channel payload ↔ **Envelope** parsing (`SPEC.md` §7).
- **Its own state** in `<workspace>/.acolyte.sqlite.db` under the `comm_tg_*` tables: chat
  resolution, dedup, delivery status/message IDs, and media references.
- Durable inbound replay: an update is recorded as pending before the turn is admitted; the
  payload is cleared only after admission/linkage, and failed rows are retried.
- Durable outbound delivery: every reply is written to `comm_tg_outbox`; the adapter attempts it
  immediately and retries transient failures with bounded backoff.
- It does **not** store conversation content. Inbound rows keep routing metadata and OpenCode
  message IDs; outbound text is cleared once delivery reaches `sent` or `failed`. The OpenCode
  session remains the source of truth.

## Turns and delivery

Inbound messages are admitted through the router as real user turns. Cron, routines, and
`acolyte inbox` admit synthetic events. **Turns always run and persist, even when no channel can
deliver the reply**; a delivery failure is logged and communicated through the outbox/audit log.
`acolyte notify-user` is the exception: it is user-only and requires a registered chat.

## Services it uses from the Orchestrator

Envelope bus (in/out), the shared store pool, the cron service (for scheduled sends), and
`orch_logs`. See [`../orchestrator/README.md`](../orchestrator/README.md) and
[`../orchestrator/daemon.md`](../orchestrator/daemon.md).

## Channels

| Channel | Doc | Status |
| ------- | --- | ------ |
| Telegram | [`telegram.md`](telegram.md) | v0 |
| agent-mail / agent-im / agent-post | — | deferred (`FUTURE-IDEAS.md`) |

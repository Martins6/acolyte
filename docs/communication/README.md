# Communication

**Intent.** Communication moves messages between the agent and the outside world: it parses inbound
channel payloads into **Envelopes** for the Orchestrator, and delivers outbound Envelopes to the
right channel. The first (and, for v0, only) channel is **Telegram**.

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

## What Communication owns
- Channel payload ↔ **Envelope** parsing (§ `SPEC.md` §7).
- **Its own state** in `.acolyte.sqlite.db` under a namespaced schema (e.g. `comm_tg_*`): chat↔user
  mapping, delivery status, message IDs (dedup / reply-threading), media file index.
- It does **not** store conversation content — that lives in the OpenCode session.

## Services it uses from the Orchestrator
Envelope bus (in/out), the cron service (retries / scheduled sends), the store (namespaced tables),
and logging. See [`../orchestrator/README.md`](../orchestrator/README.md).

## Channels
| Channel | Doc | Status |
| ------- | --- | ------ |
| Telegram | [`telegram.md`](telegram.md) | v0 |
| agent-mail / agent-im / agent-post | — | deferred (`FUTURE-IDEAS.md`) |

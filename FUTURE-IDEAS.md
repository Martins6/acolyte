# Acolyte — Future Ideas

> Everything deferred or speculative lives here so `SPEC.md` stays focused on the current design.
> When an idea is promoted, move it into `SPEC.md` / `docs/` and delete it here.

## Communication
- **Agent-to-agent messaging** — internal channels alongside Telegram:
  - `agent-mail` (async, eventful), `agent-im` (immediate notifications),
    `agent-post` (social / internal-reddit style).
  - Reuse the Envelope (`channel`, `from/to.kind = "agent"`); only routing/discovery is new.
  - Internal CLI for agent-to-agent mail/notify.
- **Merged image + final text** — deliver `send_image` output as one photo whose caption is the
  turn's final reply, instead of today's two messages. Deferred: it would require capturing
  `send_image` tool calls from the session timeline into a future `TurnResult.Images` field,
  suppressing the tool-side send, and holding the image until the turn ends. v0 deliberately keeps
  immediate tool-side delivery plus a separate final message (deterministic, works outside the
  router reply path, no dependency on turn completion). See `docs/communication/telegram.md`.
- **More human channels** — Discord, Slack, WhatsApp, email, etc. (new `Communication` adapters).

## Memory
- **`memory-load` modes:** `compact` (distilled current-focus), `rolling` (summary of prior
  sessions), `role-sliced` (load by time/role).
- **`memory-access` modes:** `recall` (query DB / past sessions), `vector` (RAG retrieval),
  `graph` (knowledge-graph query).
- **`memory-learn` modes:** `post-session` (review after each session), `inline` (`acolyte learn`
  tool written mid-session), `plugin-hook` (OpenCode plugin on session events), `consolidate`
  (nightly merge + decay of stale skills, dedup, confidence scoring).
- **`memory-export` modes:** `portable-archive` (zip bundle), `remote-catalog` (HTTP skill catalog,
  see OpenCode skill catalogs).

## Capabilities
- **More runtime-editable keys** — `acolyte config <agent> get|set|edit` already mutates
  `description`, `engine.model/agent`, `communication.channels`,
  `communication.telegram.allowed_users`, `schedule.timezone`, `schedule.heartbeat_minutes`, and
  the four `memory-*` keys in place (SPEC §10.5, `docs/orchestrator/config.md`). Still deferred:
  modes/channels that do not exist in v0 yet (so nothing to switch *to*), editing an agent's
  prompt/persona through the CLI (define the agent in `opencode.jsonc` / `.opencode/agents/`
  first, then `set engine.agent`), and hot-reloading a running daemon's comm settings without
  its normal config refresh.

## Orchestrator
- **Status cache** — maintain an `agent_status` snapshot in the registry for fast `acolyte status`
  vs. fan-out reads.
- **Per-agent isolation hardening** — one daemon already serves all registered agents; harden
  failure containment, resource limits, and lifecycle supervision per agent.

## Identity
- **Folder-portable identity** — keep a tiny identity record in the folder so name/description
  travel with it (vs. registry-only today).

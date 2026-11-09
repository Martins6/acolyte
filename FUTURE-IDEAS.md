# Acolyte — Future Ideas

> Everything deferred or speculative lives here so `SPEC.md` stays focused on the current design.
> When an idea is promoted, move it into `SPEC.md` / `docs/` and delete it here.

## Communication
- **Agent-to-agent messaging** — internal channels alongside Telegram:
  - `agent-mail` (async, eventful), `agent-im` (immediate notifications),
    `agent-post` (social / internal-reddit style).
  - Reuse the Envelope (`channel`, `from/to.kind = "agent"`); only routing/discovery is new.
  - Internal CLI for agent-to-agent mail/notify.
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
- **Smooth runtime capability updates** — change an agent's capabilities in place (switch a memory
  mode, add/remove a channel, update Telegram allowed users, schedule, or engine) without re-running
  `acolyte new`. v0 bakes capabilities at creation, and `acolyte new` only adopts/preserves existing
  settings; there is currently **no CLI to mutate an agent's baked config**. Potential surface:
  `acolyte config <agent> set <key> <value>` / `acolyte config <agent> edit` with validation,
  diffing, and a dry-run mode. Promote to Phase 3/4 once the per-agent daemon config surface is
  stable.

## Orchestrator
- **`acolyte install-service`** — write the launchd plist / systemd unit for auto-start automatically.
- **Status cache** — maintain an `agent_status` snapshot in the registry for fast `acolyte status`
  vs. fan-out reads.
- **Per-agent isolation hardening** — one daemon already serves all registered agents; harden
  failure containment, resource limits, and lifecycle supervision per agent.

## Identity
- **Folder-portable identity** — keep a tiny identity record in the folder so name/description
  travel with it (vs. registry-only today).

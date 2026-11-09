# Acolyte — Agent Runtime Spec (v0)

> Living draft for discussion. Reflects the decisions in §15. Headline: **Acolyte is *powered by
> OpenCode*** — the engine is OpenCode and there is **no engine abstraction** (§5). **Memory is
> contract-free** and plugs straight into OpenCode features (§6). The one real code/data contract
> is **Communication ⇄ Orchestrator** (§8), backed by a per-module-namespaced store that holds only
> what OpenCode sessions don't (§9). There is **no `.acolyte.jsonc`** — capabilities live in
> `opencode.jsonc` + the `acolyte` CLI (§4). Deferred ideas live in [`FUTURE-IDEAS.md`](FUTURE-IDEAS.md).

---

## 1. Vision

Acolyte is a **runtime for a folder of agents.** One agent = one self-contained workspace folder:
identity, personality, learned skills, state, and logs all inside it. Pick up the folder and you
have the whole agent.

Each agent folder **is an OpenCode project** with an Acolyte overlay. OpenCode runs the model +
tools; Acolyte layers high-level modules on top and wires them together. **One `acolyte` daemon
serves all registered agents**; per-agent isolation hardening is deferred (`FUTURE-IDEAS.md`).

| Module            | Responsibility                                                    | Pluggable? |
| ----------------- | ----------------------------------------------------------------- | ---------- |
| **Engine**        | OpenCode — model + tools. Contract = OpenCode V2 (§5).             | **Fixed** (no abstraction) |
| **Memory**        | `memory-load` / `memory-access` / `memory-learn` / `memory-export`. Contract-free; plugs into OpenCode (§6). | **Yes** (per sub-module, incl. `null`) |
| **Communication** | Channel adapters in/out (Telegram now) (§8).                       | **Yes** |
| **Orchestrator**  | `acolyte` daemon/CLI — turn loop, envelope bus, **cron service**, DB, registry. | **Fixed** |

**Capabilities are baked at `acolyte new`** (§10). Runtime capability-switching is deferred
(`FUTURE-IDEAS.md`). Agent-to-agent messaging is deferred (`FUTURE-IDEAS.md`).

---

## 2. Core concepts

- **Agent** — a workspace folder + registered identity. The unit of everything.
- **Workspace** — the absolute folder path. **The agent's true ID.**
- **Name** — a unique human handle, held in the registry. Decoupled from the OpenCode agent ID.
- **Capability / mode** — a documented way to wire a sub-module (e.g. `memory-learn: simple-dreaming`).
  Documented in `docs/`, applied at `acolyte new` as concrete OpenCode + cron artifacts.
- **Envelope** — the universal message type between Communication and Orchestrator (§7).
- **Turn** — one inbound envelope in → zero-or-more outbound envelopes out.
- **Persona** — the `.acolyte/{CONFIG,SOUL,TOOLS,USER,HEARTBEAT}.md` bundle (who the agent is).
- **Skill** — a learned capability under `.agents/skills/` (an OpenCode skill).
- **Cron service** — an Orchestrator service any module registers jobs with; all visible via
  `acolyte cron list` (§10).
- **Registry** — global name ↔ workspace directory (`$ACOLYTE_HOME/registry.sqlite.db`).

---

## 3. The Workspace Contract (C1 — layout)

The folder *is* the contract. It is an **OpenCode project** (left) + an **Acolyte overlay**
(`.acolyte*`). Paths are fixed by convention.

```
<workspace>/                          # the agent's true ID (absolute path)
├── AGENTS.md                         # OPENCODE hub: persistent guidance + persona index
├── opencode.jsonc                    # OPENCODE V2 config = capability wiring (§4)
├── .acolyte.sqlite.db                # ACOLYTE store: non-session state, per-module schemas (§9)
│
├── .acolyte/                         # PERSONA (memory-load/access; OpenClaw-style)
│   ├── CONFIG.md                     #   meta: "you are an Acolyte agent on OpenCode" + docs link
│   ├── SOUL.md                       #   core behaviour / unbreakable traits
│   ├── TOOLS.md                      #   tool behaviour + workflows (skills add more)
│   ├── USER.md                       #   user profile memory
│   └── HEARTBEAT.md                  #   "what to look for and do every N minutes"
│
├── .agents/skills/                   # OPENCODE-native skill source (learned skills)
│   └── <skill-id>/SKILL.md           #   one dir per skill (+ scripts/references)
│
├── .opencode/agents/                 # OPENCODE agent definitions (optional overrides)
└── downloads/ …                      # media, transcripts, artifacts
```

**Required for validity** (checked by `acolyte new`, §10): `AGENTS.md`, `opencode.jsonc`,
`.acolyte/` with `CONFIG.md` + `SOUL.md`, `.agents/skills/` (may be empty), `.acolyte.sqlite.db`.

> **No `.acolyte.jsonc`.** Identity lives in the registry; capability wiring lives in
> `opencode.jsonc` + cron; persona lives in `.acolyte/`. See §4.

---

## 4. Config & capabilities — where everything lives (no `.acolyte.jsonc`)

There is **no Acolyte config file.** All necessary info is visible to the agent via **`opencode.jsonc`**
and the **`acolyte` CLI** (plus `CONFIG.md` for orientation).

| Info | Lives in | Agent sees it via |
| ---- | -------- | ----------------- |
| Engine + capability wiring (instructions, skills, tools, model, agent) | `opencode.jsonc` | reads `opencode.jsonc` |
| Scheduled capabilities (heartbeat, dream/learn runs, tasks) | cron jobs | `acolyte cron list` |
| Identity (name, description), comms settings (allowed users) | registry / agent DB | `acolyte status` |
| Persona / self-description | `.acolyte/*.md` | loaded into context (memory-load) |
| Secrets (bot token, API keys) | `$ACOLYTE_HOME/.env` (global) + `<workspace>/.env` (per-agent) | never in-repo; loaded by the daemon/CLI |

**Secrets precedence [DECISION]:** existing process env > `<workspace>/.env` > `$ACOLYTE_HOME/.env`.
Both files are gitignored and never scaffolded by `acolyte new`. Workspace values are scoped to that
agent (returned as a per-agent map, not applied with a process-global `os.Setenv`), so one daemon can
serve many agents without leaking secrets between them. Provider credentials may still live in
OpenCode's own auth store.

**`opencode.jsonc` is shared + live-editable** (§5): Acolyte seeds it at `acolyte new`; any module may
extend it (add tools/skills); **the agent may edit it on the fly** (OpenCode reloads on save). Rule:
targeted edits that preserve unrelated keys.

**Capabilities are baked, not runtime-selected.** `acolyte new` takes a `--config` capability selector
(see `docs/README.md`) and realizes
them as concrete artifacts (opencode.jsonc entries, skills, cron jobs, persona). Changing a
capability later = re-run a capability step or edit artifacts — smooth in-place capability updates
are **deferred** (`FUTURE-IDEAS.md`).

**Identity note / tradeoff:** because there's no per-folder config, `name`/`description` live in the
**registry**, not the folder. Moving a folder to a new machine = re-register (`acolyte new` on the
existing folder adopts it). This keeps the folder lean but means identity isn't fully portable with
the folder alone. (Closed — see §15 #13.)

---

## 5. The Engine = OpenCode (the V2 contract)

No engine interface/adapter. Acolyte builds directly on OpenCode V2 primitives
(source: `opencode.ai/v2/docs`).

| Primitive       | OpenCode contract                                                                 | Acolyte use |
| --------------- | ---------------------------------------------------------------------------------- | ----------- |
| **Project**     | `opencode.json(c)` in the workspace (or `.opencode/`); discovered up the tree.       | The workspace is an OpenCode project. |
| **Config**      | JSONC; `model`, `default_agent`, `agents`, `instructions`, `skills`, `permissions`, `providers`, `mcp`, `plugins`. **Reloaded on save.** | `opencode.jsonc` = capability wiring (§4). |
| **Agents**      | `.opencode/agents/<name>.md` or `agents{}`. `default_agent` selects primary.        | The acolyte agent maps to an OpenCode agent. |
| **Skills**      | `.agents/skills/` (project source); dir with `SKILL.md` (frontmatter `name`, `description`, `metadata.opencode/autoinvoke`) or flat `.md`. ID = path-derived. Loaded on demand via the `skill` tool. Extra sources via `skills: []`. | Learned skills in `.agents/skills/` (memory-access). |
| **Instructions**| `AGENTS.md` (global + workspace-up) plus `instructions: [files, globs, URLs]`. Combined. Live-edited. | Persona (`.acolyte/*.md`) surfaced here (memory-load). |
| **Sessions**    | Client–server service owns sessions; driven via API / `@opencode/client` / SDK / `opencode run`. **Sessions keep the full conversation + tool activity.** | Orchestrator maps threads → OpenCode session IDs (§9). |
| **Tools**       | Built-in + `skill` + `subagent` + MCP + plugin tools.                               | Acolyte adds CLI tools (`notify-user`, `inbox`, `cron`, `inspect`, `attach`). |
| **Plugins**     | Hooks/transforms/tools via `plugins` + JS/Effect.                                   | Candidate host for `memory-learn` / `memory-export`. |

**How a turn runs:** the Orchestrator drives one turn through the **OpenCode V2 API**, supplying an
inbound prompt and reading output + tool activity. Acolyte does not reimplement the reasoning/tool
loop.

### 5.1 Integration transport — the V2 API, not the CLI **[DECISION]**

The Orchestrator integrates against the **OpenCode V2 HTTP API** (the background service), **not** by
shelling out to `opencode run`. The CLI stays only for (a) the agent's own tool calls and (b) human
debugging.

**Why the API (external events are first-class — no faking the user):**
- **`synthetic` inputs + the session inbox.** A background/external event — a cron "you've got mail"
  notification, a heartbeat, an inbound message, a learning pass — is admitted to the session
  **inbox** as a `synthetic` input (`type: "synthetic"`) with a `description` ("Notification: new
  mail") and `metadata` (structured source). It is delivered via `queue` (at the next idle) or `steer`
  (interrupt now). Real user messages use `session.prompt`. So external events are perceived as
  *external*, not as the user speaking.
- **Sessions are first-class:** create / list / resume; map thread→session; `memory-learn` iterates
  sessions (`GET /api/session`); `acolyte status` reads stats (`GET /api/session/stats`).
- **`event.subscribe()` streams** tool activity + text deltas → stream to Telegram, feed learning,
  live status. (CLI returns only final stdout, parsed — brittle.)
- **Long-running daemon ↔ long-running service** (matches autostart). No spawn-per-turn overhead or
  5-minute timeout.

**Key endpoints:** `POST /api/session`, `GET /api/session`, `POST /api/session/{id}/synthetic`,
`POST /api/session/{id}/inbox` (+ `inbox/{id}/steer` | `inbox/{id}/queue`),
`POST /api/session/{id}/prompt` (real user turns), `GET /api/session/stats`,
`GET /api/agent`, `event.subscribe()`.

**Caveats:**
- The orchestrator must discover/start the OpenCode service (`opencode serve --service`); internal
  `opencode/server.go` already does this.
- The V2 API is OpenCode's "only intentional breaking change" — pin it and **generate a Go client
  from `/openapi.json`**, behind one thin internal client so breakage is contained. (Not an engine
  abstraction — still OpenCode-only; just transport hygiene.)
- Existing `internal/opencode/client.go` uses V1-style paths (`/session/{id}/message`); retarget to
  V2 (`/api/...`) with `synthetic` + `inbox` for external events and `prompt` for real user turns, and
  retire the CLI `runner.go` turn path.

### 5.2 Spike validation — `synthetic`/`inbox` reach the model ✅ (2026-11-05)

**Validated live against OpenCode v2.2.5** (throwaway `spike/`). A `synthetic` external event
**reaches the model and triggers an agent reaction** — it is *not* filtered from the prompt.

**Evidence:** injecting a synthetic carrying a canary produced an assistant turn whose **reasoning
verbatim quoted the synthetic content + canary** (the model can't quote text it never saw). The model
treated it as an *external* event, not a user message — exactly the goal. It ran as its own turn
without any user prompt. (A `--trusted` marker is **unnecessary**: trust is structural — only the
orchestrator can inject synthetic events — and the `synthetic` type + `description` already mark it a
system event.)

**Confirmed request shapes** (the basis for the `OpenCodeClient`):

| Call | Body → Response |
| ---- | --------------- |
| `POST /api/session` | `{"location":{"directory":"<ws>"}}` → `{"data":{"id":"ses_…"}}` |
| `POST /api/session/{id}/synthetic` | `{"text"*, "description", "metadata", "delivery":"steer\|queue", "resume"}` → synthetic message |
| `POST /api/session/{id}/prompt` | `{"text"*}` → admitted **user** message (**async**) |
| read reply | `GET /api/session/{id}/message` (assistant reply lands here) |
| auth | HTTP Basic `opencode:<password>` (password in `~/.config/opencode/service.json`) |

- `prompt` is **async** — it returns the admitted user message; the assistant reply is read from the
  session messages (or streamed via `event.subscribe()`).
- `POST /api/session/{id}/inbox` (+ `inbox/{id}/steer` \| `inbox/{id}/queue`) is the finer-grained
  admission path; `synthetic` is the convenient one-shot that also admits + delivers.

---

## 6. The Memory module (contract-free; plugs into OpenCode)

Memory is **not a Go interface contract** — it is a set of **sub-modules**, each a documented *mode*
that plugs directly into OpenCode features (instructions, skills, tools, plugins) and, where needed,
the Orchestrator's cron service. Different modes (including `null`) are chosen per agent. There is no
fixed API a mode must implement; a mode is defined by its **behavior and the OpenCode/cron artifacts
it produces**, documented in `docs/memory/…`.

| Sub-component | Purpose | v0 mode | `args` |
| ------------- | ------- | ------- | ------ |
| **`memory-load`**   | Surface initial memory into context.        | `default` | — |
| **`memory-access`** | Read/query memory during a session.         | `null` (= native **both**) | — |
| **`memory-learn`**  | Improve memory from sessions.               | `simple-dreaming` | `{days: 3}` |
| **`memory-export`** | Export memory for portability/sharing.      | `opencode-export` | — |

**Value semantics:** `null` = native/built-in baseline (no extra module). `{ "mode": X, "args": {…} }`
= apply mode X. `memory-access: null` already means **both** (instructions-in-context + `skill` tool).

### 6.1 `memory-load` — initial memory
- **`default`:** `AGENTS.md` = hub/index; persona (`.acolyte/*.md`, incl. `CONFIG.md`) loaded via
  `instructions: [".acolyte/*.md"]` (always-in-context); skills auto-discovered from `.agents/skills/`.
- *Other modes (planned):* `compact`, `rolling`, `role-sliced`. See `docs/memory/memory-load/`.

### 6.2 `memory-access` — runtime memory → **both**
- **`null` (native) = both:** instructions-in-context (`AGENTS.md` + `.acolyte/*.md`) **and** tools
  (built-in `skill` tool loads a skill body by ID).
- *Extra modes (planned):* `recall` (query DB/past sessions), `vector` (RAG), `graph`. See `docs/memory/memory-access/`.

### 6.3 `memory-learn` — learning loop
- **`simple-dreaming` (v0):** a **headless `acolyte cron` job** (`kind: routine`) that every
  `args.days` days reviews the **last `args.days` days** of sessions and decides whether to distill
  anything into the **persona files** (`.acolyte/*.md`) or a **skill** (`.agents/skills/<id>/SKILL.md`).
  **Conservative:** when in doubt, don't write — only when it's *super extra clear*; and be **super
  compact** when there are many sessions. Rides the scheduler shown by `acolyte cron list`.
- *Other modes (planned):* `post-session`, `inline` (`acolyte learn` tool), `plugin-hook`,
  `consolidate`. See `docs/memory/memory-learn/simple-dreaming.md`.

### 6.4 `memory-export` — portability
- **`opencode-export` (v0):** bundles the agent's memory (skills + persona + hub) into an
  **OpenCode-native** portable format — a drop-in skills/instructions set another OpenCode workspace
  or Acolyte agent can import. Exposed as a CLI (`acolyte memory export`) and/or a skill.
- *Other modes (planned):* `portable-archive`, `remote-catalog`. See `docs/memory/memory-export/`.

> Because Memory is contract-free, adding a mode = adding a documented recipe under `docs/memory/…`
> plus whatever OpenCode/cron artifacts it needs. No Acolyte API change.

---

## 7. The Envelope Contract (C4)

The **Envelope** is the single message type between Communication and Orchestrator.

```jsonc
{
  "id": "uuid",
  "direction": "in" | "out",
  "channel": "telegram" | "cron" | "heartbeat" | "agent-mail" | "agent-im" | "agent-post",
  "thread": "telegram:123456789",       // conversation key → maps to an OpenCode session
  "from": { "kind": "user"|"agent"|"system", "id": "...", "name": "..." },
  "to":   { "kind": "user"|"agent"|"system", "id": "...", "name": "..." },
  "content": {
    "type": "text" | "media" | "event",
    "text": "…",
    "media": ["<workspace>/downloads/images/foo.jpg"]   // resolved absolute paths
  },
  "meta": {},
  "created_at": "2026-11-05T12:00:00Z"
}
```

Media is resolved to file paths before reaching the engine. `channel` and `from/to.kind` reserve the
agent-to-agent case (`FUTURE-IDEAS.md`).

---

## 8. The Communication ⇄ Orchestrator contract

This is the one real code/data contract. Acolyte already has much of the Telegram plumbing built.

**Envelope bus (the interface):**
```go
// Comm → Orchestrator (inbound): channel parses its payload into an Envelope and sinks it.
type InboundSink func(Envelope)

// Orchestrator → Comm (outbound): route an Out envelope to its channel.
type Communication interface {
    Name() string                                            // "telegram"
    Start(ctx context.Context, sink InboundSink) error       // begin delivering inbound
    Send(Envelope) error                                     // deliver outbound
    Stop() error
}
```

**What each side owns:**
- **Communication** parses channel payloads ↔ Envelope, delivers outbound, and stores **its own
  state** in `.acolyte.sqlite.db` under a **namespaced schema** (§9): chat↔user mapping, delivery
  status, message IDs (dedup/reply-threading), media file index. It does **not** store conversation
  content — that lives in the OpenCode session.
- **Orchestrator** resolves the agent, maps thread→OpenCode session, drives the turn, persists
  envelope metadata, and routes outbound.

**Services the Orchestrator exposes to *every* module (incl. Communication):**
1. **Envelope bus** — inbound sink + outbound send.
2. **Cron service** — register scheduled jobs (owner-tagged); all listed by `acolyte cron list` (§10).
3. **Store access** — `.acolyte.sqlite.db`, each module owning namespaced tables (§9).
4. **Turn engine** — run a prompt through OpenCode (for heartbeat, dream, etc.).
5. **Identity/registry + status** — name/workspace resolution, `acolyte status`.

Right now the only channel is **Telegram**. New channels (incl. agent-to-agent later) are new
`Communication` implementations reusing the same Envelope + services. See `docs/communication/`.

---

## 9. Persistence — the store that holds what OpenCode sessions don't

**Principle:** OpenCode sessions already keep the full conversation + tool activity. **Don't
duplicate it.** `.acolyte.sqlite.db` stores only what's *not* in a session: routing/delivery state,
scheduling, identity maps, module bookkeeping.

**Per-module namespaced schemas** — each module owns its tables (a prefix), ships/migrates its own
schema, and evolves independently. The DB is shared; the schemas are not coupled.

```sql
-- Orchestrator core (prefix: orch_)
CREATE TABLE orch_sessions (          -- thread <-> OpenCode session mapping
  id TEXT PRIMARY KEY, thread TEXT, opencode_session_id TEXT,
  created_at DATETIME, last_active_at DATETIME
);
CREATE TABLE orch_cron_jobs (         -- §10: shared cron service
  id TEXT PRIMARY KEY,
  title TEXT NOT NULL UNIQUE,         -- human title (unique)
  owner TEXT,                         -- who registered: user|memory-learn|heartbeat|comm|…
  kind TEXT,                          -- prompt | shell | routine
  spec TEXT,                          -- cron expression (the WHEN)
  prompt TEXT, shell_path TEXT,       -- the WHAT (by kind): prompt text | shell script path
  routine TEXT, routine_args TEXT,    --   …or routine name + JSON args
  agent_target TEXT, session_id TEXT, -- where it runs (agent name/workspace; session)
  timezone TEXT, enabled INTEGER DEFAULT 1,
  last_run_at DATETIME, next_run_at DATETIME, created_at DATETIME, meta TEXT
);
CREATE TABLE orch_logs (              -- append-only audit log
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  level TEXT, source TEXT, message TEXT, meta TEXT, created_at DATETIME
);
CREATE TABLE orch_settings (          -- baked capability metadata (non-secret)
  key TEXT PRIMARY KEY, value TEXT NOT NULL
);

-- Communication / Telegram (prefix: comm_tg_)  -- routing/delivery state, NOT conversation content
CREATE TABLE comm_tg_settings ( key TEXT PRIMARY KEY, value TEXT NOT NULL );  -- allowed users, channel settings
CREATE TABLE comm_tg_chats  ( chat_id INTEGER PRIMARY KEY, username TEXT, allowed INTEGER, resolved_at DATETIME );
CREATE TABLE comm_tg_inbox  ( id TEXT PRIMARY KEY, tg_update_id INTEGER, chat_id INTEGER, envelope_id TEXT, received_at DATETIME, media_path TEXT );
CREATE TABLE comm_tg_outbox ( id TEXT PRIMARY KEY, envelope_id TEXT, chat_id INTEGER, tg_message_id INTEGER, delivery TEXT, sent_at DATETIME );
CREATE TABLE comm_tg_media  ( file_path TEXT PRIMARY KEY, tg_file_id TEXT, type TEXT, chat_id INTEGER, saved_at DATETIME );

-- Memory (prefix: mem_)  -- optional; most memory lives in files + OpenCode
CREATE TABLE mem_dreams ( id TEXT PRIMARY KEY, ran_at DATETIME, sessions_scanned INTEGER, skills_created INTEGER, note TEXT );
```

**Table prefixes = module boundaries.** A module's schema doc lives in `docs/<module>/`. New module =
new prefix + its own tables. Each module records its schema version in
`schema_meta(module TEXT PRIMARY KEY, version INTEGER NOT NULL)` and migrates its own tables; the
orchestrator provides the DB handle and migration helper. Modules don't reach into each other's tables.

---

## 10. Scheduler, routines, and the CLI

### 10.1 Cron job model (owner × kind)
Every scheduled thing is one row in `orch_cron_jobs`, on two orthogonal axes:
- **`owner`** — *who registered it* (`user`, `memory-learn`, `heartbeat`, `comm`, …). Attribution +
  management (grouping in `acolyte cron list`; lets a module find its own jobs). Does **not** affect
  execution.
- **`kind`** — *what runs when it fires*: **`prompt` | `shell` | `routine`**.

| kind | runs the model? | authored by | what runs | output |
|------|-----------------|-------------|-----------|--------|
| `prompt` | yes | user | a prompt (`--prompt`) through the agent | delivered to the user |
| `shell`  | no  | user | a shell script (`--shell <path>`) run with **`$SHELL`** (never `sh`) | routed (`inbox` / `notify-user` / `log`; default success `inbox`, failure `notify-user`) |
| `routine`| yes | module | a registered named routine (`--routine <name>`) + args | internal / by routine |

- **`$SHELL`** — shell jobs run the script with the user's `$SHELL` (e.g. zsh), never hardcoded `sh`.
- **Target** — a job binds to an agent via `--agent-name` **xor** `--workspace-folder` (if neither,
  use `$ACOLYTE_WORKSPACE_PATH`, then infer from a valid pwd) and optionally a `--session-id`
  (default: latest session).
- **Actions are mutually exclusive**: `--prompt` | `--shell` | `--routine`.

### 10.2 Routines (`routine` kind)
A **routine** is a named, headless behavior registered by a module (e.g. `dream`, `heartbeat`), with a
declared **arguments schema**. Scheduled as `routine`-kind cron jobs (auto-registered when a
capability is enabled, or manually via `acolyte cron --routine <name> [--routine-args '<json>']`).
- **`acolyte cron routine-list`** → JSON of every registered routine + its arguments (name, type,
  required, default). This is the catalog; it is also documented in `docs/README.md` §routines.
- Examples: `dream` (owner `memory-learn`, args `{days}`), `heartbeat` (args `{interval}`).

### 10.3 Heartbeat (the `heartbeat` routine)
Runs every `schedule.heartbeat_minutes` minutes **in its own session**. It inspects the **latest
session** + the notes in **`HEARTBEAT.md`** and decides whether to surface anything to the user —
**erring toward silence** ("didn't find anything particularly interesting… not sending"). Only when
something is genuinely interesting does it **send it to the user on the latest session** (via
`acolyte inbox` on the latest session). See `docs/orchestrator/heartbeat.md`.

### 10.4 Message primitives
Two ways to move a message (both target an agent/session like the CLI below):
- **`acolyte inbox {text}`** — deliver an **external event** to the **agent** on a session (a
  `synthetic` inbox input). The agent reacts and reaches the user; it shows **in the session**.
  (= v0 `mail`.) This generalizes the old `acolyte heartbeat "{text}"` — any cron/agent can use it.
- **`acolyte notify-user {text}`** — post a notification to the **user only** (e.g. Telegram). It is
  **never** shown to the agent and **never** appears in the session. (= v0 `notify`.)

### 10.5 CLI
Common targeting flags: `--agent-name <name>` **xor** `--workspace-folder <path>` (if neither, infer
from a valid pwd), and `--session-id <id>` (optional; default = latest session).

| Command | Behavior |
| ------- | -------- |
| `acolyte new --name <name> --workspace-folder <path> --config '<json>'` | **Scaffold + validate + register** a fresh agent with the capabilities chosen in `--config` (full key reference in [`docs/README.md`](docs/README.md)). On an existing folder it validates + adopts. |
| `acolyte status [name \| workspace]` | All agents, or one: name, workspace, last message, job count + next job. |
| `acolyte delete <name \| workspace>` | Remove from the registry **only**. Folder untouched. |
| `acolyte cron --title <t> --datetime <cron> (--prompt <text> \| --shell <path> \| --routine <name>) [--routine-args '<json>'] [--agent-name\|--workspace-folder] [--session-id] [--timezone <IANA>] [--on-success inbox\|notify-user\|log] [--on-failure inbox\|notify-user\|log]` | Create a job. `--title` unique per agent; `--datetime` cron syntax; actions mutually exclusive; agent-name/workspace-folder mutually exclusive. |
| `acolyte cron list` | Markdown table of **all** jobs: `title \| datetime \| agent \| owner \| kind \| prompt \| shell \| session` (routine jobs show `routine:<name>`; prompt jobs show `prompt:<text>`). |
| `acolyte cron view <title>` | Full JSON of one job; target-aware when titles collide across agents. |
| `acolyte cron routine-list` | JSON catalog of registered routines + their arguments. |
| `acolyte cron rm <title>` | Remove a job; target-aware when titles collide across agents. |
| `acolyte cron tick` | Drain currently due jobs (testing/debugging). |
| `acolyte inbox "<text>"` | Deliver an external event to the agent on a session (§10.4). |
| `acolyte notify-user "<text>"` | Notify the user only — never agent, never session (§10.4). |
| `acolyte inspect --session-id <id>` | Merge OpenCode session messages with communication routing/delivery events, ordered by timestamp. |
| `acolyte attach [--session-id <id>]` | Attach the OpenCode TUI to the managed service and agent workspace/session. |

> Runtime capability mutation is not exposed yet; `acolyte new` preserves existing settings on
> adoption. See `FUTURE-IDEAS.md`.

---

## 11. CONFIG.md (persona) & the persona bundle

`CONFIG.md` is one of the persona files (loaded into context by `memory-load`). It gives the agent
**meta-awareness**: that it is an Acolyte agent on OpenCode, with high-level modules, and where the
docs live. Sample:

```md
# CONFIG

You are an **Acolyte** agent. Acolyte hosts you on top of **OpenCode**.

- **Engine:** OpenCode (https://opencode.ai) — your reasoning + tools come from here.
- **Acolyte modules** (high-level capabilities layered on OpenCode):
  - **Memory** — `memory-load` / `memory-access` / `memory-learn` / `memory-export`.
  - **Communication** — channels to the outside world (e.g. Telegram).
  - **Orchestrator** — the `acolyte` daemon: turn loop, cron, store, registry.

## Inspect your own capabilities
- `opencode.jsonc` — engine + capability wiring (instructions, skills, tools).
- `acolyte cron list` — scheduled jobs (heartbeats, dream/learn runs, tasks).
- `acolyte status` — your agent status.

## Learn more
Module docs: https://github.com/Martins6/acolyte/tree/main/docs/
Read a module's docs to understand or change how a capability works.
```

---

## 12. Registry

`$ACOLYTE_HOME/registry.sqlite.db` (default `$ACOLYTE_HOME = ~/.acolyte`).

```sql
CREATE TABLE agents (
  name        TEXT PRIMARY KEY UNIQUE,   -- unique handle
  workspace   TEXT NOT NULL UNIQUE,      -- true ID (absolute path)
  description TEXT,
  engine      TEXT DEFAULT 'opencode',
  enabled     INTEGER DEFAULT 1,
  registered_at DATETIME, last_seen_at DATETIME
);
CREATE TABLE agent_status (              -- optional snapshot for fast `acolyte status`; v0 uses fan-out
  workspace TEXT PRIMARY KEY,
  last_message_at DATETIME, next_job_at DATETIME, job_count INTEGER, updated_at DATETIME
);
```

---

## 13. Auto-start (Orchestrator always-on)

The orchestrator should start at boot and stay up. Per-OS mechanism (`docs/orchestrator/autostart.md`):

**macOS — `launchd`:**
- Per-user at login: `~/Library/LaunchAgents/ai.acolyte.daemon.plist`.
- System-wide at boot (before login): `/Library/LaunchDaemons/ai.acolyte.daemon.plist`.
- Load: `launchctl bootstrap gui/501 <plist>` (user) / `sudo launchctl bootstrap system <plist>` (system).
```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>ai.acolyte.daemon</string>
  <key>ProgramArguments</key>
  <array><string>/Users/YOU/.local/bin/acolyte</string><string>start</string></array>
  <key>WorkingDirectory</key><string>/Users/YOU/.acolyte</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>/Users/YOU/.acolyte/.logs/daemon.out.log</string>
  <key>StandardErrorPath</key><string>/Users/YOU/.acolyte/.logs/daemon.err.log</string>
</dict></plist>
```

**Linux — `systemd`:**
- System: `/etc/systemd/system/acolyte.service` + `systemctl enable --now acolyte`.
- User: `~/.config/systemd/user/acolyte.service` + `systemctl --user enable --now acolyte`, and
  `loginctl enable-linger $USER` to run at boot without login.
```ini
[Unit]
Description=Acolyte orchestrator
After=network-online.target
Wants=network-online.target
[Service]
ExecStart=/home/you/.local/bin/acolyte start
WorkingDirectory=/home/you/.acolyte
Restart=always
RestartSec=5
Environment=ACOLYTE_HOME=/home/you/.acolyte
EnvironmentFile=-/home/you/.acolyte/.env
[Install]
WantedBy=multi-user.target        # use default.target for a --user unit
```

> Nice-to-have: `acolyte install-service [--user|--system]` writes the right unit/plist.

---

## 14. Turn lifecycle

```
Telegram msg
  → Comm.Telegram: parse → Envelope{in, telegram}
  → Orchestrator: resolve agent (registry), map thread→OpenCode session
  → memory-load + memory-access   → instructions + skills wiring (OpenCode)
  → OpenCode runs the turn (model + tools)
  → result: output + tool activity + proposed learning/export deltas
  → memory-learn / memory-export  → persist skills/persona or export
  → Comm.Telegram.Send(each Out envelope)
  → update store (comm_tg_outbox) + registry status
```

**Every external event = admit a `synthetic` inbox input.** A cron fire (`simple-dreaming`, heartbeat),
an inbound message, or a background notification is admitted to the mapped session's **inbox** as
`type: "synthetic"` (with `description` + `metadata`) and delivered via `queue` (next idle) or `steer`
(interrupt now). Real user messages use `session.prompt`. **Cron** jobs either run a shell command or
admit a synthetic event; output is routed via Communication.

---

## 15. Decisions & Open Questions

**Settled:**
1. **No engine abstraction** — OpenCode is the engine (§5).
2. **Memory is contract-free** — 4 sub-modules (`load`/`access`/`learn`/`export`) plug into OpenCode
   features + cron; modes include `null`. No Go interface required (§6).
3. **`memory-export` added** — first mode `opencode-export` (§6.4).
4. **The real contract = Communication ⇄ Orchestrator** (§8) over an Envelope bus + shared services.
5. **Store holds only non-session state**; OpenCode sessions keep the conversation (§9).
6. **Per-module namespaced schemas** in `.acolyte.sqlite.db` (prefixes) (§9).
7. **Shared cron service**; `acolyte cron list` shows all jobs from every module (§10).
8. **No `.acolyte.jsonc`** — capabilities in `opencode.jsonc` + CLI; identity in the registry (§4).
9. **`acolyte new` bakes capabilities at creation**; smooth capability updates deferred (§4/§10).
10. **`CONFIG.md`** persona file for meta-awareness (§11).
11. **Docs live in `docs/`** per module/mode (§ see `docs/`), linked from `CONFIG.md`.
12. **Deferred ideas live in `FUTURE-IDEAS.md`.**
13. **Identity lives in the persona + registry** — UX identity in `.acolyte/*.md`; name/description
    in the registry. No per-folder identity record needed (closed).
14. **Engine transport = the OpenCode V2 API, not the CLI** (§5.1). External/background events enter
    as **`synthetic` inbox inputs** (not faked user turns); real user turns use `session.prompt`. CLI
    stays for agent tools + debug.
15. **`acolyte new --config {}`** is the capability surface (§10) — `memory-load/access/learn/export`,
    `communication`, `schedule`, `engine`; full key reference in `docs/README.md`.
16. **`simple-dreaming`** = headless cron job, every `days` days over the last `days` days;
    conservative (write only when super extra clear) + super-compact (§6.3).
17. **Heartbeat** = the `heartbeat` routine (a `routine`-kind cron job) in its own session; errs
    toward silence; surfaces to the user on the latest session (§10.3).
18. **Cron job model** = `owner` × `kind`; kinds **`prompt` | `shell` | `routine`** (renamed from
    `agent`); shell jobs use **`$SHELL`** (not `sh`) (§10.1).
19. **Routines** = named headless behaviors registered by modules with arg schemas; listed via
    `acolyte cron routine-list` + documented in `docs/README.md` (§10.2).
20. **`acolyte inbox`** = external event to the agent (in-session; v0 `mail`); **`acolyte
    notify-user`** = user-only, never agent, never session (v0 `notify`) (§10.4).
21. **`synthetic`/`inbox` validated by spike** (2026-11-05, OpenCode v2.2.5): external events reach
    the model + trigger reactions; confirmed request shapes in §5.2; no `--trusted` marker needed.
22. **Secrets = two-tier `.env`** (§4): `$ACOLYTE_HOME/.env` (global) + `<workspace>/.env`
    (per-agent); precedence process env > workspace > global; workspace values are scoped, never
    process-global. Both files are gitignored and never scaffolded.
23. **One daemon serves all registered agents** (§1/§14); per-agent isolation and supervision
    hardening is deferred (`FUTURE-IDEAS.md`).
24. **`acolyte status` uses fan-out reads in v0** (§12); the `agent_status` snapshot cache is deferred.

**[OPEN] forks:**
1. **Heartbeat delivery** (§10.3): via `inbox` (in-session) vs `notify-user` (user-only)? — currently
   modeled as `inbox` on the latest session.
```

# Acolyte — Module Docs & `acolyte new` Config Reference

Acolyte is *powered by OpenCode*. It layers high-level modules on top of the engine. This file is
both the **module documentation index** and the **complete `acolyte new` config reference** — every
flag and every `--config` key in the system.

---

## Creating an agent

```bash
acolyte new --name <name> --workspace-folder <path> --config '<json>'
```

| Flag | Type | Required | Meaning |
| ---- | ---- | -------- | ------- |
| `--name` | string | yes | Unique agent handle (registry-enforced). Decoupled from the OpenCode agent id. |
| `--workspace-folder` | path | yes | The agent's true ID (absolute folder). Scaffolded if missing, validated + adopted if present. |
| `--config` | JSON object | no | Capability/config selection. Omitted keys take defaults. |

`acolyte new` **bakes** the chosen capabilities as concrete artifacts (OpenCode config + cron jobs +
skills + persona). The same keys can later be changed in place with
`acolyte config <agent> set <key> <value>` — see [Runtime config](#runtime-config).

---

## `--config` reference (every key)

Every top-level key is optional. A sub-module value is `null` (native/built-in baseline) or
`{"mode": <mode>, "args": {…}}`. **`null` = native baseline** — e.g. `memory-access: null` already
means *both* instructions-in-context + the `skill` tool.

### Identity
| Key | Type | Default | Meaning |
| --- | ---- | ------- | ------- |
| `description` | string | `""` | Human description of the agent (stored in the registry). |

### Engine → `opencode.jsonc`
| Key | Type | Default | Meaning |
| --- | ---- | ------- | ------- |
| `engine.model` | string `provider/model` | seeded default | Default model. |
| `engine.agent` | string | `build` | OpenCode agent id used as primary. |

### Memory — [`memory/README.md`](memory/README.md)
| Key | Type | Modes | Args | Doc |
| --- | ---- | ----- | ---- | --- |
| `memory-load` | `{mode,args}` \| null | `default` | — | [`memory-load/default.md`](memory/memory-load/default.md) |
| `memory-access` | `{mode,args}` \| null | `native` (= null) | — | [`memory-access/native.md`](memory/memory-access/native.md) |
| `memory-learn` | `{mode,args}` \| null | `simple-dreaming` | `days:int` | [`memory-learn/simple-dreaming.md`](memory/memory-learn/simple-dreaming.md) |
| `memory-export` | `{mode,args}` \| null | `opencode-export` | — | [`memory-export/opencode-export.md`](memory/memory-export/opencode-export.md) |

The `opencode-export` mode is run with `acolyte memory export --out <zip>`; it writes a
portable Markdown + JSON trace bundle. `acolyte memory import <zip>` moves a bundle into
another agent (extract + index, and/or replay its sessions).

### Communication — [`communication/README.md`](communication/README.md)
| Key | Type | Channels | Meaning | Doc |
| --- | ---- | -------- | ------- | --- |
| `communication.channels` | string[] | `telegram` | Enabled channels. | [`telegram.md`](communication/telegram.md) |
| `communication.telegram.allowed_users` | string[] | — | Allowed Telegram users (non-secret). | [`telegram.md`](communication/telegram.md) |

### Schedule / heartbeat — [`orchestrator/README.md`](orchestrator/README.md)
| Key | Type | Default | Meaning | Doc |
| --- | ---- | ------- | ------- | --- |
| `schedule.timezone` | string (IANA) | — | Timezone for cron + heartbeat. | [`cron.md`](orchestrator/cron.md) |
| `schedule.heartbeat_minutes` | int | `0` (disabled) | Heartbeat interval in minutes; `0` disables. | [`heartbeat.md`](orchestrator/heartbeat.md) |

### Example

```bash
acolyte new --name valet --workspace-folder ~/agents/valet --config '{
  "description": "Personal assistant",
  "engine": { "model": "minimax-coding-plan/MiniMax-M3" },
  "memory-learn":  { "mode": "simple-dreaming", "args": { "days": 3 } },
  "memory-export": { "mode": "opencode-export", "args": {} },
  "communication": { "channels": ["telegram"], "telegram": { "allowed_users": ["@you"] } },
  "schedule": { "timezone": "America/Sao_Paulo", "heartbeat_minutes": 60 }
}'
```

> Capabilities are baked at creation, then mutable in place: `acolyte config <agent> set <key>
> <value>` updates the listed keys (engine, channels, allowed users, schedule, memory) with full
> validation and a `--dry-run` preview. See [Runtime config](#runtime-config).

---

## Runtime config

```bash
acolyte config <agent> get|set|edit
```

`<agent>` is an agent name or a workspace path. `get` prints the effective config as JSON in the
exact `--config` shape above (engine from `opencode.jsonc`, identity from the registry, comm
settings from `comm_tg_settings`, schedule/memory from `orch_settings` + the dream/heartbeat
jobs). `edit` opens `<workspace>/opencode.jsonc` in `$EDITOR` (fallback `vi`) and re-validates it
as JSONC with sane `model`/`default_agent` values — an invalid edit is rolled back.

`set <key> <value>` supports every `--config` key except nested engine/mode overrides that v0
does not own:

| Key | Value | Effect |
| --- | ----- | ------ |
| `description` | string | registry description |
| `engine.model` | `provider/model` | `opencode.jsonc` `"model"` (targeted edit) |
| `engine.agent` | `build`, `plan`, or a defined agent id | `opencode.jsonc` `"default_agent"` (no prompts are invented) |
| `communication.channels` | JSON array (v0: `telegram` only; `[]` disables) | `comm_tg_settings.channels` |
| `communication.telegram.allowed_users` | JSON array or comma-separated | normalized to a deduped JSON array |
| `schedule.timezone` | IANA name | `orch_settings` + re-times the dream/heartbeat jobs and their `next_run_at` |
| `schedule.heartbeat_minutes` | int | `0` removes the `heartbeat` job; `>0` upserts `@every <n>m` with `{"interval":n}` |
| `memory-learn` | `{"mode":"simple-dreaming","args":{"days":N}}` or `null` | `null` removes the `dream` job + `memory.learn`; otherwise upserts `@every <N*24>h` with `{"days":N}` |
| `memory-load`, `memory-access`, `memory-export` | JSON object or `null` | `orch_settings` only (`null` clears the setting) |

Rules:

- Everything is validated **before** any write; `--dry-run` prints a unified diff for
  `opencode.jsonc` and the before/after values for store/cron changes, writing nothing.
- `--json` makes `get` (one line) and `set` (result object) machine-readable.
- `opencode.jsonc` writes refuse symlinks and go through temp + rename; comments and unrelated
  keys are preserved.
- A timezone is required while heartbeat or dream run (same rule as `acolyte new --config`).

See [`orchestrator/config.md`](orchestrator/config.md) for the full walkthrough.

---

## Secrets

Acolyte loads two optional `.env` files:

- `$ACOLYTE_HOME/.env` — global secrets (bot tokens, shared keys).
- `<workspace>/.env` — per-agent secrets; overrides the global file for that agent.

Precedence is **process environment > `<workspace>/.env` > `$ACOLYTE_HOME/.env`**. Both files are
gitignored and are never scaffolded by `acolyte new`; secrets belong there, never in
`opencode.jsonc` or any tracked file. Model-provider credentials are managed by OpenCode, not
these files: run `opencode auth login` to store them in OpenCode's auth store.

---

## Routines

**Routines** are named, headless behaviors the scheduler can run (`kind: routine`). Modules register
them with an argument schema; `acolyte cron routine-list` dumps the catalog as JSON. Schedule one with
`acolyte cron --title <t> --datetime <cron> --routine <name> [--routine-args '<json>'] …`. Routine jobs
run in their own `cron:<job ID>` session and do not accept `--session-id`; prompt/shell jobs default
to the latest session unless one is passed explicitly.

> Dream and heartbeat are implemented as prompt-guided routines. Dream writes conservatively by
> default and supports `acolyte cron --dry-run`; heartbeat replies `NOTIFY: <message>` only when
> something is genuinely interesting, and the routine delivers it to the latest user session.

| Routine | Owner | Args | What it does | Doc |
| ------- | ----- | ---- | ------------ | --- |
| `dream` | `memory-learn` | `days:int` | Every `days` days, review the last `days` days and conservatively distill persona/skills (direct write; `--dry-run` for proposals). Both phases use a session-scoped permission ruleset: analysis is read-only, writer limited to memory paths. | [`memory-learn/simple-dreaming.md`](memory/memory-learn/simple-dreaming.md) |
| `heartbeat` | `heartbeat` | `interval:int` | Every `interval` minutes, review `HEARTBEAT.md` + the latest non-cron session; notify the user only when something is genuinely interesting. | [`heartbeat.md`](orchestrator/heartbeat.md) |

`acolyte cron routine-list` output shape (per routine):

```json
{
  "name": "dream",
  "owner": "memory-learn",
  "description": "Periodic memory distillation over recent sessions",
  "args": [
    { "name": "days", "type": "int", "required": true, "description": "Run interval + lookback window (days)" }
  ]
}
```

Routines are added by modules; see `docs/<module>/` for each routine's behavior.

---

## Module docs layout

```
docs/
├── README.md                       ← this file (index + `acolyte new` config reference)
├── memory/
│   ├── README.md                   ← Memory intent + sub-components + value semantics
│   ├── memory-load/<mode>.md
│   ├── memory-access/<mode>.md
│   ├── memory-learn/<mode>.md
│   └── memory-export/<mode>.md
├── communication/
│   ├── README.md
│   └── <channel>.md                ← telegram.md
├── opencode/
│   └── v2-contract.md              ← pinned V2 API version + update procedure
└── orchestrator/
    ├── README.md
    ├── daemon.md                   ← per-agent adapters, routing, inspect/attach
    ├── cron.md                     ← shared cron service (shell + headless agent jobs)
    ├── config.md                   ← `acolyte config` runtime capability updates
    ├── heartbeat.md                ← the special always-on cron
    └── autostart.md                ← running always-on (macOS/Linux)
```

**A module `README.md`** covers intent, sub-components/args, and how it plugs into OpenCode. **A mode
file** covers intent, arguments, how it works, and the OpenCode/cron artifacts it produces.

## Modules
| Module | Doc | What it does |
| ------ | --- | ------------ |
| **Memory** | [`memory/`](memory/README.md) | Load / Access / Learn / Export memory. Contract-free. |
| **Communication** | [`communication/`](communication/README.md) | Channel adapters in/out (Telegram now). |
| **Orchestrator** | [`orchestrator/`](orchestrator/README.md) | `acolyte` daemon: turn loop, envelope bus, cron, store, registry. |

The **Engine** (OpenCode) is the platform, not a module — see the
[OpenCode V2 docs](https://opencode.ai/v2/docs/), the pinned
[V2 contract](opencode/v2-contract.md), and `SPEC.md` §5.

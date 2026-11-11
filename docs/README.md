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
skills + persona). Smooth capability updates are deferred (`FUTURE-IDEAS.md`).

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

> Capabilities are baked at creation. `acolyte new` adopts and preserves existing settings/jobs;
> there is currently no CLI to mutate an agent's channels, allowed users, schedule, or engine
> in place. See `FUTURE-IDEAS.md`.

---

## Secrets

Acolyte loads two optional `.env` files:

- `$ACOLYTE_HOME/.env` — global secrets (bot tokens, shared keys).
- `<workspace>/.env` — per-agent secrets; overrides the global file for that agent.

Precedence is **process environment > `<workspace>/.env` > `$ACOLYTE_HOME/.env`**. Both files are
gitignored and are never scaffolded by `acolyte new`; secrets belong there, never in
`opencode.jsonc` or any tracked file. Provider credentials may also live in OpenCode's own auth store.

---

## Routines

**Routines** are named, headless behaviors the scheduler can run (`kind: routine`). Modules register
them with an argument schema; `acolyte cron routine-list` dumps the catalog as JSON. Schedule one with
`acolyte cron --title <t> --datetime <cron> --routine <name> [--routine-args '<json>'] …`.

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

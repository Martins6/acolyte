# Runtime config — `acolyte config <agent> get|set|edit`

`acolyte new` bakes capabilities at creation; `acolyte config` changes them in place afterwards.
There is still **no `.acolyte.jsonc`** (SPEC §4): every key maps onto the same artifacts
`acolyte new` writes — `opencode.jsonc`, the registry, and the per-agent store
(`<workspace>/.acolyte.sqlite.db`).

```bash
acolyte config <agent> get [--json]
acolyte config <agent> set <key> <value> [--dry-run] [--json]
acolyte config <agent> edit [--json]
```

`<agent>` is an agent name or a workspace path (the same resolver as `acolyte status`).

## `get`

Prints the effective config as JSON in the `acolyte new --config` shape:

| Field | Source |
| ----- | ------ |
| `description` | registry (`$ACOLYTE_HOME/registry.sqlite.db`) |
| `engine.model`, `engine.agent` | `<workspace>/opencode.jsonc` (`model`, `default_agent`), JSONC-aware |
| `memory-load` | `orch_settings.memory.load`; the matching `.acolyte/*.md` entry in `opencode.jsonc` `instructions` is the actual wiring |
| `memory-access/learn/export` | `orch_settings` (`memory.*`); the `dream` job is the source of truth for `memory-learn` when present |
| `communication.channels`, `communication.telegram.allowed_users` | `comm_tg_settings` |
| `schedule.timezone` | `orch_settings.schedule.timezone`, falling back to the dream/heartbeat job timezone |
| `schedule.heartbeat_minutes` | the `heartbeat` job (`args.interval`, then its `@every <n>m` spec) |

Missing settings report the `acolyte new --config` defaults (agent `build`, channels
`["telegram"]`, no memory modules, heartbeat `0`). `--json` prints one compact line; without it
the JSON is indented.

## `set`

Supported keys and their values:

| Key | Value | Effect |
| --- | ----- | ------ |
| `description` | string | registry description |
| `engine.model` | `provider/model` (non-empty, must contain `/`) | targeted edit of `"model"` in `opencode.jsonc` |
| `engine.agent` | `build`, `plan`, an entry of `agent{}`/`agents{}`, or a `.opencode/agents/*.md` id | targeted edit of `"default_agent"`; unknown ids are rejected, no prompt is invented |
| `communication.channels` | JSON array of channels (v0: `telegram` only; `[]` disables all channels) | `comm_tg_settings.channels` |
| `communication.telegram.allowed_users` | JSON array **or** comma-separated list | normalized (trimmed, empties dropped, deduped) into `comm_tg_settings.telegram.allowed_users` |
| `schedule.timezone` | IANA name (or empty to clear) | `orch_settings.schedule.timezone`; existing `dream`/`heartbeat` jobs get the new timezone and a recomputed `next_run_at` |
| `schedule.heartbeat_minutes` | integer `>= 0` | `0` deletes the `heartbeat` job; `>0` upserts it as `@every <n>m` with `args {"interval": n}` |
| `memory-learn` | `{"mode":"simple-dreaming","args":{"days":N}}` or `null` | `null` deletes the `dream` job and `memory.learn`; otherwise upserts `dream` as `@every <N*24>h` with `args {"days": N}` and stores `memory.learn` |
| `memory-load` | `{"mode":"default"}` or `null` | `{"mode":"default"}` ensures exactly `.acolyte/*.md` is present in the `opencode.jsonc` `instructions` array and stores `memory.load`; `null` removes only that entry and deletes the setting (native baseline) |
| `memory-access`, `memory-export` | JSON object or `null` | `orch_settings` only; `null` deletes the setting (native baseline) |

Guarantees:

- **Validate first, write second.** The key/value is parsed, the v0 mode/arg rules run, and the
  *whole* effective config is validated (including "timezone required while heartbeat/dream run")
  before a single byte or row changes. Unknown keys and values fail with the list of supported
  keys.
- **`--dry-run` writes nothing.** It prints a unified diff for `opencode.jsonc` (including the
  `memory-load` instructions edit) and the before/after values plus the planned setting/job changes.
- **Targeted `opencode.jsonc` edits.** Comments, formatting, and unrelated keys are preserved;
  a missing key is inserted before the final `}`; missing keys with non-string values are
  rejected instead of duplicated. `memory-load` edits only the exact `.acolyte/*.md` entry:
  duplicate/ambiguous `instructions` keys and non-array `instructions` values are rejected, and
  other instruction entries and comments survive. Writes refuse a symlinked file and go
  temp-file + rename.
- **Idempotent.** Re-setting the same value reports `no changes` and does not reset a job's
  `next_run_at`.
- **Capability titles are not trusted.** Cron titles are not reserved: `set` verifies that an
  existing `dream`/`heartbeat` job has the expected owner, kind, and routine before touching it,
  and refuses (naming the job) to delete or rewrite a user job that merely shares the title. The
  identity check is repeated inside the same SQL statement as the write, so a job created
  concurrently under the title is never overwritten or deleted either. `get` ignores such a job
  instead of reporting it as the capability.
- `--json` prints the result object (`key`, `before`, `after`, `changed`, `dry_run`,
  `opencode_diff`, `settings`, `jobs`).

## `edit`

Opens `<workspace>/opencode.jsonc` in `$EDITOR` (fallback `vi`), then validates the saved file:

- it must parse as JSONC (`//` and `/* */` comments and trailing commas are tolerated),
- `model`, when set, must look like `provider/model`,
- `default_agent`, when set, must be `build`, `plan`, or an already-defined agent
  (`agent{}`/`agents{}` entries or `.opencode/agents/*.md`).

An invalid save is rolled back to the previous content, so a bad edit cannot leave the agent's
engine config broken. `edit` never touches cron or store state.

## Examples

```bash
# Inspect
acolyte config valet get

# Move the agent to another model, preview first
acolyte config valet set engine.model openrouter/anthropic/claude-sonnet-4 --dry-run
acolyte config valet set engine.model openrouter/anthropic/claude-sonnet-4

# Enable the heartbeat (timezone already set at creation)
acolyte config valet set schedule.heartbeat_minutes 30

# Turn learning on/off
acolyte config valet set memory-learn '{"mode":"simple-dreaming","args":{"days":7}}'
acolyte config valet set memory-learn null

# Turn the always-in-context persona bundle on/off
acolyte config valet set memory-load '{"mode":"default"}'
acolyte config valet set memory-load null

# Telegram access
acolyte config valet set communication.telegram.allowed_users '@alice, @bob'
acolyte config valet set communication.channels '[]'   # disable channels
```

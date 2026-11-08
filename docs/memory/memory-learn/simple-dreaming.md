# memory-learn / `simple-dreaming`

**Module:** memory-learn · **Mode:** `simple-dreaming` · **Args:** `days` (int, e.g. `3`)

## Intent
Periodic "dreaming": every `days` days, review the **last `days` days** of sessions and decide
whether anything is worth distilling into the **persona files** or a **skill**. It is deliberately
**conservative** — memory hygiene, not noise generation.

## How it works
1. Registered as a **headless `acolyte cron` job** (`kind: agent`, owner `memory-learn`) that fires
   every `days` days. It shows up in `acolyte cron list`.
2. On fire, it reviews the **last `days` days of sessions** (via the OpenCode session store).
3. Per candidate finding it decides: store in the **persona files** (`.acolyte/*.md`) or create a
   **skill** (`.agents/skills/<id>/SKILL.md`)?

## Writing rules (conservative)
- **When in doubt, don't write.** Only write when it is **super extra clear** the item is a reusable
  procedure or a durable fact.
- **Too many sessions → be super compact.** Distill only the essential; never dump.
- Prefer one precise skill/persona line over a broad, vague one.

## Args
| Arg | Type | Meaning |
| --- | ---- | ------- |
| `days` | int | Both the run interval **and** the lookback window: every `days` days, over the last `days` days. |

## Artifacts
- A **headless cron job** (`orch_cron_jobs`: `owner=memory-learn`, `kind=agent`, `spec` = every N days).
- Output: skills under `.agents/skills/`, persona edits in `.acolyte/*.md`, a `mem_dreams` log row.

## Notes
Headless and non-interactive — it runs as an agent routine in its own turn, with no user attached.
See `SPEC.md` §6.3 and `docs/orchestrator/cron.md`.

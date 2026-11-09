# memory-learn / `simple-dreaming`

**Module:** memory-learn · **Mode:** `simple-dreaming` · **Args:** `days` (int, e.g. `3`)

## Intent
Periodic "dreaming": every `days` days, review the **last `days` days** of sessions and decide
whether anything is worth distilling into the **persona files** or a **skill**. It is deliberately
**conservative** — memory hygiene, not noise generation.

## How it works
1. Registered as a **headless `acolyte cron` job** (`kind: routine`, owner `memory-learn`) that fires
   every `days` days. It shows up in `acolyte cron list`.
2. On fire, it lists the agent's sessions, keeps those updated in the lookback window, and builds a
   compact digest.
3. It runs a **prompt-guided turn** in its own cron session:
   - read `.acolyte/*.md` and `.agents/skills/` as needed;
   - only when it is **super extra clear**, update the persona or create/update a skill directly.
4. Every run records `mem_dreams` (sessions scanned, skills created, dry-run, note, changed files).

## Writing rules (conservative)
- **When in doubt, don't write.** Only write when it is **super extra clear** the item is a reusable
  procedure or a durable fact.
- **Too many sessions → be super compact.** Distill only the essential; never dump.
- Prefer one precise skill/persona line over a broad, vague one.
- A `dry_run` run tells the model not to modify files; changed paths are still detected and recorded
  as an audit warning.

## Args
| Arg | Type | Meaning |
| --- | ---- | ------- |
| `days` | int | Both the run interval **and** the lookback window: every `days` days, over the last `days` days. |
| `dry_run` | job meta bool | Optional. `acolyte cron --dry-run` proposes changes without writing. |

## Artifacts
- A **headless cron job** (`orch_cron_jobs`: `owner=memory-learn`, `kind=routine`, `spec` = every N days).
- Output: skills under `.agents/skills/`, persona edits in `.acolyte/*.md`, a `mem_dreams` row with
  the digest summary, dry-run flag, and changed paths.

## Notes
Headless and non-interactive — it runs as an agent routine in its own turn, with no user attached.
See `SPEC.md` §6.3 and `docs/orchestrator/cron.md`.

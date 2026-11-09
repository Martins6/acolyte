# Memory

**Intent.** Memory decides how an agent's knowledge is **loaded** into context, **accessed** at
runtime, **learned** from sessions, and **exported** for portability. Memory is **contract-free**:
each sub-component is a documented *mode* that plugs straight into OpenCode features (instructions,
skills, tools, plugins) and, where needed, the Orchestrator's cron service. There is no fixed Go API
a mode must implement — a mode is defined by its behavior and the artifacts it produces.

## Sub-components

| Sub-component | Purpose | v0 mode | Args |
| ------------- | ------- | ------- | ---- |
| [`memory-load`](memory-load/default.md) | Surface initial memory into context. | `default` | — |
| [`memory-access`](memory-access/native.md) | Read/query memory during a session. | `null` (= native **both**) | — |
| [`memory-learn`](memory-learn/simple-dreaming.md) | Improve memory from sessions. | `simple-dreaming` | `days: 3` |
| [`memory-export`](memory-export/opencode-export.md) | Export memory for portability. | `opencode-export` | — |

## Value semantics

Each sub-component is selected per agent (baked at `acolyte new`) as either:

- `null` → the **native / built-in baseline**, no extra module. (`memory-access: null` already means
  **both** — instructions-in-context + the `skill` tool.)
- `{ "mode": <mode>, "args": {…} }` → apply the named mode with its arguments.

## Where memory lives

- **Persona** (who the agent is): `.acolyte/{CONFIG,SOUL,TOOLS,USER,HEARTBEAT}.md`.
- **Learned skills** (what it can do): `.agents/skills/<id>/SKILL.md` (OpenCode-native).
- **Bookkeeping** (optional): `.acolyte.sqlite.db` tables prefixed `mem_` (§ `SPEC.md` §9).

## Adding a mode

Because Memory is contract-free: add a doc under `docs/memory/<sub-component>/<mode>.md` and whatever
OpenCode/cron artifacts the mode needs. No Acolyte API change. See `FUTURE-IDEAS.md` for planned modes.

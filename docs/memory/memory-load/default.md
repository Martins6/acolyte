# memory-load / `default`

**Module:** memory-load · **Mode:** `default` · **Args:** none

## Intent
Surface the agent's initial memory into the model's context at session start, using OpenCode's
native instruction/skill assembly — no custom plumbing.

## How it works
- **`AGENTS.md`** is the hub/index: it names the persona files and how to use them.
- **Persona files** (`.acolyte/*.md` — `CONFIG`, `SOUL`, `TOOLS`, `USER`, `HEARTBEAT`) are loaded via
  the OpenCode `instructions` array, so they are **always-in-context**.
- **Skills** (`.agents/skills/`) are auto-discovered by OpenCode and advertised with their
  `description`; the model loads bodies on demand via the `skill` tool.

## OpenCode artifacts it produces
```jsonc
// opencode.jsonc (seeded at acolyte new)
{
  "instructions": [".acolyte/*.md"]
}
```
plus an `AGENTS.md` hub at the workspace root. Project `.agents/skills/` is auto-discovered by
OpenCode, so it does not need a `skills` entry.

## Notes
This is the baseline loader. Other planned modes (`compact`, `rolling`, `role-sliced`) change *what*
is surfaced or *how much* — see `FUTURE-IDEAS.md`.

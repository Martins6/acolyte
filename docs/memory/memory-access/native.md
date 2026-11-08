# memory-access / `native` (the `null` baseline)

**Module:** memory-access · **Mode:** `native` (config value `null`) · **Args:** none

## Intent
Define how the model reads/queries memory *during* a session. The native baseline is **both**
always-in-context instructions **and** on-demand tools — this is what `memory-access: null` gives you.

## How it works — **both**
- **Instructions (always-in-context):** `AGENTS.md` + `.acolyte/*.md` (persona, `USER.md`, `TOOLS.md`)
  are present every turn.
- **Tools (on-demand):** the built-in OpenCode `skill` tool loads a skill body by ID from
  `.agents/skills/`; the model pulls in just the skill it needs.

## OpenCode artifacts
None beyond what `memory-load` sets up — this is why `null` is the value: OpenCode already provides
both access paths natively.

## Notes
Extra access modes (`recall`, `vector`, `graph`) add tools on top of the native baseline (query past
sessions, RAG, knowledge-graph). See `FUTURE-IDEAS.md`.

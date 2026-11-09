# memory-export / `opencode-export`

**Module:** memory-export · **Mode:** `opencode-export` · **Args:** none (v0)

## Intent
Export the agent's memory in an **OpenCode-native, portable** form so it can be moved into another
OpenCode workspace or Acolyte agent (or shared). This is the first export mode.

## How it works
Bundles the memory artifacts that are already OpenCode-shaped:
- **Skills** — `.agents/skills/**` (each `SKILL.md` + supporting files).
- **Persona / instructions** — `.acolyte/*.md` (as an `instructions` set) + the `AGENTS.md` hub.
- **Wiring** — the relevant `opencode.jsonc` entries (`instructions`, `skills`).

The output is a drop-in set another workspace can import (place skills under `.agents/skills/`, add
the `instructions` entries).

## Artifacts
- A planned CLI entry: `acolyte memory export` (not registered yet).
- Optionally a skill wrapping the export for self-service.
- (Planned) `portable-archive` (zip) and `remote-catalog` (HTTP skill catalog) modes — see
  `FUTURE-IDEAS.md`.

## Notes
Because the export targets OpenCode-native layout, importing into a plain OpenCode workspace needs no
Acolyte runtime — that's the point of `opencode-export`.

# memory-export / `opencode-export`

**Module:** memory-export · **Mode:** `opencode-export` · **Args:** none (v0)

## Intent
Export an agent's memory **and full trace** in an OpenCode-native, portable form so it can be
moved into another OpenCode workspace or Acolyte agent (or shared).

The v0 command is:

```bash
acolyte memory export --agent-name <name> --out <path/to/trace.zip>
```

`--out` is required. Use `--agent-name` **or** `--workspace-folder`, and `--session-id` to export a
single session. `--no-opencode` exports the store/persona only; `--force` overwrites an existing zip.

## What goes into the zip

```
README.md                 # what this is + how to use it as memory
manifest.json             # machine-readable index, counts, warnings
session-index.md          # table of every exported session
sessions/<id>.md          # full timeline per session: user/synthetic input, assistant text,
                          # reasoning, tool calls (input/output/error/metadata), idle markers,
                          # and communication inbound/outbound routing events
sessions/<id>.json        # OpenCode-native session transfer payload (when supported)
persona/                  # AGENTS.md, opencode.jsonc, .acolyte/*.md,
                          # .agents/skills/**, .opencode/agents/**
orchestrator/             # logs.md, cron-jobs.json, cron-routes.json,
                          # sessions.json, settings.json
communication/            # chats.json, inbox.json, outbox.json,
                          # media.json, settings.json
memory/dreams.json        # memory-learn audit rows
```

The Markdown is the primary, self-contained trace. A model can read the unzipped folder without
Acolyte or OpenCode. The JSON files preserve exact routing/orchestration data, and the native
`sessions/<id>.json` files can be imported with OpenCode's
`POST /api/experimental/session/import`.

## Safety
- Symlinks and non-regular files are skipped.
- `.env`, `.acolyte.sqlite.db`, and process logs are never included.
- The zip is written atomically with mode `0600`.
- Existing output files are not overwritten unless `--force` is passed.

## Notes
- `memory-export` is contract-free like the other memory sub-modules: the mode is defined by this
  documented behavior and the artifacts it produces.
- Importing the bundle into a plain OpenCode workspace needs no Acolyte runtime.
```

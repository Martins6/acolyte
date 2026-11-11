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
`--sanitize` redacts common secret patterns (see below).

## What goes into the zip

```
README.md                 # what this is + how to use it as memory
manifest.json             # machine-readable index, counts, warnings, sanitized flag
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
`POST /api/experimental/session/import` (or by `acolyte memory import`, below).

## Sanitizing secrets

Raw traces may contain secrets from the source workspace (environment values, tokens, keys)
embedded in conversation text and tool output. With `--sanitize`, every written entry is redacted
before it goes into the zip: env assignments whose key contains `TOKEN`/`KEY`/`SECRET`/`PASSWORD`,
`authorization:` values, `bearer` tokens, `api_key=` values, `sk-...` keys, and Telegram bot
tokens. JSON entries are parsed and walked so redaction can never break their structure;
`manifest.json` records `"sanitized": true` and the README carries a warning.

Sanitization is **best-effort**: it cannot catch every secret. Review a bundle before sharing it.

## Importing a trace

```bash
acolyte memory import <trace.zip> --agent-name <name> [--mode trace|sessions|all]
```

- **`trace` (default):** safely extracts the bundle into
  `<workspace>/downloads/memory-traces/<trace-id>/` (trace id = hash of the zip) and upserts a
  managed block in `.acolyte/MEMORY-TRACES.md` listing every imported trace. The index file is
  auto-loaded via `opencode.jsonc` instructions, so the target agent sees it and can read the
  trace on demand. User content outside the block markers is preserved; a symlinked index is
  refused.
- **`sessions`:** replays each `sessions/<id>.json` through the OpenCode import API in manifest
  order and maps the imported session to thread `memory:<trace-id>:<source-session-id>` in the
  agent store. Sessions already mapped are skipped unless `--force`. Because the service preserves
  ids, a trace exported from the same service conflicts on import (409/500); the importer
  rewrites the session/message ids and retries, and drops missing parent/fork references (404).
- **`all`:** both of the above.

`--dry-run` validates the zip and prints the planned actions without writing or importing.
`--json` prints one JSON result object. `--force` replaces an existing trace folder and
re-imports mapped sessions.

### Import safety

- Absolute paths, `..` traversal (including backslash variants), symlink entries, and entries
  resolving outside the temp root are rejected.
- Size caps: 512 MiB total, 64 MiB per file (enforced on actual decompressed bytes).
- Extraction happens in a temp folder and is renamed into place atomically; an existing trace
  folder requires `--force`.

## Safety
- Symlinks and non-regular files are skipped.
- `.env`, `.acolyte.sqlite.db`, and process logs are never included.
- The zip is written atomically with mode `0600`.
- Existing output files are not overwritten unless `--force` is passed.

## Notes
- `memory-export` is contract-free like the other memory sub-modules: the mode is defined by this
  documented behavior and the artifacts it produces.
- Importing the bundle into a plain OpenCode workspace needs no Acolyte runtime.


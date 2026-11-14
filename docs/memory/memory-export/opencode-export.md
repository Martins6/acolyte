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
communication/media/      # byte-for-byte copies of referenced media files,
                          # named <base>-<hash><ext> (hash of source path)
memory/dreams.json        # memory-learn audit rows
```

### Media files

`communication/media.json` stays the metadata index and keeps each item's original
`file_path` (Telegram saves inbound files under `<workspace>/media/inbound/...`). After the
rows are filtered, every referenced regular file inside the workspace is opened without following
symlinks in any path component and copied byte-for-byte into `communication/media/`, so the bundle
is self-contained on another machine. Each copy keeps the original base name and extension plus a
short, stable hash of its workspace-relative source path, e.g.
`1700000000000-photo-3f9c2a1b.jpg`; this avoids collisions when different sources share a name.
The same source file referenced twice is copied once.

Source containment is enforced before opening: absolute paths must resolve inside the workspace and
relative paths may not escape through `..`. The file is then walked from an opened workspace
directory with `openat`/`O_NOFOLLOW` on every component, and the opened descriptor is `fstat`ed and
streamed, so a symlinked parent or a concurrent path swap cannot export a file from outside the
workspace. On platforms without that Unix primitive, media copying is skipped with a clear warning
instead of falling back to a symlink-following open.

Missing, outside, escaping, symlinked, or non-regular source files are skipped and recorded in the
manifest warnings; they never fail the export. Session Markdown still shows the original absolute
source paths as provenance, but the portable form is the copy under `communication/media/`.

### Single-session scope (`--session-id`)

With `--session-id`, the bundle is scoped to that session so it cannot disclose unrelated
conversations. Only the scoped subset of the files above is written:

- `sessions/` — only the selected session's transcript (unchanged behavior).
- `communication/` — only inbox/outbox rows with `opencode_session_id` equal to the selected
  session, only the chats those rows reference, and only media saved for those chats (including
  copies under `communication/media/`). `settings.json` (global channel settings) is omitted;
  `media.json` is omitted when no chat can be attributed to the session.
- `orchestrator/` — only `sessions.json`, filtered to the selected session's mapping. Audit
  logs, cron jobs, cron routes, and settings are omitted.
- `memory/dreams.json` — omitted (global memory, unrelated to one conversation).
- `persona/` — kept: persona and skills are agent identity, not conversation content.

Manifest counts reflect the filtered data, `manifest.json` records
`"scoped_session": "<id>"`, and both the manifest warnings and the bundle README carry a note
about the omitted data. The default export (no `--session-id`) is unchanged.

The Markdown is the primary, self-contained trace. A model can read the unzipped folder without
Acolyte or OpenCode. The JSON files preserve exact routing/orchestration data, and the native
`sessions/<id>.json` files can be imported with OpenCode's
`POST /api/experimental/session/import` (or by `acolyte memory import`, below).

## Sanitizing secrets

Raw traces may contain secrets from the source workspace (environment values, tokens, keys)
embedded in conversation text and tool output. With `--sanitize`, every written text/JSON entry is
redacted before it goes into the zip: env assignments whose key contains
`TOKEN`/`KEY`/`SECRET`/`PASSWORD`, `authorization:` values, `bearer` tokens, `api_key=` values,
`sk-...` keys, and Telegram bot tokens. JSON entries are parsed and walked so redaction can never
break their structure; `manifest.json` records `"sanitized": true` and the README carries a warning.
Media copies under `communication/media/` are binary payloads and are preserved byte-for-byte, so
they are not sanitized.

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
- Media source paths are confined to the workspace: absolute paths outside it, relative paths
  escaping through `..`, and symlinked path components are skipped with a manifest warning, as are
  missing or non-regular files. The export still succeeds.
- Media files are opened with `openat`/`O_NOFOLLOW` from an opened workspace directory and streamed
  from the validated descriptor, so symlinked parents and path-swap races cannot read outside the
  workspace. Unsupported platforms warn and skip instead of following symlinks.
- `.env`, `.acolyte.sqlite.db`, and process logs are never included.
- The zip is written atomically with mode `0600`.
- Existing output files are not overwritten unless `--force` is passed.

## Notes
- `memory-export` is contract-free like the other memory sub-modules: the mode is defined by this
  documented behavior and the artifacts it produces.
- Importing the bundle into a plain OpenCode workspace needs no Acolyte runtime.


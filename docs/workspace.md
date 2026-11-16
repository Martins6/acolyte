# Agent workspace

`acolyte new` creates the SPEC v0 layout and uses it exclusively:

```text
<workspace>/
├── AGENTS.md                 # OpenCode hub: persistent guidance + persona index
├── opencode.jsonc            # OpenCode V2 config = capability wiring
├── .acolyte.sqlite.db        # per-agent store (orch_*, comm_tg_*, mem_*)
├── .acolyte/                 # persona
│   ├── CONFIG.md             #   what Acolyte is + how to inspect capabilities
│   ├── SOUL.md               #   core behaviour
│   ├── TOOLS.md              #   tool behaviour + workflows
│   ├── USER.md               #   user profile memory
│   └── HEARTBEAT.md          #   what to look for on a heartbeat
├── .agents/skills/           # learned OpenCode skills
├── .opencode/agents/         # optional OpenCode agent overrides
├── media/                    # inbound/ and outbound/ media
├── downloads/                # legacy media + memory-trace artifacts
└── .logs/                    # daily log files
```

## Notes

- The registry lives outside the workspace, in `$ACOLYTE_HOME/registry.sqlite.db` (`~/.acolyte` by
  default). `$ACOLYTE_HOME` also holds the global `.env` and, under launchd, `logs/`.
- `workspace/.env` and `$ACOLYTE_HOME/.env` are optional, gitignored, and never scaffolded by
  `acolyte new`; see [Secrets](README.md#secrets).
- Inbound Telegram media is written under `media/inbound/<type>/`, outbound images under
  `media/outbound/images/`. Legacy files under `downloads/<type>/` remain readable, but new writes go
  to `media/`. See [`communication/telegram.md`](communication/telegram.md).
- Memory-trace imports land in `downloads/memory-traces/<trace-id>/` and are indexed in
  `.acolyte/MEMORY-TRACES.md`; see
  [`memory/memory-export/opencode-export.md`](memory/memory-export/opencode-export.md).
- The old single-workspace `~/.acolyte/` layout (`MAIN-PROMPTS/`, `opencode.json`, `data.db`,
  `conversations/`) is no longer used. `acolyte new` still rejects a legacy `opencode.json` when
  adopting an existing folder.

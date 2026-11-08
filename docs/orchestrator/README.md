# Orchestrator

**Intent.** The Orchestrator is the `acolyte` daemon/CLI. It wires the modules together, drives the
turn loop, and provides the shared services every module relies on. It is **fixed** (not pluggable).

## Services it provides (to every module)

1. **Envelope bus** — inbound sink + outbound send (the Communication contract).
2. **Cron service** — register scheduled jobs, owner-tagged; all visible via `acolyte cron list`
   ([`cron.md`](cron.md)).
3. **Store access** — `.acolyte.sqlite.db`, each module owning **namespaced tables** (prefixes like
   `orch_`, `comm_tg_`, `mem_`). See `SPEC.md` §9.
4. **Turn engine** — run a prompt through OpenCode (for heartbeat, dream/learn, prompt jobs).
5. **Identity / registry + status** — name↔workspace resolution, `acolyte status`.

## CLI surface
| Command | Purpose |
| ------- | ------- |
| `acolyte new` | Scaffold + validate + register a fresh agent with chosen capabilities. |
| `acolyte status` | Show agents (name, workspace, last message, jobs + next job). |
| `acolyte delete` | Remove an agent from the registry (folder untouched). |
| `acolyte cron list\|add\|rm` | List **all** cron jobs / add prompt, shell, or routine jobs / remove. |
| `acolyte memory export` | Run the active `memory-export` mode. |
| `acolyte start` | Run the daemon (turn loop + cron + channels). |

## Always-on
The daemon runs at boot via `launchd` (macOS) or `systemd` (Linux) — see
[`autostart.md`](autostart.md).

## Docs
- [`cron.md`](cron.md) — the shared cron service.
- [`autostart.md`](autostart.md) — running always-on.

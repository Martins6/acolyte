# Orchestrator

**Intent.** The Orchestrator is the `acolyte` daemon/CLI. It wires the modules together, drives the
turn loop, and provides the shared services every module relies on. It is **fixed** (not pluggable).

## Services it provides (to every module)

1. **Envelope bus** — inbound sink + outbound send (the Communication contract).
2. **Cron service** — register scheduled jobs, owner-tagged; all visible via `acolyte cron list`
   ([`cron.md`](cron.md)).
3. **Store access** — one refcounted per-agent store, shared by cron and Communication
   (`<workspace>/.acolyte.sqlite.db`; namespaced tables such as `orch_`, `comm_tg_`).
4. **Turn engine** — run a prompt through OpenCode (for heartbeat, dream/learn, prompt jobs).
5. **Identity / registry + status** — name↔workspace resolution, `acolyte status`.

## CLI surface
| Command | Purpose |
| ------- | ------- |
| `acolyte new` | Scaffold + validate + register a fresh agent with chosen capabilities. |
| `acolyte status` | Show agents (name, workspace, last message, jobs + next job). |
| `acolyte delete` | Remove an agent from the registry (folder untouched). |
| `acolyte cron ...` | List/add/remove prompt, shell, or routine jobs. |
| `acolyte notify-user` | Queue a user-only notification through the target agent. |
| `acolyte inbox` | Deliver a synthetic external event to an agent session. |
| `acolyte run` | Run one real user turn headlessly (no Telegram/TUI). |
| `acolyte inspect` | Merge OpenCode history with communication routing events. |
| `acolyte attach` | Attach the OpenCode TUI to the managed service/session. |
| `acolyte start` | Run the daemon (turn loop + cron + channels). |

## Always-on
The daemon runs at boot via `launchd` (macOS) or `systemd` (Linux) — see
[`autostart.md`](autostart.md).

## Docs
- [`daemon.md`](daemon.md) — per-agent adapters, turn routing, inspect/attach.
- [`cron.md`](cron.md) — the shared cron service.
- [`autostart.md`](autostart.md) — running always-on.

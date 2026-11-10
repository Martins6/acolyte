# Acolyte

A runtime for a folder of agents, powered by OpenCode. Each agent is a self-contained workspace with its own identity, persona, learned skills, state, and logs.

## Installation

### Prerequisites

**Both the OpenCode CLI and this binary are required to run the daemon.**

- [OpenCode CLI](https://github.com/anomalyco/opencode) installed (`opencode` command available)

### Quick Install (Linux/macOS)

```bash
curl -sSL https://raw.githubusercontent.com/martins6/acolyte/main/install.sh | bash
```

This will install the binary to `~/.local/bin`. Make sure to add it to your PATH:

```bash
export PATH="$HOME/.local/bin:$PATH"
```

Or install system-wide with sudo:

```bash
curl -sSL https://raw.githubusercontent.com/martins6/acolyte/main/install.sh | bash -s -- -p /usr/local/bin
```

Install a specific version:

```bash
curl -sSL https://raw.githubusercontent.com/martins6/acolyte/main/install.sh | bash -s -- -v v0.1.0
```

#### Development Install (Linux/macOS)

<details>
<summary>Instructions</summary>

For developers who want to build from source, use `local_install.sh`:

```bash
./local_install.sh
```

**Requirements:**

- Go 1.21+ installed
- OpenCode CLI installed (`opencode` command available)

This script builds the binary from local source code instead of downloading from GitHub releases.

</details>

## Quick Start

### 1. Create a Telegram Bot

1. Open Telegram and search for @BotFather
2. Send `/newbot` to create a new bot
3. Follow the instructions and get your bot token

### 2. Create an Agent

```bash
acolyte new --name valet --workspace-folder ~/agents/valet --config '{
  "description": "Personal assistant",
  "engine": { "model": "minimax-coding-plan/MiniMax-M3" },
  "memory-learn": { "mode": "simple-dreaming", "args": { "days": 3 } },
  "communication": { "channels": ["telegram"], "telegram": { "allowed_users": ["@you"] } },
  "schedule": { "timezone": "America/Sao_Paulo", "heartbeat_minutes": 60 }
}'
```

This scaffolds the agent workspace, bakes the chosen capabilities, and registers the
agent in `$ACOLYTE_HOME/registry.sqlite.db` (`~/.acolyte` by default). Use
`acolyte status` to inspect registered agents and `acolyte delete` to remove one from
the registry (the folder is left untouched).

### 3. Configure Secrets

Acolyte loads secrets from two optional `.env` files:

- `$ACOLYTE_HOME/.env` — global secrets.
- `<workspace>/.env` — per-agent secrets, overriding the global file.

Precedence is **process environment > workspace `.env` > global `.env`**. Both files
are gitignored and are never created by `acolyte new`.

```bash
# Global (shared) secrets
cat >> "${ACOLYTE_HOME:-$HOME/.acolyte}/.env" <<'EOF'
TELEGRAM_BOT_TOKEN=YOUR_BOT_TOKEN
EOF

# Optional per-agent override
cat >> ~/agents/valet/.env <<'EOF'
TELEGRAM_BOT_TOKEN=YOUR_BOT_TOKEN
EOF
```

### 4. Start the Daemon

```bash
acolyte start
```

The daemon serves every registered agent:

- Each agent's Telegram adapter reads its token from `<workspace>/.env`, falling
  back to `$ACOLYTE_HOME/.env` and then the process environment.
- **Each agent needs a distinct bot token.** Two agents sharing a token cannot
  poll Telegram at the same time; the daemon starts one and warns about the rest.
- Agents without a token, without Telegram in `communication.channels`, or
  without allowed users are skipped with a warning.

Press `Ctrl+C` to stop gracefully.

### 5. Inspect a Session

`acolyte inspect` merges the OpenCode conversation with the communication
routing/delivery rows for one session:

```bash
acolyte inspect --session-id ses_... --agent-name valet
acolyte inspect --session-id ses_... --no-opencode   # routing events only
acolyte inspect --session-id ses_... --ndjson        # one event per line
```

To open the same session in the OpenCode TUI, attach to the managed service:

```bash
acolyte attach --agent-name valet
acolyte attach --agent-name valet --session-id ses_...
```

Running `opencode` directly in a folder starts its own server; use
`acolyte attach` (or `opencode attach <url> --dir <workspace> --session <id>`)
to share Acolyte's sessions.

## Agent Workspace

`acolyte new` creates the SPEC v0 layout:

```
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
├── downloads/                # media, transcripts, artifacts
└── .logs/                    # daily log files
```

`acolyte new` uses this layout exclusively. The old single-workspace `~/.acolyte/`
layout (`MAIN-PROMPTS/`, `opencode.json`, `data.db`, `conversations/`) is no longer
used; `acolyte new` still rejects a legacy `opencode.json` when adopting a folder.

## Logging

The daemon logs to standard output/error; under launchd or systemd those streams
go to the service log (`journalctl -u acolyte`, or the `StandardOutPath` /
`StandardErrorPath` in the launchd plist).

Per-agent audit events (cron runs, delivery failures, no-channel inbox replies)
are stored in the `orch_logs` table in `<workspace>/.acolyte.sqlite.db`. Use
`acolyte inspect --session-id ...` to see the conversation plus routing history
for one session.

## Media Handling

When an allowed Telegram user sends media:

1. The Telegram adapter downloads the file (reusing an existing download when the
   Telegram file ID was seen before).
2. It saves the file under `<workspace>/downloads/<type>/`.
3. It builds a prompt of the form `File located at: <abs-path>\n\nUser message: <caption-or-empty>`
   and offers it to the agent.
4. The agent can read or otherwise act on the file via its normal tools.

Photos, documents, audio, voice, and video are all downloaded. Media the agent
sends back to the user is currently text-only; sender-side media is deferred.

## CLI Commands

| Command                                      | Description                                    |
| -------------------------------------------- | ---------------------------------------------- |
| `acolyte new --name <n> --workspace-folder <p> --config '<json>'` | Scaffold, validate, and register an agent |
| `acolyte status [name\|workspace]`           | List agents or show one agent's jobs/activity  |
| `acolyte delete <name\|workspace>`           | Remove an agent from the registry (folder kept) |
| `acolyte cron ...`                           | Manage scheduled prompt/shell/routine jobs     |
| `acolyte cron list\|view\|rm\|routine-list\|tick` | Inspect or run the cron service            |
| `acolyte notify-user "<text>"`               | Notify the user only (never the agent)         |
| `acolyte inbox "<text>"`                     | Deliver a synthetic external event to an agent |
| `acolyte inspect --session-id <id>`          | Merge OpenCode history with routing events     |
| `acolyte attach [--agent-name <n>]`          | Attach the OpenCode TUI to the agent session   |
| `acolyte start`                              | Start the multi-agent daemon                   |
| `acolyte update`                             | Check & apply updates                          |
| `acolyte version`                            | Print binary version                           |

> Routine jobs support `acolyte cron --dry-run` (dream proposal-only runs). Dream and heartbeat
> turns run under a session-scoped OpenCode permission ruleset: dream analysis is read-only, the
> dream writer is limited to memory paths, and heartbeat can only run `acolyte inbox`.

> Runtime capability edits are not exposed yet: `acolyte new` preserves existing
> settings/jobs when adopting an agent. See `FUTURE-IDEAS.md`.

## Updating

The bot can fetch and install its own updates from GitHub Releases.

- `acolyte start` runs a non-blocking check at startup. If you're behind, it queues a notification through every running agent's channel.
- `acolyte update --check` is the dry-run command. It prints current vs latest and exits without downloading.
- `acolyte update` downloads the matching release asset, verifies its SHA256 against the `checksums.txt` attached to the release, replaces the binary in place, and (by default) re-execs the new process.
- `acolyte update --version vX.Y.Z` pins a specific release instead of `latest`.
- `acolyte update --restart=false` replaces the binary but leaves the running process alone; restart `acolyte start` to pick it up.
- `acolyte update --yes` skips the confirmation prompt for non-interactive use.

For system-wide installs (`/usr/local/bin`) the replace may hit a read-only file system. Reinstall via `install.sh` into `~/.local/bin`, or run the update under `sudo`.

## Development

### Project Structure

```
.
├── cmd/                    # CLI commands
│   ├── root.go
│   ├── start.go
│   ├── new.go
│   ├── cron.go
│   ├── notify_user.go
│   ├── inbox.go
│   ├── inspect.go
│   └── attach.go
├── internal/
│   ├── agent/             # --config parsing + create/adopt workflow
│   ├── comm/              # Envelope router, outbox sink, Telegram adapter
│   ├── cron/              # Shared scheduler + execution
│   ├── daemon/            # Per-agent adapter lifecycle + routing
│   ├── envelope/          # The Communication ⇄ Orchestrator message type
│   ├── media/             # Media handling
│   ├── opencode/          # OpenCode V2 integration
│   ├── paths/             # $ACOLYTE_HOME + workspace layout
│   ├── registry/          # Global agent identity store
│   ├── routine/           # Registered routines (dream, heartbeat)
│   ├── secrets/           # Layered .env loading
│   ├── store/             # Per-agent SQLite store + module schemas
│   ├── storepool/         # Refcounted shared per-agent stores
│   └── workspace/         # Agent scaffold/validate
├── main.go
└── go.mod
```

### Dependencies

- `github.com/go-telegram/bot` - Telegram Bot API
- `github.com/spf13/cobra` - CLI framework
- `github.com/robfig/cron/v3` - Cron schedule parsing
- `github.com/mattn/go-sqlite3` - SQLite store

### GitHub Workflow

A single GitHub Actions workflow runs automatic PR review with the OpenCode `build` agent using the official [`anomalyco/opencode/github`](https://opencode.ai/docs/github/) action:

- `.github/workflows/opencode-review.yml` — reviews every non-draft PR opened by the repository owner: on open, on each new commit (`synchronize`), on reopen, and when moved out of draft. PRs from any other author never trigger the agent.

Each review posts exactly one PR review whose body starts with a `## Verdict` heading (`LGTM` or `NEEDS_REVIEW`), swaps the matching `LGTM`/`NEEDS_REVIEW` label on the PR, and splits findings into **Necessary** (must fix) and **Non-necessary** (optional). The review hunts for security issues, loopholes, edge cases, concurrency issues, unaccounted logic, and mismatches against the PR description. Every new commit re-runs the review on the latest diff. The workflow removes the action's automatic comment so the review is the only thing left on the PR.

The model is pinned in the workflow itself (`opencode-go/gpt-6-luna`); no project-level `opencode.json` is committed — this is a public repo, and the review's runtime-config guard strips any PR-added `opencode.json`/`opencode.jsonc`/`AGENTS.md`/`.opencode/` (restoring base-branch versions) and any `CLAUDE.md` (unsupported in this project) before the agent runs.

Branch protection keeps `main` safe: pull requests are required, they must come from `dev` in this repository and be authored by the owner, and only the owner has merge rights. The gate (`.github/workflows/pr-guard.yml`) runs from the base branch definition via `pull_request_target` and posts a `pr-guard` commit status on the PR head SHA, so a PR cannot redefine its own gate.

To enable it:

1. Add the provider's API key under *Settings → Secrets and variables → Actions* as `OPENCODE_GO_API_KEY` (either as a repository secret, or as an environment secret on the `OPENCODE_GO_API_KEY` environment — the review job declares that environment). The workflow maps it to `OPENCODE_API_KEY`, the variable the `opencode-go` provider reads.
2. Create the `LGTM` and `NEEDS_REVIEW` labels — or skip this, the review creates them on first use.

Usage:

- Open a PR to get an automatic code review; every new push re-runs the review on the latest diff.
- Land work by merging feature PRs into `dev`, then a `dev` → `main` PR merged by the repository owner.

## License

MIT

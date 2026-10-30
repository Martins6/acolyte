# Acolyte

A lightweight, dynamic Telegram-based agent gateway to OpenCode. The "acolyte" to your OpenCode server — a faithful helper that learns new skills over time.

## Installation

### Prerequisites

**Both OpenCode CLI AND this binary are required to run the bot.**

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

### 2. Initialize Workspace

```bash
acolyte new
```

This creates a workspace at `~/.acolyte/` with template files.

### 3. Configure the Bot

Use the `opencode auth login`, choose your provider. Then also set it for `acolyte`. Please, check [models.dev](https://models.dev/) for the standard reference of names for providers and models used in OpenCode. You can also inspect the logs for any mismatch of the naming.

To set the specific settings for your bot, you can use the following commands:

```bash
# Set your bot token
acolyte config set bot.token "YOUR_BOT_TOKEN"

# Add your Telegram user ID to allowed users
acolyte config set bot.allowed_users [123456789, "YourUserName"]

# Optional: Set default agent
acolyte config set defaults.agent "acolyte"

# Optional: Set default model
acolyte config set defaults.model "MiniMax-M3"

# Optional: Set default provider
acolyte config set defaults.provider "minimax-coding-plan"

# View current configuration
acolyte config list
```

### 4. Start the Bot

```bash
acolyte start
```

This will:

1. Initialize the Telegram bot
2. Begin handling messages

Press `Ctrl+C` to stop gracefully.

## Configuration

The configuration file is stored at `~/.acolyte/config.toml`:

```toml
[bot]
token = "YOUR_BOT_TOKEN"
allowed_users = [123456789]

[workspace]
path = "~/.acolyte/"

[defaults]
agent = "acolyte"
model = "MiniMax-M3"
provider = "minimax-coding-plan"
```

### Configuration Commands

| Command                                      | Description            |
| -------------------------------------------- | ---------------------- |
| `acolyte config set <key> <value>` | Set a config value     |
| `acolyte config get <key>`         | Get a config value     |
| `acolyte config list`              | List all config values |

### Config Keys

| Key                 | Description                       |
| ------------------- | --------------------------------- |
| `bot.token`         | Telegram bot token                |
| `bot.allowed_users` | List of allowed Telegram user IDs |
| `workspace.path`    | Workspace directory path          |
| `defaults.agent`    | Default agent name                |
| `defaults.model`    | Default LLM model                 |
| `defaults.provider` | Default LLM provider              |

## Slash Commands

| Command                | Description        | Example                        |
| ---------------------- | ------------------ | ------------------------------ |
| `/start`               | Start the bot      | `/start`                       |
| `/help`                | Show help message  | `/help`                        |
| `/set-agent <name>`    | Set active agent   | `/set-agent coder`             |
| `/set-model <name>`    | Set LLM model      | `/set-model claude-sonnet-4-5` |
| `/set-provider <name>` | Set LLM provider   | `/set-provider anthropic`      |
| `/workspace <path>`    | Set workspace path | `/workspace /path/to/folder`   |

## Workspace Structure

```
~/.acolyte/
├── AGENTS.md           # Agent definitions
├── SOUL.md            # System operator behavior
├── USER.md            # User information
├── IDENTITY.md        # Model identity
├── BOOTSTRAP.md       # First-time setup
├── TOOLS.md           # Tool definitions
├── downloads/
│   ├── images/        # Downloaded images
│   ├── audio/        # Downloaded audio
│   ├── documents/    # Downloaded documents
│   └── videos/       # Downloaded videos
├── conversations/    # Per-user conversation history
└── .logs/
    └── YYYY-MM-DD.log # Daily log files
```

## Logging

Logs are stored in `~/.acolyte/.logs/` with format `YYYY-MM-DD.log`.

### View Logs

```bash
# View today's logs
acolyte logs today

# View specific date
acolyte logs 2026-03-05
```

Log format:

```
[INPUT]  [2026-03-05 10:30:45] User 123456789: "Hello"
[OUTPUT] [2026-03-05 10:30:46] Response text...
```

Log retention is 30 days (auto-cleanup).

## Media Handling

When you send a **photo** to the bot:

1. The bot downloads the largest available size from Telegram
2. Saves it to `<workspace>/downloads/images/<filename>` using the original Telegram filename (which is guaranteed unique server-side)
3. Builds a prompt of the form `File located at: <abs-path>\n\nUser message: <caption-or-empty>` and forwards it to OpenCode
4. The agent can then read or otherwise act on the file via its normal tools

Photos with no caption are accepted; the prompt will then contain only the file location.

### Supported Media Types

| Telegram Type | Supported? | Notes                                                |
| ------------- | ---------- | ---------------------------------------------------- |
| Photo         | Yes        | Downloaded and forwarded to the agent                |
| Audio / Voice | No         | Silently ignored                                     |
| Document      | No         | Silently ignored                                     |
| Video         | No         | Silently ignored                                     |
| Sticker       | No         | Silently ignored                                     |

Sender-side media (the bot sending photos/documents back to you) is not implemented — all agent replies arrive as plain text messages.

## CLI Commands

| Command                         | Description                 |
| ------------------------------- | --------------------------- |
| `acolyte start`       | Start bot + OpenCode server |
| `acolyte config`      | Configuration management    |
| `acolyte new [path]`  | Initialize new workspace    |
| `acolyte logs [date]` | View logs                   |
| `acolyte update`      | Check & apply updates       |
| `acolyte version`     | Print binary version        |

## Updating

The bot can fetch and install its own updates from GitHub Releases.

- `acolyte start` runs a non-blocking check at startup. If you're behind, it sends a Telegram notification to the allowed user saying which version is available.
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
│   ├── config.go
│   ├── new.go
│   └── logs.go
├── internal/
│   ├── bot/               # Telegram bot
│   ├── config/            # Configuration
│   ├── logger/            # Logging
│   ├── media/             # Media handling
│   ├── opencode/          # OpenCode integration
│   ├── session/           # Session management
│   └── workspace/         # Workspace templates
├── main.go
└── go.mod
```

### Dependencies

- `github.com/go-telegram/bot` - Telegram Bot API
- `github.com/spf13/cobra` - CLI framework
- `github.com/spf13/viper` - Configuration
- `github.com/google/uuid` - UUID generation

### GitHub Workflow

A single GitHub Actions workflow runs automatic PR review with the OpenCode `build` agent using the official [`anomalyco/opencode/github`](https://opencode.ai/docs/github/) action:

- `.github/workflows/opencode-review.yml` — reviews every non-draft PR opened by the repository owner: on open, on each new commit (`synchronize`), on reopen, and when moved out of draft. PRs from any other author never trigger the agent.

Each review posts exactly one PR review whose body starts with a `## Verdict` heading (`LGTM` or `NEEDS_REVIEW`), swaps the matching `LGTM`/`NEEDS_REVIEW` label on the PR, and splits findings into **Necessary** (must fix) and **Non-necessary** (optional). The review hunts for security issues, loopholes, edge cases, concurrency issues, unaccounted logic, and mismatches against the PR description. Every new commit re-runs the review on the latest diff. The workflow removes the action's automatic comment so the review is the only thing left on the PR.

The model is pinned in the workflow itself (`opencode-go/mimo-v2.6-pro`); no project-level `opencode.json` is committed — this is a public repo, and the review's runtime-config guard strips any PR-added `opencode.json`/`opencode.jsonc`/`AGENTS.md`/`CLAUDE.md`/`.opencode/` before the agent runs.

Branch protection keeps `main` safe: pull requests are required, they must come from `dev` in this repository and be authored by the owner (`.github/workflows/pr-guard.yml` enforces this), and only the owner has merge rights.

To enable it:

1. Add the provider's API key under *Settings → Secrets and variables → Actions* as `OPENCODE_GO_API_KEY` (either as a repository secret, or as an environment secret on the `OPENCODE_GO_API_KEY` environment — the review job declares that environment). The workflow maps it to `OPENCODE_API_KEY`, the variable the `opencode-go` provider reads.
2. Create the `LGTM` and `NEEDS_REVIEW` labels — or skip this, the review creates them on first use.

Usage:

- Open a PR to get an automatic code review; every new push re-runs the review on the latest diff.
- Land work by merging feature PRs into `dev`, then a `dev` → `main` PR merged by the repository owner.

## License

MIT

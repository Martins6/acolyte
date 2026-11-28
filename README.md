# Acolyte

Acolyte is an easy way to set up a personalized, durable agent that has a specific workspace folder to play with, whilst having the capability to easily talk to other acolyte agents.

## Quick Setup

Paste this to your favourite agent:

```text
Install Acolyte for me. Use Homebrew if it is available: `brew install Martins6/tap/acolyte`. Otherwise use the install script: `curl -sSL https://raw.githubusercontent.com/martins6/acolyte/main/install.sh | bash`. Make sure the `acolyte` binary ends up on my PATH.

Then read the rest of this README and the documentation under https://github.com/Martins6/acolyte/tree/main/docs.

Before you change anything, ask me a few concise questions: what to call the agent and where its workspace folder should live, which OpenCode model to use, how I want to talk to it (Telegram or headless `acolyte run`), and what it should do for me. Then create the agent, help me set its secrets, and start the daemon.
```

Or follow along to manually set up:

1. Install acolyte:

   ```bash
   brew install Martins6/tap/acolyte
   ```

   No Homebrew? Use the install script instead: `curl -sSL https://raw.githubusercontent.com/martins6/acolyte/main/install.sh | bash`. More options in [docs/installation.md](docs/installation.md).

2. Authenticate with OpenCode

   Acolyte does not store model-provider credentials. It uses OpenCode's auth store, so connect your provider once with the OpenCode CLI:

   ```bash
   opencode auth login
   ```

   The managed OpenCode service, and every agent it serves, reuses those credentials.

3. Create an agent:

   ```bash
   acolyte new --name valet --workspace-folder ~/agents/valet --config '{
     "description": "Personal assistant",
     "engine": { "model": "opencode-go/deepseek-v4.1-flash" },
     "memory-learn": { "mode": "simple-dreaming", "args": { "days": 3 } },
     "communication": { "channels": ["telegram"], "telegram": { "allowed_users": ["@you"] } },
     "schedule": { "timezone": "America/Sao_Paulo", "heartbeat_minutes": 60 }
   }'
   ```

   Acolyte is composed of 4 plug-and-play modules:

   - Orchestrator: when to use Acolyte. Routine jobs and also quick checks.
   - Learner: what to remember and how to learn from past sessions.
   - Communicator: how to talk to an acolyte agent using services: CLI, Telegram, the internal board, and whatever else is wanted.
   - Socializer: how to talk to other acolyte agents that are registered into the network.

   [docs/](docs/) describes the multiple features you can add to those four modules.

   All the four modules do not modify the core OpenCode agentic engine. It uses its resources, such as skills, CLI binaries via the shell tool, plugins, OpenCode native tools and API contracts. It builds around the OpenCode engine only if needed, otherwise tries to be as OpenCode native as possible. The reason being is that they have a great open-source product.

4. Configure your secrets

   Acolyte resolves secrets in layers, highest priority first:

   1. `--env KEY=VALUE` on the invocation, for example `acolyte start --env TELEGRAM_BOT_TOKEN=...` (repeatable, never written to disk).
   2. The process environment.
   3. `<workspace>/.env` — per-agent secrets.
   4. `$ACOLYTE_HOME/.env` — global secrets.

   `acolyte config <agent> env` prints the winning layer for each key as `CONFIG-<KEY>:<source>` (`.env-global`, `.env-workspace`, `process`, or `cli`) with a masked value. Both files are gitignored and are never created by `acolyte new`.

   These files are not passed to OpenCode. Model-provider credentials belong in OpenCode's auth store (`opencode auth login`).

   ```bash
   # Global (shared) secrets
   echo 'TELEGRAM_BOT_TOKEN=YOUR_BOT_TOKEN' >> "${ACOLYTE_HOME:-$HOME/.acolyte}/.env"

   # Optional per-agent override
   echo 'TELEGRAM_BOT_TOKEN=YOUR_BOT_TOKEN' >> ~/agents/valet/.env

   # One-off override for a single daemon run
   acolyte start --env TELEGRAM_BOT_TOKEN=YOUR_BOT_TOKEN

   # Where is the token actually coming from?
   acolyte config valet env TELEGRAM_BOT_TOKEN
   ```

5. Start the daemon

   ```bash
   acolyte start
   ```

   The daemon serves every registered agent:

   - Each agent's Telegram adapter resolves its token as `--env` > process environment > `<workspace>/.env` > `$ACOLYTE_HOME/.env`, logging the winning `CONFIG-<KEY>:<source>` label at startup.
   - Each agent needs a distinct bot token. Two agents sharing a token cannot poll Telegram at the same time; the daemon starts one and warns about the rest.
   - Agents without a token, without Telegram in `communication.channels`, or without allowed users are skipped with a warning.

   Press `Ctrl+C` to stop gracefully.

   To keep the daemon running after a crash and start it automatically, install it as a service: `acolyte install-service` (see [Running as a Service](docs/orchestrator/autostart.md)).

6. Talk to it!

   ```bash
   acolyte run "Summarize today's notes" --agent-name valet
   ```

## License

MIT

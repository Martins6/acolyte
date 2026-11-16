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

4. Configure your secrets

   Acolyte loads secrets from two optional `.env` files:

   - `$ACOLYTE_HOME/.env` — global secrets.
   - `<workspace>/.env` — per-agent secrets, overriding the global file.

   Precedence is **process environment > workspace `.env` > global `.env`**. Both files are gitignored and are never created by `acolyte new`.

5. Start the daemon

   ```bash
   acolyte start
   ```

   The daemon serves every registered agent:

   - Each agent's Telegram adapter reads its token from the process environment, falling back to `<workspace>/.env` and then `$ACOLYTE_HOME/.env`.
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

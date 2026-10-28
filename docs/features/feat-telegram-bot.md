# Overview

Core Telegram bot that enables users to interact with OpenCode AI agent from Telegram. Handles text messages, photo uploads, slash commands, and enforces user access control.

# Details

- Implements Telegram Bot API using go-telegram/bot library
- Handles text messages and forwards to OpenCode
- Accepts media attachments: photos, documents, audio files, voice messages, and videos. Each is downloaded from Telegram, saved under `<workspace>/downloads/<type>/` with UTC-timestamped filenames (e.g. `20240706_143052.pdf`, with `_1`/`_2` collision suffixes for same-second duplicates), and forwarded to OpenCode with a metadata-rich prompt:

  ```
  File located at: <abs-path>
  File type: <Photo|Document|Audio|Voice|Video>
  File size: <bytes> bytes
  Original name: <name>      ← only when Telegram provides FileName
  MIME type: <mime>           ← only when Telegram provides MimeType

  User message: <caption>
  ```

  `Original name` and `MIME type` lines are omitted when Telegram does not provide them (common for photos and voice messages). The trailing `User message:` line is always present, even when the caption is empty.
- Stickers, contacts, and locations are still ignored.
- Sender-side media (the bot sending photos/documents back to Telegram) is NOT implemented; all replies are plain text
- Implements slash commands: /set-agent, /set-model, /set-provider, /workspace, /help, /new-session
- Restricts access to the configured allowed user
- Manages per-user conversation sessions (in-memory)
- Integrates with the local `opencode` CLI via `opencode run` (NDJSON)

# File Paths

- cmd/root.go - CLI root command
- cmd/start.go - Start command to launch bot
- cmd/config.go - Config management commands
- cmd/new.go - Workspace initialization
- cmd/logs.go - Log viewing command
- internal/bot/client.go - Bot initialization
- internal/bot/handlers.go - Message handlers (text + media)
- internal/bot/commands.go - Slash command implementations
- internal/config/config.go - Configuration loading
- internal/opencode/runner.go - OpenCode CLI runner
- internal/media/downloader.go - Telegram file downloader + prompt builder
- internal/session/manager.go - Session management
- internal/logger/logger.go - Logging system
- internal/workspace/template.go - Workspace file/directory templates
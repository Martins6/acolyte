# Overview

Telegram bot gateway that allows users to interact with an OpenCode AI agent directly from Telegram. The bot handles text messages, photo uploads, slash commands, and restricts access to the allowed user.

# Files

- feat-telegram-bot.md - Core Telegram bot with message handling, media support (photos, documents, audio, voice, video with UTC-timestamped filenames), and user authentication
- feat-install-script.md - Installation script for easy binary deployment
- feat-opencode-run-integration.md - OpenCode run command integration replacing HTTP server
- feat-progress-updates.md - Progress updates for long-running operations
- feat-telegram-agent.md - Custom telegram-agent in opencode.json configuration
- feat-default-prompts.md - Improved default prompts for workspace template files
- feat-notify-mail-cron.md - SQLite-based notification and mail system with scheduler, timezone gating, and runtime config hot-reload
- feat-self-update.md - In-app self-update via GitHub releases with `acolyte update`/`version` and non-blocking startup outdated-check

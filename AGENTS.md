# Project: Acolyte

A lightweight, dynamic Telegram-based agent gateway to OpenCode. The "acolyte" to your OpenCode server — a faithful helper that learns new skills over time.

## Core Tech

- **Go 1.21+** - Backend language
- **go-telegram/bot** - Telegram Bot API
- **spf13/cobra** - CLI framework
- **OpenCode CLI** - AI agent server (runs locally on port 4096)

## Features

- Text messaging with OpenCode
- Media support (images, audio, documents, video)
- Slash commands for agent/model/provider configuration
- Per-user session management
- Structured logging with 30-day retention

# Orchestrator / autostart (always-on)

**Intent.** Run the `acolyte` daemon at boot and keep it up. Mechanism is per-OS.

## macOS — `launchd`

| Scope | Plist | Load |
| ----- | ----- | ---- |
| Per-user (at login) | `~/Library/LaunchAgents/ai.acolyte.daemon.plist` | `launchctl bootstrap gui/501 <plist>` |
| System-wide (at boot, before login) | `/Library/LaunchDaemons/ai.acolyte.daemon.plist` | `sudo launchctl bootstrap system <plist>` |

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>ai.acolyte.daemon</string>
  <key>ProgramArguments</key>
  <array>
    <string>/Users/YOU/.local/bin/acolyte</string>
    <string>start</string>
  </array>
  <key>WorkingDirectory</key><string>/Users/YOU/.acolyte</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>/Users/YOU/.acolyte/.logs/daemon.out.log</string>
  <key>StandardErrorPath</key><string>/Users/YOU/.acolyte/.logs/daemon.err.log</string>
</dict></plist>
```

Unload: `launchctl bootout gui/501 ai.acolyte.daemon` (or `sudo launchctl bootout system/…`).
Restart: `launchctl kickstart -k gui/501/ai.acolyte.daemon`.

## Linux — `systemd`

| Scope | Unit | Enable |
| ----- | ---- | ------ |
| System | `/etc/systemd/system/acolyte.service` | `systemctl enable --now acolyte` |
| User | `~/.config/systemd/user/acolyte.service` | `systemctl --user enable --now acolyte` |

For a **user** unit to run at boot without logging in: `loginctl enable-linger $USER`.

```ini
[Unit]
Description=Acolyte orchestrator
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/home/you/.local/bin/acolyte start
WorkingDirectory=/home/you/.acolyte
Restart=always
RestartSec=5
Environment=ACOLYTE_HOME=/home/you/.acolyte
EnvironmentFile=-/home/you/.acolyte/.env

[Install]
WantedBy=multi-user.target        # use default.target for a --user unit
```

Logs: `journalctl -u acolyte -f` (system) or `journalctl --user -u acolyte -f` (user).

## Nice-to-have
`acolyte install-service [--user|--system]` writes the right plist/unit automatically — see
`FUTURE-IDEAS.md`.

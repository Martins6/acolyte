# Orchestrator / autostart (always-on)

**Intent.** Run the `acolyte` daemon at login or boot and keep it up.

## Install the service

`acolyte install-service` writes the native definition, loads it, and starts it:

```bash
acolyte install-service                 # current user, starts at login
sudo acolyte install-service --system   # starts at boot (requires root)
acolyte install-service --print         # show the unit and commands first
```

The service runs the binary that invoked the command. Re-run it after moving or
reinstalling `acolyte`. Stop any foreground `acolyte start` first: Telegram
allows only one long-poller per bot token.

| Platform | Scope | Definition | Starts |
| -------- | ----- | ---------- | ------ |
| macOS | `--user` (default) | `~/Library/LaunchAgents/ai.acolyte.daemon.plist` | at login |
| macOS | `--system` | `/Library/LaunchDaemons/ai.acolyte.daemon.plist` | at boot |
| Linux | `--user` (default) | `~/.config/systemd/user/acolyte.service` | at login |
| Linux | `--system` | `/etc/systemd/system/acolyte.service` | at boot |

`--system` installs run as the invoking user (`SUDO_USER` when run through
`sudo`), so `$ACOLYTE_HOME` and the SQLite stores keep their normal ownership.

If the platform is unsupported, the command prints manual instructions for both
Linux and macOS. If `--system` is requested without root, it prints the current
platform's instructions plus the exact `sudo` command instead of writing
anything.

## Crash recovery

- **launchd:** `RunAtLoad` and `KeepAlive` restart the job, with a 10 second
  `ThrottleInterval` so a failing start does not spin.
- **systemd:** `Restart=always`, `RestartSec=5`, and `StartLimitIntervalSec=0`
  disable the start-rate limit so the daemon keeps retrying indefinitely.

## Logging

- **Linux:** journald. `journalctl --user -u acolyte -f` for a user unit, or
  `journalctl -u acolyte -f` for a system unit. Rotation is handled by journald.
- **macOS:** `$ACOLYTE_HOME/logs/daemon.log`, rotated at 10 MiB with three
  backups (`daemon.log.1` … `daemon.log.3`). Crash output goes to
  `$ACOLYTE_HOME/logs/daemon.err.log`.

`acolyte start --log-file <path>` uses the same rotating writer without a
service manager. All service definitions bake `PATH`, `HOME`, and
`ACOLYTE_HOME`, because launchd and systemd do not inherit a login shell's
environment (notably, `opencode` must be findable).

## Manual reference

### macOS — `launchd`

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>ai.acolyte.daemon</string>
  <key>ProgramArguments</key>
  <array>
    <string>/Users/YOU/.local/bin/acolyte</string>
    <string>start</string>
    <string>--log-file</string>
    <string>/Users/YOU/.acolyte/logs/daemon.log</string>
  </array>
  <key>WorkingDirectory</key><string>/Users/YOU/.acolyte</string>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ThrottleInterval</key><integer>10</integer>
  <key>EnvironmentVariables</key><dict>
    <key>HOME</key><string>/Users/YOU</string>
    <key>ACOLYTE_HOME</key><string>/Users/YOU/.acolyte</string>
    <key>PATH</key><string>/Users/YOU/.opencode/bin:/Users/YOU/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
  </dict>
  <key>StandardOutPath</key><string>/Users/YOU/.acolyte/logs/daemon.err.log</string>
  <key>StandardErrorPath</key><string>/Users/YOU/.acolyte/logs/daemon.err.log</string>
  <!-- Required for /Library/LaunchDaemons, omit for ~/Library/LaunchAgents. -->
  <key>UserName</key><string>YOU</string>
</dict></plist>
```

```bash
mkdir -p ~/.acolyte/logs
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/ai.acolyte.daemon.plist
# system scope: sudo launchctl bootstrap system /Library/LaunchDaemons/ai.acolyte.daemon.plist
```

Unload: `launchctl bootout gui/$(id -u)/ai.acolyte.daemon` (or
`sudo launchctl bootout system/ai.acolyte.daemon`).
Restart: `launchctl kickstart -k gui/$(id -u)/ai.acolyte.daemon`.

### Linux — `systemd`

```ini
[Unit]
Description=Acolyte orchestrator
StartLimitIntervalSec=0
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
Environment="HOME=/home/you"
Environment="ACOLYTE_HOME=/home/you/.acolyte"
Environment="PATH=/home/you/.opencode/bin:/home/you/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin:/snap/bin"
WorkingDirectory="/home/you/.acolyte"
ExecStart="/home/you/.local/bin/acolyte" "start"
Restart=always
RestartSec=5
SyslogIdentifier=acolyte
StandardOutput=journal
StandardError=journal
# System units only (omit for a --user unit):
User=you

[Install]
WantedBy=default.target      # multi-user.target for a system unit
```

```bash
systemctl --user daemon-reload
systemctl --user enable acolyte.service
systemctl --user restart acolyte.service
# system scope: sudo systemctl daemon-reload && sudo systemctl enable --now acolyte.service
```

For a **user** unit to run at boot without logging in:
`loginctl enable-linger "$USER"` (the installer attempts this and prints the
command if it fails).

Logs: `journalctl --user -u acolyte -f` (user) or `journalctl -u acolyte -f`
(system).

## Removal

```bash
# macOS, user
launchctl bootout gui/$(id -u)/ai.acolyte.daemon
rm ~/Library/LaunchAgents/ai.acolyte.daemon.plist

# macOS, system
sudo launchctl bootout system/ai.acolyte.daemon
sudo rm /Library/LaunchDaemons/ai.acolyte.daemon.plist

# Linux, user
systemctl --user disable --now acolyte.service
rm ~/.config/systemd/user/acolyte.service
systemctl --user daemon-reload

# Linux, system
sudo systemctl disable --now acolyte.service
sudo rm /etc/systemd/system/acolyte.service
sudo systemctl daemon-reload
```

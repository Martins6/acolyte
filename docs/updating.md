# Updating

Acolyte can fetch and install its own updates from
[GitHub Releases](https://github.com/Martins6/acolyte/releases). The downloaded archive is verified
against the `checksums.txt` attached to the release, and on macOS the replaced binary is re-signed
ad-hoc.

| Command | Effect |
| ------- | ------ |
| `acolyte update --check` | Print current vs latest and exit; nothing is downloaded. |
| `acolyte update` | Download the release asset, verify its SHA-256, replace the binary in place, and re-exec the new binary. |
| `acolyte update --version vX.Y.Z` | Pin a specific release instead of `latest`. |
| `acolyte update --restart=false` | Replace the binary but leave the running process alone; restart `acolyte start` to pick it up. |
| `acolyte update --yes` | Skip the confirmation prompt for non-interactive use. |

`acolyte start` runs a non-blocking check at startup. When the daemon is behind, it queues a
notification through every running agent's channel (and logs the version when no agent can deliver),
but it never installs anything on its own.

Homebrew installs are better upgraded with `brew upgrade acolyte`, which keeps the formula's
version tracking intact; `acolyte update` also works because it replaces the binary in place.

## Read-only install locations

For system-wide installs (`/usr/local/bin`) the replace may hit a read-only file system. Reinstall
via the install script into `~/.local/bin` (see [`installation.md`](installation.md)), or run the
update under `sudo`.

# Installation

**Intent.** Get the `acolyte` binary on the machine and wired to OpenCode's credentials.

## Prerequisites

**Both the OpenCode CLI and this binary are required to run the daemon.**

- [OpenCode CLI](https://github.com/anomalyco/opencode) installed, with `opencode` on `PATH`.
- Model-provider credentials stored with OpenCode:

  ```bash
  opencode auth login
  ```

Acolyte does not store model-provider credentials. The managed OpenCode service, and every agent it
serves, reuses OpenCode's auth store. Prefer that over putting provider keys in Acolyte `.env` files:
those are for Acolyte's own secrets, such as `TELEGRAM_BOT_TOKEN` (see
[Secrets](README.md#secrets)).

## Homebrew (macOS and Linux)

```bash
brew install Martins6/tap/acolyte
```

The formula lives in [`Martins6/homebrew-tap`](https://github.com/Martins6/homebrew-tap) and uses the
platform archives plus the SHA-256 checksums attached to each
[release](https://github.com/Martins6/acolyte/releases). Upgrade with `brew upgrade acolyte`; the
`acolyte update` path is documented in [`updating.md`](updating.md).

## Install script (macOS and Linux)

```bash
curl -sSL https://raw.githubusercontent.com/martins6/acolyte/main/install.sh | bash
```

The script installs the latest release for the detected OS/architecture into `~/.local/bin`. If that
directory is not on `PATH`, add it:

```bash
export PATH="$HOME/.local/bin:$PATH"
```

Options:

| Flag | Meaning |
| ---- | ------- |
| `-p, --prefix <dir>` | Install into `<dir>` instead of `~/.local/bin`. |
| `-v, --version <tag>` | Install a specific release, for example `v0.7.0`, instead of the latest. |
| `-h, --help` | Print usage. |

For a system-wide install, run the script with enough privileges — the prefix must be writable by the
user running it:

```bash
curl -sSL https://raw.githubusercontent.com/martins6/acolyte/main/install.sh | sudo bash -s -- -p /usr/local/bin
```

## Development install

To build from local source instead of downloading a release:

```bash
git clone https://github.com/Martins6/acolyte.git
cd acolyte
./local_install.sh
```

- Requires Go 1.23+ (see `go.mod`) and the OpenCode CLI.
- Installs into `~/.local/bin`, falling back to `$(go env GOPATH)/bin` and then `/usr/local/bin`
  when that is writable.
- On macOS it ad-hoc codesigns the binary and then verifies that it runs.

## Verify

```bash
acolyte version
acolyte --help
```

Next: create an agent, add its secrets, and start the daemon — see the
[quick setup](../README.md#quick-setup), the
[`acolyte new` config reference](README.md#creating-an-agent), and [`cli.md`](cli.md).

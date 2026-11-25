# Development

## Project structure

```text
.
├── cmd/                    # one file per CLI command (new, start, cron, run, inspect, ...)
├── e2e/                    # Opt-in live end-to-end tests (build tag: e2e)
├── internal/
│   ├── agent/             # --config parsing + create/adopt workflow
│   ├── comm/              # Envelope router, outbox sink, Telegram adapter + test emulator
│   ├── cron/              # Shared scheduler + execution
│   ├── daemon/            # Per-agent adapter lifecycle + routing
│   ├── envelope/          # The Communication ⇄ Orchestrator message type
│   ├── media/             # Media handling
│   ├── opencode/          # OpenCode V2 integration
│   ├── paths/             # $ACOLYTE_HOME + workspace layout
│   ├── registry/          # Global agent identity store
│   ├── routine/           # Registered routines (dream, heartbeat)
│   ├── secrets/           # Layered .env loading
│   ├── store/             # Per-agent SQLite store + module schemas
│   ├── storepool/         # Refcounted shared per-agent stores
│   └── workspace/         # Agent scaffold/validate
├── main.go
└── go.mod
```

## Dependencies

- `github.com/go-telegram/bot` — Telegram Bot API
- `github.com/spf13/cobra` — CLI framework
- `github.com/robfig/cron/v3` — Cron schedule parsing
- `github.com/mattn/go-sqlite3` — SQLite store
- `github.com/subosito/gotenv` — layered `.env` loading

## Tests

Unit tests need no provider key and no OpenCode service:

```bash
go test ./...
go test -race -count=1 ./...
```

The Telegram communication layer has a hermetic regression suite backed by an in-process Bot API
emulator (`internal/comm/telegram/telegramtest`). It drives the real adapter over HTTP — polling,
dedup, authorization, media downloads, chunking, retries, replay, and daemon routing — without a bot
token or Telegram network access:

```bash
go test -race -count=1 -v -run EmulatedTelegram ./internal/comm/telegram/... ./internal/daemon/
```

Live end-to-end tests build the CLI and run real turns against the OpenCode service configured on the
machine:

```bash
ACOLYTE_E2E=1 \
ACOLYTE_E2E_MODEL=opencode-go/gpt-6-luna \
go test -tags=e2e ./e2e/... -v -count=1
```

The live suite is deliberately small (three live turns plus one zero-cost failure case, including one
turn through the Telegram emulator and the real daemon). CI runs the unit and Telegram emulator
suites on every PR. In `.github/workflows/test.yml` the live job runs after merges to `dev` (and on
manual dispatch), never on PRs, because the provider credential must not be exposed to PR-controlled
code. It has a hard 15-minute timeout.

## GitHub workflows

- `.github/workflows/test.yml` — `go vet` and `go test -race ./...` on every PR (`test / unit`), plus
  a dedicated hermetic `test / telegram` job for the Bot API emulator suite. After merges to `dev`
  (and on manual dispatch) it starts the managed OpenCode service with `OPENCODE_GO_API_KEY` and
  runs the live E2E suite as `test / e2e`, capped at 15 minutes. PR jobs in this workflow never
  receive the provider credential.
- `.github/workflows/opencode-review.yml` — reviews every non-draft PR opened by the repository
  owner: on open, on each new commit (`synchronize`), on reopen, and when moved out of draft. PRs
  from any other author never trigger the agent. Because it calls the `opencode-go` model on the PR,
  this workflow *does* receive `OPENCODE_GO_API_KEY`, but only for the owner's non-draft PRs from
  this repository. Each review posts exactly one PR review whose body starts with a `## Verdict`
  heading (`LGTM` or `NEEDS_REVIEW`), swaps the matching label, and splits findings into
  **Necessary** (must fix) and **Non-necessary** (optional). Every new commit re-runs the review on
  the latest diff. The review state is the merge gate: `NEEDS_REVIEW` is submitted as a
  changes-requested review, which blocks the merge on protected branches, and `LGTM` as an approval,
  which supersedes the bot's earlier changes-requested verdict. A follow-up step reads the verdict
  back from the review posted for the head SHA and re-submits it in the matching state if the agent
  used the wrong one, so the gate always follows the verdict. An approval is the only way to clear a
  changes-requested review here, because the Actions token cannot dismiss reviews on a protected
  branch.
- `.github/workflows/pr-guard.yml` — verifies that a PR to `main` comes from `dev` in this repository
  and posts a `pr-guard` commit status on the PR head SHA, from the base-branch definition via
  `pull_request_target`, so a PR cannot redefine its own gate.

Branch protection keeps `main` safe: pull requests are required, they must come from `dev` in this
repository and be authored by the owner, and only the owner has merge rights. Mark `test / unit` and
`test / telegram` as required status checks for PRs; the live E2E is a post-merge check on `dev`
(and manual dispatch). See [`ci/review.md`](ci/review.md) for the PR #85 review-check investigation
and [`orchestrator/merge-policy.md`](orchestrator/merge-policy.md) for the merge-red policy and the
stop-on-red trigger.

## Enabling the review and E2E jobs

1. Add the provider's API key under *Settings → Secrets and variables → Actions* as
   `OPENCODE_GO_API_KEY` (a repository secret, or an environment secret on the `OPENCODE_GO_API_KEY`
   environment — the review and E2E jobs declare that environment). The workflows map it to
   `OPENCODE_API_KEY`, the variable the `opencode-go` provider reads.
2. Create the `LGTM` and `NEEDS_REVIEW` labels — or skip this, the review creates them on first use.
3. Enable *Settings → Actions → General → Workflow permissions → Allow GitHub Actions to create and
   approve pull requests* (`can_approve_pull_request_reviews`). Without it `gh pr review --approve`
   fails and a PR that later passes review stays stuck on the bot's earlier changes-requested
   verdict.

## Landing changes

Open a PR to get an automatic code review; every new push re-runs the review on the latest diff.
Land feature PRs by merging into `dev`, then merge a `dev` → `main` PR as the repository owner.

The review model is pinned in the workflow itself (`opencode-go/gpt-6-luna`); no project-level
`opencode.json` is committed. Because this is a public repo, the review's runtime-config guard strips
any PR-added `opencode.json`/`opencode.jsonc`/`AGENTS.md`/`.opencode/` (restoring base-branch
versions) and any `CLAUDE.md` before the agent runs.

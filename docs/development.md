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

## Turn recovery

Turns are resumable. The marker returned by `prompt`/`synthetic` is persisted at admission
(`TurnConfig.OnAdmitted` → `comm_tg_inbox.opencode_message_id` + `admitted_at`), so recovery
re-collects a finished turn instead of admitting the message a second time.

Where it lives:

| Concern | Location |
|---|---|
| Admission hook | `internal/opencode/turn.go` (`Admitted`, `admitInput`) |
| Collect-only probe | `internal/opencode/client.go` (`CollectDelivered`, `SessionTime.InFlight`) |
| Reconcile + escalation | `internal/comm/router.go` (`Reconcile`, `Recover`) |
| Replay wiring | `internal/comm/telegram/replay.go` (`reconcileItem`) |
| Store state | `internal/store/commtg/inbox.go` (`MarkInboundAdmitted`, `RecordResumeAttempt`) |
| Cron recovery | `internal/cron/service.go` (`recoverPendingTurns`) |

Behaviour worth remembering when changing this code:

- A marker must be stored **while** the turn runs, not when it finishes — that invariant is what makes
  restart recovery possible. `TestRouterPersistsMarkerBeforeTurnCompletes` guards it.
- Admission also enqueues a durable acknowledgement (`ack_<inbound>`), so a running turn is never
  silent even though the typing indicator and 👀 reaction are best-effort. It says only that work
  started; the reply, an error, or an operator notice reports the outcome. Reconciliation heals a
  missing ack (crash between marker and ack) using the same stable identity.
- Recovery probes; it never waits and never re-prompts. Escalation is the only path that admits again,
  and only into a fresh session for input that provably never executed. A session still in flight past
  the grace window is interrupted and settled as incomplete, never re-admitted.
- A collection error after admission keeps the marker and the reply identity free; replay reconciles
  and queues the real reply when it exists. Only definitive failures settle with a notice.
- Definitive outcomes settle with a notice and processed row: an empty finished turn, a truncated
  output, a consumed marker with nothing to collect, and permanent probe failures (missing session,
  rejected request). Only transient probe errors stay pending.
- Cron recovery checks `SessionTime.InFlight()` before collecting, and a run that errors after
  admission keeps its `pending_*` marker until a successful collection clears it. Permanent probe
  errors, truncated output, and an empty delivered turn settle the run as incomplete; a marker still
  queued in the inbox stays recoverable.
- Reply envelope IDs are derived from the inbound (`out_<inbound>`) so re-sending is idempotent.
  Acknowledgements (`ack_`) and operator notices (`sys_`) use their own identities so they can never
  occupy or suppress the reply slot.
- Operator notices use `ACOLYTE SYSTEM MESSAGE:` and never claim success that did not happen.
- Cron routine bodies are never replayed; an interrupted routine is reported as incomplete.

Grace window (`resumeGraceWindow`, `cronResumeGrace`) and the attempt cap (`maxResumeAttempts`) are
package variables so tests can shorten them.

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
  the latest diff.
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

## Landing changes

Open a PR to get an automatic code review; every new push re-runs the review on the latest diff.
Land feature PRs by merging into `dev`, then merge a `dev` → `main` PR as the repository owner.

The review model is pinned in the workflow itself (`opencode-go/gpt-6-luna`); no project-level
`opencode.json` is committed. Because this is a public repo, the review's runtime-config guard strips
any PR-added `opencode.json`/`opencode.jsonc`/`AGENTS.md`/`.opencode/` (restoring base-branch
versions) and any `CLAUDE.md` before the agent runs.

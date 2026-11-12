# Acolyte — Implementation Tasks

## Autopilot Run Instructions

This file is the execution plan for an autonomous run. Follow these rules unless the user says
otherwise:

- Use the `implementation-plan` workflow: create/accept the plan, then auto-approve each task.
  Track execution with sbeads (epic + child tasks); run `sb show <task>` before starting and
  `sb close <task>` when done.
- Work one task at a time, in the order below unless a dependency changes. Keep each PR focused on
  one task; do not mix unrelated fixes.
- Branch from the latest `origin/dev` and open every PR against `dev`.
- Before opening a PR, run `go vet ./...` and `go test ./...` locally. Include the commands and
  results in the PR body.
- After opening a PR, wait for **two** PR checks (`unit` and `telegram`). Do not wait for a third
  check or for the review agent to finish. If the two checks have completed, merge the PR even if
  they are red. Use `--admin` only if branch protection blocks the merge.
- If a check never appears, wait a bounded 10 minutes, record that in the PR, and merge anyway.
- Do not stop to ask for approval between tasks. If a task is genuinely blocked by a decision,
  credential, or manual step, stop and record the blocker in `# Next Steps`.
- At the end, add a final `# Next Steps` section at the bottom of this file with comments,
  follow-ups, and anything that looked suspicious during the run. Include the merged PR numbers and
  the CI outcome for each task.

Work from the SPEC v0 implementation audit. Tasks 1–4 are code fixes. Task 5 standardizes the media
directory layout. Task 6 adds single-image outbound media through an OpenCode tool and
`acolyte notify-user --media`. Task 7 updates `SPEC.md` and the affected docs.

Agreed scope for media: **one image per message, photo only**, stored under `media/outbound/`.
Inbound media moves to `media/inbound/`.

## 1. Preserve an explicit `communication.channels` value in `acolyte new`

**Priority:** high  
**Area:** agent creation / runtime config

**Problem.** `internal/agent/create.go` only persists communication settings when
`hasTelegram(cfg)` is true. An explicit `"channels": []` therefore writes no setting, and
`acolyte config get` later reports the default `["telegram"]` because a missing setting defaults to
Telegram. Any `allowed_users` in the same config are dropped as well.

**Work.**
- Persist `comm_tg_settings.channels` for every validated config, including `[]`.
- Persist `telegram.allowed_users` from the same config even when `channels` is empty, so
  `acolyte config get` round-trips the user's input. The daemon will still ignore the channel while
  `channels` is empty.
- Keep adoption semantics: do not overwrite existing settings/jobs when an existing folder is
  adopted.
- Keep the existing symlink and write-safety behavior.

**Acceptance.**
- `acolyte new ... --config '{"communication":{"channels":[]}}'` stores `[]`; `acolyte config get`
  reports `"channels": []`.
- The same config with `telegram.allowed_users` round-trips those users.
- Existing create/adopt tests pass; add regression coverage in `internal/agent/create_test.go`
  and/or `cmd/config_test.go`.

## 2. Normalize `allowed_users` during `acolyte new`

**Priority:** medium  
**Area:** input validation / agent creation

**Problem.** `internal/agent/config.go` only trims each allowed user. It does not drop empty
entries or deduplicate. A config containing `[""]` is persisted as configured; with a token
present, the daemon sees a non-empty allowed list, starts a Telegram adapter, and then rejects
every user. `acolyte config set` already normalizes users.

**Work.**
- Reuse or extract the normalization used by `acolyte config set`: trim, drop empty entries, and
  deduplicate while preserving order.
- Apply it in `Config.Validate` so `acolyte new` and `acolyte config get` agree.
- Store only the normalized, non-empty list.

**Acceptance.**
- `["", " @a ", "@a"]` normalizes to `["@a"]`.
- `[""]` normalizes to an empty list and is not persisted.
- Tests cover both cases in `internal/agent/config_test.go`, plus persisted state in
  `internal/agent/create_test.go`.

## 3. Make routine cron jobs run in their own session

**Priority:** medium  
**Area:** cron CLI / routine execution

**Problem.** `cmd/cron.go` assigns the latest session to every cron job kind. A manually created
`acolyte cron --routine heartbeat` or `--routine dream` without `--session-id` is therefore bound
to the latest session, and `runRoutine` runs there. That contradicts SPEC §10.3: heartbeat runs in
its own session, and dream uses its own cron session. Capability jobs created by `acolyte new` are
already correct because they leave `SessionID` empty.

**Work.**
- Only apply the “latest session” default to `prompt` and `shell` jobs.
- Leave `SessionID` empty by default for `routine` jobs so `runRoutine` uses `cron:<job ID>`.
- Decide the explicit `--session-id` behavior for routines. Recommended: reject it for routine
  jobs, since routines are headless and own their session. If it is kept, document that it
  overrides the normal isolation.
- Leave `acolyte new` capability jobs and `acolyte config`-managed jobs unchanged.

**Acceptance.**
- Creating a routine job without `--session-id` stores `session_id = ''`.
- A tick executes it in `cron:<job ID>`; `acolyte cron view` reflects the chosen behavior.
- Tests cover prompt/shell defaulting and routine isolation in `cmd/cron_test.go` and
  `internal/cron/service_test.go`.

## 4. Implement OpenCode `event.subscribe()` support

**Priority:** high (scope decision required)  
**Area:** OpenCode transport / turns / live status

**Problem.** SPEC §5.1 promises that `event.subscribe()` streams tool activity and text deltas to
Telegram, learning, and live status. There is currently no event-subscription implementation;
`RunUserTurn`/`RunSyntheticTurn` use `POST /api/experimental/session/{id}/wait` plus paginated
`GET /api/session/{id}/message` polling.

**Work (split into reviewable phases).**
- **4a — contract + client.** Add a thin SSE client for `GET /api/event` with Basic auth,
  cancellation, bounded reconnect/backoff, and a `types` filter. Parse `V2EventEncoded` /
  `V2Event` payloads. Add `/api/event` and the event schemas used by Acolyte to
  `internal/opencode/contract_test.go`.
- **4b — turn integration.** Use events for turn completion (`session.idle`), text deltas
  (`session.text.delta`), and tool activity (`session.tool.*`). Keep the public
  `RunUserTurn`/`RunSyntheticTurn` result shape stable. Fall back to the current polling path when
  the stream is unavailable, so a stream failure cannot hang a turn.
- **4c — consumers.** Choose the v0 consumers. Recommended: live agent status and the learning
  feed first; Telegram partial-text streaming is a follow-up unless explicitly required, because it
  needs message editing and rate limiting. Do not silently promise Telegram streaming in v0.
- **4d — tests.** Add an `httptest` SSE server covering normal events, filtered event types,
  reconnect, cancellation, malformed frames, and turn completion.

**Acceptance.**
- A turn can be observed end-to-end through `event.subscribe()` with no regression to turn
  completion or reply collection.
- The contract test fails if the event endpoint or the schemas used by the client disappear.
- `SPEC.md` clearly says whether Telegram live streaming is in or out of v0.

**Note.** If this is not v0 scope, move the feature to `FUTURE-IDEAS.md` and soften/remove the
§5.1 promise instead of leaving it unimplemented.

## 5. Standardize media storage on `media/inbound` and `media/outbound`

**Priority:** medium-high  
**Area:** workspace layout / media paths

**Problem.** Inbound media currently lands in `<workspace>/downloads/<type>/`, and there is no
outbound media convention. We want one predictable layout for both directions.

**Target layout.**

```text
<workspace>/media/
├── inbound/
│   ├── images/
│   ├── audio/
│   ├── documents/
│   └── videos/
└── outbound/
    └── images/
```

**Work.**
- Make `internal/media` direction-aware: inbound paths use `media/inbound/<type>/`, outbound paths
  use `media/outbound/images/` for the v0 image case.
- Update the Telegram inbound adapter to save downloaded files under `media/inbound/<type>/`.
- Scaffold `media/inbound/` and `media/outbound/` in `acolyte new`; add them to the agent
  `.gitignore` behavior.
- Keep legacy `downloads/` paths readable so old `comm_tg_inbox.media_path` /
  `comm_tg_media.file_path` rows and old transcripts still work. New writes go to `media/`.
- Leave non-media artifacts alone for now. In particular, memory trace bundles remain under
  `downloads/memory-traces/`.
- Update path-focused tests and any fixtures that assume `downloads/`.

**Acceptance.**
- A new inbound image is saved under `<workspace>/media/inbound/images/`.
- New outbound images are stored under `<workspace>/media/outbound/images/`.
- Existing media references under `downloads/` still resolve and export correctly.
- Workspace/scaffold tests assert the new directories and ignore rules.

## 6. Add outbound single-image sending

**Priority:** high  
**Area:** outbound media / Telegram / OpenCode tool / CLI

**Decisions.**
- One image per message.
- Telegram photo only (`sendPhoto`); no document fallback in v0.
- Two triggers:
  1. An OpenCode custom tool, `send_image`.
  2. `acolyte notify-user --media <path> [caption]`.
- Both use the same outbox and Telegram delivery path.
- Images are canonicalized under `media/outbound/images/`.

**Work.**

### 6a — Outbox and schema
- Add a migration for `comm_tg_outbox.media_path TEXT NOT NULL DEFAULT ''`.
- Allow an outbound row with media and empty text; `text` is the optional caption.
- Update `OutboxItem`, `EnqueueOutbound`, `scanOutbox`, `MarkOutboundSent`, and trace/inspect
  projections.
- Keep one media path per row; reject multiple images for now.

### 6b — Telegram photo delivery
- In `internal/comm/telegram/outbound.go`, branch on `media_path`:
  - Open the file and send `bot.SendPhoto` with
    `models.InputFileUpload{Filename: filepath.Base(path), Data: file}`.
  - Use `text` as the caption when non-empty.
  - Preserve `ReplyParameters`, `OnSuccess`/retry behavior, and token redaction.
- Enforce Telegram limits before enqueue: one image, JPEG/PNG/WebP, max 10 MiB, caption max 1024
  characters.
- On a permanent photo error, mark the row failed; do not retry forever.

### 6c — `acolyte notify-user --media`
- Extend the command so text is optional when `--media` is present:
  ```bash
  acolyte notify-user "Here is the chart" --media media/outbound/images/chart.png
  acolyte notify-user --media media/outbound/images/chart.png
  ```
- Resolve the path relative to the target workspace.
- Require a regular, non-symlink file inside the workspace; copy it into
  `media/outbound/images/<timestamp>.<ext>` if it is not already there.
- Enqueue a user-only outbox row through the existing sink; never create or touch an OpenCode
  session.
- Add an optional `--session-id` used **only for attribution**: store it in
  `comm_tg_outbox.opencode_session_id` so `acolyte inspect --session-id` and trace export can link
  the delivery to the conversation. It must not inject a message into that session.
- Add `--media` and `--session-id` to the command flags and update `cmd/message_test.go` /
  `cmd/notify_user` tests.

### 6d — OpenCode `send_image` tool
- Scaffold `.opencode/tools/send_image.ts` from `acolyte new` (add `.opencode/tools/` to the
  workspace layout).
- The custom tool should:
  - Accept `path` (string, required) and `caption` (string, optional).
  - Receive `context.sessionID` and `context.directory` from OpenCode. Use
    `context.directory` as the workspace, not `process.cwd()`, and pass
    `--session-id context.sessionID` for attribution.
  - Validate the path as above.
  - Invoke
    `acolyte notify-user --workspace-folder <context.directory> --session-id <context.sessionID> --media <path> [caption]`
    (use `ACOLYTE_BIN` when set, otherwise `acolyte` from `PATH`).
  - Return a structured result containing the canonical media path and outbox ID, or a clear error
    so the model can correct the path.
  - Optionally include a `Tool.FileContent` part for the image (`uri: file://...`,
    `mime: image/png|jpeg|webp`, `name`) so the session keeps the image reference. The agent
    already sees the tool call and result either way; the Telegram notification remains invisible
    to the agent by design.
- Do not add an OpenCode plugin: `.opencode/tools/` custom tools are auto-discovered.
- Note: the tool sends the photo immediately, so the agent's final text is a separate message.
  If one-message image+caption behavior becomes important, extend `TurnResult` to extract the tool
  call and merge it with the reply later.

### 6e — Tests and docs
- Extend `internal/comm/telegram/telegramtest` with `sendPhoto` upload handling and assertions for
  uploaded bytes, filename, caption, chat, and reply target.
- Test: happy path, missing file, symlink escape, outside-workspace path, unsupported extension,
  oversized image, caption too long, and retry/failure.
- Test the generated `send_image.ts` scaffold exists and contains the expected command.
- Update `docs/communication/telegram.md`, `README.md`, and the workspace/media docs.

**Acceptance.**
- `acolyte notify-user "caption" --media <path>` sends a Telegram photo and queues durably.
- The generated `send_image` tool sends a single photo via the same path and passes
  `context.sessionID` for attribution.
- No session is created or modified by either trigger. The notification itself never appears in
  the agent context; only the OpenCode tool call/result is visible there.
- `acolyte inspect --session-id <id>` shows the outbox media row produced by `send_image`.
- Invalid/unsafe images are rejected before enqueue with a clear error.

## 7. Update `SPEC.md` for the implemented v0

**Priority:** high (after tasks 1–6 decisions)  
**Area:** specification + docs

**Work.**
- **§5.1 / §5.2 — endpoint truth.** Replace the stale endpoint list with the pinned OpenCode
  2.2.7 reality: stats is `GET /api/experimental/session/stats`; the inbox is
  `GET /api/session/{id}/inbox` plus `PATCH/DELETE /api/session/{id}/inbox/{inboxID}`; there is no
  `POST /api/session/{id}/inbox` and no `.../steer|queue` route. Document that `synthetic` is the
  one-shot admission/delivery path. Remove or explicitly defer the unsupported `resume` field.
- **§4 / §10.5 — status.** Decide whether `acolyte status` must show communication settings and a
  true “last message,” or whether the spec should point to `acolyte config get` and the local
  session `last_active_at` semantics. Do not leave the spec claiming fields the command omits.
- **§9 — persistence.** Make the SQL examples match the real tables, or mark the SQL as
  illustrative. Current drift includes `comm_tg_chats.resolved_at`, the `comm_tg_media` primary
  key, and the extra inbox replay columns. Add the new outbox `media_path` field and clarify that
  `notify-user`/`send_image` may record `opencode_session_id` for attribution without injecting a
  message into that session.
- **§3 / §7 — media layout.** Replace `downloads/` media paths with `media/inbound/` and
  `media/outbound/`; document the outbound image limits and the one-image/photo-only v0 scope.
- **§10.1 / §10.3 — routines.** Document the session behavior chosen in task 3, including whether
  `--session-id` is allowed for routine jobs and why capability routines stay isolated.
- **§10.4 / §10.5 — notify-user.** Document `--media` and the optional caption.
- **§6.3 / §6.4 — memory.** Verify the final wording matches the implemented dream permissions,
  session scoping, sanitize, and import behavior; update only if code changes in tasks 1–6 affect
  them.
- **§5.1 — streaming.** Reflect the task 4 outcome: implemented, deferred to `FUTURE-IDEAS.md`, or
  limited to live status/learning.

**Docs to sync while updating the spec.**
- `docs/orchestrator/daemon.md`: remove “there is currently no CLI to change … channels, allowed
  users, schedule, or engine”; `acolyte config` now does that.
- `docs/memory/memory-load/default.md`: the shown `"skills": ["./.agents/skills"]` entry is not
  seeded by `acolyte new` (OpenCode auto-discovers project `.agents/skills`).
- `internal/opencode/client.go`: the comment still says v2.2.5; the pin is 2.2.7.
- Remove or clearly mark the dead legacy scaffold in `internal/workspace/template.go` and
  `internal/workspace/files.go` (`opencode.json` / `MAIN-PROMPTS/`).
- Update `docs/communication/telegram.md` for `media/inbound`, `media/outbound`, and photo uploads.

**Acceptance.**
- `SPEC.md` contains no path, endpoint, column, or behavior that the code does not implement.
- `rg` finds no stale “no CLI to change …” text.
- New links and doc references resolve.

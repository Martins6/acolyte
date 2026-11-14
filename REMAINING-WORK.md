# Remaining work from #89

Based on PRs #90–#97, these are the next steps left after closing the seven follow-up bullets from
[#89](https://github.com/Martins6/acolyte/issues/89). Each section records what landed and what is
still open, with pointers to the relevant code and docs.

## 1. Event-observer race (PR #93)

**Landed.** The per-turn observer now establishes the SSE connection before the prompt is admitted,
with a bounded 2s `eventReadyTimeout`; `RunUserTurn` and `RunSyntheticTurn` share the handshake and
regression tests cover both the ordering and the stalled-stream fallback.

**Next steps.**
- **Reconnect gaps remain best-effort.** The handshake waits for the first connection for at most
  `eventReadyTimeout` (2s) and then proceeds even if the stream is still unavailable, so it cannot
  guarantee that connection. If the stream drops mid-turn, `SubscribeEvents` reconnects with backoff
  and events emitted during the gap are lost. Consider an event ID/resume cursor, or state the
  best-effort semantics explicitly in `SPEC.md` §5.1.
- **Tune the readiness timeout.** A slow OpenCode service can still lose early events after the 2s
  cap. Measure startup latency and consider a configurable timeout or a longer first-connect budget.
- **Do not block the SSE reader.** `OnEvent` runs synchronously on the stream reader; a slow handler
  delays event dispatch. A bounded queue belongs in the activity recorder (see §5).

## 2. #85 review-check failure (PR #92)

**Landed.** `docs/ci/review.md` records the evidence: the `review` job died in the third-party
action's `Install opencode` step (`curl: (56) Recv failure`, exit 56) before the model ran. It is an
infrastructure flake, not a diff/model/workflow-logic failure.

**Next steps.**
- **Pin and retry the installer.** Replace the composite action's unretried `curl | bash` (and the
  unretried `releases/latest` lookup) with a pinned, checksummed download using
  `curl --retry 5 --retry-all-errors`, or vendor the action under `.github/actions/`.
- **Keep the check advisory** unless it becomes deterministic; a flake must cost a re-run, not a
  merge.

## 3. Image + final text delivery (PR #90)

**Landed.** The v0 decision is documented: `send_image` and `notify-user --media` deliver the photo
immediately (with their explicit caption) and never merge it with the reply. Telegram turns and
`acolyte inbox` turns deliver the final text as a separate Telegram message (`inbox` also prints
it); `acolyte run` prints the reply to stdout only. The tradeoff and the merge path are recorded in
`docs/communication/telegram.md` and `FUTURE-IDEAS.md`.

**Next steps.**
- **One photo + reply caption, if wanted.** Extend `TurnResult` with an `Images` field populated from
  `send_image` tool calls in the assistant timeline, suppress the tool-side send (or reconcile the
  duplicate), and queue one outbox row whose caption is the final reply. Respect the 1024-rune
  caption limit and decide whether to truncate or fall back to two messages.
- **Keep the immediate path** for CLI/routine turns that never go through the router reply path.

## 4. Outbound image validation (PR #94)

**Landed.** `PrepareOutboundImage` sniffs magic bytes, requires the detected format to match the
extension, rejects images above 50 million pixels, and fully decodes JPEG/PNG/WebP before accepting
or copying the file. Files already inside `media/outbound/images/` are validated too.

**Next steps.**
- **Lower or stream the decode bound.** 50M pixels still permits a large bounded allocation during
  a full decode; a smaller bound or a streaming format validator would tighten memory exposure.
- **Close the TOCTOU window.** The validated path can be swapped before `copyImage` opens it.
  Validate the opened descriptor (or copy first and validate the destination) instead of validating
  by path and reopening.
- **Document/decide exotic formats.** HEIC/AVIF and animated WebP are not supported; keep the
  explicit rejection or add a supported-format matrix.
- **Inbound parity.** Telegram media is still trusted by source; consider the same content sniffing
  for inbound files if agents later execute or re-publish them.

## 5. Event-stream consumers (PR #95)

**Landed.** The first production consumer persists `session.tool.called/success/failed` events to
`orch_logs` (IDs only — no input, content, prompts, or deltas), wired into router user turns and
inbox synthetic turns, so trace export and memory/learning feeds now see tool activity.

**Next steps.**
- **Retention.** Activity rows have no pruning or rotation. Add a cap (per agent or per age) or fold
  them into the existing log-retention policy.
- **Tool names.** The event payload carries the tool call ID and input, not the tool name. Enrich
  rows by joining the session message that owns the call (safe metadata only) so activity is
  readable without exposing arguments.
- **Live surfaces.** Telegram partial-text streaming and live status are still deferred; both need
  message editing plus rate limiting. `acolyte status`/`inspect` do not surface activity rows yet.
- **Opt-out.** Add a setting to disable activity logging for privacy-sensitive agents, and consider
  sampling/dedup for high-tool-volume turns.

## 6. `acolyte status` (PR #96)

**Landed.** The list table has `CHANNELS` and `LAST MESSAGE` columns; the detail view shows
`Channels`, `Allowed`, and `Last message` (the newest of the latest inbox `received_at` and sent
outbox `sent_at`). `Last active` and `Jobs` are unchanged.

**Next steps.**
- **Tighten settings parsing.** The review noted that `json.Unmarshal` accepts the literal `null`
  into `[]string` without error, so an invalid non-array setting renders as empty instead of the
  default. Reject `null`/non-arrays and fall back to defaults.
- **Status cache.** The fan-out over per-agent stores remains; the deferred `agent_status` snapshot
  (see `FUTURE-IDEAS.md`) would make the list cheaper at scale.
- **Message previews.** Last message is a timestamp only because delivered text is cleared by
  design. If previews are wanted, add a deliberate short-retention preview store, not a full history.
- **Surface live activity.** Expose the §5 activity rows (tool calls, failures) in the detail view.

## 7. Merge-red policy (PR #91)

**Landed.** `docs/orchestrator/merge-policy.md` records the current protection facts, the scoped
merge-red decision, the stop-on-red trigger, the no-`--admin` rule, and an enabling checklist;
`TASK.md` step 4 carries the conditional.

**Next steps.**
- **Enable required checks only after** autopilot is switched to stop-on-red, then add approvals.
  `dev` currently has no required status checks; `main` requires only `pr-guard`.
- **Decide whether `review` should ever be required.** It is an LLM review, currently advisory.
- **Review-state ergonomics.** The CI agent posts `COMMENTED` reviews, so a `CHANGES_REQUESTED`
  review keeps blocking a `dev` merge even after a later LGTM; stale reviews must be dismissed
  manually. Either have the workflow submit `APPROVE` for LGTM (with the matching token permission)
  or stop treating reviews as blocking.

## Cross-cutting

- Re-run the full `test` workflow on `dev` after the merge batch and confirm the secret-gated `e2e`
  job (push-only) remains green.
- Keep the sbeads epics `sb-8d6b`, `sb-070f`, `sb-f29e`, `sb-71a2`, `sb-b091`, `sb-5a3b`, `sb-9985`
  closed; this file supersedes them as the durable backlog.

# Remaining work from the SPEC v0 suggested-order fixes

Based on PRs #98–#108, these are the next steps left after the nine SPEC v0 suggested-order items:
turn timeout, dream validator sessions, inbound text+media, notify-user attribution, memory-load
wiring, export media, turn tool activity, routine routing/SHELL, and V2 agent config/contract/docs.

## 1. Turn timeout (PR #101)

**Landed.** The hard five-minute cap is gone from `RunUserTurn`/`RunSyntheticTurn`; callers can set
`TurnConfig.Timeout`, and `acolyte run --timeout 0` means no limit.

**Next steps.**
- **No implicit bound remains for daemon/cron turns.** A wedged provider call can hold a session and
  worker forever unless the caller's context is canceled. Decide whether the daemon/router should
  set a per-agent or per-job default and expose it through config.
- **Live long-turn coverage.** Unit tests prove deadline presence/absence but do not run a turn past
  five minutes; add an opt-in E2E or a clock-injected harness.
- **Document caller responsibility.** Make it explicit that `Timeout: 0` inherits only the caller
  context and is not a liveness guarantee.

## 2. Dream validator sessions (PR #99)

**Landed.** The writer reuses `cron:<job>:validator`, recovers from a deleted session with a 404
probe, propagates other probe errors, and records `mem_dreams` on probe failure.

**Next steps.**
- **Feedback loop.** The persistent validator session is itself a recent workspace session, so later
  dream digests can include prior writer conversation. Exclude validator sessions from the dream
  lookback or explicitly accept the self-review feedback.
- **Probe cost.** Each writer run adds one `GET /api/session/{id}` before the turn, on top of the
  turn's own fetch. Cache liveness or tolerate a failed fetch inside `RunSyntheticTurn` with a
  one-shot recreate if latency matters.
- **Cleanup.** Reusing the mapping stops per-run leaks, but validator sessions for deleted agents or
  workspaces still need a retention/cleanup policy.

## 3. Inbound text + media (PR #100)

**Landed.** The router appends media paths even when text is present, and suppresses only an exact
`File located at: <path>` marker line.

**Next steps.**
- **Prose mentions now get a marker line.** That is safe but can be redundant. If prompts become
  noisy, parse the media block structurally instead of using a text marker.
- **OpenCode file attachments.** The router still sends media as text paths. Native file/attachment
  parts would let vision-capable models use images without a shell read.
- **Multiple media.** One path per marker is supported; consider a compact list and per-file type
  metadata instead of repeating the line.

## 4. `notify-user` attribution (PR #98)

**Landed.** Text-only notifications now persist `--session-id` in
`comm_tg_outbox.opencode_session_id`; media already did.

**Next steps.**
- **Cron sink parity.** `internal/cron.Sink` still exposes only `NotifyUser` (no attribution). Add a
  session-aware method if cron/routine notifications should link to conversations.
- **Interface consistency.** `comm.OutboxSink` has `NotifyUser`, `NotifyUserAttributed`, and
  `NotifyUserMedia`; consider one options struct so new notification kinds cannot forget
  attribution.
- **Attribution validation.** `--session-id` is intentionally not ownership-checked for
  notify-user; document that it is free-form metadata and can point at a deleted/foreign session.

## 5. `memory-load` wiring (PR #107)

**Landed.** Omitted/default config wires `.acolyte/*.md`; explicit `null` selects the native
baseline. `acolyte config set memory-load` edits the `instructions` array with a targeted JSONC edit,
and case-insensitive config keys are honored.

**Next steps.**
- **`config get` still reports `memory-load: null`** when the setting is absent even if the default
  instructions are wired. Report the effective mode (`default` vs `null`) or document the difference.
- **Adoption is non-destructive.** Passing a different memory-load choice to `acolyte new` on an
  existing workspace does not rewrite `opencode.jsonc`; use `config set` and document that.
- **Array edit ergonomics.** Restoring the default appends the entry rather than restoring its
  original position. The JSONC array helper is now large; keep dedicated tests for comments,
  trailing commas, duplicate keys, and non-array values.
- **Other memory keys remain setting-only.** `memory-access`/`memory-export` have no OpenCode
  artifact; either keep that explicit or add future modes with real artifacts.

## 6. Export media (PR #104)

**Landed.** Media files are copied byte-for-byte under `communication/media/` with collision-safe
names, workspace containment, no-follow path walking, and a `media_files` count. Scoped exports copy
only the selected chats' media.

**Next steps.**
- **`--sanitize` does not touch media bytes.** Document this clearly (already noted) and consider a
  separate opt-in metadata-only mode for sensitive bundles.
- **Import caps still apply.** Export can produce a bundle larger than the 512 MiB/64 MiB import
  limits; warn at export time or add matching export caps.
- **Containment is textual.** Canonical aliases outside the configured workspace are rejected
  conservatively; if agents use symlinked media roots, add a canonical-root policy instead of
  silently skipping.
- **Media provenance.** `media.json` keeps original paths while the zip stores hashed copies; a
  machine-readable mapping from entry to original path would make external readers simpler.

## 7. Turn tool activity (PR #105)

**Landed.** `TurnResult.ToolActivity` summarizes assistant tool parts; `acolyte run --json` exposes
`tool_activity`; malformed message content no longer fails turn collection.

**Next steps.**
- **Post-turn only.** This is a summary, not streaming. Telegram partial-text/tool streaming and
  live status remain deferred.
- **Names vs event IDs.** The event-stream activity recorder (PR #95) can log tool call IDs without
  names; joining those rows to `TurnResult.ToolActivity` or the owning message would make audit logs
  readable without exposing inputs.
- **Retention.** Activity rows in `orch_logs` still have no pruning or per-agent cap.
- **Privacy/opt-out.** Tool names/statuses are safe, but users may want a per-agent switch to
  disable activity logging.

## 8. Routine routing/session and service SHELL (PR #102)

**Landed.** Routine output defaults to `log`, routines always execute in `cron:<job ID>`, stored
session IDs are ignored with a warning, and generated launchd/systemd units export `SHELL` when the
installer has one.

**Next steps.**
- **Sudo shell resolution.** Under `sudo`, `SHELL` is taken from the sudo environment; resolve the
  target user's login shell explicitly if the service must match it.
- **Surface ignored sessions.** A stored routine session is ignored only in the daemon log; add an
  `orch_logs` warning or reject/migrate the row during config validation.
- **Fallback shell.** If `$SHELL` is empty at install time, units omit it and shell jobs still fail
  with `$SHELL is not set`; derive a safe fallback or fail the install with guidance.
- **Routine delivery knobs.** Explicit `--on-success/--on-failure notify-user` still works; document
  that custom routines default to internal logging and must opt into user delivery.

## 9. V2 config, contract coverage, docs (PR #108)

**Landed.** The scaffold emits canonical V2 `agents` entries with `system` + `mode: primary`;
`build`/`plan` stay built-in with no invented definition. Contract tests guard client request-body
properties and query params; README/telegram docs use `process > workspace > global` secret
precedence; cron docs describe routine session isolation. Legacy `agent`/`prompt` blocks remain
readable for existing workspaces.

**Next steps.**
- **Contract coverage can go further:** guard response schemas used by `Prompt`/`Synthetic` and every
  client-used `Session.Info`/`Message` property; request-body guards are now in place.
- **Legacy migration.** New scaffolds are V2, but existing workspace `opencode.jsonc` files may still
  use `agent`/`prompt`. Add a config migration or document that OpenCode performs the migration.
- **Unused endpoints.** `/api/agent` and `/api/experimental/session/stats` are listed in SPEC but
  have no v0 consumer; decide whether to add clients or mark them explicitly unused.
- **Status docs.** PR #96 changed `acolyte status` to show channels/last message; keep SPEC §10.5
  and orchestrator docs aligned with that behavior.

## Cross-cutting follow-ups

- **`acolyte cron tick` still requires OpenCode even for shell-only jobs.** It calls
  `openCodeEnsure` before inspecting due jobs; make the client optional for shell-only ticks.
- **`Communication.Send` has no production call site.** The daemon writes the durable outbox
  directly; either wire `Send` or update the §8/§14 lifecycle wording to match the outbox design.
- **Envelope shape drift.** IDs are `env_<hex>`, not UUIDs; empty `meta` is omitted; `Validate` does
  not enforce the documented channel enum. Align the spec or the envelope.
- **Shared migration helper.** Each store module duplicates `schemaVersion`/`Migrate`; extract the
  helper promised by SPEC §9 or soften the claim.
- **`downloads/` is not scaffolded.** SPEC §3 lists it; `acolyte new` creates it only on first
  memory import. Either scaffold it or mark it lazy/legacy.
- **`engine.model` validation asymmetry.** `acolyte new` accepts a model without `provider/model`;
  `acolyte config set engine.model` rejects it. Share the validator.
- **Deferred by design:** agent-to-agent channels, more human channels, extra memory modes, live
  Telegram streaming/status feeds, `agent_status` cache/OpenCode stats, per-agent isolation
  hardening, folder-portable identity, synthetic `resume`, and inbox item-level PATCH/DELETE.

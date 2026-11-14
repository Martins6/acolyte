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
immediately (with their explicit caption). For turns routed through the daemon's inbound router the
agent's final text is a separate Telegram message; headless callers such as `acolyte run` print it
to stdout. The tradeoff and the merge path are recorded in `docs/communication/telegram.md` and
`FUTURE-IDEAS.md`.

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

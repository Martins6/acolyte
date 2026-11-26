# OpenCode V2 contract pin

Acolyte integrates with OpenCode through the **V2 HTTP API** (`opencode serve --service`), not by
shelling out to `opencode run`. The V2 API is OpenCode's intentionally breaking surface, so the client
is kept thin and pinned to one release.

## Pin

| Item | Value |
| ---- | ----- |
| OpenCode version | `2.2.7` |
| Vendored spec | [`internal/opencode/contract/openapi-v2.2.7.json`](../../internal/opencode/contract/openapi-v2.2.7.json) |
| Version constant | `opencode.PinnedOpenCodeVersion` |
| SHA-256 constant | `opencode.PinnedOpenAPISHA256` |
| Contract test | `TestPinnedOpenAPIContract` in `internal/opencode/contract_test.go` |
| CI version | `.github/workflows/test.yml` → `OPENCODE_VERSION` |

The contract test checks the committed spec's hash and asserts every path, method, schema, and
schema property the client uses still exists. It runs in the normal unit-test job. The live E2E job
installs the same pinned CLI version and fails if `opencode --version` does not match.

## Headless turn invariant

Every turn Acolyte drives is headless: it never presents an interactive prompt and never waits for a
human client. The `OnEvent` SSE observer remains best-effort and is **not** the correctness
mechanism. Settlement is pull-based and independent of the stream.

For every prompt or synthetic turn, the thin client:

1. **Pre-sweeps** the session and its descendants before admitting the input. A form or permission
   left by a crashed turn is settled here; if the sweep cannot list or settle, the turn fails closed.
2. Runs a **1s poller** while the turn is active. It re-discovers the root and descendant sessions and
   settles anything that appeared since the last pass.
3. Runs a **final sweep** after collection and before the session lock is released.
4. Aborts with `ErrInteractionUnsettled` if settlement keeps failing. It never waits forever.

Discovery and settlement use only the pinned V2 routes:

| Purpose | Request |
| ------- | ------- |
| Descendants | `GET /api/session?parentID={id}&limit=100`, following `cursor.next` until empty |
| Pending forms | `GET /api/session/{id}/form` |
| Cancel a form | `DELETE /api/session/{id}/form/{formID}?message=…` |
| Pending permissions | `GET /api/session/{id}/permission` |
| Reject a permission | `POST /api/session/{id}/permission/{requestID}/reply` with `{"decision":"reject","message":"…"}` |

Messages match `opencode run`:

- Question forms: `This non-interactive run cannot ask the user questions, so the question was cancelled. Continue without an answer; make reasonable assumptions and state them.`
- Other forms: a generic non-interactive message. They are not described as questions.
- Permissions: `This non-interactive run cannot ask the user for permission, so the request was rejected. Continue without this action.`

A `404` or `409` from a settlement call is an already-settled outcome, not a failure. Transient `5xx`
responses are retried; a known pending interaction that cannot be settled fails the turn after a
bounded number of attempts.

### Question policy

Acolyte does **not** add a `question` deny rule. `Permission.Rule` has no message field, so denying
the tool would produce only `Permission denied: question` and would not satisfy the headless
explanation. Letting the form appear and cancelling it with the message above is the reference
behavior. If a deny rule is added later, the tool result becomes the generic denial and the
acceptance wording must change.

### Boundaries

- **Background subagents:** subagent sessions active during the root turn are covered. A background
  subagent that outlives root idle is settled by the next turn's pre-sweep; this version does not
  track `experimental.job` descendants after the root turn returns.
- **Location-scoped forms:** `sessionID: "global"` forms are not listed. Acolyte drives sessions, not
  a standalone location.
- **`acolyte attach`:** debug-only. It does not take the session lock, so a pre-sweep or poller can
  cancel a form or permission a human is answering. Do not run it concurrently with an acolyte turn.

Settlement outcomes are logged through `TurnConfig.OnInteraction` (IDs only: session, kind, id,
action); failures are always also written to the daemon log. The router, inbox sink, cron, and
routines persist the callback to `orch_logs` where a store is available.

## Updating the pin

1. Install the target release and start its service:
   ```bash
   npm install --global @opencode/cli@<version>
   opencode --version
   opencode service start
   ```
2. Capture the spec (the password is in `~/.config/opencode/service.json`):
   ```bash
   curl -sS -u "opencode:$(jq -r .password ~/.config/opencode/service.json)" \
     http://127.0.0.1:49374/openapi.json \
     > internal/opencode/contract/openapi-v<version>.json
   ```
3. Update `PinnedOpenCodeVersion` and `PinnedOpenAPISHA256` in
   `internal/opencode/contract.go`.
4. Update `OPENCODE_VERSION` in `.github/workflows/test.yml`.
5. Update this document's table and run:
   ```bash
   go test ./internal/opencode
   go test ./...
   ```

If the contract test reports a missing path/schema/property, adapt the thin client and the export
renderer before refreshing the spec. The failure is the point: an OpenCode API change should never
silently break turns, cron routines, or memory export.

## Why not a generated client?

The client is deliberately small and reply-oriented (`internal/opencode/client.go`). Generating a
full OpenAPI client would add a large generated surface for endpoints Acolyte does not use. The
vendored spec plus a contract test gives the same breakage containment while keeping the transport
behind one thin internal client. This is not an engine abstraction: Acolyte remains OpenCode-only.

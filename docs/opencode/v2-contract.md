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

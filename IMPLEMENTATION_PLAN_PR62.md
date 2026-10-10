# Implementation plan: port fixes from PR #62

**Reference:** [buxtronix/phev2mqtt#62](https://github.com/buxtronix/phev2mqtt/pull/62)  
**Goal:** Prevent PHEV connection leaks and blocked goroutines from disabling MQTT reconnects, while preserving this fork's existing configuration and behavior.

## Instructions for the implementing agent

Inspect the current fork before editing. Port the underlying fixes from PR #62, but do not replace fork-specific code with upstream versions. In particular, preserve configurable TCP read/write, handshake, and register timeouts, as well as MQTT connection-state and Wi-Fi power-save cleanup.

Make the changes below as one coherent client-lifecycle fix. Keep unrelated changes out of scope.

## Work items

### 1. Make `client.Client` shutdown and sends safe

File: `client/client.go`

- Replace the unsynchronized `closed` state with a close signal and idempotent close behavior (for example, a `done` channel and `sync.Once`).
- Ensure `Close` can be called by reader, writer, MQTT cleanup, and command cleanup without panicking; close the underlying connection at most once.
- Add a client send method that can return an error promptly if the client has closed, instead of blocking forever on a full/unserviced send channel.
- Route sends originating inside the client through that method: register requests/retries, pings, and handshake responses.
- Make `SetRegister` return promptly if the connection closes while it is waiting for a response. Use the configured register timeout (`registerTimeout`), not a hard-coded replacement.
- On reader exit, close the receive channel and stop and close registered listener channels safely and exactly once.
- Make listener send/stop/close state safe under concurrent access. Preserve the existing non-blocking listener delivery behavior.
- Ensure reader, writer, pinger, and manager goroutines all terminate when the connection ends. Preserve handshake and model-year behavior.

### 2. Close each MQTT connection attempt on exit

File: `cmd/mqtt.go`

- In `handlePhev`, arrange for the newly created client to be closed on every exit after creation, including a failed `Connect`, a failed/timed-out `Start`, a reader failure, and normal/power-save termination.
- Retain existing `setConnected(false)`, last-connect tracking, and Wi-Fi power-save cleanup. Avoid introducing cleanup ordering that leaves Wi-Fi enabled or changes MQTT availability semantics.
- Replace direct sends through `m.phev.Send` with the new client send method, and handle/report errors consistently with nearby code.
- Ensure reconnect behavior still works after `handlePhev` returns an error.

### 3. Update command send call sites

Files: `cmd/register.go`, `cmd/watch.go`

- Replace direct sends through the `client.Client.Send` channel with the new send method.
- Handle a send error by terminating or reporting the operation appropriately; do not silently ignore it or leave the command blocked.
- Keep the emulator's separate `Send` channels unchanged unless inspection shows they use `client.Client`. Emulator connection sends are not the client API addressed by this PR.

### 4. Add client lifecycle regression tests

File: `client/client_test.go` (create if it does not exist)

Add focused tests for:

- Sending after `Close` returns promptly with an error.
- Listener channels close on client disconnect and are not closed more than once.
- A pending `SetRegister` returns promptly when the client closes rather than waiting for the full register timeout.
- A failed or timed-out startup/handshake closes the connection so another connection attempt is not prevented by a leaked socket, where this can be tested deterministically.

Use local test connections or other deterministic fixtures; do not require a real vehicle or external broker. Avoid timing-only assertions where synchronization can be used.

## Validation and acceptance criteria

1. Run `gofmt` on all changed Go files.
2. Run `go test ./...`.
3. Run `go test -race ./client ./cmd` (or `go test -race ./...` if practical).
4. Confirm there are no remaining direct sends to `client.Client.Send` in production command/client code. Do not mistake the emulator's distinct send channels for this API.
5. Review shutdown paths to confirm connection closure is idempotent, listener and receive channels close exactly once, and sends cannot block indefinitely after shutdown.
6. Confirm the fork's configurable timeout options and MQTT Wi-Fi power-save behavior remain intact.

Report the files changed, tests run and their results, and any limitation in deterministically testing real vehicle reconnect behavior.

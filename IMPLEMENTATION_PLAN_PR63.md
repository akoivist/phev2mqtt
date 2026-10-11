# Implementation plan: port MQTT reconnect fixes from PR #63

**Reference:** [buxtronix/phev2mqtt#63](https://github.com/buxtronix/phev2mqtt/pull/63)  
**Goal:** Restore MQTT command subscriptions and Home Assistant discovery after broker or Home Assistant restarts, without changing this fork's existing MQTT configuration, PHEV connection lifecycle, or availability behavior.

## Instructions for the implementing agent

Inspect the current fork before editing. Port the behavior from PR #63 rather than replacing fork-specific `cmd/mqtt.go` with the upstream file. Keep this change focused on MQTT reconnection and Home Assistant discovery recovery.

Preserve the current `mqtt_disable_register_set_command` behavior, configurable `mqtt_topic_prefix` and `ha_discovery_prefix`, configured-VIN validation, discovery triggered by a PHEV-reported VIN, MQTT callbacks, and the existing PHEV/Wi-Fi power-save lifecycle. The broker reconnect path must not incorrectly report the vehicle online when the PHEV is unavailable.

## Work items

### 1. Restore MQTT subscriptions on each connection

File: `cmd/mqtt.go`

- Move the `/set/#`, `/connection`, and `/settings/#` subscriptions into Paho's connect callback so the subscriptions are restored after every broker connection, not just the initial `Connect`.
- Continue to omit `/set/#` when `mqtt_disable_register_set_command` is enabled; always restore the other existing command subscriptions.
- Subscribe to the Home Assistant birth topic (`<ha_discovery_prefix>/status`) when discovery is enabled.
- Log subscription failures explicitly. Keep the initial-connect and reconnect paths consistent and avoid duplicate subscriptions or callback workarounds that can block Paho's network loop.

### 2. Replay availability and Home Assistant discovery

File: `cmd/mqtt.go`

- On each successful broker connection, republish the gateway's current availability state and retained VIN where appropriate.
- Cache the VIN learned from the PHEV independently from the optional configured `vehicle_vin`. Use the configured VIN when present and otherwise use the most recently learned VIN to restore discovery without waiting for another vehicle register response.
- Allow discovery configuration messages to be replayed after a broker reconnect even if discovery was already published earlier in this process. Preserve the existing VIN-mismatch handling and do not duplicate initial discovery unnecessarily.
- Handle Home Assistant's birth message on the subscribed status topic. When Home Assistant reports `online`, republish discovery and availability; ignore unrelated payloads and messages on other topics.
- Keep retained discovery publications retryable when publishing fails, and log failures rather than marking discovery as successfully restored prematurely.
- Protect state shared by the Paho connect callback, MQTT message handler, and PHEV register path against concurrent access. Do not introduce races around cached VIN, discovery state, or connection/availability state.

### 3. Add focused MQTT recovery tests

Files: add focused tests under `cmd/` (no command tests currently exist).

- Verify the subscription set includes all required command topics and conditionally omits `/set/#` when register-set commands are disabled.
- Verify the Home Assistant birth subscription is configured only when discovery is enabled and uses the configured discovery prefix.
- Verify `online` on the exact birth topic triggers discovery/availability replay, while unrelated topics or payloads do not.
- Verify reconnect recovery republishes discovery using a configured VIN or, if unset, the cached VIN learned from the PHEV.
- Verify repeated connect/birth events do not corrupt shared state or create duplicate/incorrect discovery entities.

Prefer small testable helpers for topic selection and recovery decisions. For broker-level behavior, use a local Mosquitto broker in a Linux container or provide reproducible manual steps; do not require a live vehicle or external broker for the unit tests.

## Validation and acceptance criteria

1. Run `gofmt` on changed Go files.
2. Run `go test ./...` and, where practical, `go test -race ./cmd` **inside a Linux container**. Do not run Go tests natively on Windows; if a Linux container is unavailable, leave test execution for manual validation.
3. Verify by inspection that all required command subscriptions are re-established for both initial connection and reconnect, and that the set-command flag remains respected.
4. In a Linux container with Mosquitto, or manually on a test broker:
   - Start the client and confirm the command subscriptions are active.
   - Restart the broker and confirm subscriptions return without restarting the process.
   - Confirm retained discovery and availability are restored, using the configured VIN or the VIN already learned from the PHEV.
   - Publish `online` to `<ha_discovery_prefix>/status` and confirm discovery/availability are restored; send another payload and confirm it does not trigger the replay.
5. Confirm a broker reconnect while the PHEV is disconnected does not incorrectly publish vehicle availability as online.
6. Report changed files, Linux-container or manual tests run and their results, and any validation that remains for the user.

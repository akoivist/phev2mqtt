# Live MQTT restart test

This test builds the application, runs it against a temporary Mosquitto broker,
restarts that broker, and verifies MQTT subscription, retained VIN/availability,
Home Assistant discovery, and Home Assistant birth-message recovery.

The test deliberately does **not** require a vehicle. It expects the availability
state to remain `offline`. This verifies that a broker or Home Assistant restart
does not incorrectly mark a disconnected PHEV online.

## Run on Windows with Docker Desktop

From the repository root, run in PowerShell:

```powershell
$repo = (Get-Location).Path
docker run --rm --mount "type=bind,source=$repo,target=/src" --workdir /src golang:1.24 bash scripts/mqtt-restart-test.sh
```

The Linux container installs Mosquitto and its command-line clients if needed,
builds the current source, then runs the broker and application inside the
container. The broker is isolated from host ports and existing containers.
Everything is stopped and temporary files are removed when the script exits.

## Run on Linux

Install Go 1.24+, Mosquitto, and the Mosquitto client tools, then run from the
repository root:

```sh
bash scripts/mqtt-restart-test.sh
```

The script uses an anonymous broker bound only to `127.0.0.1` and a dummy MQTT
password accepted by the application's configuration validation. Do not reuse
the test credentials for a real broker.

## Expected checks

The script reports `PASS` for:

1. Initial retained offline availability, VIN, and Home Assistant discovery.
2. Delivery to the `/connection` subscription before broker restart.
3. Retained availability, VIN, and discovery restored after broker restart.
4. Delivery to `/connection` after broker restart.
5. No discovery replay for a Home Assistant `offline` status message.
6. Discovery replay for `hass/status online`, with vehicle availability still offline.

The test prints relevant application logs if a check times out. It exits nonzero
on a failed check.

## Scope and limitations

The tested connection address intentionally has no PHEV service listening on it.
This keeps the test independent of a vehicle and explicitly checks the offline
availability behavior. The current project emulator did not complete its startup
handshake in the prior container run, so this script does not claim to validate
the connected-vehicle (`online`) availability case. A real PHEV or a separately
validated emulator is needed for that scenario.

To manually exercise the register-set subscription as well, run the client with
`--mqtt_disable_register_set_command=false` (the default) and inspect broker
subscriptions or send a safe test command only when connected to a test vehicle.

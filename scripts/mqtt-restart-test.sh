#!/usr/bin/env bash
set -euo pipefail

if [[ ! -f go.mod ]]; then
	echo "Run this script from the repository root." >&2
	exit 2
fi

if ! command -v mosquitto >/dev/null || ! command -v mosquitto_pub >/dev/null || ! command -v mosquitto_sub >/dev/null; then
	if ! command -v apt-get >/dev/null; then
		echo "Mosquitto and its client tools are required." >&2
		exit 2
	fi
	apt-get update -qq
	DEBIAN_FRONTEND=noninteractive apt-get install -y -qq mosquitto mosquitto-clients
fi

work_dir=$(mktemp -d)
broker_pid=
app_pid=
broker_config="$work_dir/mosquitto.conf"
app_log="$work_dir/phev2mqtt.log"
broker_log="$work_dir/mosquitto.log"

cleanup() {
	for pid in "$app_pid" "$broker_pid"; do
		if [[ -n "$pid" ]]; then
			kill "$pid" 2>/dev/null || true
			wait "$pid" 2>/dev/null || true
		fi
	done
	rm -rf "$work_dir"
}
trap cleanup EXIT

cat >"$broker_config" <<'EOF'
listener 1883 127.0.0.1
allow_anonymous true
persistence false
EOF

start_broker() {
	mosquitto -c "$broker_config" >"$broker_log" 2>&1 &
	broker_pid=$!
	for _ in $(seq 1 30); do
		if mosquitto_pub -h 127.0.0.1 -p 1883 -t live/test/probe -m ready >/dev/null 2>&1; then
			return 0
		fi
		sleep 1
	done
	echo "Mosquitto did not start." >&2
	cat "$broker_log" >&2
	return 1
}

wait_for_retained_value() {
	local topic=$1 expected=$2 label=$3 timeout=${4:-30} value=
	for _ in $(seq 1 "$timeout"); do
		value=$(mosquitto_sub -h 127.0.0.1 -p 1883 -t "$topic" -C 1 -W 2 2>/dev/null || true)
		if [[ "$value" == "$expected" ]]; then
			printf 'PASS: %s (topic=%s, payload=%s)\n' "$label" "$topic" "$value"
			return 0
		fi
		sleep 1
	done
	printf 'FAIL: %s did not reach %s (last payload=%s)\n' "$label" "$expected" "${value:-<empty>}" >&2
	tail -n 80 "$app_log" >&2 || true
	return 1
}

wait_for_retained_message() {
	local topic=$1 label=$2 timeout=${3:-30}
	for _ in $(seq 1 "$timeout"); do
		if mosquitto_sub -h 127.0.0.1 -p 1883 -t "$topic" -C 1 -W 2 >"$work_dir/message" 2>/dev/null &&
			[[ -s "$work_dir/message" ]]; then
			printf 'PASS: %s (topic=%s)\n' "$label" "$topic"
			return 0
		fi
		sleep 1
	done
	printf 'FAIL: timed out waiting for retained %s on %s\n' "$label" "$topic" >&2
	tail -n 80 "$app_log" >&2 || true
	return 1
}

wait_for_command_count() {
	local expected=$1 label=$2 count=0
	for _ in $(seq 1 30); do
		count=$(grep -Fc "Ignoring deprecated connection command: 'off'" "$app_log" || true)
		if (( count >= expected )); then
			printf 'PASS: %s\n' "$label"
			return 0
		fi
		sleep 1
	done
	printf 'FAIL: %s (received %s of %s command messages)\n' "$label" "$count" "$expected" >&2
	tail -n 80 "$app_log" >&2 || true
	return 1
}

start_broker
go build -o "$work_dir/phev2mqtt" .

"$work_dir/phev2mqtt" client mqtt \
	--address 127.0.0.1:18080 \
	--mqtt_server tcp://127.0.0.1:1883 \
	--mqtt_username mqtttest \
	--mqtt_password LiveTestSecret2026 \
	--mqtt_topic_prefix live \
	--ha_discovery_prefix hass \
	--vehicle_vin 1HGCM82633A004352 \
	--connection_retry_interval 30s \
	--availability_offline_timeout 1h \
	--verbosity info >"$app_log" 2>&1 &
app_pid=$!

vin=1HGCM82633A004352
discovery_topic="hass/sensor/${vin}_battery_level/config"

wait_for_retained_value live/available offline "initial offline availability"
wait_for_retained_value live/vin "$vin" "initial retained VIN"
wait_for_retained_message "$discovery_topic" "initial retained Home Assistant discovery"

mosquitto_pub -h 127.0.0.1 -p 1883 -t live/connection -m off
wait_for_command_count 1 "initial /connection subscription"

printf 'Restarting the isolated Mosquitto broker...\n'
kill "$broker_pid"
wait "$broker_pid" 2>/dev/null || true
broker_pid=
sleep 1
start_broker

wait_for_retained_value live/available offline "availability restored after broker restart"
wait_for_retained_value live/vin "$vin" "VIN restored after broker restart"
wait_for_retained_message "$discovery_topic" "discovery restored after broker restart"

mosquitto_pub -h 127.0.0.1 -p 1883 -t live/connection -m off
wait_for_command_count 2 "/connection subscription restored after broker restart"

mosquitto_pub -h 127.0.0.1 -p 1883 -t "$discovery_topic" -n -r
sleep 1
mosquitto_pub -h 127.0.0.1 -p 1883 -t hass/status -m offline
sleep 2
if mosquitto_sub -h 127.0.0.1 -p 1883 -t "$discovery_topic" -C 1 -W 2 >"$work_dir/unexpected" 2>/dev/null &&
	[[ -s "$work_dir/unexpected" ]]; then
	echo "FAIL: non-online Home Assistant birth payload replayed discovery." >&2
	exit 1
fi
echo "PASS: non-online Home Assistant birth payload did not replay discovery"

mosquitto_pub -h 127.0.0.1 -p 1883 -t hass/status -m online
wait_for_retained_message "$discovery_topic" "discovery restored after Home Assistant birth message"
wait_for_retained_value live/available offline "vehicle remains offline after Home Assistant birth message"

echo "Live MQTT broker restart test passed."

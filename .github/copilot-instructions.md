# Copilot Instructions for phev2mqtt

A robust MQTT gateway for Mitsubishi Outlander PHEV vehicles with Home Assistant integration and advanced WiFi management.

## Build, Test, and Development Commands

### Local Development (Go)

```bash
# Build the application locally
go build -o phev2mqtt .

# Run tests for the protocol package (where test coverage exists)
go test ./protocol

# Run all tests
go test ./...

# Build and run the MQTT client with environment variables
./phev2mqtt client mqtt \
  --mqtt_server tcp://192.168.1.2:1883/ \
  --mqtt_username phevmqttuser \
  --mqtt_password yourpassword
```

### Docker Development

```bash
# Build development image
docker build -f Dockerfile.dev -t phev2mqtt:dev .

# Run development container with docker-compose
docker-compose -f docker-compose.dev.yml up --build

# View logs
docker-compose -f docker-compose.dev.yml logs -f

# Stop container
docker-compose -f docker-compose.dev.yml down
```

### VS Code Tasks (Ctrl+Shift+P → Tasks: Run Task)

- **Docker: Build Development Image** - Build the Docker image
- **Docker: Rebuild and Run** - Rebuild image and start container
- **Docker: View Logs** - Follow container logs
- **Go: Build Local** - Compile binary locally (Ctrl+Shift+B)
- **Go: Run MQTT Client** - Build and run the client

### CI/CD

- **Release builds** are triggered by git tags (`v*`) and pushed to `ghcr.io/stefanh12/phev2mqtt`
- **Dev builds** on master branch pushed to `ghcr.io/stefanh12/phev2mqtt:master-dev`
- Container registry cleanup runs on schedule to manage image retention

## Architecture Overview

### Core Packages

**protocol/** - PHEV binary protocol implementation
- Handles packet encoding/decoding for Mitsubishi PHEV communication
- Implements XOR security encoding (reverse-engineered rolling code scheme)
- Packet format: Type, Length, Ack, Data, Checksum
- SecurityKey manages the encryption key map for session communication
- Contains unit tests for message encoding/decoding

**cmd/** - CLI command implementation (Cobra-based)
- `client mqtt` - Main MQTT gateway mode connecting PHEV to MQTT broker
- `client register` - Vehicle registration mode (one-time setup)
- `emulator` - PHEV device emulator for testing
- `root.go` - Base command configuration and validation
- Other commands: `decode`, `pcap` (packet capture), `hex` utilities

**client/** - MQTT client logic
- Establishes and manages PHEV WiFi connection
- Publishes vehicle state to MQTT
- Handles MQTT discovery for Home Assistant
- Manages connection retries and timeouts

**emulator/** - PHEV device simulator
- State machine for simulating vehicle responses
- Car connection simulator for protocol testing
- Used for development/testing without a real vehicle

### Configuration and Environment

- Configuration sources: `.env` file (primary), environment variables, CLI flags
- Uses Viper for config management and fsnotify for hot-reload detection
- `.env` file changes detected every 5 seconds (config_reload_interval)
- Hot-reloadable settings: update_interval, retry_interval, WiFi control topics
- Settings requiring restart: MQTT connection, logging, registration mode, routing

### Key Flows

1. **Startup**: Load config → Register with vehicle (if first-time) → Connect to MQTT → Start listening for updates
2. **Update cycle**: Timer triggers → Request vehicle state via PHEV protocol → Decode response → Publish to MQTT
3. **WiFi management**: Monitor connection status → Trigger remote WiFi restart if needed → Power-save mode (enable WiFi before update, disable after)
4. **Configuration reload**: File watcher detects `.env` changes → Reload applicable settings → Continue running without restart

## Key Conventions

### Protocol Packet Handling

- All PHEV packets follow the standard format with checksum validation
- XOR encoding uses a rolling code - security key changes throughout the session
- Message types have request/response pairs (e.g., type 0xf6 request vs 0xf7 response)
- Packets from car are XOR-encoded; encoding controlled via SecurityKey.keyMap

### Error Handling

- Security: Config file validation prevents path traversal and limits size to 1MB
- Connection: Timeouts are configurable (phev_tcp_read_timeout, phev_tcp_write_timeout, etc.)
- Encoding: Errors tracked with reset intervals to prevent cascade failures
- Logging: Five levels (none, error, warning, info, debug) controlled by log_level

### MQTT Topics and Discovery

- Uses standard MQTT Discovery format for Home Assistant integration
- Automatic discovery on startup if vehicle_vin configured; otherwise waits for first connection
- WiFi management topics customizable via remote_wifi_restart_topic, remote_wifi_control_topic
- Status published to standard MQTT prefix (default: homeassistant/)

### Docker and Deployment

- Development: `Dockerfile.dev` mounts workspace and builds on run
- Production: Uses `unraid/Dockerfile` for release builds
- Environment variables can be passed directly or via `.env` file
- Network routing: route_add parameter adds route to PHEV network (192.168.8.0/24)
- Registration mode: Only used once; `phev_register=true` in `.env` triggers registration

### Testing Notes

- Protocol package has unit tests for message encoding/decoding
- Tests use golden data with known input/output pairs
- Run specific test: `go test ./protocol -run TestDecodeEncodeBytes`
- Emulator useful for integration testing without real vehicle

## Common Development Tasks

### Adding a New PHEV Command

1. Define packet structure in `protocol/message.go` with Type and Data fields
2. Add parsing logic in `protocol/message.go` decode functions
3. Update client to send command and handle response via `client/client.go`
4. Publish results to appropriate MQTT topic
5. Add unit tests in `protocol/message_test.go`

### Debugging Protocol Issues

- Enable debug logging: `log_level=debug` in `.env`
- Use emulator for reproducible testing: `phev2mqtt emulator`
- Check protocol packet format in `protocol/README.md`
- Verify SecurityKey state if XOR encoding issues occur

### Home Assistant Integration Issues

- Verify vehicle_vin set or first connection occurred
- Check MQTT discovery topics published (typically `homeassistant/*/phev/*`)
- Use remote_wifi_control topics for WiFi management integration
- Lovelace example in `lovelace.yaml`

### Running in Production

- Use `docker-compose up -d` with `.env` file
- Monitor logs for configuration reload messages
- MikroTik integration: see `routeros.md` for WiFi bridge setup
- Unraid: use template in `unraid/phev2mqtt.xml` for easy installation

## References

- **Protocol details**: `protocol/README.md` - PHEV network transport, packet format, security
- **Deployment**: `DEPLOYMENT.md` - Comprehensive setup guide with examples
- **Development**: `README.dev.md` - VS Code, Docker, and local development
- **MikroTik setup**: `routeros.md` - WiFi bridge configuration
- **Home Assistant**: `lovelace.yaml` - Example dashboard configuration

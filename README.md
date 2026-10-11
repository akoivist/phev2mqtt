# phev2mqtt - Mitsubishi Outlander PHEV to MQTT Gateway

A robust MQTT gateway for Mitsubishi Outlander PHEV vehicles with Home Assistant integration and advanced WiFi management features.

Built on [buxtronix/phev2mqtt](https://github.com/buxtronix/phev2mqtt) and [CodeCutterUK/phev2mqtt](https://github.com/CodeCutterUK/phev2mqtt), this version is optimized for running on Unraid with separate VLAN support and enhanced reliability through MikroTik WiFi client bridge integration.

**Container Images:**
- GitHub Container Registry: `ghcr.io/akoivist/phev2mqtt:unraid` (built from the `unraid` branch)
- GitHub Container Registry: `ghcr.io/akoivist/phev2mqtt:latest` (tagged releases)


## Overview

This version separates Home Assistant, Unraid, and the WiFi client bridge into independent components. WiFi client availability is managed by MikroTik, which monitors the PHEV connection via ping and publishes MQTT status updates. The RBSXTsq2nD client bridge is highly stable with Model Year 2020 (MY20) and newer PHEV models and only disconnects when the vehicle is out of WiFi range.

**Key improvements:**
- Removed maximum connection time constraints (WiFi stability handled by MikroTik)
- Hot-reload configuration without container restarts
- Advanced timeout configuration for fine-tuned behavior
- Power-saving WiFi management
- Comprehensive logging levels

## Core Features

### Configuration Management

**Hot Reload Configuration**
The application automatically watches the `.env` file for changes and reloads configuration without requiring a container restart. Changes are detected every 5 seconds.

**Hot-reloadable settings:**
- `update_interval` - Update request frequency
- `retry_interval` - Connection retry frequency  
- `remote_wifi_restart_topic` - Remote restart MQTT topic
- `remote_wifi_restart_message` - Remote restart message
- Remote WiFi control and power save settings

**Settings requiring restart:**
- MQTT connection settings (server, username, password)
- Logging configuration
- PHEV registration mode
- Network routing configuration

To apply changes, simply edit your `.env` file and save. Changes will be applied within 5 seconds. Check the logs to confirm reload:
```
Configuration file changed, reloading...
Configuration reloaded successfully
```

### Logging Levels

Fine-grained control over application logging:
- `none` - Only fatal errors
- `error` - Error messages only
- `warning` - Warnings and errors
- `info` - Normal operation info (recommended for production)
- `debug` - Detailed debug information (for troubleshooting)

Set via `log_level` in `.env` file. Default: `info`

### Home Assistant Integration

**Automatic Discovery**
Home Assistant MQTT discovery is enabled by default. The application automatically publishes discovery messages for seamless integration with Home Assistant - no manual configuration needed. All PHEV sensors, switches, and controls will appear automatically in Home Assistant.

**Vehicle VIN Configuration**
Optionally configure your vehicle's VIN number (`vehicle_vin` in `.env`) to enable immediate Home Assistant discovery on startup, without waiting for PHEV connection. Leave empty to wait for VIN from vehicle (discovery will be delayed until first connection).

### Update Intervals

Configure how often the application requests updates from your PHEV:
- `update_interval` - Force update frequency (default: 5m)
- `retry_interval` - Connection retry frequency (default: 60s)

Examples: `5m`, `10m`, `15m` for update intervals; `1s`, `5s`, `10s` for retry intervals.

## WiFi Management Features

The application supports multiple WiFi management mechanisms to handle connection issues and optimize power consumption:

### Local WiFi Restart
Automatically restarts the local WiFi interface when connection to the PHEV is lost. Useful if phev2mqtt is running on hardware with its own WiFi interface.
- `local_wifi_restart_enabled=true` - Enable local WiFi restart
- `wifi_restart_time=10m` - Duration without connection before restarting
- `wifi_restart_command` - Optional custom restart command
- Note: Only works on some hardware configurations

### Remote WiFi Restart (MikroTik Integration)
Sends MQTT commands to remotely restart WiFi on external devices like MikroTik access points when connection is lost.
- `remote_wifi_restart_enabled=true` - Enable remote restart via MQTT
- `remote_wifi_restart_topic=mikrotik/phev/restart` - MQTT topic to publish to
- `remote_wifi_restart_message=restart` - Message payload to send
- The MikroTik script will receive the restart command and reset the WiFi interface
- Useful when using a dedicated WiFi bridge like RBSXTsq2nD

See the [routeros.md](routeros.md) file for MikroTik configuration examples.

### Remote WiFi Power Save Mode
Automatically turn off WiFi between update intervals to save power on your WiFi bridge or access point.
- `remote_wifi_power_save_enabled=true` - Enable power save mode
- `remote_wifi_control_topic` - MQTT topic for WiFi control commands
- `remote_wifi_enable_message={"wifi": "enable"}` - Message to enable WiFi
- `remote_wifi_disable_message={"wifi": "disable"}` - Message to disable WiFi
- `remote_wifi_power_save_wait=5s` - Time to wait after enabling WiFi for link establishment
- `remote_wifi_command_wait=10s` - Time to keep WiFi on after commands to receive status updates

**Requirements:**
- Only activates when `update_interval` > 1 minute
- Requires `remote_wifi_control_topic` to be configured
- WiFi is automatically turned on before each update, then off after completion
- When manual commands are sent, WiFi stays on longer to receive status updates

## Advanced Configuration

### Timeout Settings

Advanced timeout parameters are available for fine-tuning system behavior. **Warning:** Only modify these if you understand their impact, as incorrect values may cause connection issues, increased battery drain, or unexpected behavior.

**Connection and Retry Timeouts:**
- `connection_retry_interval=60s` - Time between connection retry attempts
- `availability_offline_timeout=30s` - Time before publishing MQTT offline status
- `remote_wifi_restart_min_interval=2m` - Minimum time between remote WiFi restart attempts

**PHEV Communication Timeouts:**
- `phev_start_timeout=20s` - Timeout for PHEV start command response
- `phev_register_timeout=10s` - Timeout for register set acknowledgment
- `phev_tcp_read_timeout=30s` - TCP read deadline for PHEV connection
- `phev_tcp_write_timeout=15s` - TCP write deadline for PHEV connection

**Other Timeouts:**
- `encoding_error_reset_interval=15s` - Time after which encoding error count resets
- `config_reload_interval=5s` - How often to check for configuration file changes

All timeout values support standard duration formats: `s` (seconds), `m` (minutes), `h` (hours).

## Deployment

### Initial Setup - Vehicle Registration

**First-time setup only:** Before using phev2mqtt, you must register it with your vehicle. This is a one-time process.

1. Put your PHEV into registration mode (consult your vehicle manual)
2. Set `phev_register=true` in your `.env` file
3. Start the container
4. Wait for successful registration (check logs)
5. Stop the container
6. Set `phev_register=false` in your `.env` file
7. Restart the container for normal operation

**Important:** The car registers the MAC address that connects to it. If using NAT or a WiFi bridge, ensure consistent network routing.

### Quick Start with Docker Compose

See [DEPLOYMENT.md](DEPLOYMENT.md) for comprehensive deployment instructions including:
- Docker Compose setup with `.env` file configuration
- Unraid deployment (recommended method with template)
- Environment variable reference
- Security best practices

**Basic setup:**
```bash
# 1. Copy example environment file
cp .env.example .env

# 2. Edit .env with your credentials
nano .env  # or use your preferred editor

# 3. Deploy
docker-compose up -d

# 4. View logs
docker-compose logs -f
```

### Development Setup

See [README.dev.md](README.dev.md) for development instructions including:
- VS Code setup with debugging support
- Docker development workflow
- Local Go development
- Available VS Code tasks

## Network Configuration

The application needs to route traffic to the PHEV network (192.168.8.0/24). Configure `route_add` in your `.env` file with your gateway IP address.

Example: If your router is at 192.168.1.1, set:
```
route_add=192.168.1.1
```

Leave empty if using host networking mode.

## Tested Hardware

- **Vehicle:** Mitsubishi Outlander PHEV MY20 and newer (Model Year 2020+)
- **WiFi Bridge:** MikroTik RBSXTsq2nD (highly recommended for stability)
- **Server:** Unraid with separate VLAN 308
- **Home Automation:** Home Assistant with MQTT integration

## Additional Resources

- **MikroTik Configuration:** [routeros.md](routeros.md) - Complete RouterOS setup with ping monitoring and MQTT integration
- **Unraid Template:** [unraid/phev2mqtt.xml](unraid/phev2mqtt.xml) - Container template for Unraid Community Applications
- **Home Assistant Lovelace:** [lovelace.yaml](lovelace.yaml) - Example Lovelace card configuration
- **Deployment Guide:** [DEPLOYMENT.md](DEPLOYMENT.md) - Detailed deployment instructions
- **Development Guide:** [README.dev.md](README.dev.md) - Developer setup and debugging

## Configuration Examples

### Basic Setup (.env)
```bash
# Required
mqtt_server=192.168.1.2:1883
mqtt_user=phevmqttuser
mqtt_password=your_secure_password
route_add=192.168.1.1

# Optional
log_level=info
update_interval=5m
vehicle_vin=
```

### With Remote WiFi Management (.env)
```bash
# Basic config...
mqtt_server=192.168.1.2:1883
mqtt_user=phevmqttuser
mqtt_password=your_secure_password

# Remote WiFi Restart (MikroTik)
remote_wifi_restart_enabled=true
remote_wifi_restart_topic=mikrotik/phev/restart
remote_wifi_restart_message=restart

# Remote WiFi Power Save
remote_wifi_power_save_enabled=true
remote_wifi_control_topic=homeassistant/sensor/mikrotik_sqtsqlite2garage/wifi
remote_wifi_enable_message={"wifi": "enable"}
remote_wifi_disable_message={"wifi": "disable"}
remote_wifi_power_save_wait=5s
remote_wifi_command_wait=10s
```

## License

This program is free software under the GNU General Public License v3.0. See [LICENSE](LICENSE) for details.

## Credits

Built on:
- [buxtronix/phev2mqtt](https://github.com/buxtronix/phev2mqtt) - Original PHEV to MQTT gateway
- [CodeCutterUK/phev2mqtt](https://github.com/CodeCutterUK/phev2mqtt) - Enhanced version

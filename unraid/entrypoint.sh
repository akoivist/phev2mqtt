#!/bin/bash

# Check for .env file in config directory and load it
if [ -f "/config/.env" ]; then
    echo "Loading configuration from /config/.env"
    # Export all variables from .env file
    set -a
    source /config/.env
    set +a
    
    # Check if the .env file has been edited by the user
    if grep -q "mqtt_password=your_secure_password_here" /config/.env; then
        echo ""
        echo "=========================================="
        echo "  ERROR: Default .env file detected"
        echo "=========================================="
        echo ""
        echo "The .env file has not been edited!"
        echo ""
        echo "Please update /config/.env with your actual:"
        echo "  - mqtt_password (REQUIRED)"
        echo "  - mqtt_server (if different)"
        echo "  - mqtt_user (if different)"
        echo "  - route_add (if needed)"
        echo ""
        echo "Container will exit now."
        echo "Restart after editing the configuration."
        echo "=========================================="
        echo ""
        exit 1
    fi
else
    # Create example .env file if it doesn't exist
    if [ -d "/config" ] && [ ! -f "/config/.env" ]; then
        echo "=========================================="
        echo "  Creating example .env file"
        echo "=========================================="
        cat > /config/.env << 'EOF'
# phev2mqtt Configuration
# Edit this file with your actual credentials and settings

# ==========================================
# MQTT Broker Configuration (REQUIRED)
# ==========================================
mqtt_server=192.168.1.2:1883
mqtt_user=phevmqttuser
mqtt_password=your_secure_password_here

# ==========================================
# PHEV Registration Mode
# ==========================================
# Set to 'true' ONLY when registering a new vehicle
# After successful registration, change back to 'false'
phev_register=false

# ==========================================
# Log Level
# ==========================================
# Set log level for application output
# Valid values: none, error, warning, info, debug
# - none: Only fatal errors
# - error: Error messages only
# - warning: Warnings and errors
# - info: Normal operation info (recommended for production)
# - debug: Detailed debug information (for troubleshooting)
# Default: info
log_level=info
# Enable timestamps in logs (true/false)
log_timestamps=false

# ==========================================
# Network Routing
# ==========================================
# IP address of the gateway that routes to your PHEV network (192.168.8.0/24)
# This is typically your router's IP address on the main network
# Example: If your router is at 192.168.1.1, set this to 192.168.1.1
route_add=192.168.1.1

# ==========================================
# Home Assistant Integration (optional)
# ==========================================
# Vehicle VIN for Home Assistant discovery
# Setting this enables immediate discovery on startup without waiting for PHEV connection
# Leave empty to wait for VIN from vehicle (discovery delayed until first connection)
vehicle_vin=

# ==========================================
# Update Interval (optional)
# ==========================================
# How often to request force updates from the PHEV
# Examples: 5m (5 minutes), 10m, 15m
# Leave empty to use default (5m)
update_interval=

# ==========================================
# Retry Interval (optional)
# ==========================================
# How often to retry connection when PHEV is not available
# Examples: 1s (1 second), 5s, 10s
# Leave empty to use default (1s)
retry_interval=

# ==========================================
# WiFi Restart Configuration (optional)
# ==========================================
# Automatically restart WiFi interface if connection to PHEV is lost
# NOTE: Only works on some hardware configurations
# local_wifi_restart_enabled: Set to 'true' to enable, 'false' to disable
# wifi_restart_time: Duration without connection before restarting WiFi (e.g., 5m, 10m)
# wifi_restart_command: Command to restart WiFi interface (leave empty for default)
local_wifi_restart_enabled=false
wifi_restart_time=10m
wifi_restart_command=

# ==========================================
# Remote WiFi Restart Configuration (optional)
# ==========================================
# Send MQTT command to restart WiFi on remote device (e.g., MikroTik access point)
# remote_wifi_restart_enabled: Set to 'true' to enable remote restart via MQTT
# remote_wifi_restart_topic: MQTT topic to publish restart command to
# remote_wifi_restart_message: Message payload to send (default: "restart")
# Example for MikroTik: topic="mikrotik/phev/restart", message="restart"
remote_wifi_restart_enabled=false
remote_wifi_restart_topic=mikrotik/phev/restart
remote_wifi_restart_message=restart

# ==========================================
# Remote WiFi Control Configuration (optional)
# ==========================================
# Send MQTT commands to enable/disable WiFi on remote device
# remote_wifi_control_topic: MQTT topic to publish wifi control commands to
# remote_wifi_enable_message: Message payload to enable wifi
# remote_wifi_disable_message: Message payload to disable wifi
remote_wifi_control_topic=homeassistant/sensor/mikrotik_sqtsqlite2garage/wifi
remote_wifi_enable_message={"wifi": "enable"}
remote_wifi_disable_message={"wifi": "disable"}

# Remote WiFi Power Save Mode (optional)
# Automatically turn off WiFi between update intervals to save power
# Only activates when update_interval > 1 minute and remote_wifi_control_topic is configured
# WiFi is turned on shortly before each update, then off after completion
remote_wifi_power_save_enabled=false
remote_wifi_power_save_wait=5s
remote_wifi_power_save_duration=30s
remote_wifi_command_wait=10s

# ==========================================
# Advanced Timeout Settings (optional)
# ==========================================
# WARNING: Only change these if you know what you are doing!
# Connection and Retry Timeouts
connection_retry_interval=60s
availability_offline_timeout=30s
remote_wifi_restart_min_interval=2m
# PHEV Communication Timeouts
phev_start_timeout=20s
phev_register_timeout=10s
phev_tcp_read_timeout=30s
phev_tcp_write_timeout=15s
# Error Handling
encoding_error_reset_interval=15s
# Configuration Reload
config_reload_interval=5s

# ==========================================
# Additional Arguments (optional)
# ==========================================
# Any extra command-line arguments to pass to phev2mqtt
# Leave empty if not needed
extra_add=""
EOF
        echo ""
        echo "✓ Example .env file created at /config/.env"
        echo ""
        echo "IMPORTANT: Please follow these steps:"
        echo "  1. Edit /config/.env with your actual settings"
        echo "  2. Update mqtt_password with your MQTT password"
        echo "  3. Restart the container to apply changes"
        echo ""
        echo "Note: Container is currently using environment"
        echo "      variables from the Docker template."
        echo "=========================================="
    fi
fi

export CONNECT_log_level=$log_level
export CONNECT_log_timestamps=$log_timestamps
export CONNECT_phev_register=$phev_register
export CONNECT_mqtt_server=$mqtt_server
export CONNECT_mqtt_user=$mqtt_user
export CONNECT_mqtt_password=$mqtt_password
export CONNECT_route_add=$route_add
export CONNECT_extra_add=$extra_add
export CONNECT_update_interval=$update_interval
export CONNECT_local_wifi_restart_enabled=$local_wifi_restart_enabled
export CONNECT_wifi_restart_time=$wifi_restart_time
export CONNECT_wifi_restart_command=$wifi_restart_command
export CONNECT_remote_wifi_restart_enabled=$remote_wifi_restart_enabled
export CONNECT_remote_wifi_restart_topic=$remote_wifi_restart_topic
export CONNECT_remote_wifi_restart_message=$remote_wifi_restart_message
export CONNECT_remote_wifi_control_topic=$remote_wifi_control_topic
export CONNECT_remote_wifi_enable_message=$remote_wifi_enable_message
export CONNECT_remote_wifi_disable_message=$remote_wifi_disable_message
export CONNECT_remote_wifi_power_save_enabled=$remote_wifi_power_save_enabled
export CONNECT_remote_wifi_power_save_wait=$remote_wifi_power_save_wait
export CONNECT_remote_wifi_power_save_duration=$remote_wifi_power_save_duration
export CONNECT_remote_wifi_command_wait=$remote_wifi_command_wait
export CONNECT_vehicle_vin=$vehicle_vin

# Advanced timeout settings
export CONNECT_connection_retry_interval=$connection_retry_interval
export CONNECT_availability_offline_timeout=$availability_offline_timeout
export CONNECT_remote_wifi_restart_min_interval=$remote_wifi_restart_min_interval
export CONNECT_phev_start_timeout=$phev_start_timeout
export CONNECT_phev_register_timeout=$phev_register_timeout
export CONNECT_phev_tcp_read_timeout=$phev_tcp_read_timeout
export CONNECT_phev_tcp_write_timeout=$phev_tcp_write_timeout
export CONNECT_encoding_error_reset_interval=$encoding_error_reset_interval
export CONNECT_config_reload_interval=$config_reload_interval


echo "Using the following environment variables:"
echo "log_level=$CONNECT_log_level"
echo "log_timestamps=$CONNECT_log_timestamps"
echo "phev_register=$CONNECT_phev_register"
echo "mqtt_server=$CONNECT_mqtt_server"
#echo "mqtt_user=$CONNECT_mqtt_user"
#echo "mqtt_password=$CONNECT_mqtt_password"
echo "route_add=$CONNECT_route_add"
echo "extra_add=$CONNECT_extra_add"
echo "update_interval=$CONNECT_update_interval"
echo "local_wifi_restart_enabled=$CONNECT_local_wifi_restart_enabled"
if [[ "$CONNECT_local_wifi_restart_enabled" == "true" ]]; then
    echo "  wifi_restart_time=$CONNECT_wifi_restart_time"
    [[ -n "$CONNECT_wifi_restart_command" ]] && echo "  wifi_restart_command=$CONNECT_wifi_restart_command"
fi
echo "remote_wifi_restart_enabled=$CONNECT_remote_wifi_restart_enabled"
if [[ "$CONNECT_remote_wifi_restart_enabled" == "true" ]]; then
    echo "  remote_wifi_restart_topic=$CONNECT_remote_wifi_restart_topic"
    echo "  remote_wifi_restart_message=$CONNECT_remote_wifi_restart_message"
fi

# Add route if configured
if [[ -n "$CONNECT_route_add" ]]; then
    echo "Adding route to 192.168.8.0/24 via gateway $CONNECT_route_add"
    route add -net 192.168.8.0 netmask 255.255.255.0 gw $CONNECT_route_add eth0
else
    echo "Warning: route_add not set. This may cause connectivity issues with the PHEV (192.168.8.0/24)"
fi

# Set log level from environment variable
if [[ -z $CONNECT_log_level ]]; then
    LOG_LEVEL="info"
else
    LOG_LEVEL=$CONNECT_log_level
fi
echo "Starting phev2mqtt with log level: $LOG_LEVEL"

if [[ $CONNECT_phev_register == "true" ]]
then
	echo "Registering client with PHEV"
	exec /usr/local/bin/phev2mqtt client register --verbosity "$LOG_LEVEL"
else
    echo "Starting phev2mqtt with log level: $LOG_LEVEL"
    
    # Build command with optional parameters
    CMD_ARGS=(
        client
        mqtt
        --verbosity "$LOG_LEVEL"
        --mqtt_server "tcp://$CONNECT_mqtt_server/"
        --mqtt_username "$CONNECT_mqtt_user"
        --mqtt_password "$CONNECT_mqtt_password"
    )
    
    # Add optional parameters if set
    [[ -n "$CONNECT_update_interval" ]] && CMD_ARGS+=(--update_interval "$CONNECT_update_interval")
    [[ -n "$CONNECT_vehicle_vin" ]] && CMD_ARGS+=(--vehicle_vin "$CONNECT_vehicle_vin")
    
    # Add WiFi restart parameters only if enabled
    if [[ "$CONNECT_local_wifi_restart_enabled" == "true" ]]; then
        [[ -n "$CONNECT_wifi_restart_time" ]] && CMD_ARGS+=(--wifi_restart_time "$CONNECT_wifi_restart_time")
        [[ -n "$CONNECT_wifi_restart_command" ]] && CMD_ARGS+=(--wifi_restart_command "$CONNECT_wifi_restart_command")
    fi
    
    # Add remote WiFi restart parameters only if enabled
    if [[ "$CONNECT_remote_wifi_restart_enabled" == "true" ]]; then
        [[ -n "$CONNECT_remote_wifi_restart_topic" ]] && CMD_ARGS+=(--remote_wifi_restart_topic "$CONNECT_remote_wifi_restart_topic")
        [[ -n "$CONNECT_remote_wifi_restart_message" ]] && CMD_ARGS+=(--remote_wifi_restart_message "$CONNECT_remote_wifi_restart_message")
    fi
    
    # Add remote WiFi control parameters if set
    [[ -n "$CONNECT_remote_wifi_control_topic" ]] && CMD_ARGS+=(--remote_wifi_control_topic "$CONNECT_remote_wifi_control_topic")
    [[ -n "$CONNECT_remote_wifi_enable_message" ]] && CMD_ARGS+=(--remote_wifi_enable_message "$CONNECT_remote_wifi_enable_message")
    [[ -n "$CONNECT_remote_wifi_disable_message" ]] && CMD_ARGS+=(--remote_wifi_disable_message "$CONNECT_remote_wifi_disable_message")
    [[ "$CONNECT_remote_wifi_power_save_enabled" == "true" ]] && CMD_ARGS+=(--remote_wifi_power_save_enabled)
    [[ -n "$CONNECT_remote_wifi_power_save_wait" ]] && CMD_ARGS+=(--remote_wifi_power_save_wait "$CONNECT_remote_wifi_power_save_wait")
    [[ -n "$CONNECT_remote_wifi_power_save_duration" ]] && CMD_ARGS+=(--remote_wifi_power_save_duration "$CONNECT_remote_wifi_power_save_duration")
    [[ -n "$CONNECT_remote_wifi_command_wait" ]] && CMD_ARGS+=(--remote_wifi_command_wait "$CONNECT_remote_wifi_command_wait")
    
    # Add advanced timeout settings if set
    [[ -n "$CONNECT_connection_retry_interval" ]] && CMD_ARGS+=(--connection_retry_interval "$CONNECT_connection_retry_interval")
    [[ -n "$CONNECT_availability_offline_timeout" ]] && CMD_ARGS+=(--availability_offline_timeout "$CONNECT_availability_offline_timeout")
    [[ -n "$CONNECT_remote_wifi_restart_min_interval" ]] && CMD_ARGS+=(--remote_wifi_restart_min_interval "$CONNECT_remote_wifi_restart_min_interval")
    [[ -n "$CONNECT_phev_start_timeout" ]] && CMD_ARGS+=(--phev_start_timeout "$CONNECT_phev_start_timeout")
    [[ -n "$CONNECT_phev_register_timeout" ]] && CMD_ARGS+=(--phev_register_timeout "$CONNECT_phev_register_timeout")
    [[ -n "$CONNECT_phev_tcp_read_timeout" ]] && CMD_ARGS+=(--phev_tcp_read_timeout "$CONNECT_phev_tcp_read_timeout")
    [[ -n "$CONNECT_phev_tcp_write_timeout" ]] && CMD_ARGS+=(--phev_tcp_write_timeout "$CONNECT_phev_tcp_write_timeout")
    [[ -n "$CONNECT_encoding_error_reset_interval" ]] && CMD_ARGS+=(--encoding_error_reset_interval "$CONNECT_encoding_error_reset_interval")
    [[ -n "$CONNECT_config_reload_interval" ]] && CMD_ARGS+=(--config_reload_interval "$CONNECT_config_reload_interval")    
    [[ -n "$CONNECT_extra_add" ]] && CMD_ARGS+=($CONNECT_extra_add)
    
    exec /usr/local/bin/phev2mqtt "${CMD_ARGS[@]}"
fi

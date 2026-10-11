CA xml template for running the phev2mqtt on docker in unraid instead of on HA. 

You can install this by adding the xml file to /boot/config/plugins/dockerMan/templates-user on your Unraid USB. then in CA add container from templates. There you will find it.

The container image is located at https://github.com/akoivist/phev2mqtt/pkgs/container/phev2mqtt and is built from the `unraid` branch by GitHub Actions.

## Configuration

All configuration is done via the `/config/.env` file. On first start, an example file will be created. Edit it with your settings:

### Required Settings
- `mqtt_server` - MQTT broker address (e.g., 192.168.1.2:1883)
- `mqtt_user` - MQTT username
- `mqtt_password` - MQTT password
- `route_add` - Gateway IP for routing to PHEV network (192.168.8.0/24)

### Optional Settings
- `debug` - Set to `true` for detailed debug logging
- `phev_register` - Set to `true` only when registering a new vehicle
- `update_interval` - How often to request updates from PHEV (default: 5m)

### WiFi Restart Features

Two WiFi restart mechanisms are available:

**Local WiFi Restart** (for systems with built-in WiFi):
- `local_wifi_restart_enabled=true` - Enable local WiFi restart
- `wifi_restart_time=10m` - Time to wait before restarting
- Note: Only works on some hardware configurations

**Remote WiFi Restart** (for MikroTik or external WiFi bridges):
- `remote_wifi_restart_enabled=true` - Enable remote restart via MQTT
- `remote_wifi_restart_topic=mikrotik/phev/restart` - MQTT topic to publish to
- `remote_wifi_restart_message=restart` - Message to send

The setup is only tested with one setup. That is Home Assistant running on a pi4, Unraid and Mikrotik SXTsq Lite2. 

The SXTsq needs the package add-on mqtt, this enables home assistant to automate actions towards phev2mqtt depending on the connection. Most common issue is that the wifi is lost from phev and the link goes down. With remote WiFi restart enabled, phev2mqtt can automatically trigger the MikroTik to restart its WiFi interface via MQTT, which usually resolves the connection issue.

## Hot Reload

Configuration changes in `/config/.env` are automatically detected and reloaded every 5 seconds without requiring a container restart. Simply edit the file and save - changes will be applied automatically.

**Hot-reloadable settings:**
- `update_interval`
- `retry_interval`
- `remote_wifi_restart_topic`
- `remote_wifi_restart_message`

**Requires restart:**
- MQTT connection settings
- Debug level
- Registration mode
- Network routing

Check container logs to see confirmation of reload.


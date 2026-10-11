/*
Copyright © 2021 Ben Buxton <bbuxton@gmail.com>

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program. If not, see <http://www.gnu.org/licenses/>.
*/
package cmd

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/buxtronix/phev2mqtt/client"
	"github.com/buxtronix/phev2mqtt/protocol"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	log "github.com/sirupsen/logrus"
)

const defaultWifiRestartCmd = "sudo ip link set wlan0 down && sleep 3 && sudo ip link set wlan0 up"

const (
	maxMQTTMessageSize = 10240   // 10KB - reasonable for control messages
	maxMQTTPayloadSize = 1048576 // 1MB - absolute maximum
	minUpdateInterval  = 30 * time.Second
	maxUpdateInterval  = 24 * time.Hour
)

// validateVIN validates Vehicle Identification Number format
func validateVIN(vin string) error {
	if vin == "" {
		return nil // VIN is optional
	}

	// VIN must be 16 or 17 characters (17 is ISO 3779 standard, some older vehicles use 16)
	if len(vin) != 16 && len(vin) != 17 {
		return fmt.Errorf("vehicle_vin must be 16 or 17 characters, got %d", len(vin))
	}

	// VIN should only contain alphanumeric (excluding I, O, Q to avoid confusion with 1, 0)
	// Convert to uppercase for validation
	vinUpper := strings.ToUpper(vin)
	for i, ch := range vinUpper {
		if !((ch >= 'A' && ch <= 'H') || (ch >= 'J' && ch <= 'N') || (ch >= 'P' && ch <= 'Z' && ch != 'Q') || (ch >= '0' && ch <= '9')) {
			return fmt.Errorf("vehicle_vin contains invalid character '%c' at position %d (must be A-Z, 0-9, excluding I, O, Q)", ch, i+1)
		}
	}

	log.Infof("VIN validated: %s (%d characters)", vin, len(vin))
	return nil
}

// validateMQTTMessage validates MQTT message payload size
func validateMQTTMessage(message, messageName string) error {
	if message == "" {
		return nil // Empty is allowed (will use defaults)
	}

	if len(message) > maxMQTTPayloadSize {
		return fmt.Errorf("%s exceeds absolute maximum (%d bytes, max %d)",
			messageName, len(message), maxMQTTPayloadSize)
	}

	if len(message) > maxMQTTMessageSize {
		log.Warnf("SECURITY WARNING: %s is large (%d bytes), consider keeping under %d bytes",
			messageName, len(message), maxMQTTMessageSize)
	}

	return nil
}

// validateMQTTTopic validates MQTT topic format for security
func validateMQTTTopic(topic string, topicName string, allowWildcards bool) error {
	if topic == "" {
		return nil // Optional topics can be empty
	}

	// Check length - MQTT spec allows up to 65535 bytes
	if len(topic) > 65535 {
		return fmt.Errorf("%s is too long (max 65535 bytes)", topicName)
	}

	// Reasonable practical limit
	if len(topic) > 512 {
		log.Warnf("SECURITY WARNING: %s is very long (%d chars), this may be suspicious", topicName, len(topic))
	}

	// Check for null bytes
	if strings.Contains(topic, "\x00") {
		return fmt.Errorf("%s contains null bytes", topicName)
	}

	// Check for wildcards if not allowed
	if !allowWildcards {
		if strings.Contains(topic, "+") {
			return fmt.Errorf("%s cannot contain single-level wildcard (+)", topicName)
		}
		if strings.Contains(topic, "#") {
			return fmt.Errorf("%s cannot contain multi-level wildcard (#)", topicName)
		}
	}

	// Check for invalid MQTT topic characters (control characters except tab)
	for i, r := range topic {
		if r < 0x20 && r != 0x09 { // Control chars except tab
			return fmt.Errorf("%s contains invalid control character at position %d", topicName, i)
		}
	}

	// MQTT topics shouldn't start or end with /
	if strings.HasPrefix(topic, "/") {
		log.Warnf("SECURITY WARNING: %s starts with '/', this is unusual", topicName)
	}
	if strings.HasSuffix(topic, "/") && !strings.HasSuffix(topic, "/#") {
		log.Warnf("SECURITY WARNING: %s ends with '/', this is unusual", topicName)
	}

	// Check for suspicious patterns that might indicate injection
	suspiciousPatterns := []string{
		"${",
		"$(",
		"`",
		"<script",
		"javascript:",
	}
	lowerTopic := strings.ToLower(topic)
	for _, pattern := range suspiciousPatterns {
		if strings.Contains(lowerTopic, pattern) {
			return fmt.Errorf("%s contains suspicious pattern: %q", topicName, pattern)
		}
	}

	return nil
}

// validateMQTTCredentials performs security validation on MQTT configuration
func validateMQTTCredentials(server, password string) error {
	// Validate MQTT server URL
	if server == "" {
		return fmt.Errorf("mqtt_server is required")
	}

	// Check for valid protocol prefix
	if !strings.HasPrefix(server, "tcp://") &&
		!strings.HasPrefix(server, "ssl://") &&
		!strings.HasPrefix(server, "tls://") &&
		!strings.HasPrefix(server, "ws://") &&
		!strings.HasPrefix(server, "wss://") {
		return fmt.Errorf("mqtt_server must start with tcp://, ssl://, tls://, ws://, or wss://")
	}

	// Warn if using unencrypted connection
	if strings.HasPrefix(server, "tcp://") || strings.HasPrefix(server, "ws://") {
		log.Warnf("SECURITY WARNING: MQTT connection is not encrypted (using %s). Consider using ssl:// or tls://",
			server[:6])
	}

	// Extract and validate port
	serverPart := server
	if idx := strings.Index(server, "://"); idx != -1 {
		serverPart = server[idx+3:]
	}

	// Check for port
	if strings.Contains(serverPart, ":") {
		parts := strings.Split(serverPart, ":")
		if len(parts) == 2 {
			portStr := parts[1]
			// Remove any path components
			if idx := strings.Index(portStr, "/"); idx != -1 {
				portStr = portStr[:idx]
			}
			port := 0
			if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
				return fmt.Errorf("invalid port in mqtt_server: %s", portStr)
			}
			if port < 1 || port > 65535 {
				return fmt.Errorf("invalid port in mqtt_server: must be 1-65535, got %d", port)
			}
		}
	}

	// Validate MQTT password
	if password == "" {
		return fmt.Errorf("mqtt_password is required for security")
	}

	if len(password) < 8 {
		return fmt.Errorf("mqtt_password is too weak (minimum 8 characters required)")
	}

	if len(password) < 12 {
		log.Warnf("SECURITY WARNING: mqtt_password is short (<%d chars). Consider using at least 12 characters.", len(password))
	}

	// Check for common weak passwords
	weakPasswords := []string{
		"password", "12345678", "123456789", "admin123", "password123",
		"mqtt1234", "test1234", "testpass", "admin", "mqtt",
	}
	for _, weak := range weakPasswords {
		if strings.ToLower(password) == weak {
			return fmt.Errorf("mqtt_password is too weak (common password detected)")
		}
	}

	return nil
}

// mqttCmd represents the mqtt command
var mqttCmd = &cobra.Command{
	Use:   "mqtt",
	Short: "Start an MQTT bridge.",
	Long: `Maintains a connected to the Phev (retry as needed) and also to an MQTT server.

Status data from the car is passed to the MQTT topics, and also some commands from MQTT
are sent to control certain aspects of the car. See the phev2mqtt Github page for
more details on the topics.
`,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		mc := &mqttClient{climate: new(climate)}
		return mc.Run(cmd, args)
	},
}

// Tracks complete climate state as on and mode are separately
// sent by the car.
type climate struct {
	state *protocol.PreACState
	mode  *string
}

func (c *climate) setMode(m string) {
	c.mode = &m
}
func (c *climate) setState(state protocol.PreACState) {
	c.state = &state
}

func (c *climate) mqttStates() map[string]string {
	m := map[string]string{
		"/climate/state":      "off",
		"/climate/cool":       "off",
		"/climate/heat":       "off",
		"/climate/windscreen": "off",
	}
	if c.mode == nil || c.state == nil {
		return m
	}
	switch *c.state {
	case protocol.PreACOn:
		m["/climate/state"] = *c.mode
	case protocol.PreACOff:
		{
			m["/climate/state"] = "off"
			return m
		}
	case protocol.PreACTerminated:
		{
			m["/climate/state"] = "terminated"
			return m
		}
	default:
		{
			m["/climate/state"] = "unknown"
			return m
		}
	}
	m["/climate/state"] = *c.mode
	switch *c.mode {
	case "cool":
		m["/climate/cool"] = "on"
	case "heat":
		m["/climate/heat"] = "on"
	case "windscreen":
		m["/climate/windscreen"] = "on"
	}
	return m
}

var lastWifiRestart time.Time

// validateWifiRestartCommand checks for dangerous patterns in restart commands
func validateWifiRestartCommand(cmd string) error {
	if cmd == "" {
		return nil
	}

	// Check for dangerous shell operators that could be used for command injection
	dangerousPatterns := []string{
		";", "|", "&", "$(", "`", ">", "<", "\n", "\r",
	}

	for _, pattern := range dangerousPatterns {
		if strings.Contains(cmd, pattern) {
			// Allow && for chaining commands, but warn
			if pattern == "&" && strings.Contains(cmd, "&&") {
				log.Warnf("SECURITY: wifi_restart_command contains command chaining (&&). Ensure this is intentional.")
				continue
			}
			return fmt.Errorf("command contains potentially dangerous pattern: %q", pattern)
		}
	}

	// Check command length to prevent buffer issues
	if len(cmd) > 1024 {
		return fmt.Errorf("command too long (max 1024 characters)")
	}

	return nil
}

func restartWifi(cmd *cobra.Command, m *mqttClient) error {
	// Check if local WiFi restart is enabled
	localEnabled := viper.GetBool("local_wifi_restart_enabled")
	restartCommand := viper.GetString("wifi_restart_command")

	if localEnabled && restartCommand != "" {
		// Security: Validate command to prevent command injection
		if err := validateWifiRestartCommand(restartCommand); err != nil {
			log.Errorf("SECURITY: Invalid wifi_restart_command: %v", err)
			log.Errorf("SECURITY: Command rejected to prevent potential command injection")
			return err
		}

		log.Infof("Attempting to restart local WiFi interface")
		restartCmd := exec.Command("/bin/sh", "-c", restartCommand)
		stdoutStderr, err := restartCmd.CombinedOutput()
		if len(stdoutStderr) > 0 {
			log.Infof("Output from local WiFi restart: %s", stdoutStderr)
		}
		if err != nil {
			log.Errorf("Error restarting local WiFi: %v", err)
		}
	} else {
		log.Debugf("Local WiFi restart disabled")
	}

	// Check if remote WiFi restart is enabled
	remoteEnabled := viper.GetBool("remote_wifi_restart_enabled")
	if remoteEnabled && m.remoteWifiRestartTopic != "" {
		if time.Now().Sub(m.lastRemoteWifiRestart) > m.remoteWifiRestartMinInterval {
			m.restartRemoteWifi()
		}
	} else {
		log.Debugf("Remote WiFi restart disabled")
	}

	return nil
}

func (m *mqttClient) restartRemoteWifi() {
	if m.remoteWifiRestartTopic == "" {
		return
	}

	m.lastRemoteWifiRestart = time.Now()

	message := m.remoteWifiRestartMessage
	if message == "" {
		message = "restart"
	}

	log.Infof("Sending remote WiFi restart command to topic: %s", m.remoteWifiRestartTopic)

	token := m.client.Publish(m.remoteWifiRestartTopic, 0, false, message)
	token.Wait()
	if token.Error() != nil {
		log.Errorf("Error publishing remote WiFi restart: %v", token.Error())
	} else {
		log.Infof("Remote WiFi restart command sent successfully")
	}
}

func (m *mqttClient) remoteWifiEnable() {
	if m.remoteWifiControlTopic == "" {
		log.Debugf("Remote WiFi control topic not configured")
		return
	}

	message := m.remoteWifiEnableMessage
	if message == "" {
		message = `{"wifi": "enable"}`
	}

	log.Infof("[WiFi Control] Enabling WiFi on remote device")
	log.Infof("[WiFi Control] Publishing ENABLE to topic: %s with message: %s", m.remoteWifiControlTopic, message)

	token := m.client.Publish(m.remoteWifiControlTopic, 0, false, message)
	token.Wait()
	if token.Error() != nil {
		log.Errorf("[WiFi Control] Failed to send ENABLE command: %v", token.Error())
	} else {
		log.Infof("[WiFi Control] ENABLE command published successfully")
	}
}

func (m *mqttClient) remoteWifiDisable() {
	if m.remoteWifiControlTopic == "" {
		log.Debugf("Remote WiFi control topic not configured")
		return
	}

	message := m.remoteWifiDisableMessage
	if message == "" {
		message = `{"wifi": "disable"}`
	}

	log.Infof("[WiFi Control] Disabling WiFi on remote device")
	log.Infof("[WiFi Control] Publishing DISABLE to topic: %s with message: %s", m.remoteWifiControlTopic, message)

	token := m.client.Publish(m.remoteWifiControlTopic, 0, false, message)
	token.Wait()
	if token.Error() != nil {
		log.Errorf("[WiFi Control] Failed to send DISABLE command: %v", token.Error())
	} else {
		log.Infof("[WiFi Control] DISABLE command published successfully")
	}
}

// ensureWifiOn ensures WiFi is on before sending a command, even if power save is active
func (m *mqttClient) ensureWifiOn() {
	// Only act if power save mode is enabled and WiFi is currently off
	if m.remoteWifiPowerSaveEnabled && m.remoteWifiControlTopic != "" && !m.powerSaveWifiOn {
		log.Infof("Command requested while power save active - turning on WiFi")
		m.remoteWifiEnable()
		log.Infof("Waiting %v for WiFi link to establish before sending command", m.remoteWifiPowerSaveWait)
		time.Sleep(m.remoteWifiPowerSaveWait)
		m.powerSaveWifiOn = true
	}
	// Track command time to keep WiFi on for status updates
	m.lastCommandTime = time.Now()
	m.requestCommandConnect()
}

func (m *mqttClient) requestCommandConnect() {
	if !m.enabled {
		log.Infof("[Command] Connection disabled, enabling for command")
		m.enabled = true
	}
	select {
	case m.commandWake <- struct{}{}:
	default:
	}
}

func (m *mqttClient) setConnected(connected bool) {
	m.mu.Lock()
	m.connected = connected
	m.mu.Unlock()
	if connected {
		select {
		case m.connectedCh <- struct{}{}:
		default:
		}
	}
}

func (m *mqttClient) waitForConnection(timeout time.Duration) bool {
	m.mu.RLock()
	if m.connected {
		m.mu.RUnlock()
		return true
	}
	m.mu.RUnlock()
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-m.connectedCh:
		return true
	case <-timer.C:
		return false
	}
}

func (m *mqttClient) ensureConnectedForCommand() bool {
	m.ensureWifiOn()
	if m.waitForConnection(m.remoteWifiPowerSaveWait + m.phevStartTimeout + 5*time.Second) {
		return true
	}
	log.Warnf("Command requested but PHEV connection not ready")
	return false
}

type mqttClient struct {
	client         mqtt.Client
	options        *mqtt.ClientOptions
	mqttData       map[string]string
	updateInterval time.Duration

	phev        *client.Client
	lastConnect time.Time
	lastError   error

	prefix string

	haDiscovery          bool
	haDiscoveryPrefix    string
	haPublishedDiscovery bool
	vehicleVIN           string
	lastVIN              string
	disableRegisterSet   bool
	discoveryMu          sync.Mutex
	recoveryMu           sync.Mutex

	climate *climate
	enabled bool

	// WiFi restart settings
	wifiRestartTime          time.Duration
	wifiRestartCommand       string
	localWifiRestartEnabled  bool
	remoteWifiRestartEnabled bool

	// Remote WiFi restart via MQTT
	remoteWifiRestartTopic   string
	remoteWifiRestartMessage string
	lastRemoteWifiRestart    time.Time

	// Remote WiFi enable/disable control via MQTT
	remoteWifiControlTopic      string
	remoteWifiEnableMessage     string
	remoteWifiDisableMessage    string
	remoteWifiPowerSaveEnabled  bool
	remoteWifiPowerSaveWait     time.Duration
	remoteWifiPowerSaveDuration time.Duration
	lastUpdateTime              time.Time
	powerSaveWifiOn             bool
	lastCommandTime             time.Time
	remoteWifiCommandWait       time.Duration

	// Advanced timeout settings
	connectionRetryInterval      time.Duration
	availabilityOfflineTimeout   time.Duration
	remoteWifiRestartMinInterval time.Duration
	encodingErrorResetInterval   time.Duration
	configReloadInterval         time.Duration
	phevStartTimeout             time.Duration
	phevRegisterTimeout          time.Duration
	phevTCPReadTimeout           time.Duration
	phevTCPWriteTimeout          time.Duration

	// Configuration hot reload
	configReloader *ConfigReloader
	mu             sync.RWMutex
	connected      bool
	connectedCh    chan struct{}
	commandWake    chan struct{}
}

func (m *mqttClient) topic(topic string) string {
	return fmt.Sprintf("%s%s", m.prefix, topic)
}

func mqttSubscriptionTopics(prefix, discoveryPrefix string, disableRegisterSet, haDiscovery bool) map[string]byte {
	topics := map[string]byte{
		prefix + "/connection": 0,
		prefix + "/settings/#": 0,
	}
	if !disableRegisterSet {
		topics[prefix+"/set/#"] = 0
	}
	if haDiscovery {
		topics[strings.TrimSuffix(discoveryPrefix, "/")+"/status"] = 0
	}
	return topics
}

func isHomeAssistantBirthMessage(topic, payload, birthTopic string) bool {
	return topic == birthTopic && strings.EqualFold(strings.TrimSpace(payload), "online")
}

func availabilityPayload(connected bool) string {
	if connected {
		return "online"
	}
	return "offline"
}

func discoveryVIN(configuredVIN, lastVIN string) string {
	if configuredVIN != "" {
		return configuredVIN
	}
	return lastVIN
}

func publishMQTT(client mqtt.Client, topic string, qos byte, retained bool, payload interface{}) error {
	if client == nil {
		return fmt.Errorf("MQTT client is not initialized")
	}
	token := client.Publish(topic, qos, retained, payload)
	token.Wait()
	return token.Error()
}

func (m *mqttClient) isConnected() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.connected
}

func (m *mqttClient) currentDiscoveryVIN() string {
	m.discoveryMu.Lock()
	defer m.discoveryMu.Unlock()
	return discoveryVIN(m.vehicleVIN, m.lastVIN)
}

func (m *mqttClient) publishCurrentAvailability(client mqtt.Client) {
	payload := availabilityPayload(m.isConnected())
	if err := publishMQTT(client, m.topic("/available"), 0, true, payload); err != nil {
		log.Errorf("Failed to publish availability status %q: %v", payload, err)
	}
}

func (m *mqttClient) restoreMQTTState(client mqtt.Client) error {
	m.recoveryMu.Lock()
	defer m.recoveryMu.Unlock()

	if client == nil || !client.IsConnected() {
		return fmt.Errorf("broker disconnected before MQTT state restoration")
	}

	topics := mqttSubscriptionTopics(m.prefix, m.haDiscoveryPrefix, m.disableRegisterSet, m.haDiscovery)
	token := client.SubscribeMultiple(topics, nil)
	token.Wait()
	err := token.Error()

	m.republishMQTTState(client)
	if err != nil {
		return fmt.Errorf("failed to restore MQTT subscriptions: %w", err)
	}
	return nil
}

func (m *mqttClient) republishMQTTState(client mqtt.Client) {
	m.publishCurrentAvailability(client)
	vin := m.currentDiscoveryVIN()
	if vin == "" {
		return
	}

	if err := publishMQTT(client, m.topic("/vin"), 0, true, vin); err != nil {
		log.Errorf("Failed to republish retained vehicle VIN: %v", err)
	}
	if m.haDiscovery {
		m.publishHomeAssistantDiscoveryWithClient(client, vin, m.prefix, "Phev", true)
	}
}

func (m *mqttClient) republishAfterHomeAssistantRestart(client mqtt.Client) {
	m.recoveryMu.Lock()
	defer m.recoveryMu.Unlock()
	m.republishMQTTState(client)
}

// validateConfig validates configuration values and returns an error if any are invalid
func (m *mqttClient) validateConfig() error {
	// Validate MQTT server is set
	if viper.GetString("mqtt_server") == "" {
		return fmt.Errorf("mqtt_server is required but not set")
	}

	// SECURITY: Enforce update_interval bounds to prevent abuse
	if m.updateInterval <= 0 {
		log.Warnf("update_interval is invalid (%v), using default of 5 minutes", m.updateInterval)
		m.updateInterval = 5 * time.Minute
	}
	if m.updateInterval < minUpdateInterval {
		log.Warnf("SECURITY: update_interval (%v) is below minimum (%v), enforcing minimum",
			m.updateInterval, minUpdateInterval)
		m.updateInterval = minUpdateInterval
	}
	if m.updateInterval > maxUpdateInterval {
		log.Warnf("SECURITY: update_interval (%v) exceeds maximum (%v), enforcing maximum",
			m.updateInterval, maxUpdateInterval)
		m.updateInterval = maxUpdateInterval
	}
	if m.updateInterval < 5*time.Minute {
		log.Warnf("SECURITY WARNING: update_interval is short (%v), this may drain vehicle battery", m.updateInterval)
	}

	// Validate wifi_restart_time if set
	if m.wifiRestartTime < 0 {
		log.Warnf("wifi_restart_time cannot be negative (%v), disabling", m.wifiRestartTime)
		m.wifiRestartTime = 0
	}

	// Validate remote_wifi_power_save_wait
	if m.remoteWifiPowerSaveWait < 0 {
		log.Warnf("remote_wifi_power_save_wait cannot be negative (%v), using default of 5 seconds", m.remoteWifiPowerSaveWait)
		m.remoteWifiPowerSaveWait = 5 * time.Second
	}
	if m.remoteWifiPowerSaveWait > 60*time.Second {
		log.Warnf("remote_wifi_power_save_wait is very long (%v), consider using less than 60 seconds", m.remoteWifiPowerSaveWait)
	}

	// Validate remote_wifi_command_wait
	if m.remoteWifiCommandWait < 0 {
		log.Warnf("remote_wifi_command_wait cannot be negative (%v), using default of 10 seconds", m.remoteWifiCommandWait)
		m.remoteWifiCommandWait = 10 * time.Second
	}
	if m.remoteWifiCommandWait > 60*time.Second {
		log.Warnf("remote_wifi_command_wait is very long (%v), consider using less than 60 seconds", m.remoteWifiCommandWait)
	}

	// Validate advanced timeout settings
	if m.connectionRetryInterval < 10*time.Second {
		log.Warnf("connection_retry_interval is very short (%v), consider using at least 10 seconds", m.connectionRetryInterval)
	}
	if m.connectionRetryInterval > 5*time.Minute {
		log.Warnf("connection_retry_interval is very long (%v), consider using less than 5 minutes", m.connectionRetryInterval)
	}

	if m.availabilityOfflineTimeout < 10*time.Second {
		log.Warnf("availability_offline_timeout is very short (%v), consider using at least 10 seconds", m.availabilityOfflineTimeout)
	}
	if m.availabilityOfflineTimeout > 2*time.Minute {
		log.Warnf("availability_offline_timeout is very long (%v), consider using less than 2 minutes", m.availabilityOfflineTimeout)
	}

	if m.remoteWifiRestartMinInterval < 30*time.Second {
		log.Warnf("remote_wifi_restart_min_interval is very short (%v), consider using at least 30 seconds", m.remoteWifiRestartMinInterval)
	}

	if m.encodingErrorResetInterval < 5*time.Second {
		log.Warnf("encoding_error_reset_interval is very short (%v), consider using at least 5 seconds", m.encodingErrorResetInterval)
	}

	if m.configReloadInterval < 1*time.Second {
		log.Warnf("config_reload_interval is very short (%v), consider using at least 1 second", m.configReloadInterval)
	}
	if m.configReloadInterval > 30*time.Second {
		log.Warnf("config_reload_interval is very long (%v), consider using less than 30 seconds", m.configReloadInterval)
	}

	// Validate PHEV communication timeouts
	if m.phevStartTimeout < 5*time.Second {
		log.Warnf("phev_start_timeout is very short (%v), consider using at least 5 seconds", m.phevStartTimeout)
	}
	if m.phevRegisterTimeout < 3*time.Second {
		log.Warnf("phev_register_timeout is very short (%v), consider using at least 3 seconds", m.phevRegisterTimeout)
	}
	if m.phevTCPReadTimeout < 5*time.Second {
		log.Warnf("phev_tcp_read_timeout is very short (%v), consider using at least 5 seconds", m.phevTCPReadTimeout)
	}
	if m.phevTCPWriteTimeout < 5*time.Second {
		log.Warnf("phev_tcp_write_timeout is very short (%v), consider using at least 5 seconds", m.phevTCPWriteTimeout)
	}

	// Validate WiFi restart settings consistency
	if m.localWifiRestartEnabled && m.wifiRestartCommand == "" {
		log.Warnf("local_wifi_restart_enabled is true but wifi_restart_command is not set, disabling local restart")
		m.localWifiRestartEnabled = false
	}

	// Validate remote WiFi restart settings consistency
	if m.remoteWifiRestartEnabled && m.remoteWifiRestartTopic == "" {
		log.Warnf("remote_wifi_restart_enabled is true but remote_wifi_restart_topic is not set, disabling remote restart")
		m.remoteWifiRestartEnabled = false
	}

	// Validate remote WiFi power control settings consistency
	if m.remoteWifiPowerSaveEnabled && m.remoteWifiControlTopic == "" {
		log.Warnf("remote_wifi_power_save_enabled is true but remote_wifi_control_topic is not set, disabling power save")
		m.remoteWifiPowerSaveEnabled = false
	}

	// Validate power save only makes sense with longer update intervals
	if m.remoteWifiPowerSaveEnabled && m.updateInterval <= time.Minute {
		log.Warnf("remote_wifi_power_save_enabled is true but update_interval (%v) is <= 1 minute, power save may not be beneficial", m.updateInterval)
	}

	// Validate VIN for Home Assistant discovery
	if m.haDiscovery && m.vehicleVIN == "" {
		log.Warnf("Home Assistant discovery enabled but vehicle_vin not configured - discovery will be delayed until VIN received from PHEV")
	}

	return nil
}

func (m *mqttClient) Run(cmd *cobra.Command, args []string) error {
	m.enabled = true // Default.
	// Load .env file before reading config
	configFile := GetConfigFilePath()
	if configFile != "" {
		log.Debugf("Loading configuration from: %s", configFile)
		data, err := os.ReadFile(configFile)
		if err == nil {
			lines := parseEnvFile(string(data))
			validCount := 0
			blockedCount := 0

			for key, value := range lines {
				// SECURITY: Only allow whitelisted environment variables
				if !isAllowedEnvVar(key) {
					log.Warnf("SECURITY: Ignoring unauthorized environment variable: %s", key)
					blockedCount++
					continue
				}

				// SECURITY: Sanitize value
				value = sanitizeEnvValue(value)

				os.Setenv(key, value)
				validCount++
			}

			if blockedCount > 0 {
				log.Warnf("SECURITY: Blocked %d unauthorized environment variables", blockedCount)
			}
			log.Debugf("Loaded %d valid configuration values from .env file", validCount)
		}
	}
	// MQTT Configuration
	mqttServer := viper.GetString("mqtt_server")
	mqttUsername := viper.GetString("mqtt_username")
	mqttPassword := viper.GetString("mqtt_password")
	mqttDisableSet := viper.GetBool("mqtt_disable_register_set_command")
	m.prefix = viper.GetString("mqtt_topic_prefix")

	// SECURITY: Validate MQTT credentials
	if err := validateMQTTCredentials(mqttServer, mqttPassword); err != nil {
		return fmt.Errorf("MQTT configuration validation failed: %w", err)
	}

	// SECURITY: Validate MQTT topic prefix
	if err := validateMQTTTopic(m.prefix, "mqtt_topic_prefix", false); err != nil {
		return fmt.Errorf("invalid mqtt_topic_prefix: %w", err)
	}

	// Home Assistant Integration
	m.haDiscovery = viper.GetBool("ha_discovery")
	m.haDiscoveryPrefix = viper.GetString("ha_discovery_prefix")
	m.vehicleVIN = viper.GetString("vehicle_vin")
	m.disableRegisterSet = mqttDisableSet

	// SECURITY: Validate VIN format
	if err := validateVIN(m.vehicleVIN); err != nil {
		return fmt.Errorf("invalid vehicle_vin: %w", err)
	}

	// Update Interval
	m.updateInterval = viper.GetDuration("update_interval")

	// Local WiFi Restart Configuration
	m.wifiRestartTime = viper.GetDuration("wifi_restart_time")
	m.wifiRestartCommand = viper.GetString("wifi_restart_command")
	m.localWifiRestartEnabled = viper.GetBool("local_wifi_restart_enabled")

	// Remote WiFi Restart Configuration
	m.remoteWifiRestartEnabled = viper.GetBool("remote_wifi_restart_enabled")
	m.remoteWifiRestartTopic = viper.GetString("remote_wifi_restart_topic")
	m.remoteWifiRestartMessage = viper.GetString("remote_wifi_restart_message")

	// SECURITY: Validate MQTT topics and messages
	if err := validateMQTTTopic(m.remoteWifiRestartTopic, "remote_wifi_restart_topic", false); err != nil {
		return fmt.Errorf("invalid remote_wifi_restart_topic: %w", err)
	}
	if err := validateMQTTMessage(m.remoteWifiRestartMessage, "remote_wifi_restart_message"); err != nil {
		return fmt.Errorf("invalid remote_wifi_restart_message: %w", err)
	}

	// Remote WiFi Power Control Configuration
	m.remoteWifiControlTopic = viper.GetString("remote_wifi_control_topic")
	m.remoteWifiEnableMessage = viper.GetString("remote_wifi_enable_message")
	m.remoteWifiDisableMessage = viper.GetString("remote_wifi_disable_message")

	// SECURITY: Validate MQTT topics and messages
	if err := validateMQTTTopic(m.remoteWifiControlTopic, "remote_wifi_control_topic", false); err != nil {
		return fmt.Errorf("invalid remote_wifi_control_topic: %w", err)
	}
	if err := validateMQTTMessage(m.remoteWifiEnableMessage, "remote_wifi_enable_message"); err != nil {
		return fmt.Errorf("invalid remote_wifi_enable_message: %w", err)
	}
	if err := validateMQTTMessage(m.remoteWifiDisableMessage, "remote_wifi_disable_message"); err != nil {
		return fmt.Errorf("invalid remote_wifi_disable_message: %w", err)
	}

	m.remoteWifiPowerSaveEnabled = viper.GetBool("remote_wifi_power_save_enabled")
	m.remoteWifiPowerSaveWait = viper.GetDuration("remote_wifi_power_save_wait")
	if m.remoteWifiPowerSaveWait == 0 {
		m.remoteWifiPowerSaveWait = 5 * time.Second
	}
	m.remoteWifiPowerSaveDuration = viper.GetDuration("remote_wifi_power_save_duration")
	if m.remoteWifiPowerSaveDuration == 0 {
		m.remoteWifiPowerSaveDuration = 30 * time.Second
	}
	m.remoteWifiCommandWait = viper.GetDuration("remote_wifi_command_wait")
	if m.remoteWifiCommandWait == 0 {
		m.remoteWifiCommandWait = 10 * time.Second
	}

	// Advanced Timeout Settings
	m.connectionRetryInterval = viper.GetDuration("connection_retry_interval")
	if m.connectionRetryInterval == 0 {
		m.connectionRetryInterval = 60 * time.Second
	}
	m.availabilityOfflineTimeout = viper.GetDuration("availability_offline_timeout")
	if m.availabilityOfflineTimeout == 0 {
		m.availabilityOfflineTimeout = 30 * time.Second
	}
	m.remoteWifiRestartMinInterval = viper.GetDuration("remote_wifi_restart_min_interval")
	if m.remoteWifiRestartMinInterval == 0 {
		m.remoteWifiRestartMinInterval = 2 * time.Minute
	}
	m.encodingErrorResetInterval = viper.GetDuration("encoding_error_reset_interval")
	if m.encodingErrorResetInterval == 0 {
		m.encodingErrorResetInterval = 15 * time.Second
	}
	m.configReloadInterval = viper.GetDuration("config_reload_interval")
	if m.configReloadInterval == 0 {
		m.configReloadInterval = 5 * time.Second
	}
	m.phevStartTimeout = viper.GetDuration("phev_start_timeout")
	if m.phevStartTimeout == 0 {
		m.phevStartTimeout = 20 * time.Second
	}
	m.phevRegisterTimeout = viper.GetDuration("phev_register_timeout")
	if m.phevRegisterTimeout == 0 {
		m.phevRegisterTimeout = 10 * time.Second
	}
	m.phevTCPReadTimeout = viper.GetDuration("phev_tcp_read_timeout")
	if m.phevTCPReadTimeout == 0 {
		m.phevTCPReadTimeout = 30 * time.Second
	}
	m.phevTCPWriteTimeout = viper.GetDuration("phev_tcp_write_timeout")
	if m.phevTCPWriteTimeout == 0 {
		m.phevTCPWriteTimeout = 15 * time.Second
	}

	// Validate configuration
	if err := m.validateConfig(); err != nil {
		return fmt.Errorf("configuration validation failed: %w", err)
	}

	if m.wifiRestartCommand == "" {
		log.Infof("Local WiFi restart disabled")
	}
	if m.remoteWifiRestartTopic != "" {
		log.Infof("Remote WiFi restart enabled - topic: %s", m.remoteWifiRestartTopic)
	}
	if m.remoteWifiControlTopic != "" {
		log.Infof("Remote WiFi control enabled - topic: %s", m.remoteWifiControlTopic)
		if m.remoteWifiPowerSaveEnabled && m.updateInterval > time.Minute {
			log.Infof("Remote WiFi power save mode ENABLED - WiFi will be turned off between updates")
		}
	}

	m.discoveryMu.Lock()
	m.haPublishedDiscovery = false
	m.lastVIN = m.vehicleVIN
	m.discoveryMu.Unlock()
	m.lastError = nil

	// Initialize configuration hot reload
	configFile = GetConfigFilePath()
	m.configReloader = NewConfigReloader(configFile, m.configReloadInterval)
	m.configReloader.SetReloadCallback(m.onConfigReload)
	m.configReloader.Start()
	defer m.configReloader.Stop()

	log.Infof("Connecting to MQTT broker: %s", mqttServer)
	log.Infof("MQTT username: %s", mqttUsername)
	log.Infof("MQTT topic prefix: %s", m.prefix)

	m.mqttData = map[string]string{}
	m.connectedCh = make(chan struct{}, 1)
	m.commandWake = make(chan struct{}, 1)
	initialRestore := make(chan error, 1)
	var initialRestoreOnce sync.Once

	m.options = mqtt.NewClientOptions().
		AddBroker(mqttServer).
		SetClientID("phev2mqtt").
		SetUsername(mqttUsername).
		SetPassword(mqttPassword).
		SetAutoReconnect(true).
		SetDefaultPublishHandler(m.handleIncomingMqtt).
		SetOnConnectHandler(func(client mqtt.Client) {
			err := m.restoreMQTTState(client)
			if err != nil {
				log.Errorf("Failed to restore MQTT state after broker connection: %v", err)
			}
			initialRestoreOnce.Do(func() {
				initialRestore <- err
			})
		}).
		SetWill(m.topic("/available"), "offline", 0, true)

	m.client = mqtt.NewClient(m.options)
	if token := m.client.Connect(); token.Wait() && token.Error() != nil {
		log.Errorf("Failed to connect to MQTT broker: %v", token.Error())
		return token.Error()
	}
	log.Infof("Successfully connected to MQTT broker")
	select {
	case err := <-initialRestore:
		if err != nil {
			return err
		}
	case <-time.After(30 * time.Second):
		return fmt.Errorf("timed out restoring MQTT state after broker connection")
	}

	if m.disableRegisterSet {
		log.Info("Setting vechicle registers via MQTT is disabled")
	}

	log.Infof("[Main Loop] Initial client enabled state: %v", m.enabled)
	log.Infof("Starting connection loop to PHEV at address: %s", viper.GetString("address"))

	// Initialize power save timer if enabled
	var powerSaveTicker *time.Ticker
	if m.remoteWifiPowerSaveEnabled && m.remoteWifiControlTopic != "" && m.updateInterval > time.Minute {
		log.Infof("Power save mode active - managing WiFi on/off cycles")
		powerSaveTicker = time.NewTicker(m.updateInterval)
		defer powerSaveTicker.Stop()
		m.powerSaveWifiOn = false // Start with WiFi off, enable on first cycle
	} else {
		powerSaveTicker = nil
	}

	for {
		// Power save mode: turn WiFi on at each interval and wait for connection attempt
		if powerSaveTicker != nil {
			log.Debugf("[Power Save] Waiting for next update interval (%v)...", m.updateInterval)
			select {
			case <-powerSaveTicker.C:
				log.Infof("[Power Save] Update cycle started - turning WiFi on")
			case <-m.commandWake:
				log.Infof("[Command] Connection requested, preparing to connect")
			}
			if m.remoteWifiPowerSaveEnabled && m.remoteWifiControlTopic != "" && !m.powerSaveWifiOn {
				m.remoteWifiEnable()
				log.Infof("[WiFi Link] Waiting %v for WiFi link to establish before attempting connection", m.remoteWifiPowerSaveWait)
				time.Sleep(m.remoteWifiPowerSaveWait)
				log.Infof("[WiFi Link] WiFi link establishment wait complete, ready to connect")
				m.powerSaveWifiOn = true
			}
		}

		log.Infof("[Main Loop] Attempting to connect to PHEV...")
		if err := m.handlePhev(cmd); err != nil {
			// Connection failed - set enabled to false
			if m.enabled {
				log.Infof("[Main Loop] Connection failed, setting enabled=false")
				m.enabled = false
			}
			// Do not flood the log with the same messages every second
			if m.lastError == nil || m.lastError.Error() != err.Error() {
				log.Errorf("PHEV connection error: %v", err)
				m.lastError = err
			}
			// WiFi will be turned off by defer in handlePhev()
		} else {
			// Connection succeeded - ensure enabled is true
			if !m.enabled {
				log.Infof("[Main Loop] Connection succeeded, setting enabled=true")
				m.enabled = true
			}
		}

		// Publish as offline if last connection was >availability_offline_timeout ago.
		if time.Now().Sub(m.lastConnect) > m.availabilityOfflineTimeout {
			m.client.Publish(m.topic("/available"), 0, true, "offline")
		}
		// Restart Wifi interface if > wifi_restart_time.
		if m.wifiRestartTime > 0 && time.Now().Sub(m.lastConnect) > m.wifiRestartTime {
			if err := restartWifi(cmd, m); err != nil {
				log.Errorf("Error during WiFi restart: %v", err)
			}
		}

		// Only sleep between retries when NOT in power save mode
		// In power save mode, we block on the ticker at the start of the loop
		if powerSaveTicker == nil {
			select {
			case <-time.After(m.connectionRetryInterval):
			case <-m.commandWake:
				log.Infof("[Command] Connection requested, bypassing retry wait")
			}
		}
	}
}

func (m *mqttClient) publish(topic, payload string) {
	if cache := m.mqttData[topic]; cache == payload {
		return
	}
	m.client.Publish(m.topic(topic), 0, false, payload)
	m.mqttData[topic] = payload
}

func (m *mqttClient) publishRetained(topic, payload string) {
	if cache := m.mqttData[topic]; cache == payload {
		return
	}
	if err := publishMQTT(m.client, m.topic(topic), 0, true, payload); err != nil {
		log.Errorf("Failed to publish retained MQTT topic %s: %v", m.topic(topic), err)
		return
	}
	m.mqttData[topic] = payload
}

func (m *mqttClient) handleIncomingMqtt(mqtt_client mqtt.Client, msg mqtt.Message) {
	log.Infof("Topic: [%s] Payload: [%s]", msg.Topic(), msg.Payload())

	birthTopic := strings.TrimSuffix(m.haDiscoveryPrefix, "/") + "/status"
	if m.haDiscovery && isHomeAssistantBirthMessage(msg.Topic(), string(msg.Payload()), birthTopic) {
		go m.republishAfterHomeAssistantRestart(mqtt_client)
		return
	}

	topicParts := strings.Split(msg.Topic(), "/")
	if strings.HasPrefix(msg.Topic(), m.topic("/set/register/")) {
		if len(topicParts) != 4 {
			log.Infof("Bad topic format [%s]", msg.Topic())
			return
		}
		register, err := hex.DecodeString(topicParts[3])
		if err != nil {
			log.Infof("Bad register in topic [%s]: %v", msg.Topic(), err)
			return
		}
		data, err := hex.DecodeString(string(msg.Payload()))
		if err != nil {
			log.Infof("Bad payload [%s]: %v", msg.Payload(), err)
			return
		}
		if !m.ensureConnectedForCommand() {
			return
		}
		if m.phev == nil {
			log.Warnf("PHEV client not connected, cannot set register %02x", register[0])
			return
		}
		if err := m.phev.SetRegister(register[0], data); err != nil {
			log.Infof("Error setting register %02x: %v", register[0], err)
			return
		}
	} else if msg.Topic() == m.topic("/connection") {
		payload := strings.ToLower(string(msg.Payload()))
		log.Infof("[Connection Control] Received message on /connection topic: '%s'", payload)
		switch payload {
		case "off", "on":
			log.Warnf("[Connection Control] Ignoring deprecated connection command: '%s'", payload)
		case "restart":
			log.Infof("[Connection Control] Restarting connection (enabled=true)")
			m.enabled = true
			m.client.Publish(m.topic("/available"), 0, true, "offline")
			if m.phev != nil {
				m.phev.Close()
			}
			m.requestCommandConnect()
		case "wifi_enable":
			log.Infof("[Connection Control] Manual WiFi enable command (overrides power save temporarily)")
			m.remoteWifiEnable()
		case "wifi_disable":
			log.Infof("[Connection Control] Manual WiFi disable command (overrides power save temporarily)")
			m.remoteWifiDisable()
		default:
			log.Warnf("[Connection Control] Unknown connection command: '%s'", payload)
		}
	} else if msg.Topic() == m.topic("/set/parkinglights") {
		values := map[string]byte{"on": 0x1, "off": 0x2}
		if v, ok := values[strings.ToLower(string(msg.Payload()))]; ok {
			if !m.ensureConnectedForCommand() {
				return
			}
			if m.phev == nil {
				log.Warnf("PHEV client not connected, cannot set parking lights")
				return
			}
			if err := m.phev.SetRegister(0xb, []byte{v}); err != nil {
				log.Infof("Error setting register 0xb: %v", err)
				return
			}
		}
	} else if msg.Topic() == m.topic("/set/headlights") {
		values := map[string]byte{"on": 0x1, "off": 0x2}
		if v, ok := values[strings.ToLower(string(msg.Payload()))]; ok {
			if !m.ensureConnectedForCommand() {
				return
			}
			if m.phev == nil {
				log.Warnf("PHEV client not connected, cannot set headlights")
				return
			}
			if err := m.phev.SetRegister(0xa, []byte{v}); err != nil {
				log.Infof("Error setting register 0xb: %v", err)
				return
			}
		}
	} else if msg.Topic() == m.topic("/set/cancelchargetimer") {
		if !m.ensureConnectedForCommand() {
			return
		}
		if m.phev == nil {
			log.Warnf("PHEV client not connected, cannot cancel charge timer")
			return
		}
		if err := m.phev.SetRegister(0x17, []byte{0x1}); err != nil {
			log.Infof("Error setting register 0x17: %v", err)
			return
		}
		if err := m.phev.SetRegister(0x17, []byte{0x11}); err != nil {
			log.Infof("Error setting register 0x17: %v", err)
			return
		}
	} else if strings.HasPrefix(msg.Topic(), m.topic("/set/climate/state")) {
		payload := strings.ToLower(string(msg.Payload()))
		if payload == "reset" {
			if !m.ensureConnectedForCommand() {
				return
			}
			if m.phev == nil {
				log.Warnf("PHEV client not connected, cannot reset climate state")
				return
			}
			if err := m.phev.SetRegister(protocol.SetAckPreACTermRegister, []byte{0x1}); err != nil {
				log.Infof("Error acknowledging Pre-AC termination: %v", err)
				return
			}
		}
	} else if strings.HasPrefix(msg.Topic(), m.topic("/set/climate/")) {
		topic := msg.Topic()
		payload := strings.ToLower(string(msg.Payload()))

		modeMap := map[string]byte{"off": 0x0, "OFF": 0x0, "cool": 0x1, "heat": 0x2, "windscreen": 0x3, "mode": 0x4}
		durMap := map[string]byte{"10": 0x0, "20": 0x1, "30": 0x2, "on": 0x0, "off": 0x0}
		parts := strings.Split(topic, "/")
		mode, ok := modeMap[parts[len(parts)-1]]
		if !ok {
			log.Errorf("Unknown climate mode: %s", parts[len(parts)-1])
			return
		}
		if mode == 0x4 { // set/climate/mode -> "heat"
			mode = modeMap[payload]
			payload = "on"
		}
		if payload == "off" {
			mode = 0x0
		}
		duration, ok := durMap[payload]
		if mode != 0x0 && !ok {
			log.Errorf("Unknown climate duration: %s", payload)
			return
		}

		if !m.ensureConnectedForCommand() {
			return
		}
		if m.phev == nil {
			log.Warnf("PHEV client not connected, cannot set climate mode")
			return
		}
		if m.phev.ModelYear == client.ModelYear14 {
			// Set the AC mode first
			registerPayload := bytes.Repeat([]byte{0xff}, 15)
			registerPayload[0] = 0x0
			registerPayload[1] = 0x0
			registerPayload[6] = mode | duration
			if err := m.phev.SetRegister(protocol.SetACModeRegisterMY14, registerPayload); err != nil {
				log.Infof("Error setting AC mode: %v", err)
				return
			}

			// Then, enable/disable the AC
			acEnabled := byte(0x02)
			if mode == 0x0 {
				acEnabled = 0x01
			}
			if err := m.phev.SetRegister(protocol.SetACEnabledRegisterMY14, []byte{acEnabled}); err != nil {
				log.Infof("Error setting AC enabled state: %v", err)
				return
			}
		} else if m.phev.ModelYear == client.ModelYear18 || m.phev.ModelYear == client.ModelYear24 {
			state := byte(0x02)
			if mode == 0x0 {
				state = 0x1
			}
			if err := m.phev.SetRegister(protocol.SetACModeRegisterMY18, []byte{state, mode, duration, 0x0}); err != nil {
				log.Infof("Error setting AC mode: %v", err)
				return
			}
		}
	} else if msg.Topic() == m.topic("/settings/dump") {
		if m.phev == nil {
			log.Warnf("PHEV client not connected, cannot dump settings")
			return
		}
		log.Infof("CURRENT_SETTINGS:")
		log.Infof("\n%s", m.phev.Settings.Dump())
		m.phev.Settings.Clear()
	} else if strings.HasPrefix(msg.Topic(), m.topic("/settings")) {
		log.Debugf("Ignoring echoed settings topic: %s", msg.Topic())
		return
	} else {
		log.Errorf("Unknown topic from mqtt: %s", msg.Topic())
	}
}

// onConfigReload is called when configuration file changes
func (m *mqttClient) onConfigReload() {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Reload log level if log_level changed
	logLevelStr := viper.GetString("log_level")
	if logLevelStr == "" {
		logLevelStr = "info" // Default to info
	}
	switch logLevelStr {
	case "none":
		log.SetLevel(log.FatalLevel) // Only fatal messages
		log.Infof("Log level changed to: none (fatal only)")
	case "error":
		log.SetLevel(log.ErrorLevel)
		log.Infof("Log level changed to: error")
	case "warning", "warn":
		log.SetLevel(log.WarnLevel)
		log.Infof("Log level changed to: warning")
	case "info":
		log.SetLevel(log.InfoLevel)
		log.Infof("Log level changed to: info")
	case "debug":
		log.SetLevel(log.DebugLevel)
		log.Infof("Log level changed to: debug")
	default:
		log.Warnf("Unknown log level '%s', defaulting to info", logLevelStr)
		log.SetLevel(log.InfoLevel)
	}

	logTimes := viper.GetBool("log_timestamps")
	if logTimes {
		log.SetFormatter(&log.TextFormatter{
			FullTimestamp: true,
		})
	} else {
		log.SetFormatter(&log.TextFormatter{
			FullTimestamp:    false,
			DisableColors:    true,
			DisableTimestamp: true,
		})
	}

	// Local WiFi Restart Configuration
	m.wifiRestartTime = viper.GetDuration("wifi_restart_time")
	m.wifiRestartCommand = viper.GetString("wifi_restart_command")
	m.localWifiRestartEnabled = viper.GetBool("local_wifi_restart_enabled")

	// Remote WiFi Restart Configuration
	m.remoteWifiRestartEnabled = viper.GetBool("remote_wifi_restart_enabled")
	m.remoteWifiRestartTopic = viper.GetString("remote_wifi_restart_topic")
	m.remoteWifiRestartMessage = viper.GetString("remote_wifi_restart_message")

	// SECURITY: Validate MQTT topics and messages (log warning on reload, don't fail)
	if err := validateMQTTTopic(m.remoteWifiRestartTopic, "remote_wifi_restart_topic", false); err != nil {
		log.Errorf("Invalid remote_wifi_restart_topic after reload: %v", err)
	}
	if err := validateMQTTMessage(m.remoteWifiRestartMessage, "remote_wifi_restart_message"); err != nil {
		log.Errorf("Invalid remote_wifi_restart_message after reload: %v", err)
	}

	// Remote WiFi Power Control Configuration
	m.remoteWifiControlTopic = viper.GetString("remote_wifi_control_topic")
	m.remoteWifiEnableMessage = viper.GetString("remote_wifi_enable_message")
	m.remoteWifiDisableMessage = viper.GetString("remote_wifi_disable_message")

	// SECURITY: Validate MQTT topics and messages (log warning on reload, don't fail)
	if err := validateMQTTTopic(m.remoteWifiControlTopic, "remote_wifi_control_topic", false); err != nil {
		log.Errorf("Invalid remote_wifi_control_topic after reload: %v", err)
	}
	if err := validateMQTTMessage(m.remoteWifiEnableMessage, "remote_wifi_enable_message"); err != nil {
		log.Errorf("Invalid remote_wifi_enable_message after reload: %v", err)
	}
	if err := validateMQTTMessage(m.remoteWifiDisableMessage, "remote_wifi_disable_message"); err != nil {
		log.Errorf("Invalid remote_wifi_disable_message after reload: %v", err)
	}

	m.remoteWifiPowerSaveEnabled = viper.GetBool("remote_wifi_power_save_enabled")
	m.remoteWifiPowerSaveWait = viper.GetDuration("remote_wifi_power_save_wait")
	if m.remoteWifiPowerSaveWait == 0 {
		m.remoteWifiPowerSaveWait = 5 * time.Second
	}
	m.remoteWifiPowerSaveDuration = viper.GetDuration("remote_wifi_power_save_duration")
	if m.remoteWifiPowerSaveDuration == 0 {
		m.remoteWifiPowerSaveDuration = 30 * time.Second
	}
	m.remoteWifiCommandWait = viper.GetDuration("remote_wifi_command_wait")
	if m.remoteWifiCommandWait == 0 {
		m.remoteWifiCommandWait = 10 * time.Second
	}

	// Advanced Timeout Settings
	m.connectionRetryInterval = viper.GetDuration("connection_retry_interval")
	if m.connectionRetryInterval == 0 {
		m.connectionRetryInterval = 60 * time.Second
	}
	m.availabilityOfflineTimeout = viper.GetDuration("availability_offline_timeout")
	if m.availabilityOfflineTimeout == 0 {
		m.availabilityOfflineTimeout = 30 * time.Second
	}
	m.remoteWifiRestartMinInterval = viper.GetDuration("remote_wifi_restart_min_interval")
	if m.remoteWifiRestartMinInterval == 0 {
		m.remoteWifiRestartMinInterval = 2 * time.Minute
	}
	m.encodingErrorResetInterval = viper.GetDuration("encoding_error_reset_interval")
	if m.encodingErrorResetInterval == 0 {
		m.encodingErrorResetInterval = 15 * time.Second
	}
	m.configReloadInterval = viper.GetDuration("config_reload_interval")
	if m.configReloadInterval == 0 {
		m.configReloadInterval = 5 * time.Second
	}
	m.phevStartTimeout = viper.GetDuration("phev_start_timeout")
	if m.phevStartTimeout == 0 {
		m.phevStartTimeout = 20 * time.Second
	}
	m.phevRegisterTimeout = viper.GetDuration("phev_register_timeout")
	if m.phevRegisterTimeout == 0 {
		m.phevRegisterTimeout = 10 * time.Second
	}
	m.phevTCPReadTimeout = viper.GetDuration("phev_tcp_read_timeout")
	if m.phevTCPReadTimeout == 0 {
		m.phevTCPReadTimeout = 30 * time.Second
	}
	m.phevTCPWriteTimeout = viper.GetDuration("phev_tcp_write_timeout")
	if m.phevTCPWriteTimeout == 0 {
		m.phevTCPWriteTimeout = 15 * time.Second
	}

	// Validate configuration
	if err := m.validateConfig(); err != nil {
		log.Errorf("Configuration validation failed after reload: %v", err)
		return
	}

	log.Infof("Configuration reloaded:")
	log.Infof("  update_interval: %v", m.updateInterval)
	log.Infof("  wifi_restart_time: %v", m.wifiRestartTime)
	log.Infof("  local_wifi_restart_enabled: %v", m.localWifiRestartEnabled)
	log.Infof("  remote_wifi_restart_enabled: %v", m.remoteWifiRestartEnabled)
	if m.remoteWifiRestartTopic != "" {
		log.Infof("  remote_wifi_restart_topic: %s", m.remoteWifiRestartTopic)
	}
	if m.remoteWifiControlTopic != "" {
		log.Infof("  remote_wifi_control_topic: %s", m.remoteWifiControlTopic)
	}
}

func (m *mqttClient) handlePhev(cmd *cobra.Command) error {
	// Ensure WiFi is turned off when function exits (on both success and failure)
	defer func() {
		m.setConnected(false)
		m.lastConnect = time.Now()
		// Turn WiFi off after connection ends in power save mode
		if m.remoteWifiPowerSaveEnabled && m.remoteWifiControlTopic != "" && m.updateInterval > time.Minute && m.powerSaveWifiOn {
			// Check if a command was recently sent - keep WiFi on to receive status update
			timeSinceCommand := time.Since(m.lastCommandTime)
			if !m.lastCommandTime.IsZero() && timeSinceCommand < m.remoteWifiCommandWait {
				remaining := m.remoteWifiCommandWait - timeSinceCommand
				log.Infof("[Power Save] Command recently sent, keeping WiFi on for %v to receive status update", remaining)
				time.Sleep(remaining)
				log.Infof("[Power Save] Command wait period complete")
			}
			log.Infof("[Power Save] Connection cycle complete - turning WiFi off")
			m.remoteWifiDisable()
			m.powerSaveWifiOn = false
		}
		if m.phev != nil {
			if err := m.phev.Close(); err != nil {
				log.Debugf("PHEV client close during cleanup: %v", err)
			}
		}
	}()

	var err error
	address := viper.GetString("address")
	log.Debugf("Creating new PHEV client for address: %s", address)
	m.phev, err = client.New(
		client.AddressOption(address),
		client.TCPReadTimeoutOption(m.phevTCPReadTimeout),
		client.TCPWriteTimeoutOption(m.phevTCPWriteTimeout),
		client.StartTimeoutOption(m.phevStartTimeout),
		client.RegisterTimeoutOption(m.phevRegisterTimeout),
	)
	if err != nil {
		return fmt.Errorf("failed to create PHEV client: %w", err)
	}

	log.Infof("Attempting to connect to PHEV at %s...", address)
	if err := m.phev.Connect(); err != nil {
		return fmt.Errorf("failed to connect to PHEV: %w", err)
	}
	log.Infof("Successfully connected to PHEV")

	log.Debugf("Starting PHEV client...")
	if err := m.phev.Start(); err != nil {
		return fmt.Errorf("failed to start PHEV client: %w", err)
	}
	log.Infof("PHEV client started successfully")
	m.setConnected(true)
	m.publishCurrentAvailability(m.client)
	log.Infof("Published availability status: online")

	// Request an immediate update so HA entities get state before any power-save disconnect.
	if err := m.phev.SetRegister(0x6, []byte{0x3}); err != nil {
		log.Infof("Error requesting initial update: %v", err)
	} else {
		m.lastUpdateTime = time.Now()
	}

	m.lastError = nil

	var encodingErrorCount = 0
	var lastEncodingError time.Time

	updaterTicker := time.NewTicker(m.updateInterval)

	// In power save mode, set up a connection duration timer
	var powerSaveTimer *time.Timer
	if m.remoteWifiPowerSaveEnabled && m.remoteWifiControlTopic != "" && m.updateInterval > time.Minute {
		log.Infof("[Power Save] Will disconnect after %v to turn WiFi off", m.remoteWifiPowerSaveDuration)
		powerSaveTimer = time.NewTimer(m.remoteWifiPowerSaveDuration)
		defer powerSaveTimer.Stop()
	}

	for {
		select {
		case <-updaterTicker.C:
			// Power save mode: only turn on WiFi if NOT already managed by main loop
			if m.remoteWifiPowerSaveEnabled && m.remoteWifiControlTopic != "" && m.updateInterval > time.Minute && !m.powerSaveWifiOn {
				log.Debugf("Power save: turning WiFi on for update")
				m.remoteWifiEnable()
				log.Debugf("Waiting %v for WiFi link to establish", m.remoteWifiPowerSaveWait)
				time.Sleep(m.remoteWifiPowerSaveWait)
				m.powerSaveWifiOn = true
			}
			m.phev.SetRegister(0x6, []byte{0x3})
			m.lastUpdateTime = time.Now()
		case <-func() <-chan time.Time {
			if powerSaveTimer != nil {
				return powerSaveTimer.C
			}
			return nil
		}():
			// Power save timer expired - disconnect to turn WiFi off
			log.Infof("[Power Save] Connection duration reached, disconnecting to turn WiFi off")
			updaterTicker.Stop()
			m.phev.Close()
			return nil
		case msg, ok := <-m.phev.Recv:
			if !ok {
				log.Infof("Connection closed.")
				updaterTicker.Stop()
				return fmt.Errorf("Connection closed.")
			}
			switch msg.Type {
			case protocol.CmdInBadEncoding:
				if time.Now().Sub(lastEncodingError) > m.encodingErrorResetInterval {
					encodingErrorCount = 0
				}
				if encodingErrorCount > 50 {
					m.phev.Close()
					updaterTicker.Stop()
					return fmt.Errorf("Disconnecting due to too many errors")
				}
				encodingErrorCount += 1
				lastEncodingError = time.Now()
			case protocol.CmdInResp:
				if msg.Ack != protocol.Request {
					break
				}
				m.publishRegister(msg)
				if err := m.phev.SendMessage(&protocol.PhevMessage{
					Type:     protocol.CmdOutSend,
					Register: msg.Register,
					Ack:      protocol.Ack,
					Xor:      msg.Xor,
					Data:     []byte{0x0},
				}); err != nil {
					log.Infof("Error acknowledging register update: %v", err)
					updaterTicker.Stop()
					return err
				}
			}
		}
	}
}

var boolOnOff = map[bool]string{
	false: "off",
	true:  "on",
}
var boolOpen = map[bool]string{
	false: "closed",
	true:  "open",
}

func (m *mqttClient) publishRegister(msg *protocol.PhevMessage) {
	dataStr := hex.EncodeToString(msg.Data)
	m.publish(fmt.Sprintf("/register/%02x", msg.Register), dataStr)
	switch reg := msg.Reg.(type) {
	case *protocol.RegisterVIN:
		m.discoveryMu.Lock()
		m.lastVIN = reg.VIN
		m.discoveryMu.Unlock()
		m.publishRetained("/vin", reg.VIN)
		m.publishHomeAssistantDiscovery(reg.VIN, m.prefix, "Phev")
		m.publish("/registrations", fmt.Sprintf("%d", reg.Registrations))
	case *protocol.RegisterECUVersion:
		m.publish("/ecuversion", reg.Version)
	case *protocol.RegisterBatteryWarning:
		m.publish("/battery/warning", fmt.Sprintf("%d", reg.Warning))
	case *protocol.RegisterACOperStatus:
		m.publish("/climate/operating", boolOnOff[reg.Operating])
	case *protocol.RegisterWIFISSID:
		m.publish("/wifi/ssid", reg.SSID)
	case *protocol.RegisterTime:
		m.publish("/time", reg.Time.Format(time.RFC3339))
	case *protocol.RegisterSettings:
		m.publish("/settings", reg.Raw())
	case *protocol.RegisterACMode:
		m.climate.setMode(reg.Mode)
		for t, p := range m.climate.mqttStates() {
			m.publish(t, p)
		}
	case *protocol.RegisterPreACState:
		m.climate.setState(reg.State)
		for t, p := range m.climate.mqttStates() {
			m.publish(t, p)
		}
	case *protocol.RegisterChargeStatus:
		m.publish("/charge/charging", boolOnOff[reg.Charging])
		if reg.Remaining < 1000 {
			m.publish("/charge/remaining", fmt.Sprintf("%d", reg.Remaining))
		} else {
			log.Debugf("Ignoring charge remanining reading: %v", reg.Remaining)
			if cache := m.mqttData["/charge/remaining"]; cache != "" {
				m.publish("/charge/remaining", cache)
				log.Debugf("Publishing last best known charge remaining reading: %v", cache)
			}
		}
	case *protocol.RegisterDoorStatus:
		m.publish("/door/locked", boolOpen[!reg.Locked])
		m.publish("/door/rear_left", boolOpen[reg.RearLeft])
		m.publish("/door/rear_right", boolOpen[reg.RearRight])
		m.publish("/door/front_right", boolOpen[reg.Driver])
		m.publish("/door/driver", boolOpen[reg.Driver])
		m.publish("/door/front_left", boolOpen[reg.FrontPassenger])
		m.publish("/door/front_passenger", boolOpen[reg.FrontPassenger])
		m.publish("/door/bonnet", boolOpen[reg.Bonnet])
		m.publish("/door/boot", boolOpen[reg.Boot])
		m.publish("/lights/head", boolOnOff[reg.Headlights])
	case *protocol.RegisterBatteryLevel:
		if (reg.Level > 5) && (reg.Level < 255) {
			m.publish("/battery/level", fmt.Sprintf("%d", reg.Level))
		} else {
			if cache := m.mqttData["/battery/level"]; cache != "" {
				m.publish("/battery/level", cache)
				log.Debugf("Ignoring battery level reading: %v, publishing last best known: %v", reg.Level, cache)
			}
		}
		m.publish("/lights/parking", boolOnOff[reg.ParkingLights])
	case *protocol.RegisterLightStatus:
		m.publish("/lights/interior", boolOnOff[reg.Interior])
		m.publish("/lights/hazard", boolOnOff[reg.Hazard])
	case *protocol.RegisterChargePlug:
		if reg.Connected {
			m.publish("/charge/plug", "connected")
		} else {
			m.publish("/charge/plug", "unplugged")
		}
	}
}

// Publish home assistant discovery message.
// Uses the vehicle VIN, so sent after VIN discovery.
func (m *mqttClient) publishHomeAssistantDiscovery(vin, topic, name string) {
	m.publishHomeAssistantDiscoveryWithClient(m.client, vin, topic, name, false)
}

func (m *mqttClient) publishHomeAssistantDiscoveryWithClient(client mqtt.Client, vin, topic, name string, force bool) {
	m.discoveryMu.Lock()
	defer m.discoveryMu.Unlock()

	if !m.haDiscovery {
		log.Debugf("[HA Discovery] Home Assistant discovery disabled, skipping")
		return
	}

	if m.haPublishedDiscovery && !force {
		log.Debugf("[HA Discovery] Discovery already published, skipping")
		if m.vehicleVIN != "" && m.vehicleVIN != vin {
			log.Warnf("[HA Discovery] VIN from PHEV (%s) differs from configured VIN (%s) - not republishing discovery", vin, m.vehicleVIN)
		}
		return
	}

	log.Infof("[HA Discovery] Publishing Home Assistant discovery for VIN: %s", vin)
	log.Infof("[HA Discovery] Discovery prefix: %s, MQTT topic prefix: %s", m.haDiscoveryPrefix, topic)
	discoveryData := map[string]string{
		// Doors.
		"%s/binary_sensor/%s_door_locked/config": `{
		"device_class": "lock",
		"name": "__NAME__ Locked",
		"state_topic": "~/door/locked",
		"payload_off": "closed",
		"payload_on": "open",
		"unique_id": "__VIN___door_locked",
		"device": {
			"name": "PHEV __VIN__",
			"identifiers": ["phev-__VIN__"],
			"manufacturer": "Mitsubishi",
			"model": "Outlander PHEV"},
		"~": "__TOPIC__"}`,
		"%s/binary_sensor/%s_door_bonnet/config": `{
		"device_class": "door",
		"name": "__NAME__ Bonnet",
		"state_topic": "~/door/bonnet",
		"payload_off": "closed",
		"payload_on": "open",
		"unique_id": "__VIN___door_bonnet",
		"device": {
			"name": "PHEV __VIN__",
			"identifiers": ["phev-__VIN__"],
			"manufacturer": "Mitsubishi",
			"model": "Outlander PHEV"},
		"~": "__TOPIC__"}`,
		"%s/binary_sensor/%s_door_boot/config": `{
		"device_class": "door",
		"name": "__NAME__ Boot",
		"state_topic": "~/door/boot",
		"payload_off": "closed",
		"payload_on": "open",
		"unique_id": "__VIN___door_boot",
		"dev": {
			"name": "PHEV __VIN__",
			"identifiers": ["phev-__VIN__"],
			"manufacturer": "Mitsubishi",
			"model": "Outlander PHEV"
		},
		"~": "__TOPIC__"}`,
		"%s/binary_sensor/%s_door_front_passenger/config": `{
		"device_class": "door",
		"name": "__NAME__ Front Passenger Door",
		"state_topic": "~/door/front_passenger",
		"payload_off": "closed",
		"payload_on": "open",
		"unique_id": "__VIN___door_front_passenger",
		"dev": {
			"name": "PHEV __VIN__",
			"identifiers": ["phev-__VIN__"],
			"manufacturer": "Mitsubishi",
			"model": "Outlander PHEV"
		},
		"~": "__TOPIC__"}`,
		"%s/binary_sensor/%s_door_driver/config": `{
		"device_class": "door",
		"name": "__NAME__ Driver Door",
		"state_topic": "~/door/driver",
		"payload_off": "closed",
		"payload_on": "open",
		"unique_id": "__VIN___door_driver",
		"dev": {
			"name": "PHEV __VIN__",
			"identifiers": ["phev-__VIN__"],
			"manufacturer": "Mitsubishi",
			"model": "Outlander PHEV"
		},
		"~": "__TOPIC__"}`,
		"%s/binary_sensor/%s_door_rear_left/config": `{
		"device_class": "door",
		"name": "__NAME__ Rear Left Door",
		"state_topic": "~/door/rear_left",
		"payload_off": "closed",
		"payload_on": "open",
		"unique_id": "__VIN___door_rear_left",
		"dev": {
			"name": "PHEV __VIN__",
			"identifiers": ["phev-__VIN__"],
			"manufacturer": "Mitsubishi",
			"model": "Outlander PHEV"
		},
		"~": "__TOPIC__"}`,
		"%s/binary_sensor/%s_door_rear_right/config": `{
		"device_class": "door",
		"name": "__NAME__ Rear Right Door",
		"state_topic": "~/door/rear_right",
		"payload_off": "closed",
		"payload_on": "open",
		"unique_id": "__VIN___door_rear_right",
		"dev": {
			"name": "PHEV __VIN__",
			"identifiers": ["phev-__VIN__"],
			"manufacturer": "Mitsubishi",
			"model": "Outlander PHEV"
		},
		"~": "__TOPIC__"}`,
		// Battery and charging
		"%s/sensor/%s_battery_level/config": `{
		"device_class": "battery",
		"name": "__NAME__ Battery",
		"state_topic": "~/battery/level",
		"state_class": "measurement",
		"unit_of_measurement": "%",
		"unique_id": "__VIN___battery_level",
		"dev": {
			"name": "PHEV __VIN__",
			"identifiers": ["phev-__VIN__"],
			"manufacturer": "Mitsubishi",
			"model": "Outlander PHEV"
		},
		"~": "__TOPIC__"}`,
		"%s/sensor/%s_battery_warning/config": `{
		"name": "__NAME__ Battery Warning",
		"state_topic": "~/battery/warning",
		"icon": "mdi:battery-alert",
		"entity_category": "diagnostic",
		"unique_id": "__VIN___battery_warning",
		"dev": {
			"name": "PHEV __VIN__",
			"identifiers": ["phev-__VIN__"],
			"manufacturer": "Mitsubishi",
			"model": "Outlander PHEV"
		},
		"~": "__TOPIC__"}`,
		"%s/sensor/%s_battery_charge_remaining/config": `{
		"name": "__NAME__ Charge Remaining",
		"state_topic": "~/charge/remaining",
		"unit_of_measurement": "min",
		"unique_id": "__VIN___battery_charge_remaining",
		"dev": {
			"name": "PHEV __VIN__",
			"identifiers": ["phev-__VIN__"],
			"manufacturer": "Mitsubishi",
			"model": "Outlander PHEV"
		},
		"~": "__TOPIC__"}`,
		"%s/binary_sensor/%s_charger_connected/config": `{
		"device_class": "plug",
		"name": "__NAME__ Charger Connected",
		"state_topic": "~/charge/plug",
		"payload_on": "connected",
		"payload_off": "unplugged",
		"unique_id": "__VIN___charger_connected",
		"dev": {
			"name": "PHEV __VIN__",
			"identifiers": ["phev-__VIN__"],
			"manufacturer": "Mitsubishi",
			"model": "Outlander PHEV"
		},
		"~": "__TOPIC__"}`,
		"%s/binary_sensor/%s_battery_charging/config": `{
		"device_class": "battery_charging",
		"name": "__NAME__ Charging",
		"state_topic": "~/charge/charging",
		"payload_on": "on",
		"payload_off": "off",
		"unique_id": "__VIN___battery_charging",
		"dev": {
			"name": "PHEV __VIN__",
			"identifiers": ["phev-__VIN__"],
			"manufacturer": "Mitsubishi",
			"model": "Outlander PHEV"
		},
		"~": "__TOPIC__"}`,
		"%s/switch/%s_cancel_charge_timer/config": `{
		"name": "__NAME__ Disable Charge Timer",
		"icon": "mdi:timer-off",
		"state_topic": "~/battery/charging",
		"command_topic": "~/set/cancelchargetimer",
		"unique_id": "__VIN___cancel_charge_timer",
		"dev": {
			"name": "PHEV __VIN__",
			"identifiers": ["phev-__VIN__"],
			"manufacturer": "Mitsubishi",
			"model": "Outlander PHEV"
		},
		"~": "__TOPIC__"}`,
		// Climate
		"%s/binary_sensor/%s_climate_operating/config": `{
		"device_class": "running",
		"name": "__NAME__ AC Operating",
		"icon": "mdi:air-conditioner",
		"state_topic": "~/climate/operating",
		"payload_off": "off",
		"payload_on": "on",
		"unique_id": "__VIN___climate_operating",
		"dev": {
			"name": "PHEV __VIN__",
			"identifiers": ["phev-__VIN__"],
			"manufacturer": "Mitsubishi",
			"model": "Outlander PHEV"
		},
		"~": "__TOPIC__"}`,
		"%s/switch/%s_climate_heat/config": `{
		"name": "__NAME__ Heat",
		"icon": "mdi:weather-sunny",
		"state_topic": "~/climate/heat",
		"command_topic": "~/set/climate/heat",
		"payload_off": "off",
		"payload_on": "on",
		"unique_id": "__VIN___climate_heat",
		"dev": {
			"name": "PHEV __VIN__",
			"identifiers": ["phev-__VIN__"],
			"manufacturer": "Mitsubishi",
			"model": "Outlander PHEV"
		},
		"~": "__TOPIC__"}`,
		"%s/switch/%s_climate_cool/config": `{
		"name": "__NAME__ cool",
		"icon": "mdi:air-conditioner",
		"state_topic": "~/climate/cool",
		"command_topic": "~/set/climate/cool",
		"payload_off": "off",
		"payload_on": "on",
		"unique_id": "__VIN___climate_cool",
		"dev": {
			"name": "PHEV __VIN__",
			"identifiers": ["phev-__VIN__"],
			"manufacturer": "Mitsubishi",
			"model": "Outlander PHEV"
		},
		"~": "__TOPIC__"}`,
		"%s/switch/%s_climate_windscreen/config": `{
		"name": "__NAME__ windscreen",
		"icon": "mdi:car-defrost-front",
		"state_topic": "~/climate/windscreen",
		"command_topic": "~/set/climate/windscreen",
		"payload_off": "off",
		"payload_on": "on",
		"unique_id": "__VIN___climate_windscreen",
		"dev": {
			"name": "PHEV __VIN__",
			"identifiers": ["phev-__VIN__"],
			"manufacturer": "Mitsubishi",
			"model": "Outlander PHEV"
		},
		"~": "__TOPIC__"}`,
		"%s/select/%s_climate_on/config": `{
				"name": "__NAME__ climate state",
				"icon": "mdi:car-seat-heater",
				"state_topic": "~/climate/state",
				"command_topic": "~/set/climate/mode",
				"options": [ "off", "heat", "cool", "windscreen"],
				"unique_id": "__VIN___climate_on",
		"dev": {
			"name": "PHEV __VIN__",
			"identifiers": ["phev-__VIN__"],
			"manufacturer": "Mitsubishi",
			"model": "Outlander PHEV"
		},
				"~": "__TOPIC__"}`,
		// Lights.
		"%s/light/%s_parkinglights/config": `{
		"name": "__NAME__ Park Lights",
		"icon": "mdi:car-parking-lights",
		"state_topic": "~/lights/parking",
		"command_topic": "~/set/parkinglights",
		"payload_off": "off",
		"payload_on": "on",
		"unique_id": "__VIN___parkinglights",
		"dev": {
			"name": "PHEV __VIN__",
			"identifiers": ["phev-__VIN__"],
			"manufacturer": "Mitsubishi",
			"model": "Outlander PHEV"
		},
		"~": "__TOPIC__"}`,
		"%s/light/%s_headlights/config": `{
		"name": "__NAME__ Head Lights",
		"icon": "mdi:car-light-dimmed",
		"state_topic": "~/lights/head",
		"command_topic": "~/set/headlights",
		"payload_off": "off",
		"payload_on": "on",
		"unique_id": "__VIN___headlights",
		"dev": {
			"name": "PHEV __VIN__",
			"identifiers": ["phev-__VIN__"],
			"manufacturer": "Mitsubishi",
			"model": "Outlander PHEV"
		},
		"~": "__TOPIC__"}`,
		"%s/binary_sensor/%s_interiorlights/config": `{
		"device_class": "light",
		"name": "__NAME__ Interior Lights",
		"icon": "mdi:lightbulb",
		"state_topic": "~/lights/interior",
		"payload_off": "off",
		"payload_on": "on",
		"unique_id": "__VIN___interiorlights",
		"dev": {
			"name": "PHEV __VIN__",
			"identifiers": ["phev-__VIN__"],
			"manufacturer": "Mitsubishi",
			"model": "Outlander PHEV"
		},
		"~": "__TOPIC__"}`,
		"%s/binary_sensor/%s_hazardlights/config": `{
		"device_class": "light",
		"name": "__NAME__ Hazard Lights",
		"icon": "mdi:hazard-lights",
		"state_topic": "~/lights/hazard",
		"payload_off": "off",
		"payload_on": "on",
		"unique_id": "__VIN___hazardlights",
		"dev": {
			"name": "PHEV __VIN__",
			"identifiers": ["phev-__VIN__"],
			"manufacturer": "Mitsubishi",
			"model": "Outlander PHEV"
		},
		"~": "__TOPIC__"}`,
		// General topics.
		"%s/sensor/%s_vehicle_time/config": `{
		"name": "__NAME__ Vehicle Time",
		"state_topic": "~/time",
		"icon": "mdi:clock-outline",
		"device_class": "timestamp",
		"entity_category": "diagnostic",
		"unique_id": "__VIN___vehicle_time",
		"dev": {
			"name": "PHEV __VIN__",
			"identifiers": ["phev-__VIN__"],
			"manufacturer": "Mitsubishi",
			"model": "Outlander PHEV"
		},
		"~": "__TOPIC__"}`,
		"%s/sensor/%s_wifi_ssid/config": `{
		"name": "__NAME__ WiFi SSID",
		"state_topic": "~/wifi/ssid",
		"icon": "mdi:wifi",
		"entity_category": "diagnostic",
		"unique_id": "__VIN___wifi_ssid",
		"dev": {
			"name": "PHEV __VIN__",
			"identifiers": ["phev-__VIN__"],
			"manufacturer": "Mitsubishi",
			"model": "Outlander PHEV"
		},
		"~": "__TOPIC__"}`,
		"%s/sensor/%s_settings/config": `{
		"name": "__NAME__ Settings",
		"state_topic": "~/settings",
		"icon": "mdi:cog",
		"entity_category": "diagnostic",
		"unique_id": "__VIN___settings",
		"dev": {
			"name": "PHEV __VIN__",
			"identifiers": ["phev-__VIN__"],
			"manufacturer": "Mitsubishi",
			"model": "Outlander PHEV"
		},
		"~": "__TOPIC__"}`,
		"%s/sensor/%s_registrations/config": `{
		"name": "__NAME__ Registrations",
		"state_topic": "~/registrations",
		"icon": "mdi:counter",
		"entity_category": "diagnostic",
		"unique_id": "__VIN___registrations",
		"dev": {
			"name": "PHEV __VIN__",
			"identifiers": ["phev-__VIN__"],
			"manufacturer": "Mitsubishi",
			"model": "Outlander PHEV"
		},
		"~": "__TOPIC__"}`,
		"%s/sensor/%s_ecu_version/config": `{
		"name": "__NAME__ ECU Version",
		"state_topic": "~/ecuversion",
		"icon": "mdi:chip",
		"entity_category": "diagnostic",
		"unique_id": "__VIN___ecu_version",
		"dev": {
			"name": "PHEV __VIN__",
			"identifiers": ["phev-__VIN__"],
			"manufacturer": "Mitsubishi",
			"model": "Outlander PHEV"
		},
		"~": "__TOPIC__"}`,
	}

	// Only add WiFi restart button if either local or remote WiFi restart is enabled
	if m.localWifiRestartEnabled || m.remoteWifiRestartEnabled {
		log.Debugf("[HA Discovery] Adding WiFi restart button (local: %v, remote: %v)", m.localWifiRestartEnabled, m.remoteWifiRestartEnabled)
		discoveryData["%s/button/%s_reconnect_wifi/config"] = `{
		"name": "__NAME__ Restart Wifi Connection",
		"icon": "mdi:timer-off",
		"command_topic": "~/connection",
		"payload_press": "restart",
		"unique_id": "__VIN___restart_wifi",
		"dev": {
			"name": "PHEV __VIN__",
			"identifiers": ["phev-__VIN__"],
			"manufacturer": "Mitsubishi",
			"model": "Outlander PHEV"
		},
		"~": "__TOPIC__"}`
	} else {
		log.Debugf("[HA Discovery] WiFi restart button disabled")
	}

	mappings := map[string]string{
		"__NAME__":  name,
		"__VIN__":   vin,
		"__TOPIC__": topic,
	}

	log.Infof("[HA Discovery] Publishing %d entity configurations", len(discoveryData))
	successCount := 0
	errorCount := 0

	for topic, d := range discoveryData {
		topic = fmt.Sprintf(topic, m.haDiscoveryPrefix, vin)
		for in, out := range mappings {
			d = strings.Replace(d, in, out, -1)
		}
		if err := publishMQTT(client, topic, 0, true, d); err != nil {
			log.Errorf("[HA Discovery] Failed to publish to %s: %v", topic, err)
			errorCount++
		} else {
			log.Debugf("[HA Discovery] Published: %s", topic)
			successCount++
		}
		//m.client.Publish(topic, 0, false, "{}")
	}

	log.Infof("[HA Discovery] Complete - %d entities published successfully, %d errors", successCount, errorCount)
	if errorCount == 0 {
		m.haPublishedDiscovery = true
	}
}

func init() {
	clientCmd.AddCommand(mqttCmd)

	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// mqttCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// mqttCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
	mqttCmd.Flags().String("mqtt_server", "tcp://127.0.0.1:1883", "Address of MQTT server")
	mqttCmd.Flags().String("mqtt_username", "", "Username to login to MQTT server")
	mqttCmd.Flags().String("mqtt_password", "", "Password to login to MQTT server")
	mqttCmd.Flags().String("mqtt_topic_prefix", "phev", "Prefix for MQTT topics")
	mqttCmd.Flags().Bool("mqtt_disable_register_set_command", false, "Disable vechicle register setting via MQTT")
	mqttCmd.Flags().Bool("ha_discovery", true, "Enable Home Assistant MQTT discovery")
	mqttCmd.Flags().String("ha_discovery_prefix", "homeassistant", "Prefix for Home Assistant MQTT discovery")
	mqttCmd.Flags().String("vehicle_vin", "", "Vehicle VIN for Home Assistant discovery (enables immediate discovery on startup)")
	mqttCmd.Flags().Duration("update_interval", 5*time.Minute, "How often to request force updates")
	mqttCmd.Flags().Bool("local_wifi_restart_enabled", false, "Enable local WiFi restart")
	mqttCmd.Flags().Duration("wifi_restart_time", 0, "Attempt to restart Wifi if no connection for this long")
	mqttCmd.Flags().String("wifi_restart_command", defaultWifiRestartCmd, "Command to restart Wifi connection to Phev")
	mqttCmd.Flags().Bool("remote_wifi_restart_enabled", false, "Enable remote WiFi restart via MQTT")
	mqttCmd.Flags().String("remote_wifi_restart_topic", "", "MQTT topic to send remote WiFi restart command (e.g., for MikroTik)")
	mqttCmd.Flags().String("remote_wifi_restart_message", "restart", "Message payload for remote WiFi restart")
	mqttCmd.Flags().String("remote_wifi_control_topic", "", "MQTT topic to send remote WiFi enable/disable commands")
	mqttCmd.Flags().String("remote_wifi_enable_message", `{"wifi": "enable"}`, "Message payload for remote WiFi enable")
	mqttCmd.Flags().String("remote_wifi_disable_message", `{"wifi": "disable"}`, "Message payload for remote WiFi disable")
	mqttCmd.Flags().Bool("remote_wifi_power_save_enabled", false, "Enable power save mode to turn WiFi off between updates")
	mqttCmd.Flags().Duration("remote_wifi_power_save_wait", 5*time.Second, "Time to wait after turning WiFi on for link to establish")
	mqttCmd.Flags().Duration("remote_wifi_power_save_duration", 30*time.Second, "How long to stay connected to collect data before disconnecting in power save mode")
	mqttCmd.Flags().Duration("remote_wifi_command_wait", 10*time.Second, "Time to keep WiFi on after sending a command to receive status updates")

	// Advanced timeout settings
	mqttCmd.Flags().Duration("connection_retry_interval", 60*time.Second, "Time to wait between connection retry attempts")
	mqttCmd.Flags().Duration("availability_offline_timeout", 30*time.Second, "Time without connection before publishing MQTT offline status")
	mqttCmd.Flags().Duration("remote_wifi_restart_min_interval", 2*time.Minute, "Minimum time between remote WiFi restart attempts")
	mqttCmd.Flags().Duration("phev_start_timeout", 20*time.Second, "Timeout waiting for PHEV to respond to start command")
	mqttCmd.Flags().Duration("phev_register_timeout", 10*time.Second, "Timeout waiting for register set acknowledgment")
	mqttCmd.Flags().Duration("phev_tcp_read_timeout", 30*time.Second, "TCP read deadline for PHEV connection")
	mqttCmd.Flags().Duration("phev_tcp_write_timeout", 15*time.Second, "TCP write deadline for PHEV connection")
	mqttCmd.Flags().Duration("encoding_error_reset_interval", 15*time.Second, "Time after which encoding error count resets")
	mqttCmd.Flags().Duration("config_reload_interval", 5*time.Second, "How often to check for configuration file changes")

	viper.BindPFlag("mqtt_server", mqttCmd.Flags().Lookup("mqtt_server"))
	viper.BindPFlag("mqtt_username", mqttCmd.Flags().Lookup("mqtt_username"))
	viper.BindPFlag("mqtt_password", mqttCmd.Flags().Lookup("mqtt_password"))
	viper.BindPFlag("mqtt_topic_prefix", mqttCmd.Flags().Lookup("mqtt_topic_prefix"))
	viper.BindPFlag("mqtt_disable_register_set_command", mqttCmd.Flags().Lookup("mqtt_disable_register_set_command"))
	viper.BindPFlag("ha_discovery", mqttCmd.Flags().Lookup("ha_discovery"))
	viper.BindPFlag("ha_discovery_prefix", mqttCmd.Flags().Lookup("ha_discovery_prefix"))
	viper.BindPFlag("vehicle_vin", mqttCmd.Flags().Lookup("vehicle_vin"))
	viper.BindPFlag("update_interval", mqttCmd.Flags().Lookup("update_interval"))
	viper.BindPFlag("local_wifi_restart_enabled", mqttCmd.Flags().Lookup("local_wifi_restart_enabled"))
	viper.BindPFlag("wifi_restart_time", mqttCmd.Flags().Lookup("wifi_restart_time"))
	viper.BindPFlag("wifi_restart_command", mqttCmd.Flags().Lookup("wifi_restart_command"))
	viper.BindPFlag("remote_wifi_restart_enabled", mqttCmd.Flags().Lookup("remote_wifi_restart_enabled"))
	viper.BindPFlag("remote_wifi_restart_topic", mqttCmd.Flags().Lookup("remote_wifi_restart_topic"))
	viper.BindPFlag("remote_wifi_restart_message", mqttCmd.Flags().Lookup("remote_wifi_restart_message"))
	viper.BindPFlag("remote_wifi_control_topic", mqttCmd.Flags().Lookup("remote_wifi_control_topic"))
	viper.BindPFlag("remote_wifi_enable_message", mqttCmd.Flags().Lookup("remote_wifi_enable_message"))
	viper.BindPFlag("remote_wifi_disable_message", mqttCmd.Flags().Lookup("remote_wifi_disable_message"))
	viper.BindPFlag("remote_wifi_power_save_enabled", mqttCmd.Flags().Lookup("remote_wifi_power_save_enabled"))
	viper.BindPFlag("remote_wifi_power_save_wait", mqttCmd.Flags().Lookup("remote_wifi_power_save_wait"))
	viper.BindPFlag("remote_wifi_power_save_duration", mqttCmd.Flags().Lookup("remote_wifi_power_save_duration"))
	viper.BindPFlag("remote_wifi_command_wait", mqttCmd.Flags().Lookup("remote_wifi_command_wait"))
	viper.BindPFlag("connection_retry_interval", mqttCmd.Flags().Lookup("connection_retry_interval"))
	viper.BindPFlag("availability_offline_timeout", mqttCmd.Flags().Lookup("availability_offline_timeout"))
	viper.BindPFlag("remote_wifi_restart_min_interval", mqttCmd.Flags().Lookup("remote_wifi_restart_min_interval"))
	viper.BindPFlag("phev_start_timeout", mqttCmd.Flags().Lookup("phev_start_timeout"))
	viper.BindPFlag("phev_register_timeout", mqttCmd.Flags().Lookup("phev_register_timeout"))
	viper.BindPFlag("phev_tcp_read_timeout", mqttCmd.Flags().Lookup("phev_tcp_read_timeout"))
	viper.BindPFlag("phev_tcp_write_timeout", mqttCmd.Flags().Lookup("phev_tcp_write_timeout"))
	viper.BindPFlag("encoding_error_reset_interval", mqttCmd.Flags().Lookup("encoding_error_reset_interval"))
	viper.BindPFlag("config_reload_interval", mqttCmd.Flags().Lookup("config_reload_interval"))
}

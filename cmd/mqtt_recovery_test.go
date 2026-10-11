package cmd

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type mockMQTTToken struct {
	err error
}

func (t mockMQTTToken) Wait() bool                       { return true }
func (t mockMQTTToken) WaitTimeout(_ time.Duration) bool { return true }
func (t mockMQTTToken) Done() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}
func (t mockMQTTToken) Error() error { return t.err }

type mockMQTTPublication struct {
	topic    string
	payload  interface{}
	retained bool
}

type mockMQTTClient struct {
	mqtt.Client
	mu             sync.Mutex
	connected      bool
	subscriptions  []map[string]byte
	publications   []mockMQTTPublication
	publishFailure func(topic string) error
	publishedTopic chan string
}

func (c *mockMQTTClient) IsConnected() bool {
	return c.connected
}

func (c *mockMQTTClient) SubscribeMultiple(filters map[string]byte, _ mqtt.MessageHandler) mqtt.Token {
	c.mu.Lock()
	defer c.mu.Unlock()
	copied := make(map[string]byte, len(filters))
	for topic, qos := range filters {
		copied[topic] = qos
	}
	c.subscriptions = append(c.subscriptions, copied)
	return mockMQTTToken{}
}

func (c *mockMQTTClient) Publish(topic string, _ byte, retained bool, payload interface{}) mqtt.Token {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.publications = append(c.publications, mockMQTTPublication{
		topic:    topic,
		payload:  payload,
		retained: retained,
	})
	if c.publishedTopic != nil {
		select {
		case c.publishedTopic <- topic:
		default:
		}
	}
	if c.publishFailure != nil {
		return mockMQTTToken{err: c.publishFailure(topic)}
	}
	return mockMQTTToken{}
}

func (c *mockMQTTClient) published(topic string, payload interface{}, retained bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, publication := range c.publications {
		if publication.topic == topic && publication.payload == payload && publication.retained == retained {
			return true
		}
	}
	return false
}

type mockMQTTMessage struct {
	mqtt.Message
	topic   string
	payload []byte
}

func (m mockMQTTMessage) Topic() string   { return m.topic }
func (m mockMQTTMessage) Payload() []byte { return m.payload }

func TestMQTTSubscriptionTopics(t *testing.T) {
	topics := mqttSubscriptionTopics("phev", "homeassistant", false, true)
	for _, topic := range []string{"phev/set/#", "phev/connection", "phev/settings/#", "homeassistant/status"} {
		if _, ok := topics[topic]; !ok {
			t.Errorf("subscription topics missing %q", topic)
		}
	}

	topics = mqttSubscriptionTopics("car", "ha/", true, false)
	for _, topic := range []string{"car/connection", "car/settings/#"} {
		if _, ok := topics[topic]; !ok {
			t.Errorf("subscription topics missing %q", topic)
		}
	}
	if _, ok := topics["car/set/#"]; ok {
		t.Error("register set subscription should be disabled")
	}
	if _, ok := topics["ha/status"]; ok {
		t.Error("Home Assistant birth subscription should be omitted when discovery is disabled")
	}
}

func TestHomeAssistantBirthMessageMatch(t *testing.T) {
	if !isHomeAssistantBirthMessage("ha/status", "ONLINE\n", "ha/status") {
		t.Fatal("online birth message did not match")
	}
	if isHomeAssistantBirthMessage("other/status", "online", "ha/status") {
		t.Fatal("message on another topic matched birth topic")
	}
	if isHomeAssistantBirthMessage("ha/status", "offline", "ha/status") {
		t.Fatal("offline payload matched birth message")
	}
}

func TestDiscoveryVINPrefersConfiguredVIN(t *testing.T) {
	if got := discoveryVIN("configured", "cached"); got != "configured" {
		t.Fatalf("discoveryVIN() = %q, want configured VIN", got)
	}
	if got := discoveryVIN("", "cached"); got != "cached" {
		t.Fatalf("discoveryVIN() = %q, want cached VIN", got)
	}
}

func TestRestoreMQTTStateReplaysAvailabilityVINAndDiscovery(t *testing.T) {
	client := &mockMQTTClient{connected: true}
	m := &mqttClient{
		client:            client,
		prefix:            "phev",
		haDiscovery:       true,
		haDiscoveryPrefix: "ha",
		vehicleVIN:        "configured-vin",
		connected:         true,
	}

	m.restoreMQTTState(client)

	if len(client.subscriptions) != 1 {
		t.Fatalf("subscriptions restored %d times, want 1", len(client.subscriptions))
	}
	if !client.published("phev/available", "online", true) {
		t.Error("current online availability was not retained")
	}
	if !client.published("phev/vin", "configured-vin", true) {
		t.Error("configured VIN was not retained")
	}
	foundDiscovery := false
	for _, publication := range client.publications {
		if strings.HasPrefix(publication.topic, "ha/") && strings.Contains(publication.topic, "configured-vin") {
			foundDiscovery = true
			break
		}
	}
	if !foundDiscovery {
		t.Error("Home Assistant discovery was not republished")
	}
}

func TestRestoreMQTTStateUsesCachedVINAndDoesNotReportCarOnline(t *testing.T) {
	client := &mockMQTTClient{connected: true}
	m := &mqttClient{
		client:            client,
		prefix:            "phev",
		haDiscovery:       true,
		haDiscoveryPrefix: "ha",
		lastVIN:           "cached-vin",
	}

	m.restoreMQTTState(client)

	if !client.published("phev/available", "offline", true) {
		t.Error("disconnected vehicle should remain offline after broker reconnect")
	}
	if !client.published("phev/vin", "cached-vin", true) {
		t.Error("cached VIN was not retained after broker reconnect")
	}
	foundDiscovery := false
	for _, publication := range client.publications {
		if strings.HasPrefix(publication.topic, "ha/") && strings.Contains(publication.topic, "cached-vin") {
			foundDiscovery = true
			break
		}
	}
	if !foundDiscovery {
		t.Error("discovery was not republished with cached VIN")
	}
}

func TestDiscoveryPublicationCanRetryAfterFailure(t *testing.T) {
	fail := true
	client := &mockMQTTClient{
		connected: true,
		publishFailure: func(topic string) error {
			if fail && strings.HasPrefix(topic, "ha/") {
				return errors.New("temporary broker error")
			}
			return nil
		},
	}
	m := &mqttClient{
		client:            client,
		prefix:            "phev",
		haDiscovery:       true,
		haDiscoveryPrefix: "ha",
		vehicleVIN:        "configured-vin",
	}

	m.republishMQTTState(client)
	if m.haPublishedDiscovery {
		t.Fatal("discovery marked complete despite failed retained publications")
	}

	fail = false
	m.republishMQTTState(client)
	if !m.haPublishedDiscovery {
		t.Fatal("discovery did not complete after retry")
	}
}

func TestRepublishAfterHomeAssistantRestartUsesCurrentAvailability(t *testing.T) {
	client := &mockMQTTClient{connected: true}
	m := &mqttClient{
		client:            client,
		prefix:            "phev",
		haDiscovery:       true,
		haDiscoveryPrefix: "ha",
		lastVIN:           "cached-vin",
	}

	m.republishAfterHomeAssistantRestart(client)

	if !client.published("phev/available", "offline", true) {
		t.Error("Home Assistant restart recovery reported a disconnected vehicle as online")
	}
}

func TestHandleIncomingMQTTReplaysStateForHomeAssistantBirth(t *testing.T) {
	client := &mockMQTTClient{
		connected:      true,
		publishedTopic: make(chan string, 128),
	}
	m := &mqttClient{
		client:            client,
		prefix:            "phev",
		haDiscovery:       true,
		haDiscoveryPrefix: "ha",
		lastVIN:           "cached-vin",
	}

	m.handleIncomingMqtt(client, mockMQTTMessage{topic: "ha/status", payload: []byte("online")})

	deadline := time.After(time.Second)
	for {
		select {
		case topic := <-client.publishedTopic:
			if topic == "phev/available" {
				if !client.published(topic, "offline", true) {
					t.Fatal("birth recovery did not republish current availability")
				}
			}
			if strings.HasPrefix(topic, "ha/") {
				if !strings.Contains(topic, "cached-vin") {
					t.Fatalf("discovery topic %q does not use the cached VIN", topic)
				}
				return
			}
		case <-deadline:
			t.Fatal("Home Assistant birth message did not trigger discovery replay")
		}
	}
}

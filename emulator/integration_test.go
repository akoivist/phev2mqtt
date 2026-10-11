package emulator_test

import (
	"encoding/hex"
	"io"
	"net"
	"testing"
	"time"

	"github.com/buxtronix/phev2mqtt/emulator"
	"github.com/buxtronix/phev2mqtt/protocol"
)

func TestEmulatorPingIntegration(t *testing.T) {
	address := freeTCPAddress(t)

	car, err := emulator.NewCar(emulator.AddressOption(address))
	if err != nil {
		t.Fatalf("NewCar() returned unexpected error: %v", err)
	}
	if err := car.Begin(); err != nil {
		t.Fatalf("Car.Begin() returned unexpected error: %v", err)
	}

	conn := dialWhenReady(t, address)
	defer conn.Close()

	key := &protocol.SecurityKey{}
	writeMessage(t, conn, protocol.NewPingRequestMessage(0x2a), key)
	ping := readMessage(t, conn, key)
	if ping.Type != protocol.CmdInPingResp {
		t.Fatalf("response type = 0x%02x, want 0x%02x", ping.Type, protocol.CmdInPingResp)
	}
	if ping.Register != 0x2a {
		t.Fatalf("response sequence = 0x%02x, want 0x2a", ping.Register)
	}
}

func TestEmulatorRegisterAckIntegration(t *testing.T) {
	address := freeTCPAddress(t)

	car, err := emulator.NewCar(emulator.AddressOption(address))
	if err != nil {
		t.Fatalf("NewCar() returned unexpected error: %v", err)
	}
	if err := car.Begin(); err != nil {
		t.Fatalf("Car.Begin() returned unexpected error: %v", err)
	}

	conn := dialWhenReady(t, address)
	defer conn.Close()

	key := &protocol.SecurityKey{}
	writeMessage(t, conn, protocol.NewMessage(protocol.CmdOutSend, 0x10, false, []byte{0x1}), key)
	ack := readMessage(t, conn, key)
	if ack.Type != protocol.CmdInResp {
		t.Fatalf("ack type = 0x%02x, want 0x%02x", ack.Type, protocol.CmdInResp)
	}
	if ack.Ack != protocol.Ack {
		t.Fatalf("ack flag = 0x%02x, want 0x%02x", ack.Ack, protocol.Ack)
	}
	if ack.Register != 0x10 {
		t.Fatalf("ack register = 0x%02x, want 0x10", ack.Register)
	}
}

func dialWhenReady(t *testing.T, address string) net.Conn {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for {
		conn, err := net.DialTimeout("tcp", address, 50*time.Millisecond)
		if err == nil {
			return conn
		}
		if time.Now().After(deadline) {
			t.Fatalf("emulator did not become ready: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func writeMessage(t *testing.T, conn net.Conn, message *protocol.PhevMessage, key *protocol.SecurityKey) {
	t.Helper()

	if _, err := conn.Write(message.EncodeToBytes(key)); err != nil {
		t.Fatalf("conn.Write() returned unexpected error: %v", err)
	}
}

func readMessage(t *testing.T, conn net.Conn, key *protocol.SecurityKey) *protocol.PhevMessage {
	t.Helper()

	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("SetReadDeadline() returned unexpected error: %v", err)
	}
	response := make([]byte, 6)
	if _, err := io.ReadFull(conn, response); err != nil {
		t.Fatalf("io.ReadFull() returned unexpected error: %v", err)
	}
	messages := protocol.NewFromBytes(response, key)
	if len(messages) != 1 {
		t.Fatalf("decoded %d messages from %s, want 1", len(messages), hex.EncodeToString(response))
	}
	return messages[0]
}

func freeTCPAddress(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() returned unexpected error: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("listener.Close() returned unexpected error: %v", err)
	}
	return address
}

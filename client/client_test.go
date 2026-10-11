package client

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/buxtronix/phev2mqtt/protocol"
)

func TestClientSendAfterCloseReturnsError(t *testing.T) {
	c, err := New()
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close() returned unexpected error: %v", err)
	}

	err = c.SendMessage(&protocol.PhevMessage{Type: protocol.CmdOutSend, Register: 0x10})
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("SendMessage() after Close() = %v, want client closed error", err)
	}
}

func TestListenerCloseIsIdempotent(t *testing.T) {
	c, err := New()
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	l := c.AddListener()
	c.closeListeners()
	if _, ok := <-l.C; ok {
		t.Fatal("listener channel should be closed after client disconnect")
	}
	l.Stop()
	if _, ok := <-l.C; ok {
		t.Fatal("listener channel should close exactly once")
	}
}

func TestSetRegisterReturnsPromptlyOnClose(t *testing.T) {
	c, err := New()
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	c.registerTimeout = 5 * time.Second

	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = c.Close()
	}()

	err = c.SetRegister(0x10, []byte{0x1})
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("SetRegister() after Close() = %v, want closed error", err)
	}
}

func TestReaderClosesRecvAndListenersOnDisconnect(t *testing.T) {
	server, clientConn := net.Pipe()
	defer server.Close()

	c, err := New()
	if err != nil {
		t.Fatalf("New() returned unexpected error: %v", err)
	}
	c.conn = clientConn
	c.key = &protocol.SecurityKey{}
	l := c.AddListener()

	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = server.Close()
	}()

	c.reader()

	if _, ok := <-c.Recv; ok {
		t.Fatal("Recv channel should be closed by reader exit")
	}
	if _, ok := <-l.C; ok {
		t.Fatal("listener channel should be closed on disconnect")
	}
}

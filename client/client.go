// Package client implements a client for communicating with a Mitsubishi
// Outlander Phev.
package client

import (
	"encoding/hex"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/buxtronix/phev2mqtt/protocol"
)

const DefaultAddress = "192.168.8.46:8080"

// A Listener is for communicating messages from the vehicle to
// interested clients.
type Listener struct {
	// C has received messages.
	C chan *protocol.PhevMessage

	mu     sync.Mutex
	closed bool
	stop   bool
}

func (l *Listener) Start() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stop = false
	l.closed = false
	if l.C == nil {
		l.C = make(chan *protocol.PhevMessage, 5)
	}
}

func (l *Listener) Stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.stop = true
	l.closed = true
	if l.C != nil {
		close(l.C)
	}
}

func (l *Listener) Send(m *protocol.PhevMessage) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.stop || l.C == nil {
		return
	}
	select {
	case l.C <- m:
	default:
		log.Debug("%PHEV_RECV_LISTENER% message not sent")
	}
}

func (l *Listener) ProcessStop() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stop {
		l.stop = false
		if l.C != nil && !l.closed {
			close(l.C)
			l.closed = true
		}
		return true
	}
	return false
}

type ModelYear int64

const (
	ModelYearUnknown ModelYear = iota
	ModelYear14
	ModelYear18
	ModelYear24
)

// A Client is a TCP client to a Phev.
type Client struct {
	// Recv is a channel where incoming messages from the Phev are sent.
	Recv chan *protocol.PhevMessage
	// Send is a channel to send messages to the Phev.
	Send chan *protocol.PhevMessage

	// Settings are settings for the car.
	Settings *protocol.Settings

	listeners     []*Listener
	lMu           sync.Mutex
	recvOnce      sync.Once
	closeOnce     sync.Once
	recvClosed    bool
	started       chan struct{}
	startedClose  sync.Once
	startedClosed bool
	done          chan struct{}

	address string
	conn    net.Conn
	lastRx  atomic.Int64

	key *protocol.SecurityKey

	// Keep track of the model year so we can use the correct registers
	ModelYear ModelYear

	// Configurable timeouts
	tcpReadTimeout  time.Duration
	tcpWriteTimeout time.Duration
	startTimeout    time.Duration
	registerTimeout time.Duration
}

// An Option configures the client.
type Option func(c *Client)

// AddressOption configures the address to the Phev.
func AddressOption(address string) func(*Client) {
	return func(c *Client) {
		c.address = address
	}
}

// TCPReadTimeoutOption configures the TCP read timeout.
func TCPReadTimeoutOption(timeout time.Duration) func(*Client) {
	return func(c *Client) {
		c.tcpReadTimeout = timeout
	}
}

// TCPWriteTimeoutOption configures the TCP write timeout.
func TCPWriteTimeoutOption(timeout time.Duration) func(*Client) {
	return func(c *Client) {
		c.tcpWriteTimeout = timeout
	}
}

// StartTimeoutOption configures the start command timeout.
func StartTimeoutOption(timeout time.Duration) func(*Client) {
	return func(c *Client) {
		c.startTimeout = timeout
	}
}

// RegisterTimeoutOption configures the register set timeout.
func RegisterTimeoutOption(timeout time.Duration) func(*Client) {
	return func(c *Client) {
		c.registerTimeout = timeout
	}
}

// New returns a new client, not yet connected.
func New(opts ...Option) (*Client, error) {
	cl := &Client{
		Recv:            make(chan *protocol.PhevMessage, 256),
		Send:            make(chan *protocol.PhevMessage, 5),
		Settings:        &protocol.Settings{},
		started:         make(chan struct{}, 2),
		listeners:       []*Listener{},
		address:         DefaultAddress,
		key:             &protocol.SecurityKey{},
		ModelYear:       ModelYearUnknown,
		done:            make(chan struct{}),
		tcpReadTimeout:  30 * time.Second,
		tcpWriteTimeout: 15 * time.Second,
		startTimeout:    20 * time.Second,
		registerTimeout: 10 * time.Second,
	}
	for _, o := range opts {
		o(cl)
	}
	return cl, nil
}

func (c *Client) isClosed() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

func (c *Client) SendMessage(msg *protocol.PhevMessage) error {
	if msg == nil {
		return nil
	}
	if c.isClosed() {
		return fmt.Errorf("client closed")
	}
	select {
	case <-c.done:
		return fmt.Errorf("client closed")
	case c.Send <- msg:
		return nil
	default:
		return fmt.Errorf("send queue full")
	}
}

// Create and return a new Listener.
func (c *Client) AddListener() *Listener {
	c.lMu.Lock()
	defer c.lMu.Unlock()
	l := &Listener{}
	l.Start()
	c.listeners = append(c.listeners, l)
	return l
}

func (c *Client) RemoveListener(l *Listener) {
	newL := []*Listener{}
	c.lMu.Lock()
	defer c.lMu.Unlock()
	for _, lis := range c.listeners {
		if lis != l {
			newL = append(newL, lis)
		}
	}
	c.listeners = newL
}

// Close closes the client.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		close(c.done)
		if c.conn != nil {
			_ = c.conn.Close()
		}
	})
	return nil
}

// Connect connects to the Phev.
func (c *Client) Connect() error {
	if c.isClosed() {
		return fmt.Errorf("client is closed")
	}
	log.Infof("[TCP Connect] Attempting TCP connection to %s", c.address)
	conn, err := net.Dial("tcp", c.address)
	if err != nil {
		log.Infof("[TCP Connect] Connection failed: %v", err)
		return err
	}
	log.Infof("[TCP Connect] TCP connection established to %s", c.address)
	log.Infof("[TCP Timeouts] Read: %v, Write: %v, Start: %v, Register: %v", c.tcpReadTimeout, c.tcpWriteTimeout, c.startTimeout, c.registerTimeout)
	log.Info("%PHEV_TCP_CONNECTED%")
	c.conn = conn
	go c.reader()
	go c.writer()
	go c.manage()
	go c.pinger()

	return nil
}

// Start waits for the client to start.
func (c *Client) Start() error {
	log.Infof("[PHEV Start] Waiting for start handshake (timeout: %v)", c.startTimeout)
	log.Debug("%%PHEV_START_AWAIT%%")
	startTimer := time.After(c.startTimeout)
	for {
		select {
		case _, ok := <-c.started:
			if !ok {
				log.Info("[PHEV Start] Start channel closed unexpectedly")
				log.Debug("%%PHEV_START_CLOSED%%")
				return fmt.Errorf("receiver closed before getting start request")
			}
			log.Info("[PHEV Start] Start handshake completed successfully")
			log.Debug("%%PHEV_START_DONE%%")
		case <-startTimer:
			log.Infof("[PHEV Start] Start handshake TIMED OUT after %v", c.startTimeout)
			log.Debug("%%PHEV_START_TIMEOUT%%")
			return fmt.Errorf("timed out waiting for start")
		}
		return nil
	}
}

// SetRegister sets a register on the car.
func (c *Client) SetRegister(register byte, value []byte) error {
	setRegister := func(xor byte) error {
		return c.SendMessage(&protocol.PhevMessage{
			Type:     protocol.CmdOutSend,
			Ack:      protocol.Request,
			Register: register,
			Data:     value,
			Xor:      xor,
		})
	}
	xor := byte(0)
	timer := time.NewTimer(c.registerTimeout)
	defer timer.Stop()
	l := c.AddListener()
	defer l.Stop()
	defer c.RemoveListener(l)
	if err := setRegister(xor); err != nil {
		return err
	}
	for {
		select {
		case <-c.done:
			return fmt.Errorf("client closed while setting register %02x", register)
		case <-timer.C:
			return fmt.Errorf("timed out attempting to set register %02x", register)
		case msg, ok := <-l.C:
			if !ok {
				return fmt.Errorf("listener channel closed")
			}
			if msg.Type == protocol.CmdInBadEncoding {
				xor = msg.Data[0]
				if err := setRegister(xor); err != nil {
					return err
				}
				continue
			}
			if msg.Type == protocol.CmdInResp && msg.Ack == protocol.Ack && msg.Register == register {
				return nil
			}
		}
	}
}

func (c *Client) nextRecvMsg(deadline time.Time) (*protocol.PhevMessage, error) {
	timer := time.After(deadline.Sub(time.Now()))
	for {
		select {
		case <-timer:
			return nil, fmt.Errorf("timed out waiting for message")
		case m, ok := <-c.Recv:
			if !ok {
				return nil, fmt.Errorf("error: receive channel closed")
			}
			return m, nil
		}
	}
}

// Sends periodic pings to the car.
func (c *Client) pinger() {
	pingSeq := byte(0xa)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			if time.Since(time.Unix(0, c.lastRx.Load())) < 500*time.Millisecond {
				continue
			}
			if err := c.SendMessage(protocol.NewPingRequestMessage(pingSeq)); err != nil {
				return
			}
			pingSeq++
			if pingSeq > 0x63 {
				pingSeq = 0
			}
		}
	}
}

// manages the connection, handling control messages.
func (c *Client) manage() {
	ml := c.AddListener()
	defer ml.Stop()
	for {
		select {
		case <-c.done:
			log.Debug("%PHEV_MANAGER_END%%")
			return
		case m, ok := <-ml.C:
			if !ok {
				c.startedClose.Do(func() { close(c.started) })
				log.Debug("%PHEV_MANAGER_END%%")
				return
			}
			switch m.Type {
			case protocol.CmdInResp:
				if m.Ack == protocol.Request && m.Register == protocol.SettingsRegister {
					c.Settings.FromRegister(m.Data)
				}
			case protocol.CmdInStartResp:
				if err := c.SendMessage(protocol.NewPingRequestMessage(0xa)); err != nil {
					return
				}
			case protocol.CmdInMy24StartReq:
				c.ModelYear = ModelYear24
				if err := c.SendMessage(&protocol.PhevMessage{
					Type:     protocol.CmdOutMy24StartResp,
					Register: 0x1,
					Ack:      protocol.Ack,
					Xor:      m.Xor,
					Data:     []byte{0x0},
				}); err != nil {
					return
				}
				log.Debug("%%PHEV_START24_RECV%%")
				c.started <- struct{}{}
			case protocol.CmdInMy18StartReq:
				c.ModelYear = ModelYear18
				if err := c.SendMessage(&protocol.PhevMessage{
					Type:     protocol.CmdOutMy18StartResp,
					Register: 0x1,
					Ack:      protocol.Ack,
					Xor:      m.Xor,
					Data:     []byte{0x0},
				}); err != nil {
					return
				}
				log.Debug("%%PHEV_START18_RECV%%")
				c.started <- struct{}{}
			case protocol.CmdInMy14StartReq:
				c.ModelYear = ModelYear14
				if err := c.SendMessage(&protocol.PhevMessage{
					Type:     protocol.CmdOutMy14StartResp,
					Register: 0x1,
					Ack:      protocol.Ack,
					Xor:      m.Xor,
					Data:     []byte{0x0},
				}); err != nil {
					return
				}
				log.Debug("%%PHEV_START14_RECV%%")
				c.started <- struct{}{}
			}
		}
	}
}

func (c *Client) reader() {
	log.Infof("[TCP Reader] Starting reader goroutine with read timeout: %v", c.tcpReadTimeout)
	for {
		log.Tracef("[TCP Reader] Setting read deadline to %v from now", c.tcpReadTimeout)
		if err := c.conn.SetReadDeadline(time.Now().Add(c.tcpReadTimeout)); err != nil {
			if !c.isClosed() {
				log.Infof("[TCP Reader] SetReadDeadline error: %v", err)
			}
			c.Close()
			return
		}
		data := make([]byte, 4096)
		n, err := c.conn.Read(data)
		if err != nil {
			if !c.isClosed() {
				log.Infof("[TCP Reader] Read error (timeout=%v): %v", c.tcpReadTimeout, err)
				log.Debug("%%PHEV_TCP_READER_ERROR%%: ", err)
			}
			log.Info("[TCP Reader] Closing reader due to error")
			log.Debug("%PHEV_TCP_READER_CLOSE%")
			c.Close()
			c.closeRecv()
			c.closeListeners()
			return
		}
		c.lastRx.Store(time.Now().UnixNano())
		log.Tracef("%%PHEV_TCP_RECV_DATA%%: %s", hex.EncodeToString(data[:n]))
		messages := protocol.NewFromBytes(data[:n], c.key)
		for _, m := range messages {
			log.Debugf("%%PHEV_TCP_RECV_MSG%%: [%02x] %s", m.Xor, m.ShortForm())
			c.lMu.Lock()
			for _, l := range c.listeners {
				l.Send(m)
			}
			c.lMu.Unlock()
			select {
			case <-c.done:
				return
			case c.Recv <- m:
			}
		}
	}
}

func (c *Client) closeRecv() {
	c.recvOnce.Do(func() {
		close(c.Recv)
		c.recvClosed = true
	})
}

func (c *Client) closeListeners() {
	c.lMu.Lock()
	defer c.lMu.Unlock()
	for _, l := range c.listeners {
		l.Stop()
	}
}

func (c *Client) writer() {
	for {
		select {
		case <-c.done:
			log.Debug("%PHEV_TCP_WRITER_CLOSE%")
			return
		case msg, ok := <-c.Send:
			if !ok {
				log.Debug("%PHEV_TCP_WRITER_CLOSE%")
				c.Close()
				return
			}
			msg.Xor = 0
			data := msg.EncodeToBytes(c.key)
			log.Debugf("%%PHEV_TCP_SEND_MSG%%: [%02x] %s", msg.Xor, msg.ShortForm())
			log.Tracef("%%PHEV_TCP_SEND_DATA%%: %s", hex.EncodeToString(data))
			log.Tracef("[TCP Writer] Setting write deadline to %v from now", c.tcpWriteTimeout)
			if err := c.conn.SetWriteDeadline(time.Now().Add(c.tcpWriteTimeout)); err != nil {
				if !c.isClosed() {
					log.Infof("[TCP Writer] SetWriteDeadline error (timeout=%v): %v", c.tcpWriteTimeout, err)
					log.Errorf("%%PHEV_TCP_WRITER_ERROR%%: %v", err)
				}
				log.Info("[TCP Writer] Closing writer due to error")
				log.Debug("%PHEV_TCP_WRITER_CLOSE%")
				c.Close()
				return
			}
			if _, err := c.conn.Write(data); err != nil {
				if !c.isClosed() {
					log.Infof("[TCP Writer] Write error (timeout=%v): %v", c.tcpWriteTimeout, err)
					log.Errorf("%%PHEV_TCP_WRITER_ERROR%%: %v", err)
				}
				log.Info("[TCP Writer] Closing writer due to error")
				log.Debug("%PHEV_TCP_WRITER_CLOSE%")
				c.Close()
				return
			}
		}
	}
}

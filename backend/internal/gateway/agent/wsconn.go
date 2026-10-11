package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

// DefaultHeartbeatInterval is the agent-side heartbeat cadence when the gateway
// does not dictate one in WELCOME.
const DefaultHeartbeatInterval = 20 * time.Second

// AgentEndpointPath is the WebSocket upgrade path for the tunnel.
const AgentEndpointPath = "/gw/agent"

// RegisterFunc supplies the node metadata for a REGISTER frame. It is called on
// every dial so the payload reflects the current sessions/capabilities.
type RegisterFunc func() wire.Register

// WSConnector implements a DialFunc backed by the reverse-tunnel WebSocket. It
// performs the HELLO/WELCOME/REGISTER handshake and then keeps the connection
// alive with heartbeats while a read loop services control frames.
type WSConnector struct {
	// AgentVersion is reported in HELLO.
	AgentVersion string
	// Register supplies the REGISTER payload. Required.
	Register RegisterFunc
	// Dialer overrides the WebSocket dialer (tests use it to shorten timeouts).
	Dialer *websocket.Dialer // deprecated: use Config.ProxyURL
	// HeartbeatInterval overrides the default heartbeat cadence. WELCOME may
	// still shorten it.
	HeartbeatInterval time.Duration
	// Logf receives non-fatal diagnostics; nil disables logging.
	Logf func(format string, args ...any)
	// Forward configures reverse-proxy forwarding to the local service.
	Forward ForwardConfig
	// ForwardProvider, when set, overrides Forward at dial time. Hosts use it to
	// supply the local base URL once their loopback server is listening.
	ForwardProvider func() ForwardConfig
}

// Dial establishes the tunnel: connect, HELLO/WELCOME, REGISTER/REGISTER_ACK,
// then start heartbeat + read loop. The returned Conn is live until Close.
func (c *WSConnector) Dial(ctx context.Context, cfg Config) (Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := cfg.Validate(); err != nil {
		return nil, &ConnectError{Code: ErrCodeConfigInvalid, Message: err.Error(), Retryable: false}
	}
	if c.Register == nil {
		return nil, &ConnectError{Code: ErrCodeInternal, Message: "register metadata provider is nil", Retryable: false}
	}

	endpoint, err := AgentEndpoint(cfg.URL)
	if err != nil {
		return nil, &ConnectError{Code: ErrCodeConfigInvalid, Message: err.Error(), Retryable: false}
	}

	header := http.Header{}
	header.Set("Authorization", "Bearer "+strings.TrimSpace(cfg.Token))

	dialer, derr := websocketDialer(cfg)
	if derr != nil {
		return nil, &ConnectError{Code: ErrCodeConfigInvalid, Message: "proxy config invalid: " + derr.Error(), Retryable: false}
	}
	ws, resp, err := dialer.DialContext(ctx, endpoint, header)
	if err != nil {
		return nil, classifyHandshakeError(err, resp)
	}

	forward := c.Forward
	if c.ForwardProvider != nil {
		forward = c.ForwardProvider()
	}
	conn := &wsConn{
		ws:                ws,
		nodeID:            "",
		consoleURL:        "",
		heartbeatInterval: c.heartbeatInterval(),
		register:          c.Register,
		logf:              c.Logf,
		forward:           forward,
		done:              make(chan struct{}),
		sendCredit:        make(map[uint32]int64),
		flowSignal:        make(chan struct{}, 1),
	}

	if err := conn.handshake(ctx, c.AgentVersion, cfg); err != nil {
		_ = ws.Close()
		return nil, err
	}

	go conn.heartbeatLoop()
	go conn.readLoop()
	return conn, nil
}

func (c *WSConnector) heartbeatInterval() time.Duration {
	if c.HeartbeatInterval > 0 {
		return c.HeartbeatInterval
	}
	return DefaultHeartbeatInterval
}

// AgentEndpoint converts a configured gateway URL (https/http/ws/wss) into the
// WebSocket upgrade URL. A URL without an explicit path defaults to
// AgentEndpointPath.
func AgentEndpoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("gateway url is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid gateway url %q: %w", raw, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "https", "wss":
		u.Scheme = "wss"
	case "http", "ws":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("unsupported gateway url scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("gateway url %q has no host", raw)
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = AgentEndpointPath
	}
	return u.String(), nil
}

// classifyHandshakeError maps an upgrade failure to a ConnectError. HTTP 401/403
// is treated as a terminal auth failure so the host does not retry a rejected
// device token (reconnect storm).
func classifyHandshakeError(err error, resp *http.Response) *ConnectError {
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return &ConnectError{Code: ErrCodeAuthInvalid, Message: fmt.Sprintf("gateway rejected credentials (HTTP %d)", status), Retryable: false}
	}
	if status >= 400 {
		return &ConnectError{Code: ErrCodeNetwork, Message: fmt.Sprintf("gateway handshake failed (HTTP %d)", status), Retryable: true}
	}
	return &ConnectError{Code: ErrCodeNetwork, Message: err.Error(), Retryable: true}
}

// wsConn is a live tunnel connection.
type wsConn struct {
	ws        *websocket.Conn
	writeMu   sync.Mutex
	closeOnce sync.Once
	// deadOnce guards the death channel; separate from closeOnce so Close can
	// still send DEREGISTER after a GOAWAY/network path already marked closed.
	deadOnce sync.Once
	done     chan struct{}

	nodeID     string
	consoleURL string

	heartbeatInterval time.Duration
	register          RegisterFunc
	logf              func(format string, args ...any)
	forward           ForwardConfig

	mu      sync.Mutex
	streams map[uint32]*inboundStream

	flowMu     sync.Mutex
	sendCredit map[uint32]int64
	flowSignal chan struct{}
}

func (c *wsConn) NodeID() string     { return c.nodeID }
func (c *wsConn) ConsoleURL() string { return c.consoleURL }

// Done exposes the connection death channel so the controller's auto-reconnect
// watcher can detect GOAWAY / network loss (docs/gateway/12-connection-control.md).
func (c *wsConn) Done() <-chan struct{} { return c.done }

// markClosed closes the death channel exactly once. Every path that ends the
// connection (operator Close, gateway GOAWAY, network drop in the read loop)
// must route through it — the controller's auto-reconnect watcher keys off
// this channel, so a path that skips it leaks a zombie connection that never
// triggers failover.
func (c *wsConn) markClosed() {
	c.deadOnce.Do(func() {
		close(c.done)
	})
}

// Close sends a best-effort DEREGISTER, then closes the socket. It is
// idempotent.
func (c *wsConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		c.markClosed()
		_ = c.writeControl(wire.TypeDeregister, wire.Deregister{Reason: "client shutdown"})
		err = c.ws.Close()
	})
	return err
}

func (c *wsConn) logf0(format string, args ...any) {
	if c.logf != nil {
		c.logf(format, args...)
	}
}

// handshake performs HELLO → WELCOME → REGISTER → REGISTER_ACK.
func (c *wsConn) handshake(ctx context.Context, agentVersion string, cfg Config) error {
	deadline := time.Now().Add(15 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = c.ws.SetReadDeadline(deadline)

	hello, err := wire.NewJSONFrame(wire.TypeHello, 0, wire.Hello{
		ProtocolVersion: wire.ProtocolVersion,
		AgentVersion:    agentVersion,
	})
	if err != nil {
		return &ConnectError{Code: ErrCodeInternal, Message: err.Error(), Retryable: false}
	}
	if err := c.writeFrame(hello); err != nil {
		return &ConnectError{Code: ErrCodeNetwork, Message: err.Error(), Retryable: true}
	}

	welcomeFrame, err := c.readFrame()
	if err != nil {
		return &ConnectError{Code: ErrCodeNetwork, Message: err.Error(), Retryable: true}
	}
	if welcomeFrame.Type != wire.TypeWelcome {
		return &ConnectError{Code: wire.CodeProtocolVersionUnsupported, Message: "expected WELCOME, got " + welcomeFrame.Type.String(), Retryable: false}
	}
	welcome, err := wire.DecodePayload[wire.Welcome](welcomeFrame)
	if err != nil {
		return &ConnectError{Code: ErrCodeInternal, Message: err.Error(), Retryable: false}
	}
	if welcome.HeartbeatIntervalSec > 0 {
		c.heartbeatInterval = time.Duration(welcome.HeartbeatIntervalSec) * time.Second
	}

	register := c.register()
	registerFrame, err := wire.NewJSONFrame(wire.TypeRegister, 0, register)
	if err != nil {
		return &ConnectError{Code: ErrCodeInternal, Message: err.Error(), Retryable: false}
	}
	if err := c.writeFrame(registerFrame); err != nil {
		return &ConnectError{Code: ErrCodeNetwork, Message: err.Error(), Retryable: true}
	}

	ackFrame, err := c.readFrame()
	if err != nil {
		return &ConnectError{Code: ErrCodeNetwork, Message: err.Error(), Retryable: true}
	}
	if ackFrame.Type == wire.TypeError {
		msg, _ := wire.DecodePayload[wire.ErrorMessage](ackFrame)
		return errorFromWire(msg)
	}
	if ackFrame.Type != wire.TypeRegisterAck {
		return &ConnectError{Code: ErrCodeInternal, Message: "expected REGISTER_ACK, got " + ackFrame.Type.String(), Retryable: false}
	}
	ack, err := wire.DecodePayload[wire.RegisterAck](ackFrame)
	if err != nil {
		return &ConnectError{Code: ErrCodeInternal, Message: err.Error(), Retryable: false}
	}
	c.nodeID = ack.NodeID
	c.consoleURL = ack.ConsoleURL

	_ = c.ws.SetReadDeadline(time.Time{})
	return nil
}

// errorFromWire maps a gateway ERROR frame to a ConnectError. Auth failures are
// terminal; everything else is retryable.
func errorFromWire(msg wire.ErrorMessage) *ConnectError {
	retryable := true
	switch msg.Code {
	case wire.CodeAuthInvalid, wire.CodeAuthRequired, wire.CodeProtocolVersionUnsupported, wire.CodeNodeLimitExceeded:
		retryable = false
	}
	return &ConnectError{Code: msg.Code, Message: msg.Message, Retryable: retryable}
}

func (c *wsConn) readFrame() (wire.Frame, error) {
	_, data, err := c.ws.ReadMessage()
	if err != nil {
		return wire.Frame{}, err
	}
	return wire.Decode(data)
}

func (c *wsConn) writeFrame(f wire.Frame) error {
	data, err := wire.Encode(f)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.ws.WriteMessage(websocket.BinaryMessage, data)
}

func (c *wsConn) writeControl(t wire.FrameType, payload any) error {
	f, err := wire.NewJSONFrame(t, 0, payload)
	if err != nil {
		return err
	}
	return c.writeFrame(f)
}

// SendSessionUpdate pushes a SESSION_UPDATE frame to the gateway.
func (c *wsConn) SendSessionUpdate(update wire.SessionUpdate) error {
	return c.writeControl(wire.TypeSessionUpdate, update)
}

// SendNotify pushes a NOTIFY frame (an instantaneous attention event) to the
// gateway.
func (c *wsConn) SendNotify(n wire.Notify) error {
	return c.writeControl(wire.TypeNotify, n)
}

// heartbeatLoop sends periodic HEARTBEAT frames until the connection closes.
func (c *wsConn) heartbeatLoop() {
	interval := c.heartbeatInterval
	if interval <= 0 {
		interval = DefaultHeartbeatInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			if err := c.writeControl(wire.TypeHeartbeat, wire.Heartbeat{TS: time.Now().UTC()}); err != nil {
				c.logf0("gateway agent: heartbeat write failed: %v", err)
				return
			}
		}
	}
}

// readLoop services control frames until the socket closes. Stream frames are
// not yet handled here (the reverse proxy lands in a later workstream); they are
// ignored so the read loop never blocks the heartbeat.
func (c *wsConn) readLoop() {
	for {
		frame, err := c.readFrame()
		if err != nil {
			// Network drop: the connection is dead regardless of the close
			// handshake outcome — surface it to the reconnect watcher.
			c.markClosed()
			if !errors.Is(err, websocket.ErrCloseSent) {
				c.logf0("gateway agent: read loop ended: %v", err)
			}
			select {
			case <-c.done:
			default:
				_ = c.ws.Close()
			}
			return
		}
		switch frame.Type {
		case wire.TypePing:
			_ = c.writeControl(wire.TypePong, map[string]any{})
		case wire.TypePong, wire.TypeHeartbeatAck, wire.TypeWelcome:
			// no-op
		case wire.TypeError, wire.TypeGoAway:
			c.logf0("gateway agent: gateway signalled %s", frame.Type)
			c.markClosed()
			_ = c.ws.Close()
			return
		case wire.TypeStreamOpen:
			c.handleStreamOpen(frame.StreamID, frame)
		case wire.TypeStreamData:
			c.handleStreamData(frame.StreamID, frame)
		case wire.TypeStreamEnd:
			c.handleStreamEnd(frame.StreamID)
		case wire.TypeStreamReset:
			c.handleStreamReset(frame.StreamID)
		case wire.TypeWindowUpdate:
			if delta, ok := wire.DecodeWindowUpdate(frame); ok {
				c.addCredit(frame.StreamID, int64(delta))
			}
		default:
			// Unknown frames are ignored for forward compatibility.
		}
	}
}

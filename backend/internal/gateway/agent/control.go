package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// Conn is a live tunnel connection returned by a successful DialFunc. The real
// implementation (WS + wire protocol, see docs/gateway/03-protocol.md) lives in
// the transport package; tests inject a fake.
type Conn interface {
	// NodeID is the node id assigned/confirmed by the gateway.
	NodeID() string
	// ConsoleURL is the gateway console URL for this node.
	ConsoleURL() string
	// Close tears the connection down (best effort).
	Close() error
}

// DialFunc establishes a tunnel connection. It is the single seam between the
// state machine and the transport, which keeps this package standard-library
// only and trivially testable.
type DialFunc func(ctx context.Context, cfg Config) (Conn, error)

// Error codes shared with the control surfaces. They map onto the protocol
// error codes documented in docs/gateway/03-protocol.md §7.
const (
	ErrCodeConfigInvalid = "config_invalid"
	ErrCodeDisabled      = "disabled"
	ErrCodeAuthInvalid   = "auth_invalid"
	ErrCodeNetwork       = "network"
	ErrCodeBusy          = "busy"
	ErrCodeInternal      = "internal"
)

// ConnectError classifies a failed connect attempt. Retryable=false (for
// example auth_invalid) tells the host not to auto-retry — retrying a rejected
// token only produces reconnect storms.
type ConnectError struct {
	Code      string
	Message   string
	Retryable bool
}

func (e *ConnectError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return "gateway/agent: connect failed (" + e.Code + ")"
	}
	return "gateway/agent: " + e.Message
}

// asConnectError normalises an arbitrary dial error into a *ConnectError.
func asConnectError(err error) *ConnectError {
	if err == nil {
		return nil
	}
	var ce *ConnectError
	if errors.As(err, &ce) {
		return ce
	}
	return &ConnectError{Code: ErrCodeNetwork, Message: err.Error(), Retryable: true}
}

// Option customises a Controller (tests use it to inject a clock).
type Option func(*Controller)

// WithClock overrides the time source used for ConnectedAt/LastHeartbeat.
func WithClock(now func() time.Time) Option {
	return func(c *Controller) {
		if now != nil {
			c.now = now
		}
	}
}

// Controller owns the connection state machine. All exported methods are safe
// for concurrent use. Connect/Disconnect/Reconnect are serialised through the
// mutex; the blocking dial runs outside the lock so Status stays responsive.
type Controller struct {
	mu   sync.Mutex
	cfg  Config
	dial DialFunc
	now  func() time.Time

	state  State
	conn   Conn
	status Status
}

// New creates a Controller. The initial state is StateDisabled when the config
// is disabled, otherwise StateDisconnected (the host decides when to Connect,
// typically at startup when AutoConnect is set).
func New(cfg Config, dial DialFunc, opts ...Option) *Controller {
	c := &Controller{
		cfg:  cfg,
		dial: dial,
		now:  time.Now,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	if c.now == nil {
		c.now = time.Now
	}
	c.status = Status{
		GatewayURL: strings.TrimSpace(cfg.URL),
		TokenHint:  cfg.TokenHint(),
		Mode:       ModeAuto,
	}
	if cfg.Enabled {
		c.state = StateDisconnected
	} else {
		c.state = StateDisabled
	}
	c.status.State = c.state
	return c
}

// Status returns a copy of the current status snapshot.
func (c *Controller) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// Config returns a copy of the current configuration.
func (c *Controller) Config() Config {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg
}

// SetSessions updates the session count reported to the gateway. It is called
// by the host as sessions are added/removed and never changes the connection
// state.
func (c *Controller) SetSessions(n int) {
	if n < 0 {
		n = 0
	}
	c.mu.Lock()
	c.status.Sessions = n
	c.mu.Unlock()
}

// MarkHeartbeat records that the gateway acknowledged a heartbeat.
func (c *Controller) MarkHeartbeat() {
	c.mu.Lock()
	c.status.LastHeartbeat = c.now()
	c.mu.Unlock()
}

// Connect establishes the tunnel as a config-driven (auto) connection. It is
// used at startup and by automatic reconnect. It is a no-op when already
// connecting or connected, and fails closed with a *ConnectError when the
// gateway is disabled or the configuration is incomplete.
func (c *Controller) Connect(ctx context.Context) error {
	return c.connect(ctx, ModeAuto)
}

// ConnectManual establishes the tunnel as an explicit operator action. It has
// the same semantics as Connect except that Status.Mode reports "manual".
func (c *Controller) ConnectManual(ctx context.Context) error {
	return c.connect(ctx, ModeManual)
}

func (c *Controller) connect(ctx context.Context, mode string) error {
	if ctx == nil {
		ctx = context.Background()
	}

	c.mu.Lock()
	switch c.state {
	case StateConnecting, StateConnected:
		c.mu.Unlock()
		return nil // idempotent
	case StateDisconnecting:
		c.mu.Unlock()
		return &ConnectError{Code: ErrCodeBusy, Message: "disconnect in progress", Retryable: true}
	case StateDisabled:
		c.mu.Unlock()
		return &ConnectError{Code: ErrCodeDisabled, Message: "gateway is disabled", Retryable: false}
	}
	if err := c.cfg.Validate(); err != nil {
		c.mu.Unlock()
		return &ConnectError{Code: ErrCodeConfigInvalid, Message: err.Error(), Retryable: false}
	}
	if c.dial == nil {
		c.mu.Unlock()
		return &ConnectError{Code: ErrCodeInternal, Message: "no dial function configured", Retryable: false}
	}

	old := c.conn
	c.conn = nil
	c.transition(StateConnecting)
	c.status.OperatorDisconnected = false
	c.status.Mode = mode
	cfg := c.cfg
	dial := c.dial
	c.mu.Unlock()

	if old != nil {
		_ = old.Close()
	}

	conn, err := dial(ctx, cfg)

	c.mu.Lock()
	defer c.mu.Unlock()
	// A concurrent Disconnect may have moved us out of Connecting; honour it.
	if c.state != StateConnecting {
		if conn != nil {
			_ = conn.Close()
		}
		return nil
	}
	if err != nil {
		ce := asConnectError(err)
		c.status.ErrorCode = ce.Code
		c.status.LastError = ce.Message
		c.status.Retryable = ce.Retryable
		c.transition(StateError)
		return ce
	}
	c.conn = conn
	c.status.NodeID = conn.NodeID()
	c.status.ConsoleURL = conn.ConsoleURL()
	c.status.ConnectedAt = c.now()
	c.status.LastHeartbeat = c.now()
	c.status.LastError = ""
	c.status.ErrorCode = ""
	c.status.Retryable = false
	c.transition(StateConnected)
	return nil
}

// Disconnect closes the tunnel and sets the operator-disconnected flag, which
// suppresses automatic reconnect until an explicit Connect. It never writes
// configuration: "disconnect this run" and "disable in config" are distinct.
func (c *Controller) Disconnect(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}

	c.mu.Lock()
	c.status.OperatorDisconnected = true
	switch c.state {
	case StateDisabled:
		c.mu.Unlock()
		return nil
	case StateDisconnected:
		c.mu.Unlock()
		return nil
	case StateDisconnecting:
		c.mu.Unlock()
		return nil
	}
	c.transition(StateDisconnecting)
	conn := c.conn
	c.conn = nil
	c.mu.Unlock()

	if conn != nil {
		_ = conn.Close()
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.NodeID = ""
	c.status.ConsoleURL = ""
	c.status.ConnectedAt = time.Time{}
	if c.state == StateDisconnecting {
		c.transition(StateDisconnected)
	}
	return nil
}

// Reconnect performs a disconnect followed by a connect, so the operator flag
// is cleared and a fresh tunnel is established. The disconnect flag is only
// transient here.
func (c *Controller) Reconnect(ctx context.Context) error {
	if err := c.Disconnect(ctx); err != nil {
		return err
	}
	return c.ConnectManual(ctx)
}

// HandleConnectionLost is called by the host when a live tunnel drops. It
// applies the reconnect policy:
//   - operator-disconnected: settle to Disconnected (no retry),
//   - terminal failure (retryable=false, e.g. auth_invalid): settle to Error,
//   - otherwise: enter Reconnecting and let the host schedule a retry.
func (c *Controller) HandleConnectionLost(code, message string, retryable bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
	c.status.NodeID = ""
	c.status.ConsoleURL = ""
	c.status.ConnectedAt = time.Time{}
	c.status.ErrorCode = code
	c.status.LastError = message
	c.status.Retryable = retryable

	switch {
	case c.status.OperatorDisconnected:
		c.transition(StateDisconnected)
	case !retryable:
		c.transition(StateError)
	default:
		c.status.ReconnectCount++
		c.transition(StateReconnecting)
	}
}

// Configure replaces the configuration and reconciles the enabled flag with the
// current state. Disabling a live connection triggers a disconnect; enabling a
// disabled controller moves it to Disconnected (ready to Connect). It does not
// auto-connect.
func (c *Controller) Configure(cfg Config) error {
	c.mu.Lock()
	c.cfg = cfg
	c.status.TokenHint = cfg.TokenHint()
	c.status.GatewayURL = strings.TrimSpace(cfg.URL)
	wasDisabled := c.state == StateDisabled
	switch {
	case !cfg.Enabled:
		if wasDisabled {
			c.mu.Unlock()
			return nil
		}
		c.mu.Unlock()
		_ = c.Disconnect(context.Background())
		c.mu.Lock()
		c.transition(StateDisabled)
	case wasDisabled:
		c.transition(StateDisconnected)
	}
	c.mu.Unlock()
	return nil
}

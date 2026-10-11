package agent

import "github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"

// SessionReporter is implemented by connections that can push SESSION_UPDATE
// frames (the WebSocket tunnel does).
type SessionReporter interface {
	SendSessionUpdate(wire.SessionUpdate) error
}

// NotifyReporter is implemented by connections that can push NOTIFY frames (an
// instantaneous attention event, docs/gateway/08-notifications.md §4.1).
type NotifyReporter interface {
	SendNotify(wire.Notify) error
}

// NewController builds a Controller wired to the WebSocket transport. The
// register function supplies the REGISTER payload on each dial. Passing a nil
// register is allowed only when no connect is expected (the dial then fails
// closed with an internal error).
//
// It is the convenience entry point for hosts (aicli, runtime-server): they
// supply their node metadata and let this package own the transport. Hosts that
// only learn their local loopback address after startup call SetForward once it
// is known; the value is applied on the next dial.
func NewController(cfg Config, register RegisterFunc, opts ...Option) *Controller {
	controller := New(cfg, nil, opts...)
	connector := &WSConnector{
		Register:       register,
		OnHeartbeatAck: controller.MarkHeartbeat,
	}
	controller.dial = connector.Dial
	connector.ForwardProvider = controller.ForwardConfig
	return controller
}

// SetForward updates the local-service forwarding configuration. It takes effect
// on the next dial; call Reconnect to apply immediately to a live connection.
func (c *Controller) SetForward(cfg ForwardConfig) {
	c.forwardMu.Lock()
	c.forward = cfg
	c.forwardMu.Unlock()
}

// ForwardConfig returns the current forwarding configuration.
func (c *Controller) ForwardConfig() ForwardConfig {
	c.forwardMu.RLock()
	defer c.forwardMu.RUnlock()
	return c.forward
}

// UpdateSessions pushes the current session list to the gateway as a
// reconcile SESSION_UPDATE. It is a no-op when not connected. Hosts call it on
// session create/switch/resume so the gateway registry stays fresh without a
// reconnect.
func (c *Controller) UpdateSessions(sessions []wire.SessionSummary) {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	reporter, ok := conn.(SessionReporter)
	if !ok || reporter == nil {
		return
	}
	_ = reporter.SendSessionUpdate(wire.SessionUpdate{
		Op:       wire.SessionOpReconcile,
		Sessions: sessions,
	})
}

// SendNotify pushes an instantaneous attention event to the gateway. It is a
// best-effort no-op when not connected or when the transport cannot report
// notifications.
func (c *Controller) SendNotify(n wire.Notify) {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	reporter, ok := conn.(NotifyReporter)
	if !ok || reporter == nil {
		return
	}
	_ = reporter.SendNotify(n)
}

package agent

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
	connector := &WSConnector{Register: register}
	controller := New(cfg, connector.Dial, opts...)
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

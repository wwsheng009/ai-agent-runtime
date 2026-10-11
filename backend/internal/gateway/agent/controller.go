package agent

// NewController builds a Controller wired to the WebSocket transport. The
// register function supplies the REGISTER payload on each dial. Passing a nil
// register is allowed only when no connect is expected (the dial then fails
// closed with an internal error).
//
// It is the convenience entry point for hosts (aicli, runtime-server): they
// supply their node metadata and let this package own the transport.
func NewController(cfg Config, register RegisterFunc, opts ...Option) *Controller {
	connector := &WSConnector{Register: register}
	return New(cfg, connector.Dial, opts...)
}

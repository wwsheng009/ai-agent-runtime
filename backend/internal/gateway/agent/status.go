package agent

import "time"

// Status is a read-only snapshot of the agent's connection state. It is the
// single shape rendered by the aicli `/gateway status` command and by the
// runtime-server GET /api/runtime/gateway/status endpoint, so the two control
// surfaces cannot disagree.
type Status struct {
	// State is the current lifecycle state.
	State State `json:"state"`
	// NodeID is the registered node id (empty until connected).
	NodeID string `json:"node_id,omitempty"`
	// GatewayURL is the configured gateway endpoint.
	GatewayURL string `json:"gateway_url,omitempty"`
	// ConsoleURL is the gateway-provided console URL for this node.
	ConsoleURL string `json:"console_url,omitempty"`
	// ConnectedAt is when the current connection was established.
	ConnectedAt time.Time `json:"connected_at,omitempty"`
	// LastHeartbeat is the most recent heartbeat acknowledged by the gateway.
	LastHeartbeat time.Time `json:"last_heartbeat,omitempty"`
	// LastError is a human-readable description of the most recent failure.
	LastError string `json:"last_error,omitempty"`
	// ErrorCode is the machine-readable failure code (see the ErrCode* constants).
	ErrorCode string `json:"error_code,omitempty"`
	// Retryable reports whether the last failure may be retried automatically.
	Retryable bool `json:"retryable,omitempty"`
	// ReconnectCount counts automatic reconnects since process start.
	ReconnectCount int `json:"reconnect_count"`
	// Sessions is the number of sessions currently reported to the gateway.
	Sessions int `json:"sessions"`
	// Mode is "auto" (config-driven) or "manual" (explicit connect).
	Mode string `json:"mode"`
	// OperatorDisconnected is true after a manual disconnect; it suppresses
	// automatic reconnect until an explicit connect.
	OperatorDisconnected bool `json:"operator_disconnected"`
	// TokenHint is a non-secret fingerprint of the configured token.
	TokenHint string `json:"token_hint,omitempty"`
}

// Connected reports whether the agent currently holds a live registration.
func (s Status) Connected() bool { return s.State == StateConnected }

package wire

import (
	"encoding/json"
	"fmt"
	"time"
)

// Protocol error codes carried by TypeError frames and by GOAWAY/STREAM_RESET
// (docs/gateway/03-protocol.md §7).
const (
	CodeProtocolVersionUnsupported = "protocol_version_unsupported"
	CodeAuthRequired               = "auth_required"
	CodeAuthInvalid                = "auth_invalid"
	CodeNodeLimitExceeded          = "node_limit_exceeded"
	CodeStreamLimitExceeded        = "stream_limit_exceeded"
	CodeRouteNotAllowed            = "route_not_allowed"
	CodeCapabilityNotGranted       = "capability_not_granted"
	CodeUpstreamUnreachable        = "upstream_unreachable"
	CodeUpstreamTimeout            = "upstream_timeout"
	CodeFrameTooLarge              = "frame_too_large"
	CodeInternalError              = "internal_error"
)

// Hello is the first frame sent by the agent (A→G, stream 0).
type Hello struct {
	ProtocolVersion uint8  `json:"protocol_version"`
	AgentVersion    string `json:"agent_version,omitempty"`
	NodeID          string `json:"node_id,omitempty"`
	Resume          bool   `json:"resume,omitempty"`
}

// Welcome is the gateway's reply to Hello (G→A, stream 0).
type Welcome struct {
	ServerVersion        string   `json:"server_version,omitempty"`
	HeartbeatIntervalSec int      `json:"heartbeat_interval_sec,omitempty"`
	MaxFrame             int      `json:"max_frame,omitempty"`
	Capabilities         []string `json:"capabilities,omitempty"`
}

// WorkspaceInfo is a filter/grouping dimension only, never a permission
// boundary (matches internal/mesh semantics).
type WorkspaceInfo struct {
	Path string `json:"path,omitempty"`
	Name string `json:"name,omitempty"`
}

// SessionSummary is one session reported during REGISTER / SESSION_UPDATE.
type SessionSummary struct {
	SessionID string         `json:"session_id"`
	Title     string         `json:"title,omitempty"`
	State     string         `json:"state,omitempty"`
	Attention string         `json:"attention,omitempty"`
	Busy      bool           `json:"busy,omitempty"`
	TurnID    string         `json:"turn_id,omitempty"`
	UpdatedAt time.Time      `json:"updated_at,omitempty"`
	Preview   string         `json:"preview,omitempty"`
	Workspace *WorkspaceInfo `json:"workspace,omitempty"`
}

// Register is the node self-description sent after WELCOME (A→G, stream 0).
type Register struct {
	NodeID       string           `json:"node_id,omitempty"`
	Kind         string           `json:"kind,omitempty"`
	Name         string           `json:"name,omitempty"`
	DeviceName   string           `json:"device_name,omitempty"`
	Version      string           `json:"version,omitempty"`
	BuildTime    string           `json:"build_time,omitempty"`
	OS           string           `json:"os,omitempty"`
	Arch         string           `json:"arch,omitempty"`
	Capabilities []string         `json:"capabilities,omitempty"`
	Workspace    *WorkspaceInfo   `json:"workspace,omitempty"`
	Routes       []string         `json:"routes,omitempty"`
	Sessions     []SessionSummary `json:"sessions,omitempty"`
}

// RegisterAck confirms the node registration (G→A, stream 0).
type RegisterAck struct {
	NodeID         string   `json:"node_id"`
	ConsoleURL     string   `json:"console_url,omitempty"`
	AcceptedRoutes []string `json:"accepted_routes,omitempty"`
	RejectedRoutes []string `json:"rejected_routes,omitempty"`
}

// LoadInfo is the optional load section of a heartbeat.
type LoadInfo struct {
	ActiveStreams int `json:"active_streams,omitempty"`
}

// SessionDigest is the lightweight per-session heartbeat entry.
type SessionDigest struct {
	SessionID string    `json:"session_id"`
	State     string    `json:"state,omitempty"`
	Attention string    `json:"attention,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// Heartbeat is the periodic liveness + session-digest frame (A→G, stream 0).
type Heartbeat struct {
	TS       time.Time       `json:"ts,omitempty"`
	Load     *LoadInfo       `json:"load,omitempty"`
	Sessions []SessionDigest `json:"sessions_digest,omitempty"`
}

// HeartbeatAck acknowledges a heartbeat and may carry pending actions (G→A).
type HeartbeatAck struct {
	ServerTime time.Time `json:"server_time,omitempty"`
	Pending    []string  `json:"pending,omitempty"`
}

// SessionUpdate is an incremental session change (A→G, stream 0).
//
// op is "upsert", "remove", or "reconcile" (reconcile carries the full list).
type SessionUpdate struct {
	Op       string           `json:"op"`
	Session  *SessionSummary  `json:"session,omitempty"`
	Sessions []SessionSummary `json:"sessions,omitempty"`
}

// SessionUpdate operation values.
const (
	SessionOpUpsert    = "upsert"
	SessionOpRemove    = "remove"
	SessionOpReconcile = "reconcile"
)

// Deregister is sent on graceful shutdown (A→G, stream 0).
type Deregister struct {
	Reason string `json:"reason,omitempty"`
}

// ErrorMessage is the payload of an ERROR frame (either direction, stream 0).
type ErrorMessage struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// GoAway announces connection shutdown (either direction, stream 0).
type GoAway struct {
	Code         string `json:"code,omitempty"`
	Message      string `json:"message,omitempty"`
	LastStreamID uint32 `json:"last_stream_id,omitempty"`
}

// NewJSONFrame builds a frame whose payload is the JSON encoding of v, with the
// JSON flag set. Control messages use stream 0.
func NewJSONFrame(t FrameType, streamID uint32, v any) (Frame, error) {
	payload, err := json.Marshal(v)
	if err != nil {
		return Frame{}, fmt.Errorf("gateway/wire: marshal %s: %w", t, err)
	}
	return Frame{
		Version:  ProtocolVersion,
		Type:     t,
		Flags:    FlagJSON,
		StreamID: streamID,
		Payload:  payload,
	}, nil
}

// DecodePayload decodes a JSON control frame payload into T.
func DecodePayload[T any](f Frame) (T, error) {
	var out T
	if !f.IsJSON() {
		return out, fmt.Errorf("gateway/wire: frame %s is not JSON", f.Type)
	}
	if err := json.Unmarshal(f.Payload, &out); err != nil {
		return out, fmt.Errorf("gateway/wire: decode %s: %w", f.Type, err)
	}
	return out, nil
}

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

// SessionParent marks a session row as a child of another session: a host that
// enables live subagent rows (AICLI_GATEWAY_REPORT_SUBAGENTS) appends one row per
// in-flight child task under its parent session. It is display-only metadata:
// attention/state of the managed turn still belong to the parent row, and the
// gateway treats the field as opaque.
type SessionParent struct {
	ParentSessionID string `json:"parent_session_id,omitempty"`
	AgentID         string `json:"agent_id,omitempty"`
	AgentType       string `json:"agent_type,omitempty"`
}

// SessionSummary is one session reported during REGISTER / SESSION_UPDATE.
type SessionSummary struct {
	SessionID string    `json:"session_id"`
	Title     string    `json:"title,omitempty"`
	State     string    `json:"state,omitempty"`
	Attention string    `json:"attention,omitempty"`
	Busy      bool      `json:"busy,omitempty"`
	TurnID    string    `json:"turn_id,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
	Preview   string    `json:"preview,omitempty"`
	// LastMessage / LastMessageRole expose the latest transcript message (role
	// "user"|"assistant"|"tool"|...) so the gateway console can render the full
	// last turn inline. The agent truncates the content (2000 runes) before
	// sending; the gateway treats both fields as opaque display strings.
	LastMessageRole string `json:"last_message_role,omitempty"`
	LastMessage     string `json:"last_message,omitempty"`
	// StatusText is the compact one-line mirror of the host's live activity
	// line ("Running shell", "Generating response", "Waiting for approval", …).
	// It deliberately carries no clock: StatusStartedAt anchors an elapsed
	// timer that the console advances locally, so a running turn does not need
	// a stream of near-identical updates.
	StatusText string `json:"status_text,omitempty"`
	// StatusStartedAt is when the current activity/state began (zero when the
	// session is idle or the host does not track it).
	StatusStartedAt time.Time `json:"status_started_at,omitempty"`
	// StatusTail is the display-only tail (≤512 runes, truncated by the shared
	// client core) of the host's in-progress content while a turn is active.
	// Gateway and console treat it as an opaque string; it never replaces the
	// full transcript (docs/gateway/07-console.md §6).
	StatusTail string `json:"status_tail,omitempty"`
	// Viewing indicates a local foreground viewer on the reporting host
	// (aicli TUI-attached session / runtime web session page with SSE
	// subscribers > 0). Host-scoped and best-effort; the gateway renews the
	// host presence source from it (docs/bots/10-presence-signals-design.md
	// §3.1). Absent from older agents → no behavior change.
	Viewing bool `json:"viewing,omitempty"`
	// SyncedAt is stamped by the GATEWAY when it last accepted any frame
	// (SESSION_UPDATE or heartbeat digest) for this session. Agents never set
	// it; the gateway overwrites any inbound value so it is always server
	// time. The console shows it as "同步 x 前" (freshness of the upload path,
	// distinct from UpdatedAt = content time).
	SyncedAt time.Time `json:"synced_at,omitempty"`
	// StateChangedAt / AttentionChangedAt are stamped by the GATEWAY when the
	// respective field transitions to a new value (either source: session
	// update or heartbeat digest). Agents never set them; the console renders
	// "运行中 2m 前 / 等待审批 30s 前" ages from them.
	StateChangedAt     time.Time      `json:"state_changed_at,omitempty"`
	AttentionChangedAt time.Time      `json:"attention_changed_at,omitempty"`
	Workspace          *WorkspaceInfo `json:"workspace,omitempty"`
	// Parent links a child (subagent) row to its parent session. Absent for
	// top-level sessions, so older consoles keep rendering them unchanged.
	Parent *SessionParent `json:"parent,omitempty"`
}

// Canonical session state values (SessionSummary.State).
//
// The vocabulary is intentionally small and shared by every host so the
// gateway console (and any other consumer) can label states without guessing
// host-specific strings. Attention remains a separate axis: State describes
// what the session is doing, Attention describes what the operator must do.
const (
	// SessionStateIdle: no turn in flight, nothing pending.
	SessionStateIdle = "idle"
	// SessionStateRunning: tools/processing executing.
	SessionStateRunning = "running"
	// SessionStateThinking: model reasoning/planning.
	SessionStateThinking = "thinking"
	// SessionStateAnswering: assistant response streaming.
	SessionStateAnswering = "answering"
	// SessionStateWaiting: waiting for the user (question, confirmation) or
	// for background obligations/subagents.
	SessionStateWaiting = "waiting"
	// SessionStateReview: waiting for an approval decision.
	SessionStateReview = "review"
	// SessionStateCompleted: the last turn finished successfully.
	SessionStateCompleted = "completed"
	// SessionStateInterrupted: the last turn was cancelled by the user.
	SessionStateInterrupted = "interrupted"
	// SessionStateError: the last turn failed.
	SessionStateError = "error"
)

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
	SessionID  string `json:"session_id"`
	State      string `json:"state,omitempty"`
	Attention  string `json:"attention,omitempty"`
	StatusText string `json:"status_text,omitempty"`
	// Viewing mirrors SessionSummary.Viewing: the high-frequency heartbeat is
	// the presence renewal channel (docs/bots/10 §3.1).
	Viewing   bool      `json:"viewing,omitempty"`
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
	// SessionID is the target of a "remove" update.
	SessionID string `json:"session_id,omitempty"`
}

// SessionUpdate operation values.
const (
	SessionOpUpsert    = "upsert"
	SessionOpRemove    = "remove"
	SessionOpReconcile = "reconcile"
)

// Notify kind values (docs/gateway/08-notifications.md §4.1).
const (
	NotifyKindTurnCompleted = "turn_completed"
	NotifyKindApproval      = "approval"
	NotifyKindQuestion      = "question"
	NotifyKindError         = "error"
	NotifyKindWaiting       = "waiting"
)

// Notify priority values.
const (
	NotifyPriorityHigh   = "high"
	NotifyPriorityNormal = "normal"
)

// Notify is an instantaneous attention event (A→G, stream 0,
// docs/gateway/08-notifications.md §4.1). It carries metadata only — never
// session body content — and complements the persistent attention carried by
// SESSION_UPDATE. The gateway uses it to update the registry attention and to
// derive a notification, deduplicated by DedupKey.
type Notify struct {
	SessionID string    `json:"session_id"`
	Kind      string    `json:"kind"`
	Priority  string    `json:"priority,omitempty"`
	Title     string    `json:"title,omitempty"`
	DedupKey  string    `json:"dedup_key,omitempty"`
	TS        time.Time `json:"ts,omitempty"`
}

// Deregister is sent on graceful shutdown (A→G, stream 0).
type Deregister struct {
	Reason string `json:"reason,omitempty"`
}

// StreamOpen is the payload of a STREAM_OPEN frame (G→A, stream >0): the
// gateway asks the agent to forward one HTTP request to its local service.
type StreamOpen struct {
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Headers map[string][]string `json:"headers,omitempty"`
	BodyLen int64               `json:"body_len,omitempty"`
}

// ResponseHeaders is the first STREAM_DATA payload of a response (JSON flag
// set); subsequent STREAM_DATA frames on the same stream carry the raw body.
type ResponseHeaders struct {
	Status  int                 `json:"status"`
	Headers map[string][]string `json:"headers,omitempty"`
}

// StreamReset is the payload of a STREAM_RESET frame.
type StreamReset struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
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

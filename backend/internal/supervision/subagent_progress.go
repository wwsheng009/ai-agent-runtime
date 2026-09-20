package supervision

import (
	"sort"
	"strings"
	"sync"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolprotocol"
)

// EventTypeSubagentProgress is the parent-stream mirror of a child session's
// tool progress (P1-5 方案 2).
//
// The mirror is live-only by design: hosts publish it on the runtime event bus
// for the parent session and never append it to the durable event store. Child
// progress therefore cannot pollute the parent transcript/replay, while a
// subscriber that asks for live events (live=1) can render it next to the
// existing subagent row.
const EventTypeSubagentProgress = "subagent.progress"

// DefaultSubagentProgressWindow throttles mirrored progress per child + tool
// call: identical updates inside the window are merged, state/phase changes and
// updates after the window are delivered. Without a window a chatty tool (bash
// output streaming) would flood the parent stream.
const DefaultSubagentProgressWindow = 2 * time.Second

const (
	// subagentProgressMaxEntries bounds the throttle table when a child never
	// reports completion (crash, host restart, killed process).
	subagentProgressMaxEntries = 512
	// subagentProgressStaleFactor drops stamps that stayed idle for
	// staleFactor windows, so long-running hosts do not accumulate state.
	subagentProgressStaleFactor = 10
)

// SubagentProgressTarget identifies the child whose progress is mirrored onto
// the parent stream. ParentSessionID and ChildSessionID are required; the
// remaining fields are copied into the mirror payload when non-empty so the
// parent UI can render the same identity it already has for subagent rows.
type SubagentProgressTarget struct {
	ParentSessionID string
	ChildSessionID  string
	Path            string
	Depth           int
	AgentType       string
	// ParentToolCallID 是发起该子会话的父侧 tool_call_id（spawn_agent 调用的 id）。
	// 非空时随镜像回填 parent_tool_call_id，前端/ACP 可把子代理进度挂到对应的
	// spawn_agent 行上；缺省为空（旧路径/降级时保持既有载荷形状）。
	ParentToolCallID string
}

// SubagentProgressMirror throttles child tool progress into parent-stream
// mirrors: at most one event per (child, tool call) per window, plus immediate
// delivery whenever the notification state changes (a tool moving from
// "progress" to any other state must never be swallowed by the window).
//
// The zero value is not usable; build one with NewSubagentProgressMirror. All
// methods are safe for concurrent use: progress arrives from tool workers.
type SubagentProgressMirror struct {
	mu     sync.Mutex
	window time.Duration
	last   map[string]subagentProgressStamp
}

type subagentProgressStamp struct {
	at      time.Time
	state   string
	message string
}

// NewSubagentProgressMirror builds a mirror with the given throttle window. A
// non-positive window falls back to DefaultSubagentProgressWindow.
func NewSubagentProgressMirror(window time.Duration) *SubagentProgressMirror {
	if window <= 0 {
		window = DefaultSubagentProgressWindow
	}
	return &SubagentProgressMirror{
		window: window,
		last:   make(map[string]subagentProgressStamp),
	}
}

// Observe turns one child tool.progress event into a throttled parent mirror.
//
// It returns false when the update is inside the throttle window and repeated
// (same state and same message), or when the source event is not mirrorable
// (missing child/parent session, missing tool identity, non-progress type).
func (m *SubagentProgressMirror) Observe(target SubagentProgressTarget, source runtimeevents.Event, now time.Time) (runtimeevents.Event, bool) {
	if m == nil {
		return runtimeevents.Event{}, false
	}
	parentSessionID := strings.TrimSpace(target.ParentSessionID)
	childSessionID := strings.TrimSpace(target.ChildSessionID)
	if parentSessionID == "" || childSessionID == "" {
		return runtimeevents.Event{}, false
	}
	if strings.TrimSpace(source.Type) != toolprotocol.EventTypeProgress {
		return runtimeevents.Event{}, false
	}

	payload := source.Payload
	toolCallID := strings.TrimSpace(stringPayloadText(payload, "tool_call_id"))
	toolName := firstNonEmptyText(
		strings.TrimSpace(source.ToolName),
		stringPayloadText(payload, "tool_name"),
		stringPayloadText(payload, "tool_id"),
	)
	if toolCallID == "" && toolName == "" {
		return runtimeevents.Event{}, false
	}
	// A progress event without a call id cannot be folded onto an existing tool
	// row, but the mirror only annotates the subagent row, so the tool name is
	// enough: keep the call id when present and fall back to the tool name for
	// throttling identity.
	identity := toolCallID
	if identity == "" {
		identity = "tool:" + toolName
	}
	state := firstNonEmptyText(
		stringPayloadText(payload, "kind"),
		stringPayloadText(payload, "status"),
		"progress",
	)
	message := firstNonEmptyText(
		stringPayloadText(payload, "message"),
		stringPayloadText(payload, "partial"),
	)

	if now.IsZero() {
		now = time.Now().UTC()
	}
	if m.window <= 0 {
		m.window = DefaultSubagentProgressWindow
	}
	key := childSessionID + "\x00" + identity

	m.mu.Lock()
	if m.last == nil {
		m.last = make(map[string]subagentProgressStamp)
	}
	previous, seen := m.last[key]
	stateChanged := seen && previous.state != state
	withinWindow := seen && now.Sub(previous.at) < m.window
	if seen && withinWindow && !stateChanged {
		// Leading-edge throttle: inside the window the update is merged into the
		// previously emitted mirror (the window anchors on the emitted stamp),
		// so a chatty tool cannot flood the parent stream. A state change is
		// emitted immediately: that is how a tool leaving "progress" (or a
		// background job finishing) stays visible.
		m.mu.Unlock()
		return runtimeevents.Event{}, false
	}
	m.last[key] = subagentProgressStamp{at: now, state: state, message: message}
	m.pruneLocked(now)
	m.mu.Unlock()

	mirrored := runtimeevents.Event{
		Type:      EventTypeSubagentProgress,
		TraceID:   strings.TrimSpace(source.TraceID),
		AgentName: "agent-controller",
		SessionID: parentSessionID,
		Timestamp: now.UTC(),
	}
	mirroredPayload := map[string]interface{}{
		"agent_id":          childSessionID,
		"session_id":        childSessionID,
		"parent_session_id": parentSessionID,
		"source_event_type": toolprotocol.EventTypeProgress,
		"state":             state,
		"live":              true,
	}
	// 父侧工具调用归位：目标上下文优先，其次兼容源事件载荷里已带该键的路径。
	if parentToolCallID := firstNonEmptyText(strings.TrimSpace(target.ParentToolCallID), stringPayloadText(payload, "parent_tool_call_id")); parentToolCallID != "" {
		mirroredPayload["parent_tool_call_id"] = parentToolCallID
	}
	if toolCallID != "" {
		mirroredPayload["tool_call_id"] = toolCallID
	}
	if toolName != "" {
		mirroredPayload["tool_name"] = toolName
	}
	if message != "" {
		mirroredPayload["message"] = message
	}
	if partial := stringPayloadText(payload, "partial"); partial != "" {
		mirroredPayload["partial"] = partial
	}
	if percent, ok := numericPayloadValue(payload, "percent"); ok {
		mirroredPayload["percent"] = percent
	}
	if !source.Timestamp.IsZero() {
		mirroredPayload["source_event_timestamp"] = source.Timestamp.UTC().Format(time.RFC3339Nano)
	}
	if path := strings.TrimSpace(target.Path); path != "" {
		mirroredPayload["path"] = path
	}
	if target.Depth > 0 {
		mirroredPayload["depth"] = target.Depth
	}
	if agentType := strings.TrimSpace(target.AgentType); agentType != "" {
		mirroredPayload["agent_type"] = agentType
		mirroredPayload["role"] = agentType
	}
	if metadata, ok := source.Payload["metadata"].(map[string]interface{}); ok {
		// Metadata is where tools put structured detail (step counters, file
		// paths). Copy it shallowly so the mirror stays off the source payload.
		for key, value := range metadata {
			if strings.TrimSpace(key) == "" || value == nil {
				continue
			}
			if _, exists := mirroredPayload[key]; exists {
				continue
			}
			mirroredPayload[key] = value
		}
	}
	mirroredPayload["mirror_window_ms"] = m.window.Milliseconds()
	mirrored.Payload = mirroredPayload
	return mirrored, true
}

// Forget drops throttle state for one child session. Hosts call it when the
// child completes so a long-running parent does not keep stamps for finished
// children.
func (m *SubagentProgressMirror) Forget(childSessionID string) {
	if m == nil {
		return
	}
	childSessionID = strings.TrimSpace(childSessionID)
	if childSessionID == "" {
		return
	}
	prefix := childSessionID + "\x00"
	m.mu.Lock()
	defer m.mu.Unlock()
	for key := range m.last {
		if strings.HasPrefix(key, prefix) {
			delete(m.last, key)
		}
	}
}

// Latest returns the newest mirrored stamp for one child session: state,
// message and the time it was observed. The mirror is live-only and throttled,
// so this is a best-effort "what is this child doing right now" reading (the
// P0-B progress rollup uses it for optional row detail); a host that never
// mirrored progress for the child gets ok=false.
func (m *SubagentProgressMirror) Latest(childSessionID string) (state, message string, at time.Time, ok bool) {
	if m == nil {
		return "", "", time.Time{}, false
	}
	childSessionID = strings.TrimSpace(childSessionID)
	if childSessionID == "" {
		return "", "", time.Time{}, false
	}
	prefix := childSessionID + "\x00"
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, stamp := range m.last {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if !ok || stamp.at.After(at) {
			state, message, at, ok = stamp.state, stamp.message, stamp.at, true
		}
	}
	return state, message, at, ok
}

// Pending reports how many throttle entries are currently tracked (tests and
// diagnostics; the mirror never grows past subagentProgressMaxEntries).
func (m *SubagentProgressMirror) Pending() int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.last)
}

func (m *SubagentProgressMirror) pruneLocked(now time.Time) {
	staleBefore := now.Add(-subagentProgressStaleFactor * m.window)
	for key, stamp := range m.last {
		if stamp.at.Before(staleBefore) {
			delete(m.last, key)
		}
	}
	if len(m.last) <= subagentProgressMaxEntries {
		return
	}
	type agedKey struct {
		key string
		at  time.Time
	}
	aged := make([]agedKey, 0, len(m.last))
	for key, stamp := range m.last {
		aged = append(aged, agedKey{key: key, at: stamp.at})
	}
	sort.Slice(aged, func(i, j int) bool {
		if aged[i].at.Equal(aged[j].at) {
			return aged[i].key < aged[j].key
		}
		return aged[i].at.Before(aged[j].at)
	})
	for index := 0; index < len(aged)-subagentProgressMaxEntries; index++ {
		delete(m.last, aged[index].key)
	}
}

func stringPayloadText(payload map[string]interface{}, key string) string {
	if len(payload) == 0 {
		return ""
	}
	value, ok := payload[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return ""
	}
}

func numericPayloadValue(payload map[string]interface{}, key string) (float64, bool) {
	if len(payload) == 0 {
		return 0, false
	}
	value, ok := payload[key]
	if !ok || value == nil {
		return 0, false
	}
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	default:
		return 0, false
	}
}

func firstNonEmptyText(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

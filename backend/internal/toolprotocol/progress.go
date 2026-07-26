package toolprotocol

import (
	"context"
	"strings"
	"time"
)

// NotificationKind identifies a mid-execution tool notification.
type NotificationKind string

const (
	// NotificationProgress is incremental tool progress (percent/message/partial).
	NotificationProgress NotificationKind = "progress"
	// NotificationBackgroundComplete signals a detached job finished.
	NotificationBackgroundComplete NotificationKind = "background_complete"
	// NotificationQuestion requests user input mid-tool (rare; prefer ask_user_question).
	NotificationQuestion NotificationKind = "question"
	// NotificationPermission requests elevated permission mid-tool.
	NotificationPermission NotificationKind = "permission"
)

// EventTypeProgress is the runtime event bus type for tool progress.
// Consumers: agent event bus → API SSE / aicli chat bridge.
const EventTypeProgress = "tool.progress"

// Progress is a mid-execution progress notification wire object.
type Progress struct {
	ToolID    ToolID                 `json:"tool_id"`
	CallID    CallID                 `json:"call_id,omitempty"`
	SessionID string                 `json:"session_id,omitempty"`
	TraceID   string                 `json:"trace_id,omitempty"`
	Kind      NotificationKind       `json:"kind,omitempty"`
	Message   string                 `json:"message,omitempty"`
	// Percent is 0-100 when known; omit / negative means unknown.
	Percent  float64                `json:"percent,omitempty"`
	Partial  string                 `json:"partial,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
	// Timestamp is set by reporters when publishing; zero means unset.
	Timestamp time.Time `json:"timestamp,omitempty"`
}

// Normalize prepares Progress for emission (defaults + clamps).
func (p Progress) Normalize() Progress {
	out := p
	out.ToolID = ToolID(strings.TrimSpace(string(out.ToolID)))
	out.CallID = NormalizeCallID(string(out.CallID))
	out.SessionID = strings.TrimSpace(out.SessionID)
	out.TraceID = strings.TrimSpace(out.TraceID)
	out.Message = strings.TrimSpace(out.Message)
	out.Partial = strings.TrimSpace(out.Partial)
	if out.Kind == "" {
		out.Kind = NotificationProgress
	}
	if out.Percent < 0 {
		out.Percent = 0
	}
	if out.Percent > 100 {
		out.Percent = 100
	}
	return out
}

// Payload returns a map suitable for runtimeevents.Event.Payload.
func (p Progress) Payload() map[string]interface{} {
	n := p.Normalize()
	payload := map[string]interface{}{
		"tool_call_id": n.CallID.String(),
		"kind":         string(n.Kind),
	}
	if n.Message != "" {
		payload["message"] = n.Message
	}
	if n.Partial != "" {
		payload["partial"] = n.Partial
	}
	if n.Percent > 0 {
		payload["percent"] = n.Percent
	}
	if n.TraceID != "" {
		payload["trace_id"] = n.TraceID
	}
	if n.SessionID != "" {
		payload["session_id"] = n.SessionID
	}
	if !n.Timestamp.IsZero() {
		payload["timestamp"] = n.Timestamp.UTC().Format(time.RFC3339Nano)
	}
	for key, value := range n.Metadata {
		if strings.TrimSpace(key) == "" || value == nil {
			continue
		}
		if _, exists := payload[key]; exists {
			continue
		}
		payload[key] = value
	}
	return payload
}

// Reporter receives progress notifications during tool execution.
// Implementations must be safe for concurrent use from tool workers.
type Reporter interface {
	ReportProgress(progress Progress)
}

// ReporterFunc adapts a function to Reporter.
type ReporterFunc func(Progress)

// ReportProgress implements Reporter.
func (f ReporterFunc) ReportProgress(progress Progress) {
	if f != nil {
		f(progress)
	}
}

// NopReporter discards progress.
type NopReporter struct{}

// ReportProgress implements Reporter.
func (NopReporter) ReportProgress(Progress) {}

type progressContextKey struct{}

// WithReporter stores a progress Reporter on ctx for toolkit tools.
func WithReporter(ctx context.Context, reporter Reporter) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if reporter == nil {
		return ctx
	}
	return context.WithValue(ctx, progressContextKey{}, reporter)
}

// ReporterFromContext returns the Reporter bound to ctx, or NopReporter.
func ReporterFromContext(ctx context.Context) Reporter {
	if ctx == nil {
		return NopReporter{}
	}
	if reporter, ok := ctx.Value(progressContextKey{}).(Reporter); ok && reporter != nil {
		return reporter
	}
	return NopReporter{}
}

// Report is a convenience helper for tools to emit progress when a reporter exists.
func Report(ctx context.Context, progress Progress) {
	ReporterFromContext(ctx).ReportProgress(progress.Normalize())
}

package lsp

import "time"

// EventKind identifies one observable pool fact. Events are the L2
// observability seam (docs/lsp 02 §2.2): restart policy is an implementation
// detail, but its outcome must be visible without polling `lsp_servers`.
type EventKind string

const (
	// EventServerState fires when a member's lifecycle state or failure
	// reason changes: starting / ready / unavailable / crashed / stopped.
	EventServerState EventKind = "server.state"
	// EventDiagnostics fires when a member publishes diagnostics for a
	// document the client tracks.
	EventDiagnostics EventKind = "diagnostics"
	// EventRequest fires once per post-write request (inline append, report
	// tool). It carries the low-sensitivity scalars the observability plan
	// (docs/plan/lsp-observability-and-analysis-plan-20260929.md §3) turns
	// into fallback/latency/coverage readings; source text is never included.
	EventRequest EventKind = "request.finished"
)

// Event is one observable pool fact. Observers run inline on the emitting
// goroutine (the transport reader for diagnostics, the caller for state
// transitions), so they must never block.
type Event struct {
	Kind   EventKind    `json:"kind"`
	Time   time.Time    `json:"time"`
	Server string       `json:"server,omitempty"`
	Status ServerStatus `json:"status,omitempty"`
	Path   string       `json:"path,omitempty"`
	Count  int          `json:"count,omitempty"`
	// SessionID 是事件归属的会话（Bridge 从工具执行 ctx 解析；生命周期事件
	// 没有执行上下文，保持为空）。
	SessionID string `json:"session_id,omitempty"`
	// Request 级字段（Kind == EventRequest）。
	Trigger        string `json:"trigger,omitempty"`
	Outcome        string `json:"outcome,omitempty"`
	DurationMS     int64  `json:"duration_ms,omitempty"`
	DiagCount      int    `json:"diag_count,omitempty"`
	AppendedBytes  int    `json:"appended_bytes,omitempty"`
	OmittedItems   int    `json:"omitted_items,omitempty"`
	OmittedByChars int    `json:"omitted_by_chars,omitempty"`
}

// Observer receives pool events. A nil Observer disables delivery.
type Observer func(Event)

// composeObservers runs both observers in order; either may be nil.
func composeObservers(first, second Observer) Observer {
	return func(event Event) {
		if first != nil {
			first(event)
		}
		if second != nil {
			second(event)
		}
	}
}

// logObserver renders pool events into the injected logger. It is the default
// observer, so lifecycle facts stay visible in the session log even when no
// host consumes the Observer seam.
func logObserver(logger Logger) Observer {
	logger = LoggerOrNop(logger)
	return func(event Event) {
		switch event.Kind {
		case EventServerState:
			status := event.Status
			switch status.State {
			case StateStarting:
				logger.Debugf("lsp: %s starting (%s)", status.Name, status.Command)
			case StateReady:
				logger.Infof("lsp: %s ready (pid=%d encoding=%s)", status.Name, status.PID, status.Encoding)
			case StateCrashed:
				logger.Warnf("lsp: %s crashed: %s", status.Name, firstNonEmpty(status.Reason, status.LastError))
			case StateUnavailable:
				logger.Warnf("lsp: %s unavailable: %s", status.Name, firstNonEmpty(status.Reason, status.LastError))
			case StateStopped:
				logger.Debugf("lsp: %s stopped", status.Name)
			}
		case EventDiagnostics:
			logger.Debugf("lsp: %s published %d diagnostic(s) for %s", event.Server, event.Count, event.Path)
		case EventRequest:
			logger.Debugf(
				"lsp: %s request %s outcome=%s duration_ms=%d diagnostics=%d appended_bytes=%d",
				firstNonEmpty(event.Trigger, "request"), event.Path, event.Outcome,
				event.DurationMS, event.DiagCount, event.AppendedBytes,
			)
		}
	}
}

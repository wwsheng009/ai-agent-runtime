// Package audit records gateway security events: an in-memory ring buffer plus
// an optional append-only JSONL file. It never stores secrets or session
// content (docs/gateway/04-security.md §5).
package audit

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// Event types.
const (
	TypeLoginSuccess       = "login_success"
	TypeLoginFailure       = "login_failure"
	TypeDeviceCreated      = "device_created"
	TypeDeviceRevoked      = "device_revoked"
	TypeManualBan          = "manual_ban"
	TypeManualUnban        = "manual_unban"
	TypeRateLimited        = "rate_limited"
	TypeTunnelConnected    = "tunnel_connected"
	TypeTunnelDisconnected = "tunnel_disconnected"
)

// Event is one audit record.
type Event struct {
	Time   time.Time `json:"time"`
	Type   string    `json:"type"`
	Actor  string    `json:"actor,omitempty"`
	Result string    `json:"result,omitempty"`
	IP     string    `json:"ip,omitempty"`
	Detail string    `json:"detail,omitempty"`
}

// Log is a bounded in-memory audit log with optional JSONL persistence.
type Log struct {
	mu     sync.Mutex
	max    int
	path   string
	now    func() time.Time
	events []Event
}

// New creates a log keeping at most max recent events; path "" disables the
// file (tests).
func New(max int, path string) *Log {
	if max <= 0 {
		max = 1000
	}
	return &Log{max: max, path: path, now: time.Now}
}

// Record appends an event (filling Time when zero) and trims the buffer.
func (l *Log) Record(event Event) {
	if l == nil {
		return
	}
	if event.Time.IsZero() {
		event.Time = l.now().UTC()
	}
	l.mu.Lock()
	l.events = append(l.events, event)
	if len(l.events) > l.max {
		l.events = append([]Event(nil), l.events[len(l.events)-l.max:]...)
	}
	path := l.path
	l.mu.Unlock()

	if path != "" {
		if data, err := json.Marshal(event); err == nil {
			if file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
				_, _ = file.Write(append(data, '\n'))
				_ = file.Close()
			}
		}
	}
}

// Entries returns the most recent events, newest first. limit <= 0 returns all.
func (l *Log) Entries(limit int) []Event {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	count := len(l.events)
	if limit <= 0 || limit > count {
		limit = count
	}
	out := make([]Event, 0, limit)
	for i := count - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, l.events[i])
	}
	return out
}

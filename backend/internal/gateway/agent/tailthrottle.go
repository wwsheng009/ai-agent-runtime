package agent

import (
	"strings"
	"sync"
	"time"
)

// DefaultTailPushInterval is the minimum spacing between live-output tail
// pushes for one session (the console renders at human speed; faster is
// waste and mobile traffic).
const DefaultTailPushInterval = time.Second

// TailThrottle rate-limits live-output tail pushes per session: at most one
// push per interval, and never for an unchanged value. Hosts call Allow()
// before pushing; keeping the cadence + duplicate suppression here means
// aicli and runtime-server cannot drift on either.
type TailThrottle struct {
	mu       sync.Mutex
	last     map[string]tailSample
	interval time.Duration
}

type tailSample struct {
	tail string
	at   time.Time
}

// NewTailThrottle creates a throttle; interval <= 0 falls back to
// DefaultTailPushInterval.
func NewTailThrottle(interval time.Duration) *TailThrottle {
	if interval <= 0 {
		interval = DefaultTailPushInterval
	}
	return &TailThrottle{last: make(map[string]tailSample), interval: interval}
}

// Allow reports whether the tail should be pushed now, recording the sample.
// The tail is truncated with the shared bound before comparison so two equal
// long outputs cannot push twice on an irrelevant suffix difference.
func (t *TailThrottle) Allow(sessionID, tail string, now time.Time) bool {
	if t == nil {
		return false
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false
	}
	tail = TruncateSessionTail(tail)
	t.mu.Lock()
	defer t.mu.Unlock()
	if prev, ok := t.last[sessionID]; ok {
		if prev.tail == tail {
			return false
		}
		if now.Sub(prev.at) < t.interval {
			return false
		}
	}
	t.last[sessionID] = tailSample{tail: tail, at: now}
	return true
}

// Forget drops a session's throttle state (session ended / removed).
func (t *TailThrottle) Forget(sessionID string) {
	if t == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	t.mu.Lock()
	delete(t.last, sessionID)
	t.mu.Unlock()
}

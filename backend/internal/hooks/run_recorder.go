package hooks

import (
	"sync"
	"time"
)

// RunRecord is one synchronous hook lifecycle observation for host
// diagnostics (Settings → Harness → Hooks).
type RunRecord struct {
	HookID        string    `json:"hook_id"`
	Event         string    `json:"event"`
	Phase         string    `json:"phase"`
	StatusMessage string    `json:"status_message,omitempty"`
	Source        string    `json:"source,omitempty"`
	At            time.Time `json:"at"`
}

// RunRecorder is a bounded in-memory ring of recent hook runs. It is
// process-local and intentionally not durable.
type RunRecorder struct {
	mu      sync.RWMutex
	records []RunRecord
	limit   int
}

// NewRunRecorder creates a recorder with a bounded capacity.
func NewRunRecorder(limit int) *RunRecorder {
	if limit <= 0 {
		limit = 100
	}
	return &RunRecorder{limit: limit}
}

// Record appends one observation, dropping the oldest when full.
func (r *RunRecorder) Record(record RunRecord) {
	if r == nil {
		return
	}
	if record.At.IsZero() {
		record.At = time.Now().UTC()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, record)
	if len(r.records) > r.limit {
		r.records = append([]RunRecord(nil), r.records[len(r.records)-r.limit:]...)
	}
}

// Recent returns a copy of the newest records, oldest first.
func (r *RunRecorder) Recent() []RunRecord {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]RunRecord(nil), r.records...)
}

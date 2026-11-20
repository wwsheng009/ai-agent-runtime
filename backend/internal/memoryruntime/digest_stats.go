package memoryruntime

import (
	"strings"
	"time"
)

// DigestStats is the bounded runtime view of the turn outbox write path. It is
// intentionally process-local: it answers "did this host actually enqueue
// digests?" rather than "what is the store's historical total?".
type DigestStats struct {
	Written        int64     `json:"written"`
	Skipped        int64     `json:"skipped"`
	Failed         int64     `json:"failed"`
	LastWrittenAt  time.Time `json:"last_written_at,omitempty"`
	LastSkippedAt  time.Time `json:"last_skipped_at,omitempty"`
	LastSkipReason string    `json:"last_skip_reason,omitempty"`
	LastError      string    `json:"last_error,omitempty"`
	LastErrorAt    time.Time `json:"last_error_at,omitempty"`
}

func (r *Runtime) DigestStats() DigestStats {
	if r == nil {
		return DigestStats{}
	}
	r.digestStatsMu.Lock()
	defer r.digestStatsMu.Unlock()
	return r.digestStats
}

func (r *Runtime) recordDigestWritten() {
	if r == nil {
		return
	}
	r.digestStatsMu.Lock()
	defer r.digestStatsMu.Unlock()
	r.digestStats.Written++
	r.digestStats.LastWrittenAt = r.now()
}

func (r *Runtime) recordDigestSkipped(reason string) {
	if r == nil {
		return
	}
	r.digestStatsMu.Lock()
	defer r.digestStatsMu.Unlock()
	r.digestStats.Skipped++
	r.digestStats.LastSkippedAt = r.now()
	r.digestStats.LastSkipReason = strings.TrimSpace(reason)
}

func (r *Runtime) recordDigestFailed(err error) {
	if r == nil || err == nil {
		return
	}
	message := strings.TrimSpace(err.Error())
	if runes := []rune(message); len(runes) > 512 {
		message = string(runes[:512]) + "…"
	}
	r.digestStatsMu.Lock()
	defer r.digestStatsMu.Unlock()
	r.digestStats.Failed++
	r.digestStats.LastError = message
	r.digestStats.LastErrorAt = r.now()
}

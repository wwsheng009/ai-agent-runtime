package toolbroker

import (
	"strconv"
	"strings"
	"sync"
)

// agentEventsReadMemoLimit bounds how many read windows one broker remembers.
// The memo answers a single question — "did this caller just read the exact same
// window of this target?" — so a small FIFO is enough and a long-running host
// cannot grow it without bound.
const agentEventsReadMemoLimit = 128

// agentEventsReadKey identifies one read window. The caller is part of the key so
// two parents (or a parent and a teammate) polling the same child never inherit
// each other's repeat counts.
type agentEventsReadKey struct {
	callerSessionID string
	targetSessionID string
	view            string
	afterSeq        int64
	limit           int
}

func (k agentEventsReadKey) String() string {
	return strings.Join([]string{
		k.callerSessionID,
		k.targetSessionID,
		k.view,
		strconv.FormatInt(k.afterSeq, 10),
		strconv.Itoa(k.limit),
	}, "\x00")
}

type agentEventsReadRecord struct {
	latestSeq int64
	repeats   int
}

// agentEventsReadMemo remembers the last read window per caller/target cursor so
// a repeated read_agent_events with an unchanged after_seq can be answered with
// an explicit unchanged/repeat_count signal instead of a silently identical
// payload (plan P1-7 待补).
type agentEventsReadMemo struct {
	mu      sync.Mutex
	limit   int
	records map[string]agentEventsReadRecord
	order   []string
}

func newAgentEventsReadMemo(limit int) *agentEventsReadMemo {
	if limit <= 0 {
		limit = agentEventsReadMemoLimit
	}
	return &agentEventsReadMemo{limit: limit, records: make(map[string]agentEventsReadRecord, limit)}
}

// observe records latestSeq for key and reports whether this read repeated the
// previous window (same high-water mark), together with the consecutive repeat
// count. The count is 1 for the first repeat, so a caller can tell "same answer
// again" apart from "first answer". A window that advanced resets the streak.
func (m *agentEventsReadMemo) observe(key agentEventsReadKey, latestSeq int64) (int, bool) {
	if m == nil {
		return 0, false
	}
	encoded := key.String()
	m.mu.Lock()
	defer m.mu.Unlock()
	record, seen := m.records[encoded]
	repeats := 0
	unchanged := false
	if seen {
		if record.latestSeq == latestSeq {
			repeats = record.repeats + 1
			unchanged = true
		}
	} else {
		m.order = append(m.order, encoded)
		if len(m.order) > m.limit {
			evicted := m.order[0]
			m.order = m.order[1:]
			delete(m.records, evicted)
		}
	}
	m.records[encoded] = agentEventsReadRecord{latestSeq: latestSeq, repeats: repeats}
	if !unchanged {
		return 0, false
	}
	return repeats, true
}

// forgetTarget drops every remembered window for one target session. Hosts call
// it after close_agent so a later run reusing the same id cannot inherit a stale
// repeat streak; other targets keep their own streaks.
func (m *agentEventsReadMemo) forgetTarget(targetSessionID string) {
	if m == nil || strings.TrimSpace(targetSessionID) == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.order[:0]
	for _, encoded := range m.order {
		parts := strings.SplitN(encoded, "\x00", 3)
		if len(parts) > 1 && parts[1] == targetSessionID {
			delete(m.records, encoded)
			continue
		}
		kept = append(kept, encoded)
	}
	m.order = kept
}

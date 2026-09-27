package tools

import (
	"sync/atomic"
	"time"
)

// Per-session read state (the read ledger and the view dedup cache) lives in
// two sync.Maps. A long-lived process would otherwise accumulate one bucket per
// session forever, so state is dropped two ways (review m8):
//
//   - ForgetSessionReadState: an explicit hook for session lifecycle code;
//   - an amortized idle sweep: every N accesses one pass removes buckets that
//     have not been touched for sessionReadStateIdleTTL.
const (
	// sessionReadStateIdleTTL is how long an untouched session bucket survives.
	sessionReadStateIdleTTL = 30 * time.Minute
	// sessionReadStateSweepInterval amortizes the sweep cost: one pass per N
	// state accesses instead of a scan on every call.
	sessionReadStateSweepInterval = 256
)

var sessionReadStateSweepCounter atomic.Uint64

// maybeSweepSessionReadState runs the idle sweep once per interval.
func maybeSweepSessionReadState() {
	if sessionReadStateSweepCounter.Add(1)%sessionReadStateSweepInterval != 0 {
		return
	}
	sweepIdleSessionReadState(time.Now(), sessionReadStateIdleTTL)
}

// sweepIdleSessionReadState drops session buckets untouched for longer than ttl
// and reports how many were removed. It is separated from the amortized caller
// so tests can drive it deterministically.
func sweepIdleSessionReadState(now time.Time, ttl time.Duration) int {
	removed := 0
	sessionReadLedgers.Range(func(key, value interface{}) bool {
		if ledger, ok := value.(*sessionReadLedger); ok && ledger.idleSince(now, ttl) {
			sessionReadLedgers.Delete(key)
			removed++
		}
		return true
	})
	sessionViewDedups.Range(func(key, value interface{}) bool {
		if state, ok := value.(*sessionViewDedup); ok && state.idleSince(now, ttl) {
			sessionViewDedups.Delete(key)
			removed++
		}
		return true
	})
	return removed
}

// ForgetSessionReadState drops both per-session buckets for sessionID. Session
// lifecycle code should call it when a session ends; an empty id is ignored so
// the "no session" path (which records nothing) stays a no-op.
func ForgetSessionReadState(sessionID string) {
	if sessionID == "" {
		return
	}
	sessionReadLedgers.Delete(sessionID)
	sessionViewDedups.Delete(sessionID)
}

package subagentbatch

import (
	"strings"
	"time"
)

// TurnSuspension is the §6.12 parked-turn record: the durable evidence that a
// parent turn handed work to background obligations and parked itself instead
// of blocking. The design names exactly these fields
// {turn_id, session_id, obligation_ids, parked_at, decision_window_until,
// resume_queue}; runtime-only bookkeeping (root scope for the wake plane) is
// carried alongside so a restart can re-enter the same supervision scope.
//
// The record is only meaningful on a durable store: on an in-memory store it
// dies with the process, which is why callers must gate on BatchStore.IsDurable
// before writing it (design §6.13 I9).
type TurnSuspension struct {
	// TurnID identifies the parked parent turn.
	TurnID string
	// SessionID is the parent session that owns the turn.
	SessionID string
	// RootScopeID groups every turn of one supervision scope (normally the
	// session id; kept explicit so a restart re-enters the same wake scope).
	RootScopeID string
	// ObligationIDs are the obligations dispatched before parking (batch task
	// ids and, once the obligation ledger lands, its ids too).
	ObligationIDs []string
	// ParkedAt is when the turn parked.
	ParkedAt time.Time
	// DecisionWindowUntil is the escalate-first decision deadline for the
	// parked obligations; zero means "no window recorded".
	DecisionWindowUntil time.Time
	// ResumeQueue holds the wake keys that will drive the resume; ordering is
	// the delivery order and duplicates are preserved.
	ResumeQueue []string
}

// Clone returns a deep copy so callers cannot mutate stored slices.
func (t *TurnSuspension) Clone() *TurnSuspension {
	if t == nil {
		return nil
	}
	out := *t
	out.ObligationIDs = append([]string(nil), t.ObligationIDs...)
	out.ResumeQueue = append([]string(nil), t.ResumeQueue...)
	return &out
}

// normalize trims identifiers and drops empty obligation/queue entries so the
// primary key and the JSON payload cannot disagree on "same record".
func (t *TurnSuspension) normalize() {
	if t == nil {
		return
	}
	t.TurnID = strings.TrimSpace(t.TurnID)
	t.SessionID = strings.TrimSpace(t.SessionID)
	t.RootScopeID = strings.TrimSpace(t.RootScopeID)
	if t.RootScopeID == "" {
		t.RootScopeID = t.SessionID
	}
	t.ObligationIDs = normalizeIDList(t.ObligationIDs)
	t.ResumeQueue = normalizeIDList(t.ResumeQueue)
}

func normalizeIDList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

package subagentbatch

import (
	"context"
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

// TurnObligationsSettled reports whether every obligation referenced by the
// parked-turn record reached a terminal state, i.e. whether the parked turn may
// end (design §6.12 / EC-E1 "turn 永不结束"). The batch control plane is the
// record's own source of truth — the dispatcher writes the batch/task ids from
// the same store — so the settle check reads it back instead of trusting a
// caller-supplied summary.
//
// Only *resolved* batches count as evidence: a batch id with no row (never
// created, or already GC'd) cannot prove the work finished and is skipped. A
// record whose batches are all unresolved is reported as not settled, so the
// suspension is kept and re-evaluated at the next turn boundary. The safe
// direction is keeping the turn parked (obligations are never silently dropped),
// never clearing the record on a guess.
func TurnObligationsSettled(ctx context.Context, store BatchStore, record *TurnSuspension) (bool, error) {
	if store == nil || record == nil {
		return false, nil
	}
	batchIDs := record.obligationBatchIDs()
	if len(batchIDs) == 0 {
		return false, nil
	}
	resolved := 0
	for _, batchID := range batchIDs {
		batch, err := store.GetBatch(ctx, batchID)
		if err != nil {
			return false, err
		}
		if batch == nil {
			continue
		}
		resolved++
		if !batch.Status.Terminal() {
			return false, nil
		}
	}
	return resolved > 0, nil
}

// obligationBatchIDs returns the batch ids that gate this parked turn.
// ResumeQueue carries the batch wake keys written by the dispatcher
// (parkBackgroundTurn writes exactly [batch_id]); ObligationIDs[0] is the same
// batch id and only serves as a fallback for records written without a queue.
// ObligationBatchIDs exposes the same list to the abandon executor (EC-E7 /
// EC-C5): abandoning a parked turn must cascade-cancel exactly the obligations
// the settle check reads, so both paths share one resolution rule.
func (t *TurnSuspension) ObligationBatchIDs() []string {
	if t == nil {
		return nil
	}
	if ids := normalizeIDList(t.ResumeQueue); len(ids) > 0 {
		return ids
	}
	obligations := normalizeIDList(t.ObligationIDs)
	if len(obligations) == 0 {
		return nil
	}
	return obligations[:1]
}

func (t *TurnSuspension) obligationBatchIDs() []string {
	return t.ObligationBatchIDs()
}

package chat

import (
	"context"
	"strings"
	"time"
)

const (
	// finalizationSequenceAttempts bounds how many times the finalization gate
	// retries a moving durable event sequence. A short quiet window catches
	// tail writes that were already in flight when the agent loop returned.
	finalizationSequenceAttempts = 3
	finalizationSequenceWindow   = 2 * time.Millisecond
)

// finalizationSequenceGate is the evidence collected immediately before a
// parent turn is allowed to transition to finalized. It intentionally couples
// the event high-water mark with a fresh suspension read: sequence movement
// alone is not proof of a required obligation, but it invalidates the previous
// "no suspension" observation and forces a re-read.
type finalizationSequenceGate struct {
	SuspendedTurnID    string
	SuspendedEpisodeID string

	// SequenceAvailable is false when the configured event store does not
	// expose EventSequenceStore. Legacy/custom stores keep the pre-gate
	// behavior instead of silently blocking every turn.
	SequenceAvailable bool
	SequenceChecked   bool
	SequenceStable    bool
	SequenceBefore    int64
	SequenceAfter     int64
	SequenceAttempts  int
	SequenceError     string
}

// evaluateFinalizationSequenceGate is the durable tail check for finalization.
//
// Contract:
//   - the sequence high-water mark is snapshotted before obligations are read;
//   - a quiet window is observed, then the sequence is read again;
//   - if the sequence moved, the suspension ledger is re-read (the previous
//     answer may have gone stale while the event was being persisted);
//   - a newly materialized suspension wins immediately and keeps the turn open;
//   - stores without EventSequenceStore are treated as "check unavailable",
//     not as "unstable".
//
// The check is deliberately bounded. A continuously moving sequence with no
// materialized required obligation (for example unrelated mailbox traffic) is
// reported as unstable so the caller can emit audit evidence, but it does not
// wedge the turn forever. The final suspension re-read remains the authority
// for parking.
func (a *SessionActor) evaluateFinalizationSequenceGate(ctx context.Context, turnID, fallbackEpisode string) finalizationSequenceGate {
	gate := finalizationSequenceGate{SequenceStable: true}
	if ctx == nil {
		ctx = context.Background()
	}

	reader, ok := a.eventSequenceStore()
	if !ok {
		gate.SuspendedTurnID, gate.SuspendedEpisodeID = a.syncSuspendedTurnState(ctx, turnID, fallbackEpisode)
		return gate
	}

	gate.SequenceAvailable = true
	before, err := reader.LastEventSeq(ctx, a.id)
	if err != nil {
		gate.SequenceChecked = true
		gate.SequenceStable = false
		gate.SequenceError = err.Error()
		gate.SuspendedTurnID, gate.SuspendedEpisodeID = a.syncSuspendedTurnState(ctx, turnID, fallbackEpisode)
		return gate
	}
	gate.SequenceChecked = true
	gate.SequenceBefore = before
	gate.SequenceAfter = before

	for attempt := 1; attempt <= finalizationSequenceAttempts; attempt++ {
		gate.SequenceAttempts = attempt
		gate.SuspendedTurnID, gate.SuspendedEpisodeID = a.syncSuspendedTurnState(ctx, turnID, fallbackEpisode)
		if strings.TrimSpace(gate.SuspendedTurnID) != "" {
			// The turn is parked regardless of sequence movement; the caller
			// must not finalize it.
			gate.SequenceStable = true
			return gate
		}
		if !waitFinalizationSequenceWindow(ctx) {
			gate.SequenceStable = false
			gate.SequenceError = ctx.Err().Error()
			return gate
		}
		after, err := reader.LastEventSeq(ctx, a.id)
		if err != nil {
			gate.SequenceStable = false
			gate.SequenceError = err.Error()
			break
		}
		gate.SequenceAfter = after
		if after == before {
			// The event sequence can be quiet while a suspension row is being
			// written (the batch ledger is a separate durable plane). Re-read
			// obligations after the final sequence snapshot so a suspension that
			// landed inside the quiet window still parks the turn.
			gate.SuspendedTurnID, gate.SuspendedEpisodeID = a.syncSuspendedTurnState(ctx, turnID, fallbackEpisode)
			return gate
		}
		before = after
	}

	// The sequence kept moving through the bounded window. Re-read the durable
	// suspension ledger one last time: a new required obligation can materialize
	// at the tail of the sequence and must still park the turn.
	gate.SequenceStable = false
	gate.SuspendedTurnID, gate.SuspendedEpisodeID = a.syncSuspendedTurnState(ctx, turnID, fallbackEpisode)
	return gate
}

// eventSequenceStore resolves the optional high-water-mark capability without
// widening the base EventStore contract used by custom/test stores.
func (a *SessionActor) eventSequenceStore() (EventSequenceStore, bool) {
	if a == nil || a.eventStore == nil {
		return nil, false
	}
	reader, ok := a.eventStore.(EventSequenceStore)
	if !ok || reader == nil {
		return nil, false
	}
	return reader, true
}

func waitFinalizationSequenceWindow(ctx context.Context) bool {
	timer := time.NewTimer(finalizationSequenceWindow)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

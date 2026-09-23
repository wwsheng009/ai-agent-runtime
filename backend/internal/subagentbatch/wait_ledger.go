package subagentbatch

import (
	"context"
	"time"
)

// WaitLedgerRow is one obligation row of the wait-time ledger view (plan §C3-4
// 统一返回契约 obligations[]). Rows are read back from the same durable plane
// the settle check trusts — the §6.12 parked-turn record plus the batch store —
// so wait_agent / wait_team can never report a ledger the control plane
// disagrees with, and no new storage is introduced.
type WaitLedgerRow struct {
	// ObligationID identifies the obligation (today: the batch id).
	ObligationID string
	// SubjectKind is the kind of subject this obligation gates (today: "batch").
	SubjectKind string
	// SubjectID is the subject's own id (today: the batch id).
	SubjectID string
	// State is the durable subject state; WaitLedgerStateMissing means the
	// ledger references an obligation the store has no row for.
	State string
	// Terminal reports whether the obligation can no longer transition.
	Terminal bool
	// DeadlineAt is the obligation's declared deadline, zero when none is
	// recorded (I2 requires every dispatch to carry one).
	DeadlineAt time.Time
}

// WaitLedgerStateMissing marks a ledger row whose obligation has no durable row
// (never created, or already GC'd). It is deliberately not terminal: a vanished
// obligation cannot prove the work finished, so the safe direction is keeping
// the parent turn parked — the same rule TurnObligationsSettled applies.
const WaitLedgerStateMissing = "missing"

// BuildWaitLedger renders the obligation ledger view for one parked turn. A nil
// store, a nil record, or a record without obligations yields no rows: the
// caller reports an empty ledger and must return immediately instead of blocking
// on it (AC-P2-4e). Read errors are surfaced so a wait never silently claims a
// ledger it could not read.
func BuildWaitLedger(ctx context.Context, store BatchStore, record *TurnSuspension) ([]WaitLedgerRow, error) {
	if store == nil || record == nil {
		return nil, nil
	}
	batchIDs := record.ObligationBatchIDs()
	if len(batchIDs) == 0 {
		return nil, nil
	}
	rows := make([]WaitLedgerRow, 0, len(batchIDs))
	for _, batchID := range batchIDs {
		batch, err := store.GetBatch(ctx, batchID)
		if err != nil {
			return nil, err
		}
		row := WaitLedgerRow{
			ObligationID: batchID,
			SubjectKind:  "batch",
			SubjectID:    batchID,
			State:        WaitLedgerStateMissing,
			DeadlineAt:   record.DecisionWindowUntil,
		}
		if batch != nil {
			row.State = string(batch.Status)
			row.Terminal = batch.Status.Terminal()
			if !batch.BatchDeadline.IsZero() {
				row.DeadlineAt = batch.BatchDeadline
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

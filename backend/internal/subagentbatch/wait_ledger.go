package subagentbatch

import (
	"context"
	"time"
)

// WaitLedgerRow is one obligation row of the wait-time ledger view (plan §C3-4
// 统一返回契约 obligations[]). Rows are read back from the same durable plane
// the settle check trusts — the §6.12 parked-turn record plus the batch store —
// so subagent_wait / team_wait can never report a ledger the control plane
// disagrees with, and no new storage is introduced.
type WaitLedgerRow struct {
	// ObligationID identifies the obligation (batch id, or a prefixed
	// agent_session:/team:/job: id).
	ObligationID string
	// SubjectKind is the kind of subject this obligation gates.
	SubjectKind string
	// SubjectID is the subject's own id (without an obligation prefix).
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
//
// This is the batch-only form; it delegates to BuildWaitLedgerWith with a nil
// child-session resolver (agent-session rows are then reported as missing, which
// keeps them pending — the conservative direction).
func BuildWaitLedger(ctx context.Context, store BatchStore, record *TurnSuspension) ([]WaitLedgerRow, error) {
	return BuildWaitLedgerWith(ctx, store, record, nil)
}

// BuildWaitLedgerWith renders the obligation ledger including child agent
// sessions parked by subagent_spawn (obligation ids carrying
// AgentSessionObligationPrefix). Batch rows keep the original order; agent
// session rows follow in record order. A missing durable row stays
// WaitLedgerStateMissing and non-terminal (never evidence of completion), and a
// nil resolver reports every agent-session row the same way.
func BuildWaitLedgerWith(
	ctx context.Context,
	store BatchStore,
	record *TurnSuspension,
	resolver AgentSessionObligationResolver,
) ([]WaitLedgerRow, error) {
	return BuildWaitLedgerWithResolvers(ctx, store, record, ObligationResolvers{AgentSessions: resolver})
}

// BuildWaitLedgerWithResolvers renders the obligation ledger including child
// agent sessions, team runs and required background jobs. Batch rows keep the
// original order; non-batch rows follow in record order. A missing durable row
// stays WaitLedgerStateMissing and non-terminal (never evidence of completion),
// and an unwired resolver reports that subject kind the same way.
func BuildWaitLedgerWithResolvers(
	ctx context.Context,
	store BatchStore,
	record *TurnSuspension,
	resolvers ObligationResolvers,
) ([]WaitLedgerRow, error) {
	if store == nil || record == nil {
		return nil, nil
	}
	batchIDs := record.ObligationBatchIDs()
	agentSessionIDs := record.ObligationAgentSessionIDs()
	teamIDs := record.ObligationTeamIDs()
	jobIDs := record.ObligationBackgroundJobIDs()
	if len(batchIDs) == 0 && len(agentSessionIDs) == 0 && len(teamIDs) == 0 && len(jobIDs) == 0 {
		return nil, nil
	}
	rows := make([]WaitLedgerRow, 0, len(batchIDs)+len(agentSessionIDs)+len(teamIDs)+len(jobIDs))
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
	for _, sessionID := range agentSessionIDs {
		row := WaitLedgerRow{
			ObligationID: AgentSessionObligationID(sessionID),
			SubjectKind:  "agent_session",
			SubjectID:    sessionID,
			State:        WaitLedgerStateMissing,
			DeadlineAt:   record.DecisionWindowUntil,
		}
		if resolvers.AgentSessions != nil {
			terminal, found, err := resolvers.AgentSessions.AgentSessionTerminal(ctx, sessionID)
			if err != nil {
				return nil, err
			}
			if found {
				row.Terminal = terminal
				if terminal {
					row.State = "closed"
				} else {
					row.State = "active"
				}
			}
		}
		rows = append(rows, row)
	}
	for _, teamID := range teamIDs {
		row := WaitLedgerRow{
			ObligationID: TeamObligationID(teamID),
			SubjectKind:  "team",
			SubjectID:    teamID,
			State:        WaitLedgerStateMissing,
			DeadlineAt:   record.DecisionWindowUntil,
		}
		if resolvers.Teams != nil {
			terminal, found, err := resolvers.Teams.TeamTerminal(ctx, teamID)
			if err != nil {
				return nil, err
			}
			if found {
				row.Terminal = terminal
				if terminal {
					row.State = "closed"
				} else {
					row.State = "active"
				}
			}
		}
		rows = append(rows, row)
	}
	for _, jobID := range jobIDs {
		row := WaitLedgerRow{
			ObligationID: BackgroundJobObligationID(jobID),
			SubjectKind:  "background_job",
			SubjectID:    jobID,
			State:        WaitLedgerStateMissing,
			DeadlineAt:   record.DecisionWindowUntil,
		}
		if resolvers.BackgroundJobs != nil {
			terminal, found, err := resolvers.BackgroundJobs.BackgroundJobTerminal(ctx, jobID)
			if err != nil {
				return nil, err
			}
			if found {
				row.Terminal = terminal
				if terminal {
					row.State = "completed"
				} else {
					row.State = "running"
				}
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

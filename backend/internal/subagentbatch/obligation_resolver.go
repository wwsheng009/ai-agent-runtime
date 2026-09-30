package subagentbatch

import "context"

// AgentSessionObligationResolver answers the durable control-plane state of a
// child agent session referenced by an agent_session: obligation. Lightweight
// spawn_agent children live in the agent registry (not in the batch store), so
// the settle/ledger predicates must ask the host for their state instead of
// assuming the batch table is the whole obligation universe.
//
// found=false means the control plane has no durable row for the id (never
// created, or already GC'd). The predicates treat it exactly like a missing
// batch row: skipped, and never accepted as evidence of completion — clearing a
// parked turn on a vanished obligation would drop work the parent still owes.
type AgentSessionObligationResolver interface {
	AgentSessionTerminal(ctx context.Context, sessionID string) (terminal bool, found bool, err error)
}

// TurnObligationsSettledWith is §6.12 settle predicate extended to child-session
// obligations. It preserves the batch-only rules (a resolved terminal batch is
// required; vanished batches are skipped; at least one resolved obligation must
// exist) and adds: every listed child session must be terminal, and a
// child-session id that the resolver cannot judge — or that has no durable
// lifecycle row at all — keeps the turn parked (a nil resolver with
// agent-session obligations therefore never settles).
//
// The missing-row rule is strict on purpose (matching TurnObligationsAllTerminal):
// a lightweight child has no lifecycle row while it runs, so skipping it let the
// first finished child of a cohort settle the turn while its siblings were still
// working (真机 2026-09-30：挂起记录在最后一个子代理完成前被清掉).
func TurnObligationsSettledWith(
	ctx context.Context,
	store BatchStore,
	record *TurnSuspension,
	resolver AgentSessionObligationResolver,
) (bool, error) {
	if record == nil {
		return false, nil
	}
	batchIDs := record.ObligationBatchIDs()
	agentSessionIDs := record.ObligationAgentSessionIDs()
	if len(batchIDs) == 0 && len(agentSessionIDs) == 0 {
		return false, nil
	}

	resolved := 0
	if len(batchIDs) > 0 {
		if store == nil {
			return false, nil
		}
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
	}
	for _, sessionID := range agentSessionIDs {
		if resolver == nil {
			// The child-session control plane is not wired; keep the turn
			// parked instead of guessing that the child is done.
			return false, nil
		}
		terminal, found, err := resolver.AgentSessionTerminal(ctx, sessionID)
		if err != nil {
			return false, err
		}
		if !found {
			// No durable lifecycle row yet: the child may simply still be
			// running. Never accept that as evidence of completion.
			return false, nil
		}
		resolved++
		if !terminal {
			return false, nil
		}
	}
	return resolved > 0, nil
}

// TurnObligationsAllTerminal is the strict sibling of
// TurnObligationsSettledWith for callers that are about to *start* the parent
// turn again (the settlement wake). TurnObligationsSettledWith skips ids the
// control plane has no row for — the batch-GC rule: a vanished batch is not
// evidence that work is still owed. A spawn_agent child is different: while it
// runs it has no lifecycle row at all, so a missing child row must block the
// verdict instead of being skipped. Otherwise the first finished child of a
// cohort would settle the turn while its siblings are still running, and the
// wake would resume the parent in the middle of its own work.
//
// A batch obligation keeps the strict reading too: an unreadable/missing batch
// is "not known terminal" here, because this predicate gates a resume.
func TurnObligationsAllTerminal(
	ctx context.Context,
	store BatchStore,
	record *TurnSuspension,
	resolver AgentSessionObligationResolver,
) (bool, error) {
	if record == nil {
		return false, nil
	}
	batchIDs := record.ObligationBatchIDs()
	agentSessionIDs := record.ObligationAgentSessionIDs()
	if len(batchIDs) == 0 && len(agentSessionIDs) == 0 {
		return false, nil
	}

	known := 0
	for _, batchID := range batchIDs {
		if store == nil {
			return false, nil
		}
		batch, err := store.GetBatch(ctx, batchID)
		if err != nil {
			return false, err
		}
		if batch == nil {
			return false, nil
		}
		known++
		if !batch.Status.Terminal() {
			return false, nil
		}
	}
	for _, sessionID := range agentSessionIDs {
		if resolver == nil {
			return false, nil
		}
		terminal, found, err := resolver.AgentSessionTerminal(ctx, sessionID)
		if err != nil {
			return false, err
		}
		if !found {
			// No durable lifecycle row: the child may simply still be running.
			return false, nil
		}
		known++
		if !terminal {
			return false, nil
		}
	}
	return known > 0, nil
}

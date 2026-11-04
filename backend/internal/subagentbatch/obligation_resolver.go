package subagentbatch

import "context"

// AgentSessionObligationResolver answers the durable control-plane state of a
// child agent session referenced by an agent_session: obligation. Lightweight
// subagent_spawn children live in the agent registry (not in the batch store), so
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

// TeamObligationResolver answers the durable control-plane state of a team run
// referenced by a team: obligation (team_spawn with auto_start). Team runs live
// in the team store (not the batch store), so the settle/ledger predicates must
// ask the host for their state instead of assuming the batch table is the whole
// obligation universe.
//
// found=false means the control plane has no durable row for the id. Like the
// child-session rule, that keeps the turn parked: a started team always owns a
// durable team record, so a missing row is never accepted as completion.
type TeamObligationResolver interface {
	TeamTerminal(ctx context.Context, teamID string) (terminal bool, found bool, err error)
}

// BackgroundJobObligationResolver answers the durable state of a background job
// referenced by a job: obligation (job_start with finalization_policy=required).
// Jobs live in the background manager/store, not in the batch table, so the
// settle/ledger predicates must ask the host instead of assuming the batch table
// is the whole obligation universe.
//
// found=false means the host has no durable row for the id. A required job
// always owns a row while it is resumable, so a missing row is never accepted as
// evidence of completion for a resume verdict.
type BackgroundJobObligationResolver interface {
	BackgroundJobTerminal(ctx context.Context, jobID string) (terminal bool, found bool, err error)
}

// ObligationResolvers groups the host-provided readers for obligation kinds
// outside the batch store. A nil entry keeps that kind conservatively pending.
type ObligationResolvers struct {
	AgentSessions  AgentSessionObligationResolver
	Teams          TeamObligationResolver
	BackgroundJobs BackgroundJobObligationResolver
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
	return TurnObligationsSettledWithTeams(ctx, store, record, resolver, nil)
}

// TurnObligationsSettledWithTeams is TurnObligationsSettledWith extended to
// team-run obligations: every team: id must have a durable row and be terminal.
// A nil team resolver with team obligations keeps the turn parked (never clear
// on a guess), mirroring the child-session rule above.
func TurnObligationsSettledWithTeams(
	ctx context.Context,
	store BatchStore,
	record *TurnSuspension,
	resolver AgentSessionObligationResolver,
	teamResolver TeamObligationResolver,
) (bool, error) {
	return TurnObligationsSettledWithResolvers(ctx, store, record, ObligationResolvers{
		AgentSessions: resolver,
		Teams:         teamResolver,
	})
}

// TurnObligationsSettledWithResolvers is TurnObligationsSettledWithTeams
// extended to background-job obligations. The missing-row rules are the same as
// the other non-batch kinds: an unreadable or absent job keeps the turn parked
// rather than being mistaken for settled work.
func TurnObligationsSettledWithResolvers(
	ctx context.Context,
	store BatchStore,
	record *TurnSuspension,
	resolvers ObligationResolvers,
) (bool, error) {
	if record == nil {
		return false, nil
	}
	batchIDs := record.ObligationBatchIDs()
	agentSessionIDs := record.ObligationAgentSessionIDs()
	teamIDs := record.ObligationTeamIDs()
	jobIDs := record.ObligationBackgroundJobIDs()
	if len(batchIDs) == 0 && len(agentSessionIDs) == 0 && len(teamIDs) == 0 && len(jobIDs) == 0 {
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
		if resolvers.AgentSessions == nil {
			// The child-session control plane is not wired; keep the turn
			// parked instead of guessing that the child is done.
			return false, nil
		}
		terminal, found, err := resolvers.AgentSessions.AgentSessionTerminal(ctx, sessionID)
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
	for _, teamID := range teamIDs {
		if resolvers.Teams == nil {
			// The team control plane is not wired; keep the turn parked
			// instead of guessing that the team is done.
			return false, nil
		}
		terminal, found, err := resolvers.Teams.TeamTerminal(ctx, teamID)
		if err != nil {
			return false, err
		}
		if !found {
			// No durable team row: the team may simply still be starting.
			// Never accept that as evidence of completion.
			return false, nil
		}
		resolved++
		if !terminal {
			return false, nil
		}
	}
	for _, jobID := range jobIDs {
		if resolvers.BackgroundJobs == nil {
			// The job control plane is not wired; keep the turn parked.
			return false, nil
		}
		terminal, found, err := resolvers.BackgroundJobs.BackgroundJobTerminal(ctx, jobID)
		if err != nil {
			return false, err
		}
		if !found {
			// No durable job row: the job may simply still be starting.
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
// evidence that work is still owed. A subagent_spawn child is different: while it
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
	return TurnObligationsAllTerminalWithTeams(ctx, store, record, resolver, nil)
}

// TurnObligationsAllTerminalWithTeams is TurnObligationsAllTerminal extended to
// team-run obligations with the same strict reading: a missing or non-terminal
// team row always blocks the resume verdict.
func TurnObligationsAllTerminalWithTeams(
	ctx context.Context,
	store BatchStore,
	record *TurnSuspension,
	resolver AgentSessionObligationResolver,
	teamResolver TeamObligationResolver,
) (bool, error) {
	return TurnObligationsAllTerminalWithResolvers(ctx, store, record, ObligationResolvers{
		AgentSessions: resolver,
		Teams:         teamResolver,
	})
}

// TurnObligationsAllTerminalWithResolvers is the strict resume-gate variant of
// TurnObligationsSettledWithResolvers: every listed obligation must have a
// readable durable row and that row must be terminal. Missing job rows block the
// resume instead of being skipped.
func TurnObligationsAllTerminalWithResolvers(
	ctx context.Context,
	store BatchStore,
	record *TurnSuspension,
	resolvers ObligationResolvers,
) (bool, error) {
	if record == nil {
		return false, nil
	}
	batchIDs := record.ObligationBatchIDs()
	agentSessionIDs := record.ObligationAgentSessionIDs()
	teamIDs := record.ObligationTeamIDs()
	jobIDs := record.ObligationBackgroundJobIDs()
	if len(batchIDs) == 0 && len(agentSessionIDs) == 0 && len(teamIDs) == 0 && len(jobIDs) == 0 {
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
		if resolvers.AgentSessions == nil {
			return false, nil
		}
		terminal, found, err := resolvers.AgentSessions.AgentSessionTerminal(ctx, sessionID)
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
	for _, teamID := range teamIDs {
		if resolvers.Teams == nil {
			return false, nil
		}
		terminal, found, err := resolvers.Teams.TeamTerminal(ctx, teamID)
		if err != nil {
			return false, err
		}
		if !found {
			// No durable team row: the team may simply still be starting.
			return false, nil
		}
		known++
		if !terminal {
			return false, nil
		}
	}
	for _, jobID := range jobIDs {
		if resolvers.BackgroundJobs == nil {
			return false, nil
		}
		terminal, found, err := resolvers.BackgroundJobs.BackgroundJobTerminal(ctx, jobID)
		if err != nil {
			return false, err
		}
		if !found {
			// No durable job row: the job may simply still be starting.
			return false, nil
		}
		known++
		if !terminal {
			return false, nil
		}
	}
	return known > 0, nil
}

// ObligationProgress summarizes how many obligations of one parked record are
// known terminal. It powers the wait-feedback sweep (progress deltas and the
// single long-silence escalation) without settling or resuming anything.
type ObligationProgress struct {
	Total    int
	Terminal int
}

// AllTerminal reports whether at least one obligation exists and every listed
// obligation is known terminal.
func (p ObligationProgress) AllTerminal() bool {
	return p.Total > 0 && p.Terminal >= p.Total
}

// CountObligationProgress counts obligations with the strict AllTerminal
// reading: a missing row, an unreadable row, or an unwired resolver counts as
// pending (never as completion), so a feedback sweep can only under-report
// progress — it must never tell the parent that more finished than really did.
func CountObligationProgress(
	ctx context.Context,
	store BatchStore,
	record *TurnSuspension,
	resolver AgentSessionObligationResolver,
	teamResolver TeamObligationResolver,
) (ObligationProgress, error) {
	return CountObligationProgressWithResolvers(ctx, store, record, ObligationResolvers{
		AgentSessions: resolver,
		Teams:         teamResolver,
	})
}

// CountObligationProgressWithResolvers extends CountObligationProgress to
// background-job obligations. Missing/unreadable rows stay pending, so feedback
// can only under-report progress.
func CountObligationProgressWithResolvers(
	ctx context.Context,
	store BatchStore,
	record *TurnSuspension,
	resolvers ObligationResolvers,
) (ObligationProgress, error) {
	if record == nil {
		return ObligationProgress{}, nil
	}
	var progress ObligationProgress
	for _, batchID := range record.ObligationBatchIDs() {
		progress.Total++
		if store == nil {
			continue
		}
		batch, err := store.GetBatch(ctx, batchID)
		if err != nil {
			return progress, err
		}
		if batch == nil {
			continue // missing row = pending
		}
		if batch.Status.Terminal() {
			progress.Terminal++
		}
	}
	for _, sessionID := range record.ObligationAgentSessionIDs() {
		progress.Total++
		if resolvers.AgentSessions == nil {
			continue
		}
		terminal, found, err := resolvers.AgentSessions.AgentSessionTerminal(ctx, sessionID)
		if err != nil {
			return progress, err
		}
		if found && terminal {
			progress.Terminal++
		}
	}
	for _, teamID := range record.ObligationTeamIDs() {
		progress.Total++
		if resolvers.Teams == nil {
			continue
		}
		terminal, found, err := resolvers.Teams.TeamTerminal(ctx, teamID)
		if err != nil {
			return progress, err
		}
		if found && terminal {
			progress.Terminal++
		}
	}
	for _, jobID := range record.ObligationBackgroundJobIDs() {
		progress.Total++
		if resolvers.BackgroundJobs == nil {
			continue
		}
		terminal, found, err := resolvers.BackgroundJobs.BackgroundJobTerminal(ctx, jobID)
		if err != nil {
			return progress, err
		}
		if found && terminal {
			progress.Terminal++
		}
	}
	return progress, nil
}

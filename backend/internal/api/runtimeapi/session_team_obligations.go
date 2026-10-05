package runtimeapi

import (
	"context"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// apiTeamObligationResolver is the API host's durable reader for team runs
// parked as `team:` obligations (spawn_team with auto_start). It mirrors the
// CLI host's localTeamObligationResolver: a team may only settle a parked turn
// once the team store reports a terminal state; a missing durable row is never
// evidence of completion.
type apiTeamObligationResolver struct {
	store team.Store
}

func (r apiTeamObligationResolver) TeamTerminal(ctx context.Context, teamID string) (bool, bool, error) {
	if r.store == nil {
		return false, false, nil
	}
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return false, false, nil
	}
	record, err := r.store.GetTeam(ctx, teamID)
	if err != nil {
		return false, false, err
	}
	if record == nil {
		// No durable team row: the team may still be starting. Never accept a
		// missing row as completion (same strictness as the child-session rule).
		return false, false, nil
	}
	if !team.IsTerminalTeamStatus(record.Status) {
		// active and paused both keep the turn parked: paused is resumable
		// control state, not completed work (the interrupt/abandon path owns
		// the explicit interruption cascade).
		return false, true, nil
	}
	return true, true, nil
}

// teamObligationResolver returns the durable team reader for this handler, or
// nil when the team store is not wired (in which case team obligations never
// settle — conservative, never clear on a guess).
func (h *Handler) teamObligationResolver() subagentbatch.TeamObligationResolver {
	if h == nil {
		return nil
	}
	store := h.getTeamStore()
	if store == nil {
		return nil
	}
	return apiTeamObligationResolver{store: store}
}

// maybeResumeTeamParkedTurn is the API-side §6.12 team-join closure: a terminal
// team event is the resume edge for a parent turn parked on team: obligations.
// The settlement wake is scheduled only when the durable ledger is fully
// terminal (never on a guess) and drained through the normal wake path; the
// parked record itself is cleared by the settle predicate, not here.
func (h *Handler) maybeResumeTeamParkedTurn(ctx context.Context, event team.TeamEvent) {
	if h == nil {
		return
	}
	switch strings.TrimSpace(event.Type) {
	case "team.completed", "team.summary":
	default:
		return
	}
	batches := h.getSubagentBatchStore()
	scheduler := h.getSupervisionWakeScheduler()
	if batches == nil || scheduler == nil {
		return
	}
	store := h.getTeamStore()
	if store == nil {
		return
	}
	teamID := strings.TrimSpace(event.TeamID)
	if teamID == "" {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	record, err := store.GetTeam(ctx, teamID)
	if err != nil || record == nil {
		return
	}
	parentSessionID := strings.TrimSpace(record.LeadSessionID)
	if parentSessionID == "" {
		return
	}
	rootScopeID := parentSessionID
	// Nested leads (a subagent session running its own team) park under their
	// own session id, but the supervision scope is the root session tree.
	if h.sessionManager != nil {
		if session, loadErr := h.sessionManager.Get(ctx, parentSessionID); loadErr == nil && session != nil {
			rootScopeID = apiAgentRootSessionID(session, parentSessionID)
		}
	}
	if rootScopeID == "" {
		rootScopeID = parentSessionID
	}
	settleCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, _ = supervision.ScheduleSettledTurnWakeWithTeams(
		settleCtx,
		batches,
		h.agentSessionObligationResolver(),
		h.teamObligationResolver(),
		scheduler,
		parentSessionID,
		rootScopeID,
	)
	controller := &sessionAgentController{handler: h}
	_ = controller.wakeSupervisedParent(settleCtx, rootScopeID, parentSessionID)
}

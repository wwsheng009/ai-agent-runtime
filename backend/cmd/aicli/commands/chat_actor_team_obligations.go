package commands

import (
	"context"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// localTeamObligationResolver is the CLI host's durable reader for team runs
// parked as `team:` obligations (spawn_team with auto_start). It mirrors the
// child-session resolver: a team may only settle a parked turn once the team
// store reports a terminal state and its in-process loop is gone; a missing
// durable row is never evidence of completion.
type localTeamObligationResolver struct {
	host *localChatRuntimeHost
}

func (r localTeamObligationResolver) TeamTerminal(ctx context.Context, teamID string) (bool, bool, error) {
	host := r.host
	if host == nil || host.TeamStore == nil {
		return false, false, nil
	}
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return false, false, nil
	}
	record, err := host.TeamStore.GetTeam(ctx, teamID)
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
		// control state, not completed work (the ESC/abandon path owns the
		// explicit interruption cascade).
		return false, true, nil
	}
	if lifecycle, ok := host.teamLifecycleService().(*localTeamLifecycleService); ok && lifecycle != nil && lifecycle.hasTeamLoop(teamID) {
		// Terminal status while the team loop still winds down: keep the turn
		// parked so the resume never races the final writes.
		return false, true, nil
	}
	return true, true, nil
}

// teamObligationResolver returns the durable team reader for this host, or nil
// when the team store is not wired (in which case team obligations never
// settle — conservative, never clear on a guess).
func (h *localChatRuntimeHost) teamObligationResolver() subagentbatch.TeamObligationResolver {
	if h == nil || h.TeamStore == nil {
		return nil
	}
	return localTeamObligationResolver{host: h}
}

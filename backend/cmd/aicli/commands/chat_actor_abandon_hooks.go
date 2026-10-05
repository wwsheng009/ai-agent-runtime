package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// cancelSuspendedTurnAgentSession implements chat.SuspendedTurnAbandonHooks for
// the CLI host: a parked turn's lightweight child (`agent_session:<id>`) is
// cancelled by interrupting + stopping its actor through the shared session
// hub. Already-gone actors count as cancelled (idempotent), so a retried
// abandon converges without inventing failures.
func (h *localChatRuntimeHost) cancelSuspendedTurnAgentSession(ctx context.Context, sessionID, reason string) error {
	sessionID = strings.TrimSpace(sessionID)
	if h == nil || sessionID == "" {
		return nil
	}
	if base := strings.TrimSpace(h.baseRuntimeSessionID()); base != "" && strings.EqualFold(base, sessionID) {
		// Degenerate obligation pointing at the parent itself: nothing to cancel.
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if h.interruptActorRun(ctx, sessionID) {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("interrupt agent session %s: %w", sessionID, err)
	}
	return fmt.Errorf("interrupt agent session %s: stop did not complete", sessionID)
}

// cancelSuspendedTurnTeam implements chat.SuspendedTurnAbandonHooks for the CLI
// host: the team is parked with the interactive `suspendAmbientTeam` semantics
// (loop stopped, tasks cancelled, status paused) instead of being destroyed, so
// the durable shell survives for an explicit later resume. A failed park keeps
// the parked-turn record so the next abandon retries.
func (h *localChatRuntimeHost) cancelSuspendedTurnTeam(ctx context.Context, teamID, reason string) error {
	teamID = strings.TrimSpace(teamID)
	if h == nil || h.TeamStore == nil || teamID == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "suspended turn abandoned"
	}
	h.suspendAmbientTeam(ctx, teamID, reason, "cancelled by suspended-turn abandon")
	record, err := h.TeamStore.GetTeam(ctx, teamID)
	if err != nil {
		return fmt.Errorf("park team %s: %w", teamID, err)
	}
	if record == nil || team.IsTerminalTeamStatus(record.Status) || record.Status == team.TeamStatusPaused {
		return nil
	}
	return fmt.Errorf("park team %s: status stayed %s", teamID, record.Status)
}

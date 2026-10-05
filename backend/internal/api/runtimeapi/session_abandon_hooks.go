package runtimeapi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// cancelSuspendedTurnAgentSession implements chat.SuspendedTurnAbandonHooks for
// the API host: a parked turn's lightweight child (`agent_session:<id>`) is
// cancelled by interrupting + stopping its actor through the shared session
// hub. A missing actor counts as already-cancelled (idempotent), so a retried
// abandon converges without inventing failures.
func (h *Handler) cancelSuspendedTurnAgentSession(ctx context.Context, sessionID, reason string) error {
	sessionID = strings.TrimSpace(sessionID)
	if h == nil || sessionID == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	hub := h.getSessionHub()
	if hub == nil {
		return nil
	}
	actor, ok := hub.Get(sessionID)
	if !ok || actor == nil {
		return nil
	}
	if err := actor.Interrupt(ctx); err != nil {
		return fmt.Errorf("interrupt agent session %s: %w", sessionID, err)
	}
	if err := hub.StopContext(ctx, sessionID); err != nil && !errors.Is(err, chat.ErrSessionActorStopped) {
		return fmt.Errorf("stop agent session %s: %w", sessionID, err)
	}
	return nil
}

// cancelSuspendedTurnTeam implements chat.SuspendedTurnAbandonHooks for the API
// host: the lifecycle loop is stopped and the team is parked as paused (same
// durable shell as the CLI suspend semantics) instead of being destroyed, so an
// explicit later resume stays possible. A failed park keeps the parked-turn
// record for the next retry.
func (h *Handler) cancelSuspendedTurnTeam(ctx context.Context, teamID, reason string) error {
	teamID = strings.TrimSpace(teamID)
	if h == nil || teamID == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	store := h.getTeamStore()
	if store == nil {
		return nil
	}
	if lifecycle := h.teamLifecycleService(); lifecycle != nil {
		lifecycle.StopLoop(teamID)
	}
	record, err := store.GetTeam(ctx, teamID)
	if err != nil {
		return fmt.Errorf("park team %s: %w", teamID, err)
	}
	if record == nil || team.IsTerminalTeamStatus(record.Status) || record.Status == team.TeamStatusPaused {
		return nil
	}
	if err := store.UpdateTeamStatus(ctx, teamID, team.TeamStatusPaused); err != nil {
		return fmt.Errorf("park team %s: %w", teamID, err)
	}
	return nil
}

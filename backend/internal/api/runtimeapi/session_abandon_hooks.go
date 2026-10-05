package runtimeapi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
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
// host with the same suspend semantics as the CLI (suspendAmbientTeam): the
// lifecycle loop is stopped, non-terminal tasks are cancelled (each with a
// task.cancelled lifecycle event) and non-idle teammates are parked idle before
// the team is marked paused. A status flip alone would leave live work behind a
// paused team; the task-level cancel is what actually stops the work while the
// durable shell stays resumable. A failed step returns before the pause so the
// parked-turn record survives for the next idempotent retry; a team already
// paused by a previous partial attempt still gets the task/teammate cleanup.
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
	reason = firstNonEmptyString(strings.TrimSpace(reason), "suspended_turn_abandon")
	summary := "cancelled by suspended-turn abandon"
	if err := h.cancelActiveTeamTasksForSuspend(ctx, store, teamID, reason, summary); err != nil {
		return fmt.Errorf("cancel team tasks %s: %w", teamID, err)
	}
	if err := h.idleTeamMatesForSuspend(ctx, store, teamID); err != nil {
		return fmt.Errorf("idle team mates %s: %w", teamID, err)
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

// suspendedTurnCancelTaskStatuses mirrors the CLI cascade
// (cancelActiveTeamTasks): only non-terminal tasks need an explicit cancel;
// done/failed/cancelled rows keep their history untouched.
var suspendedTurnCancelTaskStatuses = []team.TaskStatus{
	team.TaskStatusPending,
	team.TaskStatusReady,
	team.TaskStatusRunning,
	team.TaskStatusBlocked,
}

// cancelActiveTeamTasksForSuspend cancels every non-terminal task of a
// suspended team through the shared AgentControl registry seam and dispatches a
// task.cancelled lifecycle event per task (reason included) so the obligation
// ledger and the assignee mailbox converge instead of lingering behind the
// paused team.
func (h *Handler) cancelActiveTeamTasksForSuspend(ctx context.Context, store team.Store, teamID, reason, summary string) error {
	if h == nil || store == nil {
		return nil
	}
	tasks, err := store.ListTasks(ctx, team.TaskFilter{
		TeamID: teamID,
		Status: suspendedTurnCancelTaskStatuses,
	})
	if err != nil {
		return err
	}
	registry := team.NewAgentControlTaskRegistry(store).WithClaims(h.getTeamClaimsManager())
	for i := range tasks {
		task := &tasks[i]
		if _, err := registry.ReleaseAgentControlTask(ctx, agentcontrol.TaskReleaseRequest{
			ID:       task.ID,
			Workflow: agentcontrol.WorkflowSpawnTeam,
			Status:   string(team.TaskStatusCancelled),
			Summary:  summary,
		}); err != nil {
			return err
		}
		h.dispatchCancelledTaskLifecycleEvent(ctx, store, task, teamTaskAssigneeID(task), reason, summary)
	}
	return nil
}

// idleTeamMatesForSuspend parks the roster the same way the CLI cascade does: a
// still-busy teammate on a paused team would keep the team from settling.
func (h *Handler) idleTeamMatesForSuspend(ctx context.Context, store team.Store, teamID string) error {
	if h == nil || store == nil {
		return nil
	}
	teammates, err := store.ListTeammates(ctx, teamID)
	if err != nil {
		return err
	}
	for _, mate := range teammates {
		if mate.State == team.TeammateStateIdle {
			continue
		}
		if err := store.UpdateTeammateState(ctx, mate.ID, team.TeammateStateIdle); err != nil {
			return err
		}
	}
	return nil
}

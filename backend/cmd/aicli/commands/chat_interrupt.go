package commands

import (
	"context"
	"sort"
	"strings"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

const chatInterruptCleanupTimeout = 5 * time.Second

func (s *ChatSession) interruptLocalRuntimeWorkAsync() {
	if s == nil || s.LocalRuntimeHost == nil {
		return
	}
	host := s.LocalRuntimeHost
	baseSessionID := currentRuntimeSessionID(s)
	userID := strings.TrimSpace(s.SessionUserID)
	activeTeamID := activeTeamID(s)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), chatInterruptCleanupTimeout)
		defer cancel()
		host.interruptActiveRuns(ctx, baseSessionID, userID, activeTeamID)
	}()
}

func (h *localChatRuntimeHost) interruptActiveRuns(ctx context.Context, baseSessionID, userID, activeTeamID string) {
	if h == nil {
		return
	}
	baseSessionID = strings.TrimSpace(baseSessionID)
	userID = strings.TrimSpace(userID)
	teamSessionIDs := h.interruptActiveTeamRuns(ctx, baseSessionID, activeTeamID)
	h.interruptChildAgentRuns(ctx, baseSessionID, userID, teamSessionIDs)
}

func (h *localChatRuntimeHost) interruptActiveTeamRuns(ctx context.Context, baseSessionID, activeTeamID string) map[string]struct{} {
	sessionIDs := map[string]struct{}{}
	if h == nil || h.TeamStore == nil {
		return sessionIDs
	}
	teamIDs := h.interruptTargetTeamIDs(ctx, baseSessionID, activeTeamID)
	for _, teamID := range teamIDs {
		h.stopTeamLifecycleLoop(teamID)
		for _, sessionID := range h.teamSessionIDs(ctx, teamID) {
			if sessionID == "" {
				continue
			}
			sessionIDs[sessionID] = struct{}{}
			if !strings.EqualFold(sessionID, strings.TrimSpace(baseSessionID)) {
				h.interruptActorRun(ctx, sessionID)
			}
			h.markRuntimeSessionStopped(ctx, sessionID)
		}
		h.markTeamInterrupted(ctx, teamID)
	}
	return sessionIDs
}

func (h *localChatRuntimeHost) interruptChildAgentRuns(ctx context.Context, baseSessionID, userID string, skip map[string]struct{}) {
	if h == nil || h.SessionStore == nil || strings.TrimSpace(baseSessionID) == "" || strings.TrimSpace(userID) == "" {
		return
	}
	sessions, err := h.SessionStore.List(ctx, strings.TrimSpace(userID))
	if err != nil {
		return
	}
	for _, item := range sessions {
		if item == nil {
			continue
		}
		sessionID := strings.TrimSpace(item.ID)
		if sessionID == "" || strings.EqualFold(sessionID, strings.TrimSpace(baseSessionID)) {
			continue
		}
		if _, exists := skip[sessionID]; exists {
			continue
		}
		parent, _ := item.GetContext(toolbroker.AgentSessionContextParentSessionID)
		parentSessionID, _ := parent.(string)
		if !strings.EqualFold(strings.TrimSpace(parentSessionID), strings.TrimSpace(baseSessionID)) {
			continue
		}
		h.interruptActorRun(ctx, sessionID)
		h.markRuntimeSessionStopped(ctx, sessionID)
	}
}

func (h *localChatRuntimeHost) interruptTargetTeamIDs(ctx context.Context, baseSessionID, activeTeamID string) []string {
	seen := map[string]struct{}{}
	add := func(teamID string) {
		teamID = strings.TrimSpace(teamID)
		if teamID == "" {
			return
		}
		seen[teamID] = struct{}{}
	}
	if h == nil || h.TeamStore == nil {
		add(activeTeamID)
		return sortedStringKeys(seen)
	}
	if activeTeamID = strings.TrimSpace(activeTeamID); activeTeamID != "" {
		if record, err := h.TeamStore.GetTeam(ctx, activeTeamID); err == nil && record != nil && record.Status == team.TeamStatusActive {
			add(record.ID)
		}
	}
	teams, err := h.TeamStore.ListTeams(ctx, team.TeamFilter{Status: team.TeamStatusActive})
	if err != nil {
		return sortedStringKeys(seen)
	}
	for _, item := range teams {
		if strings.TrimSpace(baseSessionID) == "" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(item.LeadSessionID), strings.TrimSpace(baseSessionID)) {
			add(item.ID)
		}
	}
	return sortedStringKeys(seen)
}

func (h *localChatRuntimeHost) teamSessionIDs(ctx context.Context, teamID string) []string {
	if h == nil || h.TeamStore == nil || strings.TrimSpace(teamID) == "" {
		return nil
	}
	seen := map[string]struct{}{}
	if record, err := h.TeamStore.GetTeam(ctx, strings.TrimSpace(teamID)); err == nil && record != nil {
		if sessionID := strings.TrimSpace(record.LeadSessionID); sessionID != "" {
			seen[sessionID] = struct{}{}
		}
	}
	teammates, err := h.TeamStore.ListTeammates(ctx, strings.TrimSpace(teamID))
	if err != nil {
		return sortedStringKeys(seen)
	}
	for _, mate := range teammates {
		if sessionID := strings.TrimSpace(mate.SessionID); sessionID != "" {
			seen[sessionID] = struct{}{}
		}
	}
	return sortedStringKeys(seen)
}

func sortedStringKeys(values map[string]struct{}) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, strings.TrimSpace(value))
		}
	}
	sort.Strings(out)
	return out
}

func (h *localChatRuntimeHost) stopTeamLifecycleLoop(teamID string) {
	teamID = strings.TrimSpace(teamID)
	if h == nil || teamID == "" {
		return
	}
	if lifecycle, ok := h.teamLifecycleService().(*localTeamLifecycleService); ok && lifecycle != nil {
		lifecycle.StopLoop(teamID)
		return
	}
	if lifecycle := h.teamLifecycleService(); lifecycle != nil {
		lifecycle.StopLoops()
	}
}

func (h *localChatRuntimeHost) interruptActorRun(ctx context.Context, sessionID string) {
	if h == nil || h.SessionHub == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	actor, ok := h.SessionHub.Get(strings.TrimSpace(sessionID))
	if !ok || actor == nil {
		return
	}
	interruptCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	_ = actor.Interrupt(interruptCtx)
}

func (h *localChatRuntimeHost) markRuntimeSessionStopped(ctx context.Context, sessionID string) {
	if h == nil || h.RuntimeStore == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	state, err := h.RuntimeStore.LoadState(ctx, sessionID)
	if err != nil {
		return
	}
	if state == nil {
		state = &runtimechat.RuntimeState{SessionID: sessionID}
	}
	state.Status = runtimechat.SessionStopped
	state.CurrentTurnID = ""
	state.CurrentRunMeta = nil
	state.PendingApproval = nil
	state.PendingQuestion = nil
	state.PendingTool = nil
	state.UpdatedAt = time.Now().UTC()
	_ = h.RuntimeStore.SaveState(ctx, state)
}

func (h *localChatRuntimeHost) markTeamInterrupted(ctx context.Context, teamID string) {
	if h == nil || h.TeamStore == nil || strings.TrimSpace(teamID) == "" {
		return
	}
	teamID = strings.TrimSpace(teamID)
	_ = h.cancelActiveTeamTasks(ctx, teamID)
	_ = h.idleTeamMates(ctx, teamID)
	_ = h.TeamStore.UpdateTeamStatus(ctx, teamID, team.TeamStatusPaused)
	h.dispatchTeamLifecycleEvent(team.TeamEvent{
		Type:   "team.interrupted",
		TeamID: teamID,
		Payload: map[string]interface{}{
			"reason": "user_interrupt",
			"status": string(team.TeamStatusPaused),
		},
		Timestamp: time.Now().UTC(),
	}, true)
}

func (h *localChatRuntimeHost) cancelActiveTeamTasks(ctx context.Context, teamID string) error {
	if h == nil || h.TeamStore == nil || strings.TrimSpace(teamID) == "" {
		return nil
	}
	tasks, err := h.TeamStore.ListTasks(ctx, team.TaskFilter{
		TeamID: strings.TrimSpace(teamID),
		Status: []team.TaskStatus{
			team.TaskStatusPending,
			team.TaskStatusReady,
			team.TaskStatusRunning,
			team.TaskStatusBlocked,
		},
	})
	if err != nil {
		return err
	}
	for _, item := range tasks {
		item.Status = team.TaskStatusCancelled
		item.Summary = "cancelled by user interrupt"
		item.Assignee = nil
		item.LeaseUntil = nil
		if err := h.TeamStore.UpdateTask(ctx, item); err != nil {
			return err
		}
		_ = h.TeamStore.ReleasePathClaimsByTask(ctx, item.ID)
	}
	return nil
}

func (h *localChatRuntimeHost) idleTeamMates(ctx context.Context, teamID string) error {
	if h == nil || h.TeamStore == nil || strings.TrimSpace(teamID) == "" {
		return nil
	}
	teammates, err := h.TeamStore.ListTeammates(ctx, strings.TrimSpace(teamID))
	if err != nil {
		return err
	}
	for _, mate := range teammates {
		if mate.State == team.TeammateStateIdle {
			continue
		}
		if err := h.TeamStore.UpdateTeammateState(ctx, mate.ID, team.TeammateStateIdle); err != nil {
			return err
		}
	}
	return nil
}

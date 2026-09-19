package commands

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

const (
	chatInterruptCleanupTimeout          = 5 * time.Second
	chatInterruptCleanupWaitTimeout      = chatInterruptCleanupTimeout + time.Second
	chatInterruptCleanupIncompleteNotice = "[stop] 停止清理未在时限内完成，运行可能仍在终止；可再次按 Esc 重试或稍候。"
)

// chatInterruptCleanupOutcome reports what the async interrupt cleanup managed
// to complete before its deadline. When stopped is false the UI keeps the
// Stopping stage and appends an explicit retry hint instead of silently
// pretending the run was stopped (plan doc P2-6/E4).
type chatInterruptCleanupOutcome struct {
	stopped      bool
	actors       int
	failedActors int
}

func (h *localChatRuntimeHost) interruptActiveRuns(ctx context.Context, baseSessionID, userID, activeTeamID string) chatInterruptCleanupOutcome {
	outcome := chatInterruptCleanupOutcome{stopped: true}
	if h == nil {
		return outcome
	}
	baseSessionID = strings.TrimSpace(baseSessionID)
	userID = strings.TrimSpace(userID)
	// Team bookkeeping first: cancel/pause tasks before any actor Stop waits on
	// canceled runs, so interrupt is recorded as cancelled rather than failed.
	teamSessionIDs := h.prepareTeamInterrupt(ctx, baseSessionID, activeTeamID)
	if baseSessionID != "" {
		outcome.actors++
		if !h.interruptActorRun(ctx, baseSessionID) {
			outcome.failedActors++
		}
		h.markRuntimeSessionStopped(ctx, baseSessionID)
	}
	for sessionID := range teamSessionIDs {
		if sessionID == "" || strings.EqualFold(sessionID, baseSessionID) {
			continue
		}
		outcome.actors++
		if !h.interruptActorRun(ctx, sessionID) {
			outcome.failedActors++
		}
		h.markRuntimeSessionStopped(ctx, sessionID)
	}
	attempted, failed := h.interruptChildAgentRuns(ctx, baseSessionID, userID, teamSessionIDs)
	outcome.actors += attempted
	outcome.failedActors += failed
	outcome.stopped = outcome.failedActors == 0 && ctx.Err() == nil
	return outcome
}

func (h *localChatRuntimeHost) prepareTeamInterrupt(ctx context.Context, baseSessionID, activeTeamID string) map[string]struct{} {
	sessionIDs := map[string]struct{}{}
	if h == nil || h.TeamStore == nil {
		return sessionIDs
	}
	teamIDs := h.interruptTargetTeamIDs(ctx, baseSessionID, activeTeamID)
	for _, teamID := range teamIDs {
		h.stopTeamLifecycleLoop(teamID)
		h.markTeamInterrupted(ctx, teamID)
		for _, sessionID := range h.teamSessionIDs(ctx, teamID) {
			if sessionID == "" {
				continue
			}
			sessionIDs[sessionID] = struct{}{}
		}
	}
	return sessionIDs
}

func (h *localChatRuntimeHost) interruptChildAgentRuns(ctx context.Context, baseSessionID, userID string, skip map[string]struct{}) (attempted int, failed int) {
	if h == nil || h.SessionStore == nil || strings.TrimSpace(baseSessionID) == "" {
		return 0, 0
	}
	sessions, err := h.listInterruptCandidateSessions(ctx, userID)
	if err != nil {
		return 0, 0
	}
	byID := make(map[string]*runtimechat.Session, len(sessions))
	for _, item := range sessions {
		if item != nil && strings.TrimSpace(item.ID) != "" {
			byID[strings.TrimSpace(item.ID)] = item
		}
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
			if !localAgentHasAncestor(item, baseSessionID, byID) {
				continue
			}
		}
		attempted++
		if !h.interruptActorRun(ctx, sessionID) {
			failed++
		}
		h.markRuntimeSessionStopped(ctx, sessionID)
	}
	return attempted, failed
}

// listInterruptCandidateSessions resolves the session set used for child-agent
// cascade interrupts. The interactive host always has a user scope; when it is
// missing (non-interactive / programmatic hosts) fall back to the optional
// all-sessions lister so children still get interrupted instead of silently
// skipping the whole cascade (plan doc P2-7).
func (h *localChatRuntimeHost) listInterruptCandidateSessions(ctx context.Context, userID string) ([]*runtimechat.Session, error) {
	userID = strings.TrimSpace(userID)
	if userID != "" {
		return h.SessionStore.List(ctx, userID)
	}
	lister, ok := h.SessionStore.(runtimechat.SessionStorageAllLister)
	if !ok {
		return nil, nil
	}
	const interruptCascadeSessionLimit = 500
	return lister.ListAll(ctx, interruptCascadeSessionLimit, 0)
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

// interruptActorRun stops one session actor and reports whether the stop
// actually completed. Only an expired deadline/cancellation counts as failure:
// "no active run" / "already stopped" errors are routine and must not turn into
// a user-visible "cleanup incomplete" notice.
func (h *localChatRuntimeHost) interruptActorRun(ctx context.Context, sessionID string) bool {
	if h == nil || h.SessionHub == nil || strings.TrimSpace(sessionID) == "" {
		return true
	}
	sessionID = strings.TrimSpace(sessionID)
	actor, ok := h.SessionHub.Get(sessionID)
	if !ok || actor == nil {
		return true
	}
	interruptCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	_ = actor.Interrupt(interruptCtx)
	// Interrupt only cancels the active turn and marks runtime state stopped.
	// The SessionActor itself must be stopped so OnStop releases the session
	// lease; otherwise a later terminal/process still sees ownership conflict.
	// The next prompt recreates the actor via SessionHub.GetOrCreate.
	_ = h.SessionHub.StopContext(ctx, sessionID)
	if errors.Is(interruptCtx.Err(), context.DeadlineExceeded) || errors.Is(interruptCtx.Err(), context.Canceled) {
		return false
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.Canceled) {
		return false
	}
	return true
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
	h.suspendAmbientTeam(ctx, teamID, "user_interrupt", "cancelled by user interrupt")
}

// suspendAmbientTeam durably parks a team so it stops driving foreground
// execution without destroying its durable shell: non-terminal tasks are
// cancelled, busy teammate rows are idled, the team status becomes paused and
// the lifecycle event records why. Callers that run while the session is
// interactive (user interrupt, interactive resume) must use this instead of
// cancelling the team, so the durable team remains available for an explicit
// later resume.
func (h *localChatRuntimeHost) suspendAmbientTeam(ctx context.Context, teamID, reason, taskSummary string) {
	if h == nil || h.TeamStore == nil || strings.TrimSpace(teamID) == "" {
		return
	}
	teamID = strings.TrimSpace(teamID)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "suspended"
	}
	taskSummary = strings.TrimSpace(taskSummary)
	if taskSummary == "" {
		taskSummary = "cancelled by team suspend"
	}
	// Stop the in-process loop first: a live loop would otherwise keep the
	// paused team unsettled (RunSettled reports paused-with-loop as pending).
	h.stopTeamLifecycleLoop(teamID)
	_ = h.cancelActiveTeamTasks(ctx, teamID, taskSummary)
	_ = h.idleTeamMates(ctx, teamID)
	_ = h.TeamStore.UpdateTeamStatus(ctx, teamID, team.TeamStatusPaused)
	h.dispatchTeamLifecycleEvent(team.TeamEvent{
		Type:   "team.interrupted",
		TeamID: teamID,
		Payload: map[string]interface{}{
			"reason": reason,
			"status": string(team.TeamStatusPaused),
		},
		Timestamp: time.Now().UTC(),
	}, true)
}

func (h *localChatRuntimeHost) cancelActiveTeamTasks(ctx context.Context, teamID, summary string) error {
	if h == nil || h.TeamStore == nil || strings.TrimSpace(teamID) == "" {
		return nil
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		summary = "cancelled"
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
	taskRegistry := team.NewAgentControlTaskRegistry(h.TeamStore).WithClaims(h.TeamClaims)
	for _, item := range tasks {
		assignee := taskAssignee(item)
		if _, err := taskRegistry.ReleaseAgentControlTask(ctx, agentcontrol.TaskReleaseRequest{
			ID:       item.ID,
			Workflow: agentcontrol.WorkflowSpawnTeam,
			Status:   string(team.TaskStatusCancelled),
			Summary:  summary,
		}); err != nil {
			return err
		}
		h.dispatchCancelledTaskLifecycleEvent(ctx, item, assignee, summary)
	}
	return nil
}

func taskAssignee(task team.Task) string {
	if task.Assignee == nil {
		return ""
	}
	return strings.TrimSpace(*task.Assignee)
}

func (h *localChatRuntimeHost) dispatchCancelledTaskLifecycleEvent(ctx context.Context, task team.Task, assignee string, summary string) {
	if h == nil || strings.TrimSpace(task.ID) == "" || strings.TrimSpace(task.TeamID) == "" {
		return
	}
	event := team.TeamEvent{
		Type:   "task.cancelled",
		TeamID: strings.TrimSpace(task.TeamID),
		Payload: map[string]interface{}{
			"team_id":  strings.TrimSpace(task.TeamID),
			"task_id":  strings.TrimSpace(task.ID),
			"assignee": strings.TrimSpace(assignee),
			"status":   string(team.TaskStatusCancelled),
			"reason":   "user_interrupt",
			"summary":  strings.TrimSpace(summary),
		},
		Timestamp: time.Now().UTC(),
	}
	if h.TeamStore != nil {
		_, _ = h.TeamStore.AppendTeamEvent(ctx, event)
	}
	h.dispatchTeamLifecycleEvent(event, true)
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

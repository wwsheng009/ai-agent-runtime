package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

const (
	defaultNoInteractiveTeamDrainTimeout = 30 * time.Second
	noInteractiveTeamDrainPollInterval   = 100 * time.Millisecond
)

func awaitNoInteractiveLocalTeamDrain(session *ChatSession) {
	if session == nil || !session.NoInteractive || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.TeamStore == nil {
		return
	}
	if session.ActiveTeam == nil || strings.TrimSpace(session.ActiveTeam.TeamID) == "" {
		_ = reloadChatRuntimeSessionFromStore(session)
		return
	}

	teamID := strings.TrimSpace(session.ActiveTeam.TeamID)
	timeout := resolveNoInteractiveTeamDrainTimeout(session)
	if timeout <= 0 {
		timeout = defaultNoInteractiveTeamDrainTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	_ = session.LocalRuntimeHost.waitForTeamTerminal(ctx, teamID)
	_ = reloadChatRuntimeSessionFromStore(session)
}

func shouldDisplayInteractivePrompt(session *ChatSession) bool {
	if session == nil || session.NoInteractive || session.JSONOutput {
		return false
	}
	return !interactiveTeamPending(session)
}

func prepareInteractiveRead(session *ChatSession) (bool, string, error) {
	if session == nil || session.NoInteractive || session.JSONOutput {
		return false, "", nil
	}
	if err := waitForInteractivePromptReady(session); err != nil {
		return false, "", err
	}
	if pending := pendingInteractiveInputCount(session); pending > 0 {
		notice := ""
		if !session.queuedInputDrain {
			session.queuedInputDrain = true
			publishLocalChatDiagnosticEvent(session, chatEventInputQueueDetected, map[string]interface{}{
				"queued_input_count": pending,
				"source":             "stdin",
			})
			notice = fmt.Sprintf("[input] 检测到 %d 条后台任务期间的预输入内容，现将优先处理这些输入。", pending)
		}
		return false, notice, nil
	}
	if session.queuedInputDrain {
		publishLocalChatDiagnosticEvent(session, chatEventInputQueueDrained, map[string]interface{}{
			"queued_input_count": 0,
		})
	}
	session.queuedInputDrain = false
	return true, "", nil
}

func waitForInteractivePromptReady(session *ChatSession) error {
	if session == nil || session.NoInteractive || session.JSONOutput {
		return nil
	}
	pending := interactiveTeamPending(session)
	if !pending {
		return nil
	}
	binding := resolvedInteractiveTeamBinding(session)
	if session.LocalRuntimeHost == nil || session.LocalRuntimeHost.TeamStore == nil || binding == nil {
		return nil
	}

	teamID := strings.TrimSpace(binding.TeamID)
	if teamID == "" {
		return nil
	}
	ctx := session.cancelCtx
	if ctx == nil {
		ctx = context.Background()
	}
	if err := session.LocalRuntimeHost.waitForTeamTerminal(ctx, teamID); err != nil {
		return err
	}
	if err := reloadChatRuntimeSessionFromStore(session); err != nil {
		return err
	}
	return nil
}

func interactiveTeamPending(session *ChatSession) bool {
	binding := resolvedInteractiveTeamBinding(session)
	if binding == nil {
		return false
	}
	return interactiveTeamPendingByTeamID(session, binding.TeamID)
}

func runtimeAmbientTeamBinding(session *ChatSession) *chatTeamBinding {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.RuntimeStore == nil || session.RuntimeSession == nil {
		return nil
	}
	state, err := session.LocalRuntimeHost.RuntimeStore.LoadState(context.Background(), strings.TrimSpace(session.RuntimeSession.ID))
	if err != nil || state == nil {
		return nil
	}
	if state.AmbientRunMeta == nil || state.AmbientRunMeta.Team == nil {
		return nil
	}
	return &chatTeamBinding{
		TeamID:         strings.TrimSpace(state.AmbientRunMeta.Team.TeamID),
		AgentID:        firstNonEmptyChatValue(strings.TrimSpace(state.AmbientRunMeta.Team.AgentID), "lead"),
		TaskID:         strings.TrimSpace(state.AmbientRunMeta.Team.CurrentTaskID),
		PermissionMode: session.PermissionMode,
	}
}

func teamStoreAmbientTeamBinding(session *ChatSession) *chatTeamBinding {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.TeamStore == nil || session.RuntimeSession == nil {
		return nil
	}
	sessionID := strings.TrimSpace(session.RuntimeSession.ID)
	if sessionID == "" {
		return nil
	}
	teams, err := session.LocalRuntimeHost.TeamStore.ListTeams(context.Background(), team.TeamFilter{
		Status: team.TeamStatusActive,
		Limit:  32,
	})
	if err != nil {
		return nil
	}
	for _, record := range teams {
		if !strings.EqualFold(strings.TrimSpace(record.LeadSessionID), sessionID) {
			continue
		}
		return &chatTeamBinding{
			TeamID:         strings.TrimSpace(record.ID),
			AgentID:        "lead",
			PermissionMode: session.PermissionMode,
		}
	}
	return nil
}

func resolvedInteractiveTeamBinding(session *ChatSession) *chatTeamBinding {
	if session == nil {
		return nil
	}
	if session.ActiveTeam != nil && strings.TrimSpace(session.ActiveTeam.TeamID) != "" {
		return session.ActiveTeam.Clone()
	}
	binding := runtimeAmbientTeamBinding(session)
	if binding == nil || strings.TrimSpace(binding.TeamID) == "" {
		binding = teamStoreAmbientTeamBinding(session)
		if binding == nil || strings.TrimSpace(binding.TeamID) == "" {
			return nil
		}
	}
	session.ActiveTeam = binding.Clone()
	return binding
}

func restoreAmbientTeamBindingFromRuntimeStore(session *ChatSession) bool {
	if session == nil {
		return false
	}
	if session.ActiveTeam != nil && strings.TrimSpace(session.ActiveTeam.TeamID) != "" {
		return false
	}
	binding := runtimeAmbientTeamBinding(session)
	if binding == nil || strings.TrimSpace(binding.TeamID) == "" {
		return false
	}
	session.ActiveTeam = binding.Clone()
	return true
}

func interactiveTeamPendingByTeamID(session *ChatSession, teamID string) bool {
	if session == nil || session.LocalRuntimeHost == nil {
		return false
	}
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return false
	}
	if lifecycle := session.LocalRuntimeHost.teamLifecycleService(); lifecycle != nil {
		return lifecycle.Pending(context.Background(), teamID)
	}
	return false
}

func syncAmbientTeamLifecycleState(session *ChatSession) error {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.RuntimeStore == nil || session.RuntimeSession == nil {
		return nil
	}
	sessionID := strings.TrimSpace(session.RuntimeSession.ID)
	if sessionID == "" {
		return nil
	}
	state, err := session.LocalRuntimeHost.RuntimeStore.LoadState(context.Background(), sessionID)
	if err != nil {
		return err
	}
	if state == nil {
		state = &runtimechat.RuntimeState{
			SessionID: sessionID,
			Status:    runtimechat.SessionIdle,
		}
	}
	binding := resolvedInteractiveTeamBinding(session)
	pending := false
	if binding != nil {
		pending = interactiveTeamPendingByTeamID(session, binding.TeamID)
		if session.ActiveTeam == nil {
			session.ActiveTeam = binding.Clone()
		}
	}
	now := time.Now().UTC()
	switch {
	case pending:
		state.AmbientRunMeta = currentRunMetaForSession(session)
		state.UpdatedAt = now
		return session.LocalRuntimeHost.RuntimeStore.SaveState(context.Background(), state)
	case !pending && state.AmbientRunMeta != nil:
		state.AmbientRunMeta = nil
		state.UpdatedAt = now
		return session.LocalRuntimeHost.RuntimeStore.SaveState(context.Background(), state)
	default:
		return nil
	}
}

func resolveNoInteractiveTeamDrainTimeout(session *ChatSession) time.Duration {
	if session == nil || session.RequestTimeout <= 0 {
		return defaultNoInteractiveTeamDrainTimeout
	}
	return session.RequestTimeout
}

func reloadChatRuntimeSessionFromStore(session *ChatSession) error {
	if session == nil || session.SessionManager == nil || session.RuntimeSession == nil {
		return nil
	}
	runtimeSession, err := session.SessionManager.Get(context.Background(), session.RuntimeSession.ID)
	if err != nil || runtimeSession == nil {
		return err
	}
	if err := restoreChatStateFromRuntimeSession(session, runtimeSession); err != nil {
		return err
	}
	inferAmbientTeamBinding(session, runtimeSession)
	if session.LocalRuntimeHost != nil {
		validateAmbientTeamBinding(session, session.LocalRuntimeHost.TeamStore)
	}
	return syncAmbientTeamLifecycleState(session)
}

func (h *localChatRuntimeHost) waitForTeamTerminal(ctx context.Context, teamID string) error {
	if lifecycle := h.teamLifecycleService(); lifecycle != nil {
		return lifecycle.WaitForTerminal(ctx, teamID)
	}
	return nil
}

func (h *localChatRuntimeHost) teamRunSettled(ctx context.Context, teamID string) (bool, error) {
	if lifecycle := h.teamLifecycleService(); lifecycle != nil {
		return lifecycle.RunSettled(ctx, teamID)
	}
	return true, nil
}

func isAmbientTeamRunningRuntimeState(state *runtimechat.RuntimeState, teamID string) bool {
	if state == nil || state.AmbientRunMeta == nil || state.AmbientRunMeta.Team == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(state.AmbientRunMeta.Team.TeamID), strings.TrimSpace(teamID))
}

func shouldIgnoreAmbientTeamRuntimeState(state *runtimechat.RuntimeState, teamID string) bool {
	if !isAmbientTeamRunningRuntimeState(state, teamID) {
		return false
	}
	switch state.Status {
	case runtimechat.SessionIdle, runtimechat.SessionStopped:
		return true
	default:
		return false
	}
}

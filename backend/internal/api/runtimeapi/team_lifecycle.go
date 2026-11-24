package runtimeapi

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimehost"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/teamsupervisor"
)

type handlerTeamLifecycleService struct {
	handler           *Handler
	supervisor        *teamsupervisor.Supervisor
	terminalCleanupMu sync.Mutex
	terminalCleanup   map[string]struct{}
}

const teamTerminalCleanupTimeout = 10 * time.Second

func newHandlerTeamLifecycleService(handler *Handler) *handlerTeamLifecycleService {
	if handler == nil {
		return nil
	}
	service := &handlerTeamLifecycleService{handler: handler}
	var supervisorConfig teamsupervisor.Config
	if handler.runtimeConfig != nil {
		supervisorConfig = runtimehost.BuildTeamSupervisorConfig(handler.runtimeConfig)
	}
	service.supervisor = teamsupervisor.New(supervisorConfig, teamsupervisor.Hooks{
		DesiredTeams: service.desiredTeamIDs,
		RunLoop:      service.runLoop,
		OwnerAllowed: service.ownerAllowed,
		OnEvent:      service.publishSupervisorEvent,
		OnSettled:    service.ReplayStoredTerminalEvents,
	})
	return service
}

func (h *Handler) teamLifecycleService() *handlerTeamLifecycleService {
	if h == nil {
		return nil
	}
	h.teamStoreMu.Lock()
	if h.teamLifecycle == nil {
		h.teamLifecycle = newHandlerTeamLifecycleService(h)
	}
	service := h.teamLifecycle
	h.teamStoreMu.Unlock()
	return service
}

func (s *handlerTeamLifecycleService) desiredTeamIDs(ctx context.Context) ([]string, error) {
	if s == nil || s.handler == nil {
		return nil, nil
	}
	store := s.handler.getTeamStore()
	if store == nil {
		return nil, nil
	}
	if s.handler.getTeamOrchestrator() == nil {
		return nil, nil
	}
	teams, err := store.ListTeams(ctx, team.TeamFilter{Status: team.TeamStatusActive})
	if err != nil {
		return nil, err
	}
	teamIDs := make([]string, 0, len(teams))
	for _, item := range teams {
		if teamID := strings.TrimSpace(item.ID); teamID != "" {
			teamIDs = append(teamIDs, teamID)
		}
	}
	return teamIDs, nil
}

// ownerAllowed consults the durable orchestrator lease before starting a loop.
// The orchestrator still acquires/renews the authoritative lease in
// RunWithWake; this hook only avoids needlessly starting a guaranteed-loser
// loop while another live host owns the team.
func (s *handlerTeamLifecycleService) ownerAllowed(ctx context.Context, teamID string) bool {
	if s == nil || s.handler == nil {
		return false
	}
	orchestrator := s.handler.getTeamOrchestrator()
	ownerID := ""
	if orchestrator != nil {
		ownerID = strings.TrimSpace(orchestrator.OwnerID)
	}
	return runtimehost.TeamOwnerAllowed(ctx, s.handler.getTeamStore(), teamID, ownerID, time.Now().UTC())
}

func (s *handlerTeamLifecycleService) runLoop(ctx context.Context, teamID string, wake <-chan struct{}) error {
	if s == nil || s.handler == nil {
		return nil
	}
	orchestrator := s.handler.getTeamOrchestrator()
	if orchestrator == nil {
		return nil
	}
	err := orchestrator.RunWithWake(ctx, teamID, wake)
	if errors.Is(err, team.ErrOrchestratorLeaseHeld) {
		// Another instance holds the durable owner lease. The loop is not
		// broken: exit quietly and let the next reconcile attempt re-check,
		// which is how a live backup instance takes over after a crash.
		s.publishSupervisorEvent(teamsupervisor.Event{
			Type:   "team.orchestrator.owner_lease_held",
			TeamID: teamID,
			Reason: "durable_owner_lease_held_by_another_instance",
		})
		return nil
	}
	return err
}

func (s *handlerTeamLifecycleService) SyncLoops() {
	if s == nil || s.supervisor == nil {
		return
	}
	_ = s.supervisor.Reconcile(context.Background())
}

func (s *handlerTeamLifecycleService) publishSupervisorEvent(event teamsupervisor.Event) {
	if s == nil || s.handler == nil {
		return
	}
	payload := map[string]interface{}{
		"team_id":       strings.TrimSpace(event.TeamID),
		"generation":    event.Generation,
		"restart_count": event.RestartCount,
		"reason":        strings.TrimSpace(event.Reason),
		"timestamp":     event.Timestamp.UTC().Format(time.RFC3339Nano),
	}
	switch event.Type {
	case "team.orchestrator.loop.started":
		payload["start_reason"] = event.Reason
	case "team.orchestrator.loop.stopped", "team.orchestrator.loop.error":
		payload["stop_reason"] = event.Reason
	}
	if event.Error != "" {
		payload["error"] = event.Error
	}
	if !event.NextRestartAt.IsZero() {
		payload["next_restart_at"] = event.NextRestartAt.UTC().Format(time.RFC3339Nano)
	}
	s.handler.publishRuntimeEvent(event.Type, "trace_"+uuid.NewString(), payload)
}

func (s *handlerTeamLifecycleService) ReplayStoredTerminalEvents(teamID string) {
	if s == nil || s.handler == nil {
		return
	}
	store := s.handler.getTeamStore()
	if store == nil {
		return
	}
	s.handler.projectTeamRunTerminalState(teamID)
	events, err := store.ListTeamEvents(context.Background(), team.TeamEventFilter{
		TeamID: strings.TrimSpace(teamID),
		Limit:  16,
	})
	if err != nil {
		return
	}
	for _, event := range events {
		if !isReplayableTeamLifecycleEvent(event.Type) {
			continue
		}
		payload := map[string]interface{}{}
		for key, value := range event.Payload {
			payload[key] = value
		}
		if event.TeamID != "" {
			payload["team_id"] = event.TeamID
		}
		s.handler.getRuntimeEventBus().Publish(runtimeevents.Event{
			Type:      normalizeTeamEventType(event.Type),
			AgentName: "team-orchestrator",
			Payload:   payload,
			Timestamp: event.Timestamp,
		})
		if strings.EqualFold(strings.TrimSpace(event.Type), "team.completed") {
			s.closeTerminalTeammatesAsync(teamID)
		}
	}
}

// closeTerminalTeammatesAsync mirrors the CLI host's terminal-team cleanup:
// after a team reaches terminal state, stop its non-lead child sessions. The
// close path is idempotent and best-effort; a team whose status is still active
// is never torn down.
//
// Do not call ReconcileTerminalTeamState here. team.completed is emitted by
// finalizeTerminalTeamState before it generates and persists the terminal
// summary; re-entering reconciliation from the event bus lets a second caller
// win the summary lock and overwrite the original caller's structured summary
// metadata (trace/error type/fallback details). The terminal event is
// authoritative: wait for the sessions to settle and close the teammates.
func (s *handlerTeamLifecycleService) closeTerminalTeammatesAsync(teamID string) {
	if s == nil || s.handler == nil {
		return
	}
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return
	}
	s.terminalCleanupMu.Lock()
	if s.terminalCleanup == nil {
		s.terminalCleanup = map[string]struct{}{}
	}
	if _, exists := s.terminalCleanup[teamID]; exists {
		s.terminalCleanupMu.Unlock()
		return
	}
	s.terminalCleanup[teamID] = struct{}{}
	s.terminalCleanupMu.Unlock()
	go func() {
		defer func() {
			s.terminalCleanupMu.Lock()
			delete(s.terminalCleanup, teamID)
			s.terminalCleanupMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), teamTerminalCleanupTimeout)
		defer cancel()
		s.waitForTeamSessionsSettled(ctx, teamID)
		_ = s.closeTerminalTeammates(context.Background(), teamID)
	}()
}

func (s *handlerTeamLifecycleService) waitForTeamSessionsSettled(ctx context.Context, teamID string) {
	if s == nil || s.handler == nil || s.handler.getSessionRuntimeStore() == nil {
		return
	}
	store := s.handler.getTeamStore()
	if store == nil {
		return
	}
	record, err := store.GetTeam(ctx, teamID)
	if err != nil || record == nil {
		return
	}
	teammates, err := store.ListTeammates(ctx, teamID)
	if err != nil {
		return
	}
	sessionIDs := make([]string, 0, len(teammates)+1)
	if lead := strings.TrimSpace(record.LeadSessionID); lead != "" {
		sessionIDs = append(sessionIDs, lead)
	}
	for _, teammate := range teammates {
		if sessionID := strings.TrimSpace(teammate.SessionID); sessionID != "" {
			sessionIDs = append(sessionIDs, sessionID)
		}
	}
	runtimeStore := s.handler.getSessionRuntimeStore()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		busy := false
		for _, sessionID := range sessionIDs {
			state, stateErr := runtimeStore.LoadState(ctx, sessionID)
			if stateErr != nil || state == nil {
				continue
			}
			switch state.Status {
			case chat.SessionRunning, chat.SessionWaitingApproval, chat.SessionWaitingInput, chat.SessionRewinding:
				busy = true
			}
		}
		if !busy {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *handlerTeamLifecycleService) closeTerminalTeammates(ctx context.Context, teamID string) error {
	if s == nil || s.handler == nil {
		return nil
	}
	store := s.handler.getTeamStore()
	if store == nil {
		return nil
	}
	record, err := store.GetTeam(ctx, teamID)
	if err != nil {
		return err
	}
	if record == nil || record.Status == team.TeamStatusActive {
		return nil
	}
	leadSessionID := strings.TrimSpace(record.LeadSessionID)
	teammates, err := store.ListTeammates(ctx, teamID)
	if err != nil {
		return err
	}
	controller := s.handler.getAgentSessionController()
	var closeErr error
	for _, teammate := range teammates {
		sessionID := strings.TrimSpace(teammate.SessionID)
		if sessionID == "" || strings.EqualFold(sessionID, leadSessionID) ||
			strings.EqualFold(strings.TrimSpace(teammate.ID), "lead") {
			continue
		}
		if controller != nil {
			if _, err := controller.Close(ctx, sessionID); err != nil && closeErr == nil {
				closeErr = err
			}
			continue
		}
		s.handler.stopSessionForTerminalState(ctx, sessionID, "team_completed")
		if s.handler.sessionManager != nil {
			if err := s.handler.sessionManager.Close(ctx, sessionID); err != nil && err != chat.ErrSessionNotFound && closeErr == nil {
				closeErr = err
			}
		}
		s.handler.releaseSessionSupervisionObligations(ctx, sessionID)
	}
	return closeErr
}

func isReplayableTeamLifecycleEvent(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case "task.completed", "task.failed", "team.completed", "team.summary":
		return true
	default:
		return false
	}
}

func (s *handlerTeamLifecycleService) StopLoop(teamID string) {
	if s == nil || s.supervisor == nil {
		return
	}
	s.supervisor.StopLoop(teamID, "explicit_stop")
}

func (s *handlerTeamLifecycleService) StopAllLoops() {
	if s == nil || s.supervisor == nil {
		return
	}
	s.supervisor.Stop()
}

func (s *handlerTeamLifecycleService) HasLoop(teamID string) bool {
	return s != nil && s.supervisor != nil && s.supervisor.HasLoop(teamID)
}

func (s *handlerTeamLifecycleService) LoopCount() int {
	if s == nil || s.supervisor == nil {
		return 0
	}
	return s.supervisor.LoopCount()
}

func (s *handlerTeamLifecycleService) SupervisorSnapshot() teamsupervisor.Snapshot {
	if s == nil || s.supervisor == nil {
		return teamsupervisor.Snapshot{}
	}
	return s.supervisor.Snapshot()
}

package skills

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/teamsupervisor"
)

type handlerTeamLifecycleService struct {
	handler    *Handler
	supervisor *teamsupervisor.Supervisor
}

func newHandlerTeamLifecycleService(handler *Handler) *handlerTeamLifecycleService {
	if handler == nil {
		return nil
	}
	service := &handlerTeamLifecycleService{handler: handler}
	supervisorConfig := teamsupervisor.Config{}
	if handler.runtimeConfig != nil {
		config := handler.runtimeConfig.Team.Orchestrator
		supervisorConfig.ScanInterval = config.ReconcileInterval
		supervisorConfig.RestartBackoff = config.RestartBackoff
		supervisorConfig.MaxRestartBackoff = config.MaxRestartBackoff
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

// ownerAllowed is the single-host ownership boundary. Durable owner leases and
// fencing plug into this hook in P5 without changing the reconciler contract.
func (s *handlerTeamLifecycleService) ownerAllowed(context.Context, string) bool {
	return true
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
	}
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

package skills

import (
	"context"
	"strings"
	"sync"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/google/uuid"
)

type handlerTeamLifecycleService struct {
	handler     *Handler
	loopMu      sync.Mutex
	loopCancels map[string]context.CancelFunc
}

func newHandlerTeamLifecycleService(handler *Handler) *handlerTeamLifecycleService {
	if handler == nil {
		return nil
	}
	return &handlerTeamLifecycleService{
		handler:     handler,
		loopCancels: make(map[string]context.CancelFunc),
	}
}

func (h *Handler) teamLifecycleService() *handlerTeamLifecycleService {
	if h == nil {
		return nil
	}
	h.teamStoreMu.Lock()
	defer h.teamStoreMu.Unlock()
	if h.teamLifecycle == nil {
		h.teamLifecycle = newHandlerTeamLifecycleService(h)
	}
	return h.teamLifecycle
}

func (s *handlerTeamLifecycleService) SyncLoops() {
	if s == nil || s.handler == nil {
		return
	}
	store := s.handler.getTeamStore()
	if store == nil {
		s.StopAllLoops()
		return
	}
	orchestrator := s.handler.getTeamOrchestrator()
	if orchestrator == nil {
		return
	}
	teams, err := store.ListTeams(context.Background(), team.TeamFilter{
		Status: team.TeamStatusActive,
	})
	if err != nil {
		s.handler.publishRuntimeEvent("team.orchestrator.sync_failed", "trace_"+uuid.NewString(), map[string]interface{}{
			"error": err.Error(),
		})
		return
	}
	desired := make(map[string]struct{}, len(teams))
	for _, item := range teams {
		if strings.TrimSpace(item.ID) == "" {
			continue
		}
		desired[item.ID] = struct{}{}
	}

	toStop := make([]context.CancelFunc, 0)
	s.loopMu.Lock()
	if s.loopCancels == nil {
		s.loopCancels = make(map[string]context.CancelFunc)
	}
	for teamID, cancel := range s.loopCancels {
		if _, ok := desired[teamID]; !ok {
			toStop = append(toStop, cancel)
			delete(s.loopCancels, teamID)
		}
	}
	for teamID := range desired {
		if _, ok := s.loopCancels[teamID]; ok {
			continue
		}
		runCtx, cancel := context.WithCancel(context.Background())
		s.loopCancels[teamID] = cancel
		go s.runLoop(runCtx, orchestrator, teamID)
	}
	s.loopMu.Unlock()

	for _, cancel := range toStop {
		cancel()
	}
}

func (s *handlerTeamLifecycleService) runLoop(ctx context.Context, orchestrator *team.Orchestrator, teamID string) {
	err := orchestrator.Run(ctx, teamID)
	if err == nil && ctx.Err() == nil {
		s.ReplayStoredTerminalEvents(teamID)
	}
	s.loopMu.Lock()
	if s.loopCancels != nil {
		delete(s.loopCancels, teamID)
	}
	s.loopMu.Unlock()
	if err == nil || ctx.Err() != nil || s.handler == nil {
		return
	}
	s.handler.publishRuntimeEvent("team.orchestrator.stopped", "trace_"+uuid.NewString(), map[string]interface{}{
		"team_id": teamID,
		"error":   err.Error(),
	})
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
		if event.Type != "team.completed" && event.Type != "team.summary" {
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

func (s *handlerTeamLifecycleService) StopLoop(teamID string) {
	teamID = strings.TrimSpace(teamID)
	if s == nil || teamID == "" {
		return
	}
	var cancel context.CancelFunc
	s.loopMu.Lock()
	if s.loopCancels != nil {
		cancel = s.loopCancels[teamID]
		delete(s.loopCancels, teamID)
	}
	s.loopMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *handlerTeamLifecycleService) StopAllLoops() {
	if s == nil {
		return
	}
	s.loopMu.Lock()
	cancels := make([]context.CancelFunc, 0, len(s.loopCancels))
	for teamID, cancel := range s.loopCancels {
		if cancel != nil {
			cancels = append(cancels, cancel)
		}
		delete(s.loopCancels, teamID)
	}
	s.loopMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (s *handlerTeamLifecycleService) HasLoop(teamID string) bool {
	teamID = strings.TrimSpace(teamID)
	if s == nil || teamID == "" {
		return false
	}
	s.loopMu.Lock()
	defer s.loopMu.Unlock()
	_, ok := s.loopCancels[teamID]
	return ok
}

func (s *handlerTeamLifecycleService) LoopCount() int {
	if s == nil {
		return 0
	}
	s.loopMu.Lock()
	defer s.loopMu.Unlock()
	return len(s.loopCancels)
}


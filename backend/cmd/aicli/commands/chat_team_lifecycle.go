package commands

import (
	"context"
	"strings"
	"sync"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

type teamLifecycleService interface {
	Apply(event runtimeevents.Event)
	PublishStoredTerminalEvents(teamID string)
	WaitForTerminal(ctx context.Context, teamID string) error
	RunSettled(ctx context.Context, teamID string) (bool, error)
	Pending(ctx context.Context, teamID string) bool
	SyncLoops()
	StopLoops()
}

type localTeamLifecycleService struct {
	Host        *localChatRuntimeHost
	loopMu      sync.Mutex
	loopCancels map[string]context.CancelFunc
}

const terminalTeammateCleanupWaitTimeout = 10 * time.Second

func newLocalTeamLifecycleService(host *localChatRuntimeHost) *localTeamLifecycleService {
	if host == nil {
		return nil
	}
	return &localTeamLifecycleService{
		Host:        host,
		loopCancels: make(map[string]context.CancelFunc),
	}
}

func (c *localTeamLifecycleService) Apply(event runtimeevents.Event) {
	if c == nil || c.Host == nil {
		return
	}
	teamID := strings.TrimSpace(payloadStringValue(event.Payload["team_id"]))
	switch strings.TrimSpace(event.Type) {
	case "team.summary":
		c.Host.mirrorTeamSummaryToBaseSession(teamID, payloadStringValue(event.Payload["summary"]))
	case "team.completed":
		c.closeTerminalTeammatesAsync(teamID)
	}
}

func (c *localTeamLifecycleService) PublishStoredTerminalEvents(teamID string) {
	if c == nil || c.Host == nil || c.Host.TeamStore == nil {
		return
	}
	record, err := c.Host.TeamStore.GetTeam(context.Background(), strings.TrimSpace(teamID))
	if err != nil || record == nil || record.Status == team.TeamStatusActive {
		return
	}
	events, err := c.Host.TeamStore.ListTeamEvents(context.Background(), team.TeamEventFilter{
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
		c.Host.dispatchTeamLifecycleEvent(event.TeamEvent, false)
	}
}

func (c *localTeamLifecycleService) WaitForTerminal(ctx context.Context, teamID string) error {
	if c == nil || c.Host == nil || c.Host.TeamStore == nil {
		return nil
	}
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return nil
	}
	ticker := time.NewTicker(noInteractiveTeamDrainPollInterval)
	defer ticker.Stop()

	for {
		settled, err := c.RunSettled(ctx, teamID)
		if err == nil && settled {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *localTeamLifecycleService) Pending(ctx context.Context, teamID string) bool {
	teamID = strings.TrimSpace(teamID)
	if c == nil || c.Host == nil || teamID == "" {
		return false
	}
	if c.Host.TeamStore == nil {
		return c.hasTeamLoop(teamID)
	}
	settled, err := c.RunSettled(ctx, teamID)
	if err == nil {
		return !settled
	}
	return c.hasTeamLoop(teamID)
}

func (c *localTeamLifecycleService) SyncLoops() {
	if c == nil || c.Host == nil || c.Host.TeamStore == nil || c.Host.Orchestrator == nil {
		return
	}
	teams, err := c.Host.TeamStore.ListTeams(context.Background(), team.TeamFilter{
		Status: team.TeamStatusActive,
	})
	if err != nil {
		return
	}
	leadSessionID := c.hostLeadSessionID()
	activeTeamID := c.hostActiveTeamID()
	desired := make(map[string]struct{}, len(teams))
	for _, item := range teams {
		teamID := strings.TrimSpace(item.ID)
		if teamID == "" {
			continue
		}
		if !teamBelongsToHostLead(item, leadSessionID, activeTeamID) {
			continue
		}
		desired[teamID] = struct{}{}
	}

	toStop := make([]context.CancelFunc, 0)
	c.loopMu.Lock()
	if c.loopCancels == nil {
		c.loopCancels = make(map[string]context.CancelFunc)
	}
	for teamID, cancel := range c.loopCancels {
		if _, ok := desired[teamID]; !ok {
			toStop = append(toStop, cancel)
			delete(c.loopCancels, teamID)
		}
	}
	for teamID := range desired {
		if _, ok := c.loopCancels[teamID]; ok {
			continue
		}
		runCtx, cancel := context.WithCancel(context.Background())
		c.loopCancels[teamID] = cancel
		go c.runTeamLoop(runCtx, teamID)
	}
	c.loopMu.Unlock()

	for _, cancel := range toStop {
		cancel()
	}
}

func (c *localTeamLifecycleService) StopLoops() {
	if c == nil || c.Host == nil {
		return
	}
	c.loopMu.Lock()
	cancels := make([]context.CancelFunc, 0, len(c.loopCancels))
	for teamID, cancel := range c.loopCancels {
		if cancel != nil {
			cancels = append(cancels, cancel)
		}
		delete(c.loopCancels, teamID)
	}
	c.loopMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (c *localTeamLifecycleService) StopLoop(teamID string) {
	teamID = strings.TrimSpace(teamID)
	if c == nil || c.Host == nil || teamID == "" {
		return
	}
	var cancel context.CancelFunc
	c.loopMu.Lock()
	if c.loopCancels != nil {
		cancel = c.loopCancels[teamID]
		delete(c.loopCancels, teamID)
	}
	c.loopMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (c *localTeamLifecycleService) RunSettled(ctx context.Context, teamID string) (bool, error) {
	if c == nil || c.Host == nil || c.Host.TeamStore == nil {
		return true, nil
	}
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return true, nil
	}
	record, err := c.Host.TeamStore.GetTeam(ctx, teamID)
	if err != nil {
		return false, err
	}
	if record == nil {
		return true, nil
	}
	if record.Status == team.TeamStatusActive {
		return false, nil
	}
	if c.hasTeamLoop(teamID) {
		return false, nil
	}
	if c.Host.RuntimeStore == nil {
		return true, nil
	}

	sessionIDs, err := c.teamSessionIDs(ctx, teamID)
	if err != nil {
		return false, err
	}
	for _, sessionID := range sessionIDs {
		state, stateErr := c.Host.RuntimeStore.LoadState(ctx, sessionID)
		if stateErr != nil || state == nil {
			continue
		}
		if shouldIgnoreAmbientTeamRuntimeState(state, teamID) {
			continue
		}
		switch state.Status {
		case runtimechat.SessionRunning, runtimechat.SessionWaitingApproval, runtimechat.SessionWaitingInput, runtimechat.SessionRewinding:
			return false, nil
		}
	}
	return true, nil
}

func (c *localTeamLifecycleService) closeTerminalTeammatesAsync(teamID string) {
	if c == nil || c.Host == nil || strings.TrimSpace(teamID) == "" {
		return
	}
	go func() {
		waitCtx, cancel := context.WithTimeout(context.Background(), terminalTeammateCleanupWaitTimeout)
		_ = c.waitForTerminalCleanupReady(waitCtx, teamID)
		cancel()
		_ = c.closeTerminalTeammates(context.Background(), teamID)
	}()
}

func (c *localTeamLifecycleService) waitForTerminalCleanupReady(ctx context.Context, teamID string) error {
	teamID = strings.TrimSpace(teamID)
	if c == nil || c.Host == nil || teamID == "" {
		return nil
	}
	ticker := time.NewTicker(noInteractiveTeamDrainPollInterval)
	defer ticker.Stop()
	for {
		settled, err := c.RunSettled(ctx, teamID)
		if err == nil && settled {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *localTeamLifecycleService) closeTerminalTeammates(ctx context.Context, teamID string) error {
	if c == nil || c.Host == nil || c.Host.TeamStore == nil {
		return nil
	}
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return nil
	}
	record, err := c.Host.TeamStore.GetTeam(ctx, teamID)
	if err != nil {
		return err
	}
	if record == nil || record.Status == team.TeamStatusActive {
		return nil
	}
	leadSessionID := strings.TrimSpace(record.LeadSessionID)
	teammates, err := c.Host.TeamStore.ListTeammates(ctx, teamID)
	if err != nil {
		return err
	}
	var closeErr error
	for _, teammate := range teammates {
		sessionID := strings.TrimSpace(teammate.SessionID)
		if sessionID == "" {
			continue
		}
		if leadSessionID != "" && strings.EqualFold(sessionID, leadSessionID) {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(teammate.ID), "lead") {
			continue
		}
		if err := c.closeLocalRuntimeSession(ctx, sessionID); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	return closeErr
}

func (c *localTeamLifecycleService) closeLocalRuntimeSession(ctx context.Context, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if c == nil || c.Host == nil || sessionID == "" {
		return nil
	}
	if c.Host.ActorRegistry != nil {
		_, err := c.Host.ActorRegistry.Close(ctx, sessionID)
		return err
	}
	if c.Host.SessionHub != nil {
		c.Host.SessionHub.Stop(sessionID)
	}
	if c.Host.SessionStore != nil {
		if err := c.Host.SessionStore.Close(ctx, sessionID); err != nil && err != runtimechat.ErrSessionNotFound {
			return err
		}
	}
	return nil
}

func (c *localTeamLifecycleService) teamSessionIDs(ctx context.Context, teamID string) ([]string, error) {
	if c == nil || c.Host == nil || c.Host.TeamStore == nil {
		return nil, nil
	}
	record, err := c.Host.TeamStore.GetTeam(ctx, teamID)
	if err != nil {
		return nil, err
	}

	sessionIDs := map[string]struct{}{}
	if record != nil && strings.TrimSpace(record.LeadSessionID) != "" {
		sessionIDs[strings.TrimSpace(record.LeadSessionID)] = struct{}{}
	}
	teammates, err := c.Host.TeamStore.ListTeammates(ctx, teamID)
	if err != nil {
		return nil, err
	}
	for _, teammate := range teammates {
		if sessionID := strings.TrimSpace(teammate.SessionID); sessionID != "" {
			sessionIDs[sessionID] = struct{}{}
		}
	}

	resolved := make([]string, 0, len(sessionIDs))
	for sessionID := range sessionIDs {
		resolved = append(resolved, sessionID)
	}
	return resolved, nil
}

func (c *localTeamLifecycleService) hostLeadSessionID() string {
	if c == nil || c.Host == nil || c.Host.BaseSession == nil || c.Host.BaseSession.RuntimeSession == nil {
		return ""
	}
	return strings.TrimSpace(c.Host.BaseSession.RuntimeSession.ID)
}

func (c *localTeamLifecycleService) hostActiveTeamID() string {
	if c == nil || c.Host == nil || c.Host.BaseSession == nil || c.Host.BaseSession.ActiveTeam == nil {
		return ""
	}
	return strings.TrimSpace(c.Host.BaseSession.ActiveTeam.TeamID)
}

func teamBelongsToHostLead(record team.Team, leadSessionID, activeTeamID string) bool {
	teamID := strings.TrimSpace(record.ID)
	leadSessionID = strings.TrimSpace(leadSessionID)
	activeTeamID = strings.TrimSpace(activeTeamID)
	recordLeadSessionID := strings.TrimSpace(record.LeadSessionID)
	if leadSessionID == "" {
		if activeTeamID == "" {
			return true
		}
		return strings.EqualFold(teamID, activeTeamID)
	}
	if strings.EqualFold(recordLeadSessionID, leadSessionID) {
		return true
	}
	return recordLeadSessionID == "" && activeTeamID != "" && strings.EqualFold(teamID, activeTeamID)
}

func (c *localTeamLifecycleService) hasTeamLoop(teamID string) bool {
	teamID = strings.TrimSpace(teamID)
	if c == nil || c.Host == nil || teamID == "" {
		return false
	}
	c.loopMu.Lock()
	defer c.loopMu.Unlock()
	if c.loopCancels == nil {
		return false
	}
	_, ok := c.loopCancels[teamID]
	return ok
}

func (c *localTeamLifecycleService) runTeamLoop(ctx context.Context, teamID string) {
	if c == nil || c.Host == nil || c.Host.Orchestrator == nil {
		return
	}
	_ = c.Host.Orchestrator.Run(ctx, teamID)
	c.loopMu.Lock()
	if c.loopCancels != nil {
		delete(c.loopCancels, teamID)
	}
	c.loopMu.Unlock()
}

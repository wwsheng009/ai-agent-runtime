package commands

import (
	"context"
	"fmt"
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
	loopSignals map[string]chan struct{}
}

const terminalTeammateCleanupWaitTimeout = 10 * time.Second

func newLocalTeamLifecycleService(host *localChatRuntimeHost) *localTeamLifecycleService {
	if host == nil {
		return nil
	}
	return &localTeamLifecycleService{
		Host:        host,
		loopCancels: make(map[string]context.CancelFunc),
		loopSignals: make(map[string]chan struct{}),
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
		if !isReplayableTeamLifecycleEvent(event.Type) {
			continue
		}
		c.Host.dispatchTeamLifecycleEvent(event.TeamEvent, false)
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
			c.PublishStoredTerminalEvents(teamID)
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
		c.ensureRunnableTeam(context.Background(), item)
		desired[teamID] = struct{}{}
	}

	toStop := make([]context.CancelFunc, 0)
	c.loopMu.Lock()
	if c.loopCancels == nil {
		c.loopCancels = make(map[string]context.CancelFunc)
	}
	if c.loopSignals == nil {
		c.loopSignals = make(map[string]chan struct{})
	}
	for teamID, cancel := range c.loopCancels {
		if _, ok := desired[teamID]; !ok {
			toStop = append(toStop, cancel)
			delete(c.loopCancels, teamID)
			delete(c.loopSignals, teamID)
		}
	}
	for teamID := range desired {
		if _, ok := c.loopCancels[teamID]; ok {
			c.signalTeamLoopLocked(teamID)
			continue
		}
		runCtx, cancel := context.WithCancel(context.Background())
		wake := make(chan struct{}, 1)
		c.loopCancels[teamID] = cancel
		c.loopSignals[teamID] = wake
		go c.runTeamLoop(runCtx, teamID, wake)
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
		delete(c.loopSignals, teamID)
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
	if c.loopSignals != nil {
		delete(c.loopSignals, teamID)
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
	if record.Status == team.TeamStatusDone {
		summaryReady, err := c.terminalSummaryReady(ctx, teamID)
		if err != nil {
			return false, err
		}
		if !summaryReady {
			return false, nil
		}
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

func (c *localTeamLifecycleService) terminalSummaryReady(ctx context.Context, teamID string) (bool, error) {
	if c == nil || c.Host == nil || c.Host.TeamStore == nil {
		return true, nil
	}
	if c.Host.Orchestrator == nil || c.Host.Orchestrator.LeadPlanner == nil {
		return true, nil
	}
	events, err := c.Host.TeamStore.ListTeamEvents(ctx, team.TeamEventFilter{
		TeamID:    strings.TrimSpace(teamID),
		EventType: "team.summary*",
		Limit:     1,
	})
	if err != nil {
		return false, err
	}
	return len(events) > 0, nil
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

func (c *localTeamLifecycleService) ensureRunnableTeam(ctx context.Context, record team.Team) {
	if c == nil || c.Host == nil || c.Host.TeamStore == nil {
		return
	}
	teamID := strings.TrimSpace(record.ID)
	if teamID == "" {
		return
	}
	teammates, err := c.Host.TeamStore.ListTeammates(ctx, teamID)
	if err != nil || len(teammates) > 0 {
		return
	}
	tasks, err := c.Host.TeamStore.ListTasks(ctx, team.TaskFilter{
		TeamID: teamID,
		Status: []team.TaskStatus{team.TaskStatusPending, team.TaskStatusReady, team.TaskStatusRunning},
	})
	if err != nil || len(tasks) == 0 {
		return
	}
	for _, teammate := range synthesizeLocalRunnableTeammates(teamID, tasks, record.MaxTeammates) {
		_, _ = c.Host.TeamStore.UpsertTeammate(ctx, teammate)
	}
}

func synthesizeLocalRunnableTeammates(teamID string, tasks []team.Task, maxTeammates int) []team.Teammate {
	seen := make(map[string]struct{}, len(tasks))
	teammates := make([]team.Teammate, 0, len(tasks))
	for _, task := range tasks {
		if task.Assignee == nil {
			continue
		}
		assignee := strings.TrimSpace(*task.Assignee)
		if assignee == "" {
			continue
		}
		key := strings.ToLower(assignee)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		teammates = append(teammates, buildSyntheticLocalTeammate(teamID, assignee, assignee))
	}
	if len(teammates) > 0 {
		return teammates
	}

	count := len(tasks)
	if maxTeammates > 0 && count > maxTeammates {
		count = maxTeammates
	}
	for index := 0; index < count; index++ {
		id := fmt.Sprintf("mate-%d", index+1)
		teammates = append(teammates, buildSyntheticLocalTeammate(teamID, id, fmt.Sprintf("Teammate %d", index+1)))
	}
	return teammates
}

func buildSyntheticLocalTeammate(teamID, id, name string) team.Teammate {
	segment := normalizeLocalSessionSegment(firstNonEmptyChatValue(id, name))
	if segment == "" {
		segment = "mate"
	}
	return team.Teammate{
		ID:        strings.TrimSpace(id),
		TeamID:    strings.TrimSpace(teamID),
		Name:      strings.TrimSpace(name),
		SessionID: strings.TrimSpace(teamID) + "__" + segment,
		State:     team.TeammateStateIdle,
	}
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

func (c *localTeamLifecycleService) signalTeamLoopLocked(teamID string) {
	if c == nil || c.loopSignals == nil {
		return
	}
	wake := c.loopSignals[strings.TrimSpace(teamID)]
	if wake == nil {
		return
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (c *localTeamLifecycleService) runTeamLoop(ctx context.Context, teamID string, wake <-chan struct{}) {
	if c == nil || c.Host == nil || c.Host.Orchestrator == nil {
		return
	}
	_ = c.Host.Orchestrator.RunWithWake(ctx, teamID, wake)
	c.loopMu.Lock()
	if c.loopCancels != nil {
		delete(c.loopCancels, teamID)
	}
	if c.loopSignals != nil {
		delete(c.loopSignals, teamID)
	}
	c.loopMu.Unlock()
}

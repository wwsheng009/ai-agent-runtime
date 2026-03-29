package commands

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

type localActorRegistry struct {
	Host *localChatRuntimeHost
}

func newLocalActorRegistry(host *localChatRuntimeHost) *localActorRegistry {
	return &localActorRegistry{Host: host}
}

func (r *localActorRegistry) SubmitPrompt(ctx context.Context, sessionID, prompt string, runMeta *team.RunMeta) (*team.SessionResult, error) {
	if r == nil || r.Host == nil || r.Host.SessionHub == nil {
		return nil, fmt.Errorf("session hub not configured")
	}
	if err := r.ensureSession(ctx, sessionID); err != nil {
		return nil, err
	}
	actor, err := r.Host.SessionHub.GetOrCreate(strings.TrimSpace(sessionID))
	if err != nil {
		return nil, err
	}
	result, err := actor.SubmitPrompt(ctx, prompt, runMeta)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("session result is nil")
	}
	return &team.SessionResult{
		Success:      result.Success,
		Output:       result.Output,
		Error:        result.Error,
		TraceID:      result.TraceID,
		Steps:        result.Steps,
		Observations: team.SessionObservationsFromRuntime(result.Observations),
	}, nil
}

func (r *localActorRegistry) DispatchTeamMailboxMessage(ctx context.Context, message team.MailMessage) error {
	if r == nil || r.Host == nil || r.Host.TeamStore == nil || r.Host.SessionHub == nil {
		return nil
	}
	targets, err := r.resolveMailboxSessionTargets(ctx, message)
	if err != nil {
		return err
	}
	for _, sessionID := range targets {
		if err := r.ensureSession(ctx, sessionID); err != nil {
			return err
		}
		actor, actorErr := r.Host.SessionHub.GetOrCreate(sessionID)
		if actorErr != nil {
			return actorErr
		}
		if err := actor.DeliverMailboxMessage(ctx, message); err != nil {
			return err
		}
	}
	return nil
}

func (r *localActorRegistry) EnsureTeammateSessionIDs(teamID string, specs []toolbroker.SpawnTeammateSpec) []toolbroker.SpawnTeammateSpec {
	return ensureTeammateSessionIDs(teamID, specs)
}

func (r *localActorRegistry) Spawn(ctx context.Context, parentSessionID string, args toolbroker.SpawnAgentArgs) (*toolbroker.AgentStatusResult, error) {
	if r == nil || r.Host == nil || r.Host.SessionStore == nil || r.Host.SessionHub == nil {
		return nil, fmt.Errorf("session runtime host is not configured")
	}
	sessionID := firstNonEmptyChatValue(strings.TrimSpace(args.ID), strings.TrimSpace(args.SessionID))
	parentSessionID = strings.TrimSpace(parentSessionID)

	var parentSession *runtimechat.Session
	if parentSessionID != "" {
		parent, err := r.Host.SessionStore.Load(ctx, parentSessionID)
		if err == nil {
			parentSession = parent
		} else if err != runtimechat.ErrSessionNotFound {
			return nil, err
		}
	}

	if sessionID != "" {
		existing, err := r.Host.SessionStore.Load(ctx, sessionID)
		if err == nil && existing != nil {
			return nil, fmt.Errorf("session already exists: %s", sessionID)
		}
		if err != nil && err != runtimechat.ErrSessionNotFound {
			return nil, err
		}
	}

	userID := firstNonEmptyChatValue(strings.TrimSpace(r.Host.SessionUser), "agent")
	var childSession *runtimechat.Session
	forkContext := args.ForkContext != nil && *args.ForkContext
	if forkContext && parentSession != nil {
		childSession = parentSession.Clone()
		if sessionID == "" {
			sessionID = runtimechat.NewSession(userID).ID
		}
		childSession.ID = sessionID
		childSession.UserID = userID
		childSession.UpdateState(runtimechat.StateActive)
	} else {
		childSession = runtimechat.NewSession(userID)
		if sessionID == "" {
			sessionID = childSession.ID
		} else {
			childSession.ID = sessionID
		}
	}
	childSession.SetContext(toolbroker.AgentSessionContextParentSessionID, parentSessionID)
	if agentType := strings.TrimSpace(args.AgentType); agentType != "" {
		childSession.SetContext(toolbroker.AgentSessionContextAgentType, agentType)
	}
	if model := strings.TrimSpace(args.Model); model != "" {
		childSession.SetContext(toolbroker.AgentSessionContextRequestedModel, model)
	}
	if err := r.Host.SessionStore.Save(ctx, childSession); err != nil {
		return nil, err
	}

	actor, err := r.Host.SessionHub.GetOrCreate(sessionID)
	if err != nil {
		return nil, err
	}
	queued := false
	if message := strings.TrimSpace(args.Message); message != "" {
		if err := actor.SubmitPromptAsync(ctx, message, nil); err != nil {
			return nil, err
		}
		queued = true
	}
	result, err := r.agentSnapshot(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	result.Created = true
	result.Queued = queued
	return result, nil
}

func (r *localActorRegistry) SendInput(ctx context.Context, args toolbroker.SendAgentInputArgs) (*toolbroker.AgentStatusResult, error) {
	if r == nil || r.Host == nil || r.Host.SessionHub == nil {
		return nil, fmt.Errorf("session hub not configured")
	}
	sessionID := firstNonEmptyChatValue(strings.TrimSpace(args.ID), strings.TrimSpace(args.SessionID))
	if sessionID == "" {
		return nil, fmt.Errorf("id is required")
	}
	message := strings.TrimSpace(args.Message)
	if message == "" {
		return nil, fmt.Errorf("message is required")
	}
	if err := r.ensureSession(ctx, sessionID); err != nil {
		return nil, err
	}
	actor, err := r.Host.SessionHub.GetOrCreate(sessionID)
	if err != nil {
		return nil, err
	}
	if state := actor.State(); state != nil {
		busy := state.Status == runtimechat.SessionRunning || state.Status == runtimechat.SessionRewinding || state.Status == runtimechat.SessionWaitingApproval || state.Status == runtimechat.SessionWaitingInput
		if busy {
			interrupt := args.Interrupt != nil && *args.Interrupt
			if !interrupt {
				return nil, fmt.Errorf("session is busy (%s)", state.Status)
			}
			if err := actor.Interrupt(ctx); err != nil {
				return nil, err
			}
		}
	}
	if err := actor.SubmitPromptAsync(ctx, message, nil); err != nil {
		return nil, err
	}
	result, err := r.agentSnapshot(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	result.Queued = true
	return result, nil
}

func (r *localActorRegistry) Wait(ctx context.Context, args toolbroker.WaitAgentArgs) (*toolbroker.AgentWaitResult, error) {
	sessionIDs := normalizeLocalAgentWaitIDs(args)
	if len(sessionIDs) == 0 {
		return nil, fmt.Errorf("id is required")
	}
	timeout := time.Duration(args.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		snapshots := make([]toolbroker.AgentStatusResult, 0, len(sessionIDs))
		var matched *toolbroker.AgentStatusResult
		readyCount := 0
		for _, sessionID := range sessionIDs {
			result, err := r.agentSnapshot(waitCtx, sessionID)
			if err != nil {
				return nil, err
			}
			if result == nil {
				continue
			}
			snapshots = append(snapshots, *result)
			if isLocalAgentWaitReady(result.Status) {
				readyCount++
				if matched == nil {
					cloned := *result
					matched = &cloned
				}
			}
		}
		waitResult := &toolbroker.AgentWaitResult{
			Agents:       snapshots,
			ReadyCount:   readyCount,
			PendingCount: len(snapshots) - readyCount,
		}
		if matched != nil {
			waitResult.Agent = matched
			waitResult.MatchedID = matched.ID
			waitResult.MatchedSessionID = matched.SessionID
			return waitResult, nil
		}
		select {
		case <-waitCtx.Done():
			waitResult.TimedOut = true
			return waitResult, nil
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (r *localActorRegistry) ReadEvents(ctx context.Context, args toolbroker.ReadAgentEventsArgs) (*toolbroker.AgentEventsResult, error) {
	if r == nil || r.Host == nil || r.Host.EventStore == nil {
		return nil, fmt.Errorf("event store not configured")
	}
	sessionID := firstNonEmptyChatValue(strings.TrimSpace(args.ID), strings.TrimSpace(args.SessionID))
	if sessionID == "" {
		return nil, fmt.Errorf("id is required")
	}
	limit := args.Limit
	if limit <= 0 {
		limit = 20
	}
	waitMs := args.WaitMs
	if waitMs < 0 {
		waitMs = 0
	}
	readCtx := ctx
	cancel := func() {}
	if waitMs > 0 {
		readCtx, cancel = context.WithTimeout(ctx, time.Duration(waitMs)*time.Millisecond)
	}
	defer cancel()
	for {
		events, err := r.Host.EventStore.ListEvents(readCtx, sessionID, args.AfterSeq, limit)
		if err != nil {
			return nil, err
		}
		if len(events) > 0 || waitMs == 0 {
			return buildLocalAgentEventsResult(sessionID, events), nil
		}
		select {
		case <-readCtx.Done():
			result := buildLocalAgentEventsResult(sessionID, nil)
			result.TimedOut = true
			return result, nil
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (r *localActorRegistry) Close(ctx context.Context, sessionID string) (*toolbroker.AgentStatusResult, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("id is required")
	}
	if r != nil && r.Host != nil && r.Host.SessionHub != nil {
		r.Host.SessionHub.Stop(sessionID)
	}
	if r != nil && r.Host != nil && r.Host.SessionStore != nil {
		if session, err := r.Host.SessionStore.Load(ctx, sessionID); err == nil && session != nil {
			session.UpdateState(runtimechat.StateClosed)
			_ = r.Host.SessionStore.Update(ctx, session)
		}
	}
	result, err := r.agentSnapshot(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	result.Status = string(runtimechat.SessionStopped)
	return result, nil
}

func (r *localActorRegistry) Resume(ctx context.Context, sessionID string) (*toolbroker.AgentStatusResult, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("id is required")
	}
	if err := r.ensureSession(ctx, sessionID); err != nil {
		return nil, err
	}
	if _, err := r.Host.SessionHub.GetOrCreate(sessionID); err != nil {
		return nil, err
	}
	return r.agentSnapshot(ctx, sessionID)
}

func (r *localActorRegistry) agentSnapshot(ctx context.Context, sessionID string) (*toolbroker.AgentStatusResult, error) {
	result := &toolbroker.AgentStatusResult{
		ID:        sessionID,
		SessionID: sessionID,
		Status:    "missing",
	}
	if r == nil || r.Host == nil || r.Host.SessionStore == nil {
		return result, nil
	}
	session, err := r.Host.SessionStore.Load(ctx, sessionID)
	if err != nil {
		if err == runtimechat.ErrSessionNotFound {
			return result, nil
		}
		return nil, err
	}
	if session != nil {
		result.Exists = true
		result.MessageCount = len(session.GetMessages())
		result.SessionState = string(session.State)
		if value, ok := session.GetContext(toolbroker.AgentSessionContextParentSessionID); ok {
			if text, ok := value.(string); ok {
				result.ParentSessionID = strings.TrimSpace(text)
			}
		}
		if value, ok := session.GetContext(toolbroker.AgentSessionContextAgentType); ok {
			if text, ok := value.(string); ok {
				result.AgentType = strings.TrimSpace(text)
			}
		}
		messages := session.GetMessages()
		for i := len(messages) - 1; i >= 0; i-- {
			if result.LastMessageRole == "" {
				result.LastMessageRole = strings.TrimSpace(messages[i].Role)
				result.LastMessagePreview = truncateLocalAgentStatusPreview(messages[i].Content)
			}
			if messages[i].Role == "assistant" {
				result.Output = strings.TrimSpace(messages[i].Content)
				break
			}
		}
		result.Status = string(runtimechat.SessionIdle)
	}
	if r.Host.SessionHub != nil {
		if actor, ok := r.Host.SessionHub.Get(sessionID); ok && actor != nil {
			state := actor.State()
			if state != nil {
				result.Status = string(state.Status)
				result.PendingApproval = state.PendingApproval != nil
				result.PendingQuestion = state.PendingQuestion != nil
				result.CurrentTurnID = strings.TrimSpace(state.CurrentTurnID)
				if state.PendingTool != nil {
					result.PendingToolName = strings.TrimSpace(state.PendingTool.ToolName)
					result.PendingToolCallID = strings.TrimSpace(state.PendingTool.ToolCallID)
				}
			}
		}
	}
	return result, nil
}

func normalizeLocalAgentWaitIDs(args toolbroker.WaitAgentArgs) []string {
	seen := make(map[string]struct{})
	ordered := make([]string, 0, 1+len(args.IDs)+len(args.SessionIDs))
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, exists := seen[value]; exists {
			return
		}
		seen[value] = struct{}{}
		ordered = append(ordered, value)
	}
	add(args.ID)
	add(args.SessionID)
	for _, value := range args.IDs {
		add(value)
	}
	for _, value := range args.SessionIDs {
		add(value)
	}
	return ordered
}

func isLocalAgentWaitReady(status string) bool {
	switch strings.TrimSpace(status) {
	case string(runtimechat.SessionIdle), string(runtimechat.SessionWaitingApproval), string(runtimechat.SessionWaitingInput), string(runtimechat.SessionStopped), "missing":
		return true
	default:
		return false
	}
}

func truncateLocalAgentStatusPreview(content string) string {
	content = strings.Join(strings.Fields(strings.TrimSpace(content)), " ")
	if len(content) <= 160 {
		return content
	}
	return content[:157] + "..."
}

func buildLocalAgentEventsResult(sessionID string, events []runtimeevents.Event) *toolbroker.AgentEventsResult {
	result := &toolbroker.AgentEventsResult{
		SessionID: strings.TrimSpace(sessionID),
		Count:     len(events),
	}
	if len(events) == 0 {
		return result
	}
	items := make([]toolbroker.AgentEventItem, 0, len(events))
	var latestSeq int64
	for _, event := range events {
		item := toolbroker.AgentEventItem{
			Type:      event.Type,
			TraceID:   event.TraceID,
			SessionID: event.SessionID,
			ToolName:  event.ToolName,
			AgentName: event.AgentName,
			Timestamp: event.Timestamp,
			Payload:   cloneRuntimeEventPayload(event.Payload),
		}
		if seq := localAgentEventSeq(event); seq > 0 {
			item.Seq = seq
			if seq > latestSeq {
				latestSeq = seq
			}
		}
		items = append(items, item)
	}
	result.Events = items
	result.LatestSeq = latestSeq
	return result
}

func localAgentEventSeq(event runtimeevents.Event) int64 {
	if event.Payload == nil {
		return 0
	}
	switch value := event.Payload["seq"].(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	default:
		return 0
	}
}

func cloneRuntimeEventPayload(payload map[string]interface{}) map[string]interface{} {
	if len(payload) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(payload))
	for key, value := range payload {
		cloned[key] = value
	}
	return cloned
}

func (r *localActorRegistry) ensureSession(ctx context.Context, sessionID string) error {
	if r == nil || r.Host == nil || r.Host.SessionStore == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("session id is required")
	}
	existing, err := r.Host.SessionStore.Load(ctx, sessionID)
	if err == nil && existing != nil {
		return nil
	}
	if err != nil && err != runtimechat.ErrSessionNotFound {
		return err
	}

	runtimeSession := runtimechat.NewSession(strings.TrimSpace(r.Host.SessionUser))
	runtimeSession.ID = sessionID
	if r.Host.BaseSession != nil {
		if prompt := strings.TrimSpace(r.Host.BaseSession.SystemPromptText); prompt != "" {
			runtimeSession.AddMessage(*runtimetypes.NewSystemMessage(prompt))
		}
		runtimeSession.SetContext(chatRuntimeContextProviderName, strings.TrimSpace(r.Host.BaseSession.ProviderName))
		runtimeSession.SetContext(chatRuntimeContextProtocol, strings.TrimSpace(r.Host.BaseSession.Provider.GetProtocol()))
		runtimeSession.SetContext(chatRuntimeContextModel, strings.TrimSpace(r.Host.BaseSession.Model))
	}
	return r.Host.SessionStore.Save(ctx, runtimeSession)
}

func (r *localActorRegistry) resolveMailboxSessionTargets(ctx context.Context, message team.MailMessage) ([]string, error) {
	teamID := strings.TrimSpace(message.TeamID)
	if teamID == "" {
		return nil, nil
	}
	record, err := r.Host.TeamStore.GetTeam(ctx, teamID)
	if err != nil {
		return nil, err
	}
	targets := make(map[string]struct{})
	addTarget := func(sessionID string) {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			return
		}
		targets[sessionID] = struct{}{}
	}

	switch toAgent := strings.TrimSpace(message.ToAgent); toAgent {
	case "", "*":
		if record != nil {
			addTarget(record.LeadSessionID)
		}
		teammates, err := r.Host.TeamStore.ListTeammates(ctx, teamID)
		if err != nil {
			return nil, err
		}
		for _, mate := range teammates {
			addTarget(mate.SessionID)
		}
	case "lead":
		if record != nil {
			addTarget(record.LeadSessionID)
		}
	default:
		mate, err := r.Host.TeamStore.GetTeammate(ctx, toAgent)
		if err != nil {
			return nil, err
		}
		if mate != nil && strings.TrimSpace(mate.TeamID) == teamID {
			addTarget(mate.SessionID)
		}
	}

	resolved := make([]string, 0, len(targets))
	for sessionID := range targets {
		resolved = append(resolved, sessionID)
	}
	sort.Strings(resolved)
	return resolved, nil
}

func ensureTeammateSessionIDs(teamID string, specs []toolbroker.SpawnTeammateSpec) []toolbroker.SpawnTeammateSpec {
	if len(specs) == 0 {
		return nil
	}
	resolved := make([]toolbroker.SpawnTeammateSpec, len(specs))
	copy(resolved, specs)
	seen := make(map[string]int, len(specs))
	for i := range resolved {
		if strings.EqualFold(strings.TrimSpace(resolved[i].SessionID), "current") {
			resolved[i].SessionID = ""
		}
		if strings.TrimSpace(resolved[i].SessionID) != "" {
			continue
		}
		base := normalizeLocalSessionSegment(firstNonEmptyChatValue(resolved[i].ID, resolved[i].Name))
		if base == "" {
			base = fmt.Sprintf("mate_%02d", i+1)
		}
		candidate := fmt.Sprintf("%s__%s", strings.TrimSpace(teamID), base)
		seen[candidate]++
		if seen[candidate] > 1 {
			candidate = fmt.Sprintf("%s_%d", candidate, seen[candidate])
		}
		resolved[i].SessionID = candidate
	}
	return resolved
}

var localSessionSegmentPattern = regexp.MustCompile(`[^a-z0-9]+`)

func normalizeLocalSessionSegment(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	value = localSessionSegmentPattern.ReplaceAllString(value, "_")
	return strings.Trim(value, "_")
}

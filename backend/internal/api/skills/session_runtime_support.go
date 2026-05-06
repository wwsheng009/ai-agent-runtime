package skills

import (
	"context"
	stderrors "errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

type sessionActorClient struct {
	hub *chat.SessionHub
}

func (h *Handler) getAgentSessionController() *sessionAgentController {
	if h == nil || h.sessionManager == nil {
		return nil
	}
	hub := h.getSessionHub()
	if hub == nil {
		return nil
	}
	return &sessionAgentController{handler: h}
}

func (c *sessionActorClient) SubmitPrompt(ctx context.Context, sessionID, prompt string, runMeta *team.RunMeta) (*team.SessionResult, error) {
	if c == nil || c.hub == nil {
		return nil, fmt.Errorf("session hub not configured")
	}
	actor, err := c.hub.GetOrCreate(strings.TrimSpace(sessionID))
	if err != nil {
		return nil, err
	}
	result, err := actor.SubmitPrompt(ctx, prompt, runMeta)
	sessionResult := sessionResultFromActorRun(result, err)
	if err != nil {
		if sessionResult != nil {
			return sessionResult, err
		}
		return nil, err
	}
	if sessionResult == nil {
		return nil, fmt.Errorf("session result is nil")
	}
	return sessionResult, nil
}

func sessionResultFromActorRun(result *agent.Result, err error) *team.SessionResult {
	if result == nil && err == nil {
		return nil
	}
	sessionResult := &team.SessionResult{}
	if result != nil {
		sessionResult.Success = result.Success
		sessionResult.Output = result.Output
		sessionResult.Error = result.Error
		sessionResult.TraceID = result.TraceID
		sessionResult.Steps = result.Steps
		sessionResult.Observations = team.SessionObservationsFromRuntime(result.Observations)
	}
	if err != nil {
		if strings.TrimSpace(sessionResult.Error) == "" {
			sessionResult.Error = err.Error()
		}
		if preflightErr, ok := agent.AsPromptPreflightError(err); ok && preflightErr != nil {
			sessionResult.ErrorType = "prompt_preflight"
			sessionResult.ErrorMetadata = cloneSessionErrorMetadata(preflightErr.Metadata())
		}
	}
	return sessionResult
}

func cloneSessionErrorMetadata(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

type sessionAgentController struct {
	handler *Handler
}

const sessionAgentAllSessionListLimit = 100000

type apiAgentForkMode int

const (
	apiAgentForkNone apiAgentForkMode = iota
	apiAgentForkAll
	apiAgentForkLastN
)

func apiAgentForkModeForText(forkTurns string) (apiAgentForkMode, int, error) {
	forkTurns = strings.ToLower(strings.TrimSpace(forkTurns))
	switch forkTurns {
	case "":
		return apiAgentForkNone, 0, nil
	case "none":
		return apiAgentForkNone, 0, nil
	case "all":
		return apiAgentForkAll, 0, nil
	default:
		n, err := strconv.Atoi(forkTurns)
		if err != nil || n <= 0 {
			return apiAgentForkNone, 0, fmt.Errorf("fork_turns must be none, all, or a positive integer")
		}
		return apiAgentForkLastN, n, nil
	}
}

func resolveAPIAgentForkMode(args toolbroker.SpawnAgentArgs) (apiAgentForkMode, int, error) {
	if strings.TrimSpace(args.ForkTurns) != "" {
		return apiAgentForkModeForText(args.ForkTurns)
	}
	if args.ForkContext != nil && *args.ForkContext {
		return apiAgentForkAll, 0, nil
	}
	return apiAgentForkNone, 0, nil
}

func (c *sessionAgentController) Spawn(ctx context.Context, parentSessionID string, args toolbroker.SpawnAgentArgs) (*toolbroker.AgentStatusResult, error) {
	if c == nil || c.handler == nil || c.handler.sessionManager == nil {
		return nil, fmt.Errorf("session manager not configured")
	}
	sessionID := firstNonEmptyString(strings.TrimSpace(args.ID), strings.TrimSpace(args.SessionID))
	storage := c.handler.sessionManager.GetStorage()
	if storage == nil {
		return nil, fmt.Errorf("session storage not configured")
	}

	var parentSession *chat.Session
	if strings.TrimSpace(parentSessionID) != "" {
		if session, err := c.handler.sessionManager.Get(ctx, strings.TrimSpace(parentSessionID)); err == nil {
			parentSession = session
		} else if !stderrors.Is(err, chat.ErrSessionNotFound) && !strings.Contains(strings.ToLower(err.Error()), "not found") {
			return nil, err
		}
	}
	childDepth := apiAgentChildDepth(parentSession)
	if err := c.enforceSpawnLimits(ctx, parentSession, parentSessionID, childDepth); err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.ForkTurns) == "" && args.ForkContext == nil {
		args.ForkTurns = strings.TrimSpace(c.agentsConfig().DefaultForkTurns)
	}
	forkMode, forkTurns, err := resolveAPIAgentForkMode(args)
	if err != nil {
		return nil, err
	}
	userID := "agent"
	if parentSession != nil && strings.TrimSpace(parentSession.UserID) != "" {
		userID = strings.TrimSpace(parentSession.UserID)
	}

	var childSession *chat.Session
	if sessionID == "" {
		created, err := c.handler.sessionManager.Create(ctx, userID)
		if err != nil {
			return nil, err
		}
		childSession = created
		sessionID = created.ID
	} else {
		existing, err := storage.Load(ctx, sessionID)
		if err == nil && existing != nil {
			return nil, fmt.Errorf("session already exists: %s", sessionID)
		}
		if err != nil && !stderrors.Is(err, chat.ErrSessionNotFound) {
			return nil, err
		}
		childSession = chat.NewSession(userID)
		childSession.ID = sessionID
	}

	if forkMode != apiAgentForkNone && parentSession != nil {
		childSession = parentSession.Clone()
		if forkMode == apiAgentForkLastN {
			childSession.ReplaceHistory(parentSession.GetRecentMessages(forkTurns))
		}
		childSession.ID = sessionID
		childSession.UserID = userID
		childSession.UpdateState(chat.StateActive)
	}
	childSession.SetContext(toolbroker.AgentSessionContextParentSessionID, strings.TrimSpace(parentSessionID))
	childSession.SetContext(toolbroker.AgentSessionContextRootSessionID, apiAgentRootSessionID(parentSession, parentSessionID))
	childSession.SetContext(toolbroker.AgentSessionContextPath, apiAgentChildPath(parentSession, sessionID))
	childSession.SetContext(toolbroker.AgentSessionContextDepth, childDepth)
	if agentType := strings.TrimSpace(args.AgentType); agentType != "" {
		childSession.SetContext(toolbroker.AgentSessionContextAgentType, agentType)
	}
	if model := strings.TrimSpace(args.Model); model != "" {
		childSession.SetContext(toolbroker.AgentSessionContextRequestedModel, model)
	}
	if err := storage.Save(ctx, childSession); err != nil {
		return nil, err
	}
	c.subscribeAgentCompletion(parentSessionID, childSession)

	actor, err := c.handler.getSessionHub().GetOrCreate(sessionID)
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
	result, err := c.snapshot(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	result.Created = true
	result.Queued = queued
	return result, nil
}

func (c *sessionAgentController) subscribeAgentCompletion(parentSessionID string, childSession *chat.Session) {
	if c == nil || c.handler == nil || childSession == nil {
		return
	}
	bus := c.handler.getRuntimeEventBus()
	store := c.handler.getSessionEventStore()
	if bus == nil || store == nil {
		return
	}
	parentSessionID = strings.TrimSpace(parentSessionID)
	childSessionID := strings.TrimSpace(childSession.ID)
	if parentSessionID == "" || childSessionID == "" {
		return
	}
	childPath := apiAgentSessionPath(childSession)
	childDepth := apiAgentSessionDepth(childSession)
	childType := ""
	if value, ok := childSession.GetContext(toolbroker.AgentSessionContextAgentType); ok {
		if text, ok := value.(string); ok {
			childType = strings.TrimSpace(text)
		}
	}

	var unsubscribe func()
	handler := func(event runtimeevents.Event) {
		if !strings.EqualFold(strings.TrimSpace(event.SessionID), childSessionID) {
			return
		}
		eventType := strings.TrimSpace(event.Type)
		if eventType != chat.EventSessionEnd && eventType != chat.EventSessionInterrupted {
			return
		}
		if unsubscribe != nil {
			unsubscribe()
		}
		payload := map[string]interface{}{
			"agent_id":          childSessionID,
			"session_id":        childSessionID,
			"parent_session_id": parentSessionID,
			"path":              childPath,
			"source_event_type": eventType,
			"status":            agentCompletionStatus(event),
		}
		if childDepth > 0 {
			payload["depth"] = childDepth
		}
		if childType != "" {
			payload["agent_type"] = childType
			payload["role"] = childType
		}
		copyAgentCompletionPayload(payload, event.Payload)
		mirrored := runtimeevents.Event{
			Type:      "subagent.completed",
			TraceID:   strings.TrimSpace(event.TraceID),
			AgentName: "agent-controller",
			SessionID: parentSessionID,
			Payload:   payload,
			Timestamp: event.Timestamp,
		}
		if mirrored.Timestamp.IsZero() {
			mirrored.Timestamp = time.Now().UTC()
		}
		if seq, err := store.AppendEvent(context.Background(), mirrored); err == nil {
			if mirrored.Payload == nil {
				mirrored.Payload = map[string]interface{}{}
			}
			mirrored.Payload["seq"] = seq
		}
		bus.Publish(mirrored)
	}
	unsubscribe = bus.SubscribeCancelable("", handler)
}

func (c *sessionAgentController) List(ctx context.Context, parentSessionID string, args toolbroker.ListAgentsArgs) (*toolbroker.AgentListResult, error) {
	if c == nil || c.handler == nil || c.handler.sessionManager == nil {
		return nil, fmt.Errorf("session manager not configured")
	}
	parentSessionID = firstNonEmptyString(strings.TrimSpace(args.ParentSessionID), strings.TrimSpace(parentSessionID))
	sessions, err := c.listSessions(ctx, parentSessionID)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*chat.Session, len(sessions))
	for _, session := range sessions {
		if session != nil && strings.TrimSpace(session.ID) != "" {
			byID[strings.TrimSpace(session.ID)] = session
		}
	}
	rootSessionID := parentSessionID
	if parent := byID[parentSessionID]; parent != nil {
		rootSessionID = apiAgentRootSessionID(parent, parentSessionID)
	}
	pathPrefix := strings.TrimSpace(args.PathPrefix)
	agents := make([]toolbroker.AgentStatusResult, 0)
	for _, session := range sessions {
		if session == nil || !isAPIAgentSession(session) {
			continue
		}
		if !args.IncludeClosed && isClosedAPIAgentSession(session) {
			continue
		}
		if rootSessionID != "" && apiAgentRootSessionID(session, "") != rootSessionID && !apiAgentHasAncestor(session, parentSessionID, byID) {
			continue
		}
		path := apiAgentSessionPath(session)
		if pathPrefix != "" && !strings.HasPrefix(path, pathPrefix) {
			continue
		}
		snapshot, err := c.snapshot(ctx, strings.TrimSpace(session.ID))
		if err != nil {
			return nil, err
		}
		if snapshot != nil {
			agents = append(agents, *snapshot)
		}
	}
	sort.SliceStable(agents, func(i, j int) bool {
		left := firstNonEmptyString(agents[i].Path, agents[i].SessionID, agents[i].ID)
		right := firstNonEmptyString(agents[j].Path, agents[j].SessionID, agents[j].ID)
		return left < right
	})
	return &toolbroker.AgentListResult{Agents: agents, Count: len(agents)}, nil
}

func (c *sessionAgentController) SendMessage(ctx context.Context, fromSessionID string, args toolbroker.AgentMessageArgs) (*toolbroker.AgentMessageResult, error) {
	return c.deliverAgentMessage(ctx, fromSessionID, args, false)
}

func (c *sessionAgentController) FollowupTask(ctx context.Context, fromSessionID string, args toolbroker.AgentMessageArgs) (*toolbroker.AgentMessageResult, error) {
	return c.deliverAgentMessage(ctx, fromSessionID, args, true)
}

func (c *sessionAgentController) deliverAgentMessage(ctx context.Context, fromSessionID string, args toolbroker.AgentMessageArgs, trigger bool) (*toolbroker.AgentMessageResult, error) {
	if c == nil || c.handler == nil {
		return nil, fmt.Errorf("handler not configured")
	}
	sessionID := firstNonEmptyString(strings.TrimSpace(args.Target), strings.TrimSpace(args.ID), strings.TrimSpace(args.SessionID))
	if sessionID == "" {
		return nil, fmt.Errorf("target is required")
	}
	resolvedSessionID, err := c.resolveTargetSessionID(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	sessionID = resolvedSessionID
	message := strings.TrimSpace(args.Message)
	if message == "" {
		return nil, fmt.Errorf("message is required")
	}
	actor, err := c.handler.getSessionHub().GetOrCreate(sessionID)
	if err != nil {
		return nil, err
	}
	delivered := false
	triggered := false
	if trigger && !apiAgentActorBusy(actor.State()) {
		if err := actor.SubmitPromptAsync(ctx, message, nil); err != nil {
			return nil, err
		}
		triggered = true
	} else {
		mail := team.MailMessage{
			FromAgent: strings.TrimSpace(fromSessionID),
			ToAgent:   sessionID,
			Kind:      "agent_message",
			Body:      message,
			CreatedAt: time.Now().UTC(),
			Metadata: map[string]interface{}{
				"from_session_id":   strings.TrimSpace(fromSessionID),
				"target_session_id": sessionID,
				"trigger_turn":      trigger,
			},
		}
		if trigger {
			mail.Kind = "followup_task"
		}
		if err := actor.DeliverMailboxMessage(ctx, mail); err != nil {
			return nil, err
		}
		delivered = true
	}
	status, err := c.snapshot(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if status != nil && triggered {
		status.Queued = true
	}
	return &toolbroker.AgentMessageResult{
		TargetSessionID: sessionID,
		Delivered:       delivered || triggered,
		Triggered:       triggered,
		Status:          status,
	}, nil
}

func (c *sessionAgentController) SendInput(ctx context.Context, args toolbroker.SendAgentInputArgs) (*toolbroker.AgentStatusResult, error) {
	if c == nil || c.handler == nil {
		return nil, fmt.Errorf("handler not configured")
	}
	sessionID := firstNonEmptyString(strings.TrimSpace(args.ID), strings.TrimSpace(args.SessionID))
	if sessionID == "" {
		return nil, fmt.Errorf("id is required")
	}
	resolvedSessionID, err := c.resolveTargetSessionID(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	sessionID = resolvedSessionID
	message := strings.TrimSpace(args.Message)
	if message == "" {
		return nil, fmt.Errorf("message is required")
	}
	actor, err := c.handler.getSessionHub().GetOrCreate(sessionID)
	if err != nil {
		return nil, err
	}
	if state := actor.State(); state != nil {
		busy := state.Status == chat.SessionRunning || state.Status == chat.SessionRewinding || state.Status == chat.SessionWaitingApproval || state.Status == chat.SessionWaitingInput
		if busy {
			interrupt := args.Interrupt != nil && *args.Interrupt
			if !interrupt {
				return nil, fmt.Errorf("session is busy (%s)", state.Status)
			}
			if err := actor.Interrupt(ctx); err != nil {
				return nil, err
			}
			waited, waitErr := c.Wait(ctx, toolbroker.WaitAgentArgs{SessionID: sessionID, TimeoutMs: 5000})
			if waitErr != nil {
				return nil, waitErr
			}
			if waited != nil && waited.Agent != nil && waited.Agent.Status == string(chat.SessionRunning) {
				return nil, fmt.Errorf("session is still running")
			}
		}
	}
	if err := actor.SubmitPromptAsync(ctx, message, nil); err != nil {
		return nil, err
	}
	result, err := c.snapshot(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	result.Queued = true
	return result, nil
}

func (c *sessionAgentController) Wait(ctx context.Context, args toolbroker.WaitAgentArgs) (*toolbroker.AgentWaitResult, error) {
	sessionIDs := normalizeAgentWaitIDs(args)
	if len(sessionIDs) == 0 {
		return nil, fmt.Errorf("id is required")
	}
	for index, sessionID := range sessionIDs {
		resolvedSessionID, err := c.resolveTargetSessionID(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		sessionIDs[index] = resolvedSessionID
	}
	timeout := time.Duration(args.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		defaultWaitMs := c.agentsConfig().DefaultWaitTimeoutMs
		if defaultWaitMs <= 0 {
			defaultWaitMs = int((30 * time.Second).Milliseconds())
		}
		timeout = time.Duration(defaultWaitMs) * time.Millisecond
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	wakeCh, unsubscribe := c.subscribeWaitEvents(sessionIDs)
	defer unsubscribe()
	for {
		snapshots := make([]toolbroker.AgentStatusResult, 0, len(sessionIDs))
		var matched *toolbroker.AgentStatusResult
		readyCount := 0
		for _, sessionID := range sessionIDs {
			result, err := c.snapshot(waitCtx, sessionID)
			if err != nil {
				return nil, err
			}
			if result == nil {
				continue
			}
			snapshots = append(snapshots, *result)
			if isAgentWaitReady(result.Status) {
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
		case <-wakeCh:
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (c *sessionAgentController) subscribeWaitEvents(sessionIDs []string) (<-chan struct{}, func()) {
	if c == nil || c.handler == nil || len(sessionIDs) == 0 {
		return nil, func() {}
	}
	bus := c.handler.getRuntimeEventBus()
	if bus == nil {
		return nil, func() {}
	}
	targets := make(map[string]struct{}, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		if sessionID = strings.TrimSpace(sessionID); sessionID != "" {
			targets[sessionID] = struct{}{}
		}
	}
	if len(targets) == 0 {
		return nil, func() {}
	}
	wakeCh := make(chan struct{}, 1)
	handler := func(event runtimeevents.Event) {
		if _, ok := targets[strings.TrimSpace(event.SessionID)]; !ok {
			return
		}
		if !isAgentWaitWakeEvent(event.Type) {
			return
		}
		select {
		case wakeCh <- struct{}{}:
		default:
		}
	}
	unsubscribe := bus.SubscribeCancelable("", handler)
	return wakeCh, unsubscribe
}

func (c *sessionAgentController) ReadEvents(ctx context.Context, args toolbroker.ReadAgentEventsArgs) (*toolbroker.AgentEventsResult, error) {
	sessionID := firstNonEmptyString(strings.TrimSpace(args.ID), strings.TrimSpace(args.SessionID))
	if sessionID == "" {
		return nil, fmt.Errorf("id is required")
	}
	resolvedSessionID, err := c.resolveTargetSessionID(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	sessionID = resolvedSessionID
	store := c.handler.getSessionEventStore()
	if store == nil {
		return nil, fmt.Errorf("session event store not configured")
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
	wakeCh, unsubscribe := c.subscribeReadEvents(sessionID)
	defer unsubscribe()
	for {
		events, err := store.ListEvents(readCtx, sessionID, args.AfterSeq, limit)
		if err != nil {
			return nil, err
		}
		if len(events) > 0 || waitMs == 0 {
			return buildAgentEventsResult(sessionID, events), nil
		}
		select {
		case <-readCtx.Done():
			result := buildAgentEventsResult(sessionID, nil)
			result.TimedOut = true
			return result, nil
		case <-wakeCh:
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (c *sessionAgentController) subscribeReadEvents(sessionID string) (<-chan struct{}, func()) {
	if c == nil || c.handler == nil {
		return nil, func() {}
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, func() {}
	}
	bus := c.handler.getRuntimeEventBus()
	if bus == nil {
		return nil, func() {}
	}
	wakeCh := make(chan struct{}, 1)
	handler := func(event runtimeevents.Event) {
		if !strings.EqualFold(strings.TrimSpace(event.SessionID), sessionID) {
			return
		}
		select {
		case wakeCh <- struct{}{}:
		default:
		}
	}
	unsubscribe := bus.SubscribeCancelable("", handler)
	return wakeCh, unsubscribe
}

func (c *sessionAgentController) Close(ctx context.Context, sessionID string) (*toolbroker.AgentStatusResult, error) {
	target := strings.TrimSpace(sessionID)
	if target == "" {
		return nil, fmt.Errorf("id is required")
	}
	targetSessionID, closeIDs, err := c.resolveCloseTargets(ctx, target)
	if err != nil {
		return nil, err
	}
	if len(closeIDs) == 0 {
		closeIDs = []string{target}
		targetSessionID = target
	}
	closedIDs := make([]string, 0, len(closeIDs))
	for _, closeID := range closeIDs {
		closeID = strings.TrimSpace(closeID)
		if closeID == "" {
			continue
		}
		if hub := c.handler.getSessionHub(); hub != nil {
			hub.Stop(closeID)
		}
		if c.handler.sessionManager != nil {
			_ = c.handler.sessionManager.Close(ctx, closeID)
		}
		closedIDs = append(closedIDs, closeID)
	}
	result, err := c.snapshot(ctx, targetSessionID)
	if err != nil {
		return nil, err
	}
	if result != nil {
		result.Status = string(chat.SessionStopped)
		result.ClosedCount = len(closedIDs)
		result.ClosedSessionIDs = closedIDs
	}
	return result, nil
}

func (c *sessionAgentController) resolveCloseTargets(ctx context.Context, target string) (string, []string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", nil, fmt.Errorf("id is required")
	}
	sessions, err := c.listSessions(ctx, target)
	if err != nil {
		return "", nil, err
	}
	byID := make(map[string]*chat.Session, len(sessions))
	for _, session := range sessions {
		if session != nil && strings.TrimSpace(session.ID) != "" {
			byID[strings.TrimSpace(session.ID)] = session
		}
	}
	targetSessionID := target
	targetPath := ""
	for _, session := range sessions {
		if session == nil {
			continue
		}
		sessionID := strings.TrimSpace(session.ID)
		path := apiAgentSessionPath(session)
		if strings.EqualFold(target, path) {
			targetSessionID = sessionID
			targetPath = path
			break
		}
	}
	targetSession := byID[targetSessionID]
	if targetPath == "" && targetSession != nil {
		targetPath = apiAgentSessionPath(targetSession)
	}
	if targetSession == nil && !strings.HasPrefix(target, "/") {
		return target, []string{target}, nil
	}
	closeIDs := make([]string, 0, len(sessions))
	seen := make(map[string]struct{}, len(sessions))
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, exists := seen[value]; exists {
			return
		}
		seen[value] = struct{}{}
		closeIDs = append(closeIDs, value)
	}
	add(targetSessionID)
	for _, session := range sessions {
		if session == nil || !isAPIAgentSession(session) {
			continue
		}
		sessionID := strings.TrimSpace(session.ID)
		if sessionID == "" || strings.EqualFold(sessionID, targetSessionID) {
			continue
		}
		if targetPath != "" && strings.HasPrefix(apiAgentSessionPath(session), strings.TrimRight(targetPath, "/")+"/") {
			add(sessionID)
			continue
		}
		if targetSessionID != "" && apiAgentHasAncestor(session, targetSessionID, byID) {
			add(sessionID)
		}
	}
	sort.SliceStable(closeIDs, func(i, j int) bool {
		if strings.EqualFold(closeIDs[i], targetSessionID) {
			return true
		}
		if strings.EqualFold(closeIDs[j], targetSessionID) {
			return false
		}
		return closeIDs[i] < closeIDs[j]
	})
	return targetSessionID, closeIDs, nil
}

func (c *sessionAgentController) resolveTargetSessionID(ctx context.Context, target string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" || !strings.HasPrefix(target, "/") {
		return target, nil
	}
	sessions, err := c.listSessions(ctx, target)
	if err != nil {
		return "", err
	}
	for _, session := range sessions {
		if session == nil {
			continue
		}
		if strings.EqualFold(target, apiAgentSessionPath(session)) {
			if sessionID := strings.TrimSpace(session.ID); sessionID != "" {
				return sessionID, nil
			}
		}
	}
	return "", fmt.Errorf("unknown agent path: %s", target)
}

func (c *sessionAgentController) Resume(ctx context.Context, sessionID string) (*toolbroker.AgentStatusResult, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("id is required")
	}
	resolvedSessionID, err := c.resolveTargetSessionID(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	sessionID = resolvedSessionID
	if _, err := c.handler.getSessionHub().GetOrCreate(sessionID); err != nil {
		return nil, err
	}
	return c.snapshot(ctx, sessionID)
}

func (c *sessionAgentController) snapshot(ctx context.Context, sessionID string) (*toolbroker.AgentStatusResult, error) {
	result := &toolbroker.AgentStatusResult{
		ID:        sessionID,
		SessionID: sessionID,
		Status:    "missing",
	}
	if c == nil || c.handler == nil || c.handler.sessionManager == nil {
		return result, nil
	}
	session, err := c.handler.sessionManager.Get(ctx, sessionID)
	if err != nil {
		if stderrors.Is(err, chat.ErrSessionNotFound) || strings.Contains(strings.ToLower(err.Error()), "not found") {
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
		result.Path = apiAgentSessionPath(session)
		result.Depth = apiAgentSessionDepth(session)
		if value, ok := session.GetContext(toolbroker.AgentSessionContextAgentType); ok {
			if text, ok := value.(string); ok {
				result.AgentType = strings.TrimSpace(text)
			}
		}
		messages := session.GetMessages()
		for index := len(messages) - 1; index >= 0; index-- {
			if result.LastMessageRole == "" {
				result.LastMessageRole = strings.TrimSpace(messages[index].Role)
				result.LastMessagePreview = truncateAgentStatusPreview(messages[index].Content)
			}
			if messages[index].Role == "assistant" {
				result.Output = strings.TrimSpace(messages[index].Content)
				break
			}
		}
		result.Status = string(chat.SessionIdle)
	}
	if hub := c.handler.getSessionHub(); hub != nil {
		if actor, ok := hub.Get(sessionID); ok && actor != nil {
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

func normalizeAgentWaitIDs(args toolbroker.WaitAgentArgs) []string {
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

func (c *sessionAgentController) enforceSpawnLimits(ctx context.Context, parentSession *chat.Session, parentSessionID string, childDepth int) error {
	limits := c.agentsConfig()
	if limits.MaxDepth > 0 && childDepth > limits.MaxDepth {
		return fmt.Errorf("agent spawn depth limit reached: max_depth=%d requested_depth=%d", limits.MaxDepth, childDepth)
	}
	if limits.MaxThreads <= 0 {
		return nil
	}
	rootSessionID := apiAgentRootSessionID(parentSession, parentSessionID)
	count, err := c.countAgentTree(ctx, rootSessionID)
	if err != nil {
		return err
	}
	if count >= limits.MaxThreads {
		return fmt.Errorf("agent spawn thread limit reached: max_threads=%d active_children=%d", limits.MaxThreads, count)
	}
	return nil
}

func (c *sessionAgentController) agentsConfig() runtimecfg.AgentsConfig {
	defaults := runtimecfg.DefaultRuntimeConfig().Agents
	if c == nil || c.handler == nil || c.handler.runtimeConfig == nil {
		return defaults
	}
	cfg := c.handler.runtimeConfig.Agents
	if cfg.MaxThreads == 0 && cfg.MaxDepth == 0 && cfg.DefaultWaitTimeoutMs == 0 && strings.TrimSpace(cfg.DefaultForkTurns) == "" {
		return defaults
	}
	return cfg
}

func (c *sessionAgentController) countAgentTree(ctx context.Context, rootSessionID string) (int, error) {
	sessions, err := c.listSessions(ctx, rootSessionID)
	if err != nil {
		return 0, err
	}
	byID := make(map[string]*chat.Session, len(sessions))
	for _, session := range sessions {
		if session != nil && strings.TrimSpace(session.ID) != "" {
			byID[strings.TrimSpace(session.ID)] = session
		}
	}
	count := 0
	for _, session := range sessions {
		if session == nil || !isAPIAgentSession(session) || isClosedAPIAgentSession(session) {
			continue
		}
		if rootSessionID == "" || apiAgentRootSessionID(session, "") == rootSessionID || apiAgentHasAncestor(session, rootSessionID, byID) {
			count++
		}
	}
	return count, nil
}

func (c *sessionAgentController) listSessions(ctx context.Context, preferredSessionID string) ([]*chat.Session, error) {
	if c == nil || c.handler == nil || c.handler.sessionManager == nil {
		return nil, nil
	}
	preferredSessionID = strings.TrimSpace(preferredSessionID)
	if strings.HasPrefix(preferredSessionID, "/") {
		return c.listAllSessions(ctx)
	}
	userID := "agent"
	if preferredSessionID != "" {
		if session, err := c.handler.sessionManager.Get(ctx, preferredSessionID); err == nil && session != nil && strings.TrimSpace(session.UserID) != "" {
			userID = strings.TrimSpace(session.UserID)
		}
	}
	return c.handler.sessionManager.List(ctx, userID)
}

func (c *sessionAgentController) listAllSessions(ctx context.Context) ([]*chat.Session, error) {
	if c == nil || c.handler == nil || c.handler.sessionManager == nil {
		return nil, nil
	}
	storage := c.handler.sessionManager.GetStorage()
	listAller, ok := storage.(chat.SessionStorageAllLister)
	if !ok {
		return nil, fmt.Errorf("session storage does not support listing all sessions")
	}
	return listAller.ListAll(ctx, sessionAgentAllSessionListLimit, 0)
}

func apiAgentChildDepth(parent *chat.Session) int {
	if parent == nil {
		return 1
	}
	return apiAgentSessionDepth(parent) + 1
}

func apiAgentSessionDepth(session *chat.Session) int {
	if session == nil {
		return 0
	}
	value, ok := session.GetContext(toolbroker.AgentSessionContextDepth)
	if !ok {
		return 0
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		depth, _ := strconv.Atoi(strings.TrimSpace(typed))
		return depth
	default:
		return 0
	}
}

func apiAgentRootSessionID(session *chat.Session, fallback string) string {
	if session != nil {
		if value, ok := session.GetContext(toolbroker.AgentSessionContextRootSessionID); ok {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
		}
	}
	if fallback = strings.TrimSpace(fallback); fallback != "" {
		return fallback
	}
	if session != nil {
		return strings.TrimSpace(session.ID)
	}
	return ""
}

func apiAgentChildPath(parent *chat.Session, sessionID string) string {
	parentPath := apiAgentSessionPath(parent)
	if parentPath == "" {
		parentPath = "/root"
	}
	return strings.TrimRight(parentPath, "/") + "/" + sanitizeAPIAgentPathSegment(sessionID)
}

func apiAgentSessionPath(session *chat.Session) string {
	if session == nil {
		return ""
	}
	if value, ok := session.GetContext(toolbroker.AgentSessionContextPath); ok {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	if isAPIAgentSession(session) {
		return "/root/" + sanitizeAPIAgentPathSegment(session.ID)
	}
	return "/root"
}

func sanitizeAPIAgentPathSegment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "agent"
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	text := strings.Trim(b.String(), "-")
	if text == "" {
		return "agent"
	}
	return text
}

func isAPIAgentSession(session *chat.Session) bool {
	if session == nil {
		return false
	}
	if value, ok := session.GetContext(toolbroker.AgentSessionContextParentSessionID); ok {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			return true
		}
	}
	if value, ok := session.GetContext(toolbroker.AgentSessionContextPath); ok {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			return true
		}
	}
	return false
}

func isClosedAPIAgentSession(session *chat.Session) bool {
	if session == nil {
		return false
	}
	return session.State == chat.StateClosed || session.State == chat.StateArchived
}

func apiAgentHasAncestor(session *chat.Session, ancestorID string, byID map[string]*chat.Session) bool {
	ancestorID = strings.TrimSpace(ancestorID)
	if session == nil || ancestorID == "" {
		return false
	}
	seen := map[string]struct{}{}
	current := session
	for current != nil {
		parentID := ""
		if value, ok := current.GetContext(toolbroker.AgentSessionContextParentSessionID); ok {
			if text, ok := value.(string); ok {
				parentID = strings.TrimSpace(text)
			}
		}
		if parentID == "" {
			return false
		}
		if strings.EqualFold(parentID, ancestorID) {
			return true
		}
		if _, ok := seen[parentID]; ok {
			return false
		}
		seen[parentID] = struct{}{}
		current = byID[parentID]
	}
	return false
}

func isAgentWaitReady(status string) bool {
	switch strings.TrimSpace(status) {
	case string(chat.SessionIdle), string(chat.SessionWaitingApproval), string(chat.SessionWaitingInput), string(chat.SessionStopped), "missing":
		return true
	default:
		return false
	}
}

func isAgentWaitWakeEvent(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case chat.EventSessionEnd,
		chat.EventSessionInterrupted,
		chat.EventAssistantMessage,
		chat.EventApprovalRequested,
		chat.EventQuestionAsked,
		chat.EventMailboxReceived:
		return true
	default:
		return false
	}
}

func apiAgentActorBusy(state *chat.RuntimeState) bool {
	if state == nil {
		return false
	}
	switch state.Status {
	case chat.SessionRunning, chat.SessionRewinding, chat.SessionWaitingApproval, chat.SessionWaitingInput:
		return true
	default:
		return false
	}
}

func agentCompletionStatus(event runtimeevents.Event) string {
	if strings.TrimSpace(event.Type) == chat.EventSessionInterrupted {
		return string(chat.SessionStopped)
	}
	if event.Payload != nil {
		if success, ok := event.Payload["success"].(bool); ok && !success {
			return "failed"
		}
		if text, ok := event.Payload["status"].(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return string(chat.SessionIdle)
}

func copyAgentCompletionPayload(target map[string]interface{}, payload map[string]interface{}) {
	if len(target) == 0 || len(payload) == 0 {
		return
	}
	for _, key := range []string{"success", "error", "duration", "steps", "trace_id", "turn_id"} {
		if value, ok := payload[key]; ok {
			target[key] = value
		}
	}
}

func truncateAgentStatusPreview(content string) string {
	content = strings.Join(strings.Fields(strings.TrimSpace(content)), " ")
	if len(content) <= 160 {
		return content
	}
	return content[:157] + "..."
}

func buildAgentEventsResult(sessionID string, events []runtimeevents.Event) *toolbroker.AgentEventsResult {
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
			Payload:   cloneProfileContextValues(event.Payload),
		}
		if seq := agentEventSeq(event); seq > 0 {
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

func agentEventSeq(event runtimeevents.Event) int64 {
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

func (h *Handler) getSessionHub() *chat.SessionHub {
	if h == nil {
		return nil
	}

	h.sessionRuntimeMu.RLock()
	hub := h.sessionHub
	h.sessionRuntimeMu.RUnlock()
	if hub != nil {
		return hub
	}

	h.sessionRuntimeMu.Lock()
	defer h.sessionRuntimeMu.Unlock()
	if h.sessionHub == nil {
		h.sessionHub = chat.NewSessionHub(func(sessionID string) (*chat.SessionActor, error) {
			return h.buildSessionActor(sessionID)
		})
	}
	return h.sessionHub
}

func (h *Handler) buildSessionActor(sessionID string) (*chat.SessionActor, error) {
	if h == nil {
		return nil, fmt.Errorf("handler is nil")
	}
	if h.sessionManager == nil {
		return nil, fmt.Errorf("session manager not configured")
	}
	sessionStore := h.sessionManager.GetStorage()
	if sessionStore == nil {
		return nil, fmt.Errorf("session storage not configured")
	}

	runtimeConfig := h.resolveRuntimeConfig(UsageScope{})
	workspacePath := ""

	var profileState *profileRuntimeState
	childAgentType := ""
	requestedChildModel := ""
	if session, err := h.sessionManager.Get(context.Background(), sessionID); err == nil && session != nil {
		getContextString := func(key string) string {
			value, ok := session.GetContext(key)
			if !ok {
				return ""
			}
			text, ok := value.(string)
			if !ok {
				return ""
			}
			return strings.TrimSpace(text)
		}
		profileRef := getContextString(apiProfileContextReference)
		agentID := getContextString(apiProfileContextAgent)
		if profileRef != "" {
			if resolved, err := h.resolveProfileSessionState(profileRef, agentID, workspacePath); err == nil {
				profileState = resolved
			}
		}
		childAgentType = getContextString(toolbroker.AgentSessionContextAgentType)
		requestedChildModel = getContextString(toolbroker.AgentSessionContextRequestedModel)
	}

	selectedConfig := runtimeConfig
	if profileState != nil && profileState.RuntimeConfig != nil {
		selectedConfig = profileState.RuntimeConfig
	}

	agentProvider := resolveAgentProvider(profileState, selectedConfig, h.llmRuntime)
	agentModel := resolveAgentModel(profileState, selectedConfig, h.llmRuntime)
	if strings.TrimSpace(requestedChildModel) != "" {
		agentModel = strings.TrimSpace(requestedChildModel)
	}
	if strings.TrimSpace(agentModel) == "" {
		agentModel = defaultAgentModel(h.llmRuntime)
	}

	agentConfig := &agent.Config{
		Name:     firstNonEmptyString(strings.TrimSpace(childAgentType), "session-actor"),
		Provider: agentProvider,
		Model:    agentModel,
	}
	instructionMessages := buildRuntimeInstructionMessages(profileState, workspacePath, agentProvider)
	if systemPrompt := primarySystemInstructionContent(instructionMessages); systemPrompt != "" {
		agentConfig.SystemPrompt = systemPrompt
	} else if profileState != nil && strings.TrimSpace(profileState.PromptText) != "" {
		agentConfig.SystemPrompt = strings.TrimSpace(profileState.PromptText)
	}
	if agentConfig.MaxSteps < 0 {
		agentConfig.MaxSteps = 0
	} else if agentConfig.MaxSteps == 0 && selectedConfig != nil {
		agentConfig.MaxSteps = agent.NormalizeMaxSteps(selectedConfig.Agent.MaxMaxSteps)
	}
	if selectedConfig != nil {
		agentConfig.Options = contextOptionsFromRuntimeConfig(selectedConfig)
	}
	if workspacePath != "" {
		if agentConfig.Options == nil {
			agentConfig.Options = make(map[string]interface{})
		}
		agentConfig.Options["workspace_path"] = workspacePath
	}
	if profilePack := buildProfileContextPack(profileState); len(profilePack) > 0 {
		if agentConfig.Options == nil {
			agentConfig.Options = make(map[string]interface{})
		}
		agentConfig.Options["profile_context"] = cloneProfileContextValues(profilePack)
	}
	if profileState != nil && len(profileState.ContextValues) > 0 {
		if agentConfig.Options == nil {
			agentConfig.Options = make(map[string]interface{})
		}
		mergeProfileContextInto(agentConfig.Options, profileState.ContextValues)
	}

	apiAgent := h.newAPIAgentWithRuntime(agentConfig, &agentRuntimeComponents{
		registry:        h.skillRegistry,
		embeddingRouter: h.embeddingRouter,
		mcpManager:      h.mcpManager,
		llmRuntime:      h.llmRuntime,
	})
	h.applyAgentExecutionPolicy(apiAgent, workspacePath, selectedConfig, profileStateToolPolicy(profileState))
	h.applyAgentHooks(apiAgent, selectedConfig)
	h.applyAgentRuntimeServices(apiAgent, selectedConfig)

	actor, err := chat.NewSessionActor(sessionID, chat.SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   h.llmRuntime,
		SessionStore: sessionStore,
		StateStore:   h.getSessionRuntimeStore(),
		EventStore:   h.getSessionEventStore(),
		EventBus:     h.getRuntimeEventBus(),
		LoopConfig:   buildSessionLoopConfig(selectedConfig),
	})
	if err != nil {
		return nil, err
	}
	return actor, nil
}

func buildSessionLoopConfig(selectedConfig *runtimecfg.RuntimeConfig) *agent.LoopReActConfig {
	config := &agent.LoopReActConfig{
		MaxSteps:             0,
		EnableThought:        true,
		EnableToolCalls:      true,
		EnableParallelTools:  false,
		MaxParallelToolCalls: 1,
		Temperature:          0.7,
	}
	if selectedConfig != nil {
		config.MaxSteps = agent.NormalizeMaxSteps(selectedConfig.Agent.MaxMaxSteps)
		config.EnableParallelTools = selectedConfig.Agent.EnableParallelTools
		if selectedConfig.Agent.MaxParallelToolCalls > 0 {
			config.MaxParallelToolCalls = selectedConfig.Agent.MaxParallelToolCalls
		}
	}
	return config
}

func (h *Handler) getSessionRuntimeStore() chat.RuntimeStateStore {
	if h == nil {
		return nil
	}

	h.sessionRuntimeMu.RLock()
	store := h.sessionRuntimeStore
	h.sessionRuntimeMu.RUnlock()
	if store != nil {
		return store
	}

	_, _ = h.refreshSessionRuntimeStore(h.runtimeConfig, h.runtimeConfigFile)
	h.sessionRuntimeMu.RLock()
	store = h.sessionRuntimeStore
	h.sessionRuntimeMu.RUnlock()
	if store != nil {
		return store
	}

	h.sessionRuntimeMu.Lock()
	defer h.sessionRuntimeMu.Unlock()
	if h.sessionRuntimeStore == nil {
		memoryStore := chat.NewInMemoryRuntimeStore(2048)
		h.sessionRuntimeStore = memoryStore
		if h.sessionEventStore == nil {
			h.sessionEventStore = memoryStore
		}
	}
	return h.sessionRuntimeStore
}

func (h *Handler) getSessionEventStore() chat.EventStore {
	if h == nil {
		return nil
	}

	h.sessionRuntimeMu.RLock()
	store := h.sessionEventStore
	h.sessionRuntimeMu.RUnlock()
	if store != nil {
		return store
	}

	_ = h.getSessionRuntimeStore()
	h.sessionRuntimeMu.RLock()
	store = h.sessionEventStore
	h.sessionRuntimeMu.RUnlock()
	if store != nil {
		return store
	}

	h.sessionRuntimeMu.Lock()
	defer h.sessionRuntimeMu.Unlock()
	if h.sessionEventStore == nil {
		if runtimeStore, ok := h.sessionRuntimeStore.(chat.EventStore); ok && runtimeStore != nil {
			h.sessionEventStore = runtimeStore
		} else {
			memoryStore := chat.NewInMemoryRuntimeStore(2048)
			h.sessionEventStore = memoryStore
			if h.sessionRuntimeStore == nil {
				h.sessionRuntimeStore = memoryStore
			}
		}
	}
	return h.sessionEventStore
}

func (h *Handler) getSessionToolReceiptStore() chat.ToolReceiptStore {
	if h == nil {
		return nil
	}
	store := h.getSessionRuntimeStore()
	if store == nil {
		return nil
	}
	receiptStore, _ := store.(chat.ToolReceiptStore)
	return receiptStore
}

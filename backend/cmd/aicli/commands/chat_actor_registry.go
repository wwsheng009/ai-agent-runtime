package commands

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
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

type localAgentForkMode int

const (
	localAgentForkNone localAgentForkMode = iota
	localAgentForkAll
	localAgentForkLastN
)

func resolveLocalAgentForkMode(args toolbroker.SpawnAgentArgs) (localAgentForkMode, int, error) {
	forkTurns := strings.ToLower(strings.TrimSpace(args.ForkTurns))
	if forkTurns != "" {
		switch forkTurns {
		case "none":
			return localAgentForkNone, 0, nil
		case "all":
			return localAgentForkAll, 0, nil
		default:
			n, err := strconv.Atoi(forkTurns)
			if err != nil || n <= 0 {
				return localAgentForkNone, 0, fmt.Errorf("fork_turns must be none, all, or a positive integer")
			}
			return localAgentForkLastN, n, nil
		}
	}
	if args.ForkContext != nil && *args.ForkContext {
		return localAgentForkAll, 0, nil
	}
	return localAgentForkNone, 0, nil
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
	childDepth := localAgentChildDepth(parentSession)
	if err := r.enforceLocalAgentSpawnLimits(ctx, parentSession, parentSessionID, childDepth); err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.ForkTurns) == "" && args.ForkContext == nil {
		args.ForkTurns = strings.TrimSpace(r.localAgentsConfig().DefaultForkTurns)
	}
	forkMode, forkTurns, err := resolveLocalAgentForkMode(args)
	if err != nil {
		return nil, err
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
	if forkMode != localAgentForkNone && parentSession != nil {
		childSession = parentSession.Clone()
		if forkMode == localAgentForkLastN {
			childSession.ReplaceHistory(parentSession.GetRecentMessages(forkTurns))
		}
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
	childSession.SetContext(toolbroker.AgentSessionContextRootSessionID, localAgentRootSessionID(parentSession, parentSessionID))
	childSession.SetContext(toolbroker.AgentSessionContextPath, localAgentChildPath(parentSession, sessionID))
	childSession.SetContext(toolbroker.AgentSessionContextDepth, childDepth)
	if agentType := strings.TrimSpace(args.AgentType); agentType != "" {
		childSession.SetContext(toolbroker.AgentSessionContextAgentType, agentType)
	}
	if model := strings.TrimSpace(args.Model); model != "" {
		childSession.SetContext(toolbroker.AgentSessionContextRequestedModel, model)
	}
	if err := r.Host.SessionStore.Save(ctx, childSession); err != nil {
		return nil, err
	}
	r.subscribeLocalAgentCompletion(parentSessionID, childSession)

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

func (r *localActorRegistry) subscribeLocalAgentCompletion(parentSessionID string, childSession *runtimechat.Session) {
	if r == nil || r.Host == nil || r.Host.EventBus == nil || r.Host.EventStore == nil || childSession == nil {
		return
	}
	parentSessionID = strings.TrimSpace(parentSessionID)
	childSessionID := strings.TrimSpace(childSession.ID)
	if parentSessionID == "" || childSessionID == "" {
		return
	}
	childPath := localAgentSessionPath(childSession)
	childDepth := localAgentSessionDepth(childSession)
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
		if eventType != runtimechat.EventSessionEnd && eventType != runtimechat.EventSessionInterrupted {
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
			"status":            localAgentCompletionStatus(event),
		}
		if childDepth > 0 {
			payload["depth"] = childDepth
		}
		if childType != "" {
			payload["agent_type"] = childType
			payload["role"] = childType
		}
		copyLocalAgentCompletionPayload(payload, event.Payload)
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
		if seq, err := r.Host.EventStore.AppendEvent(context.Background(), mirrored); err == nil {
			if mirrored.Payload == nil {
				mirrored.Payload = map[string]interface{}{}
			}
			mirrored.Payload["seq"] = seq
		}
		r.Host.EventBus.Publish(mirrored)
	}
	unsubscribe = r.Host.EventBus.SubscribeCancelable("", handler)
}

func (r *localActorRegistry) List(ctx context.Context, parentSessionID string, args toolbroker.ListAgentsArgs) (*toolbroker.AgentListResult, error) {
	baseSessionID := ""
	if r != nil && r.Host != nil {
		baseSessionID = r.Host.baseRuntimeSessionID()
	}
	parentSessionID = firstNonEmptyChatValue(strings.TrimSpace(args.ParentSessionID), strings.TrimSpace(parentSessionID), baseSessionID)
	sessions, err := r.listLocalAgentSessions(ctx)
	if err != nil {
		return nil, err
	}
	for _, session := range sessions {
		changed, applyErr := r.applyTeamTeammateAgentContext(ctx, session)
		if applyErr != nil {
			return nil, applyErr
		}
		if changed {
			if err := r.Host.SessionStore.Update(ctx, session); err != nil {
				return nil, err
			}
		}
	}
	byID := make(map[string]*runtimechat.Session, len(sessions))
	for _, session := range sessions {
		if session != nil && strings.TrimSpace(session.ID) != "" {
			byID[strings.TrimSpace(session.ID)] = session
		}
	}

	rootSessionID := parentSessionID
	if parent := byID[parentSessionID]; parent != nil {
		rootSessionID = localAgentRootSessionID(parent, parentSessionID)
	}
	pathPrefix := strings.TrimSpace(args.PathPrefix)
	agents := make([]toolbroker.AgentStatusResult, 0)
	for _, session := range sessions {
		if session == nil || !isLocalAgentSession(session) {
			continue
		}
		if !args.IncludeClosed && isClosedLocalAgentSession(session) {
			continue
		}
		if rootSessionID != "" && localAgentRootSessionID(session, "") != rootSessionID && !localAgentHasAncestor(session, parentSessionID, byID) {
			continue
		}
		if parentSessionID != "" && rootSessionID == "" && !localAgentHasAncestor(session, parentSessionID, byID) {
			continue
		}
		path := localAgentSessionPath(session)
		if pathPrefix != "" && !strings.HasPrefix(path, pathPrefix) {
			continue
		}
		snapshot, err := r.agentSnapshot(ctx, strings.TrimSpace(session.ID))
		if err != nil {
			return nil, err
		}
		if snapshot != nil {
			agents = append(agents, *snapshot)
		}
	}
	sort.SliceStable(agents, func(i, j int) bool {
		left := firstNonEmptyChatValue(agents[i].Path, agents[i].SessionID, agents[i].ID)
		right := firstNonEmptyChatValue(agents[j].Path, agents[j].SessionID, agents[j].ID)
		return left < right
	})
	return &toolbroker.AgentListResult{Agents: agents, Count: len(agents)}, nil
}

func (r *localActorRegistry) SendMessage(ctx context.Context, fromSessionID string, args toolbroker.AgentMessageArgs) (*toolbroker.AgentMessageResult, error) {
	return r.deliverAgentMessage(ctx, fromSessionID, args, false)
}

func (r *localActorRegistry) FollowupTask(ctx context.Context, fromSessionID string, args toolbroker.AgentMessageArgs) (*toolbroker.AgentMessageResult, error) {
	return r.deliverAgentMessage(ctx, fromSessionID, args, true)
}

func (r *localActorRegistry) deliverAgentMessage(ctx context.Context, fromSessionID string, args toolbroker.AgentMessageArgs, trigger bool) (*toolbroker.AgentMessageResult, error) {
	if r == nil || r.Host == nil || r.Host.SessionHub == nil {
		return nil, fmt.Errorf("session hub not configured")
	}
	sessionID := firstNonEmptyChatValue(strings.TrimSpace(args.Target), strings.TrimSpace(args.ID), strings.TrimSpace(args.SessionID))
	if sessionID == "" {
		return nil, fmt.Errorf("target is required")
	}
	resolvedSessionID, err := r.resolveLocalAgentTargetSessionID(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	sessionID = resolvedSessionID
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
	delivered := false
	triggered := false
	if trigger && !localAgentActorBusy(actor.State()) {
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
	status, err := r.agentSnapshot(ctx, sessionID)
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

func (r *localActorRegistry) SendInput(ctx context.Context, args toolbroker.SendAgentInputArgs) (*toolbroker.AgentStatusResult, error) {
	if r == nil || r.Host == nil || r.Host.SessionHub == nil {
		return nil, fmt.Errorf("session hub not configured")
	}
	sessionID := firstNonEmptyChatValue(strings.TrimSpace(args.ID), strings.TrimSpace(args.SessionID))
	if sessionID == "" {
		return nil, fmt.Errorf("id is required")
	}
	resolvedSessionID, err := r.resolveLocalAgentTargetSessionID(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	sessionID = resolvedSessionID
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
	for index, sessionID := range sessionIDs {
		resolvedSessionID, err := r.resolveLocalAgentTargetSessionID(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		sessionIDs[index] = resolvedSessionID
	}
	timeout := time.Duration(args.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		defaultWaitMs := r.localAgentsConfig().DefaultWaitTimeoutMs
		if defaultWaitMs <= 0 {
			defaultWaitMs = int((30 * time.Second).Milliseconds())
		}
		timeout = time.Duration(defaultWaitMs) * time.Millisecond
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	wakeCh, unsubscribe := r.subscribeLocalAgentWaitEvents(sessionIDs)
	defer unsubscribe()
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
		case <-wakeCh:
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (r *localActorRegistry) subscribeLocalAgentWaitEvents(sessionIDs []string) (<-chan struct{}, func()) {
	if r == nil || r.Host == nil || r.Host.EventBus == nil || len(sessionIDs) == 0 {
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
		if !isLocalAgentWaitWakeEvent(event.Type) {
			return
		}
		select {
		case wakeCh <- struct{}{}:
		default:
		}
	}
	unsubscribeAll := r.Host.EventBus.SubscribeCancelable("", handler)
	return wakeCh, unsubscribeAll
}

func (r *localActorRegistry) ReadEvents(ctx context.Context, args toolbroker.ReadAgentEventsArgs) (*toolbroker.AgentEventsResult, error) {
	if r == nil || r.Host == nil || r.Host.EventStore == nil {
		return nil, fmt.Errorf("event store not configured")
	}
	sessionID := firstNonEmptyChatValue(strings.TrimSpace(args.ID), strings.TrimSpace(args.SessionID))
	if sessionID == "" {
		return nil, fmt.Errorf("id is required")
	}
	resolvedSessionID, err := r.resolveLocalAgentTargetSessionID(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	sessionID = resolvedSessionID
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
	wakeCh, unsubscribe := r.subscribeLocalAgentReadEvents(sessionID)
	defer unsubscribe()
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
		case <-wakeCh:
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (r *localActorRegistry) subscribeLocalAgentReadEvents(sessionID string) (<-chan struct{}, func()) {
	if r == nil || r.Host == nil || r.Host.EventBus == nil {
		return nil, func() {}
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
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
	unsubscribeAll := r.Host.EventBus.SubscribeCancelable("", handler)
	return wakeCh, unsubscribeAll
}

func (r *localActorRegistry) Close(ctx context.Context, sessionID string) (*toolbroker.AgentStatusResult, error) {
	target := strings.TrimSpace(sessionID)
	if target == "" {
		return nil, fmt.Errorf("id is required")
	}
	targetSessionID, closeIDs, err := r.resolveLocalAgentCloseTargets(ctx, target)
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
		if r != nil && r.Host != nil && r.Host.SessionHub != nil {
			r.Host.SessionHub.Stop(closeID)
		}
		if r != nil && r.Host != nil && r.Host.SessionStore != nil {
			if session, loadErr := r.Host.SessionStore.Load(ctx, closeID); loadErr == nil && session != nil {
				session.UpdateState(runtimechat.StateClosed)
				_ = r.Host.SessionStore.Update(ctx, session)
			}
		}
		closedIDs = append(closedIDs, closeID)
	}
	result, err := r.agentSnapshot(ctx, targetSessionID)
	if err != nil {
		return nil, err
	}
	result.Status = string(runtimechat.SessionStopped)
	result.ClosedCount = len(closedIDs)
	result.ClosedSessionIDs = closedIDs
	return result, nil
}

func (r *localActorRegistry) resolveLocalAgentCloseTargets(ctx context.Context, target string) (string, []string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", nil, fmt.Errorf("id is required")
	}
	sessions, err := r.listLocalAgentSessions(ctx)
	if err != nil {
		return "", nil, err
	}
	for _, session := range sessions {
		changed, applyErr := r.applyTeamTeammateAgentContext(ctx, session)
		if applyErr != nil {
			return "", nil, applyErr
		}
		if changed {
			if err := r.Host.SessionStore.Update(ctx, session); err != nil {
				return "", nil, err
			}
		}
	}
	byID := make(map[string]*runtimechat.Session, len(sessions))
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
		path := localAgentSessionPath(session)
		if strings.EqualFold(target, path) {
			targetSessionID = sessionID
			targetPath = path
			break
		}
	}
	targetSession := byID[targetSessionID]
	if targetPath == "" && targetSession != nil {
		targetPath = localAgentSessionPath(targetSession)
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
		if session == nil || !isLocalAgentSession(session) {
			continue
		}
		sessionID := strings.TrimSpace(session.ID)
		if sessionID == "" || strings.EqualFold(sessionID, targetSessionID) {
			continue
		}
		if targetPath != "" && strings.HasPrefix(localAgentSessionPath(session), strings.TrimRight(targetPath, "/")+"/") {
			add(sessionID)
			continue
		}
		if targetSessionID != "" && localAgentHasAncestor(session, targetSessionID, byID) {
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

func (r *localActorRegistry) resolveLocalAgentTargetSessionID(ctx context.Context, target string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" || !strings.HasPrefix(target, "/") {
		return target, nil
	}
	sessions, err := r.listLocalAgentSessions(ctx)
	if err != nil {
		return "", err
	}
	for _, session := range sessions {
		changed, applyErr := r.applyTeamTeammateAgentContext(ctx, session)
		if applyErr != nil {
			return "", applyErr
		}
		if changed {
			if err := r.Host.SessionStore.Update(ctx, session); err != nil {
				return "", err
			}
		}
	}
	for _, session := range sessions {
		if session == nil {
			continue
		}
		if strings.EqualFold(target, localAgentSessionPath(session)) {
			if sessionID := strings.TrimSpace(session.ID); sessionID != "" {
				return sessionID, nil
			}
		}
	}
	return "", fmt.Errorf("unknown agent path: %s", target)
}

func (r *localActorRegistry) Resume(ctx context.Context, sessionID string) (*toolbroker.AgentStatusResult, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("id is required")
	}
	resolvedSessionID, err := r.resolveLocalAgentTargetSessionID(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	sessionID = resolvedSessionID
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
		result.Path = localAgentSessionPath(session)
		result.Depth = localAgentSessionDepth(session)
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

func (r *localActorRegistry) enforceLocalAgentSpawnLimits(ctx context.Context, parentSession *runtimechat.Session, parentSessionID string, childDepth int) error {
	limits := r.localAgentsConfig()
	if limits.MaxDepth > 0 && childDepth > limits.MaxDepth {
		return fmt.Errorf("agent spawn depth limit reached: max_depth=%d requested_depth=%d", limits.MaxDepth, childDepth)
	}
	if limits.MaxThreads <= 0 {
		return nil
	}
	rootSessionID := localAgentRootSessionID(parentSession, parentSessionID)
	count, err := r.countLocalAgentTree(ctx, rootSessionID)
	if err != nil {
		return err
	}
	if count >= limits.MaxThreads {
		return fmt.Errorf("agent spawn thread limit reached: max_threads=%d active_children=%d", limits.MaxThreads, count)
	}
	return nil
}

func (r *localActorRegistry) localAgentsConfig() runtimecfg.AgentsConfig {
	defaults := runtimecfg.DefaultRuntimeConfig().Agents
	if r == nil || r.Host == nil {
		return defaults
	}
	runtimeConfig := r.Host.RuntimeConfig
	if runtimeConfig == nil && r.Host.Bootstrap != nil {
		runtimeConfig = r.Host.Bootstrap.Config()
	}
	if runtimeConfig == nil {
		return defaults
	}
	cfg := runtimeConfig.Agents
	if cfg.MaxThreads == 0 && cfg.MaxDepth == 0 && cfg.DefaultWaitTimeoutMs == 0 && strings.TrimSpace(cfg.DefaultForkTurns) == "" {
		return defaults
	}
	return cfg
}

func (r *localActorRegistry) countLocalAgentTree(ctx context.Context, rootSessionID string) (int, error) {
	sessions, err := r.listLocalAgentSessions(ctx)
	if err != nil {
		return 0, err
	}
	byID := make(map[string]*runtimechat.Session, len(sessions))
	for _, session := range sessions {
		if session != nil && strings.TrimSpace(session.ID) != "" {
			byID[strings.TrimSpace(session.ID)] = session
		}
	}
	count := 0
	for _, session := range sessions {
		if session == nil || !isLocalAgentSession(session) || isClosedLocalAgentSession(session) {
			continue
		}
		if rootSessionID == "" || localAgentRootSessionID(session, "") == rootSessionID || localAgentHasAncestor(session, rootSessionID, byID) {
			count++
		}
	}
	return count, nil
}

func (r *localActorRegistry) listLocalAgentSessions(ctx context.Context) ([]*runtimechat.Session, error) {
	if r == nil || r.Host == nil || r.Host.SessionStore == nil {
		return nil, nil
	}
	userID := strings.TrimSpace(r.Host.SessionUser)
	if userID == "" && r.Host.BaseSession != nil {
		userID = strings.TrimSpace(r.Host.BaseSession.SessionUserID)
	}
	if userID == "" {
		userID = "agent"
	}
	return r.Host.SessionStore.List(ctx, userID)
}

func localAgentChildDepth(parent *runtimechat.Session) int {
	if parent == nil {
		return 1
	}
	return localAgentSessionDepth(parent) + 1
}

func localAgentSessionDepth(session *runtimechat.Session) int {
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

func localAgentRootSessionID(session *runtimechat.Session, fallback string) string {
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

func localAgentChildPath(parent *runtimechat.Session, sessionID string) string {
	parentPath := localAgentSessionPath(parent)
	if parentPath == "" {
		parentPath = "/root"
	}
	return strings.TrimRight(parentPath, "/") + "/" + sanitizeLocalAgentPathSegment(sessionID)
}

func localAgentSessionPath(session *runtimechat.Session) string {
	if session == nil {
		return ""
	}
	if value, ok := session.GetContext(toolbroker.AgentSessionContextPath); ok {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	if isLocalAgentSession(session) {
		return "/root/" + sanitizeLocalAgentPathSegment(session.ID)
	}
	return "/root"
}

func (r *localActorRegistry) applyTeamTeammateAgentContext(ctx context.Context, session *runtimechat.Session) (bool, error) {
	if r == nil || r.Host == nil || r.Host.TeamStore == nil || session == nil {
		return false, nil
	}
	sessionID := strings.TrimSpace(session.ID)
	if sessionID == "" {
		return false, nil
	}
	teams, err := r.Host.TeamStore.ListTeams(ctx, team.TeamFilter{})
	if err != nil {
		return false, err
	}
	for _, record := range teams {
		teamID := strings.TrimSpace(record.ID)
		if teamID == "" {
			continue
		}
		teammates, err := r.Host.TeamStore.ListTeammates(ctx, teamID)
		if err != nil {
			return false, err
		}
		for _, mate := range teammates {
			if !strings.EqualFold(strings.TrimSpace(mate.SessionID), sessionID) {
				continue
			}
			leadSessionID := strings.TrimSpace(record.LeadSessionID)
			if leadSessionID == "" {
				leadSessionID = strings.TrimSpace(r.Host.baseRuntimeSessionID())
			}
			changed := false
			if leadSessionID != "" {
				changed = setLocalAgentSessionContextIfChanged(session, toolbroker.AgentSessionContextParentSessionID, leadSessionID) || changed
				changed = setLocalAgentSessionContextIfChanged(session, toolbroker.AgentSessionContextRootSessionID, leadSessionID) || changed
			}
			path := "/root/teams/" + sanitizeLocalAgentPathSegment(teamID) + "/" + sanitizeLocalAgentPathSegment(firstNonEmptyChatValue(mate.ID, mate.Name, sessionID))
			changed = setLocalAgentSessionContextIfChanged(session, toolbroker.AgentSessionContextPath, path) || changed
			changed = setLocalAgentSessionContextIfChanged(session, toolbroker.AgentSessionContextDepth, 1) || changed
			if profile := strings.TrimSpace(mate.Profile); profile != "" {
				changed = setLocalAgentSessionContextIfChanged(session, toolbroker.AgentSessionContextAgentType, profile) || changed
			}
			return changed, nil
		}
	}
	return false, nil
}

func setLocalAgentSessionContextIfChanged(session *runtimechat.Session, key string, value interface{}) bool {
	if session == nil || strings.TrimSpace(key) == "" {
		return false
	}
	switch typed := value.(type) {
	case string:
		value = strings.TrimSpace(typed)
		if value == "" {
			return false
		}
	}
	if existing, ok := session.GetContext(key); ok && localAgentSessionContextEqual(existing, value) {
		return false
	}
	session.SetContext(key, value)
	return true
}

func localAgentSessionContextEqual(existing interface{}, expected interface{}) bool {
	switch expectedValue := expected.(type) {
	case string:
		if text, ok := existing.(string); ok {
			return strings.TrimSpace(text) == expectedValue
		}
	case int:
		switch typed := existing.(type) {
		case int:
			return typed == expectedValue
		case int64:
			return int(typed) == expectedValue
		case float64:
			return int(typed) == expectedValue
		case string:
			parsed, err := strconv.Atoi(strings.TrimSpace(typed))
			return err == nil && parsed == expectedValue
		}
	}
	return false
}

func sanitizeLocalAgentPathSegment(value string) string {
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

func isLocalAgentSession(session *runtimechat.Session) bool {
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

func isClosedLocalAgentSession(session *runtimechat.Session) bool {
	if session == nil {
		return false
	}
	return session.State == runtimechat.StateClosed || session.State == runtimechat.StateArchived
}

func localAgentHasAncestor(session *runtimechat.Session, ancestorID string, byID map[string]*runtimechat.Session) bool {
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

func isLocalAgentWaitReady(status string) bool {
	switch strings.TrimSpace(status) {
	case string(runtimechat.SessionIdle), string(runtimechat.SessionWaitingApproval), string(runtimechat.SessionWaitingInput), string(runtimechat.SessionStopped), "missing":
		return true
	default:
		return false
	}
}

func isLocalAgentWaitWakeEvent(eventType string) bool {
	switch strings.TrimSpace(eventType) {
	case runtimechat.EventSessionEnd,
		runtimechat.EventSessionInterrupted,
		runtimechat.EventAssistantMessage,
		runtimechat.EventApprovalRequested,
		runtimechat.EventQuestionAsked,
		runtimechat.EventMailboxReceived:
		return true
	default:
		return false
	}
}

func localAgentCompletionStatus(event runtimeevents.Event) string {
	if strings.TrimSpace(event.Type) == runtimechat.EventSessionInterrupted {
		return string(runtimechat.SessionStopped)
	}
	if event.Payload != nil {
		if success, ok := event.Payload["success"].(bool); ok && !success {
			return "failed"
		}
		if text, ok := event.Payload["status"].(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return string(runtimechat.SessionIdle)
}

func copyLocalAgentCompletionPayload(target map[string]interface{}, payload map[string]interface{}) {
	if len(target) == 0 || len(payload) == 0 {
		return
	}
	for _, key := range []string{"success", "error", "duration", "steps", "trace_id", "turn_id"} {
		if value, ok := payload[key]; ok {
			target[key] = value
		}
	}
}

func localAgentActorBusy(state *runtimechat.RuntimeState) bool {
	if state == nil {
		return false
	}
	switch state.Status {
	case runtimechat.SessionRunning, runtimechat.SessionRewinding, runtimechat.SessionWaitingApproval, runtimechat.SessionWaitingInput:
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
		if changed, applyErr := r.applyTeamTeammateAgentContext(ctx, existing); applyErr != nil {
			return applyErr
		} else if changed {
			return r.Host.SessionStore.Update(ctx, existing)
		}
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
	if _, err := r.applyTeamTeammateAgentContext(ctx, runtimeSession); err != nil {
		return err
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

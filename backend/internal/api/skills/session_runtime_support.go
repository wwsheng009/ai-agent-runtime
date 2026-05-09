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
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

type sessionActorClient struct {
	hub        *chat.SessionHub
	store      team.Store
	eventStore chat.EventStore
	eventBus   *runtimeevents.Bus
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

func (c *sessionActorClient) TriggerTask(ctx context.Context, request team.TaskTriggerRequest) (*team.SessionResult, error) {
	store := team.Store(nil)
	if c != nil {
		store = c.store
	}
	_, _ = team.AppendTaskDispatchRequested(ctx, store, request)
	_ = c.deliverTaskAssignmentMailbox(ctx, request.SessionID, team.BuildTaskAssignmentMailboxMessage(request))
	result, err := c.SubmitPrompt(ctx, request.SessionID, request.Prompt, request.RunMeta)
	_, _ = team.AppendTaskDispatchCompleted(ctx, store, request, result, err)
	return result, err
}

func (c *sessionActorClient) deliverTaskAssignmentMailbox(ctx context.Context, sessionID string, mail team.MailMessage) error {
	if c == nil {
		return nil
	}
	return chat.DeliverMailboxEventFirst(ctx, c.eventStore, c.eventBus, c.deliverMailboxToActor, sessionID, mail)
}

func (c *sessionActorClient) deliverMailboxToActor(ctx context.Context, sessionID string, mail team.MailMessage) error {
	if c == nil || c.hub == nil {
		return fmt.Errorf("session hub not configured")
	}
	actor, err := c.hub.GetOrCreate(strings.TrimSpace(sessionID))
	if err != nil {
		return err
	}
	return actor.DeliverMailboxMessage(ctx, mail)
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
	if err := c.reserveOrRegisterAgentSpawn(ctx, parentSession, parentSessionID, childSession, args, childDepth); err != nil {
		_ = storage.Delete(ctx, childSession.ID)
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
		completionMessage, mailboxErr := c.deliverSubagentCompletionMailbox(context.Background(), parentSessionID, childSessionID, childPath, childType, eventType, payload)
		payload = toolbroker.AnnotateSubagentCompletionDisplayMirror(payload, completionMessage, mailboxErr)
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

func (c *sessionAgentController) deliverSubagentCompletionMailbox(ctx context.Context, parentSessionID, childSessionID, childPath, childType, sourceEventType string, payload map[string]interface{}) (team.MailMessage, error) {
	if c == nil || c.handler == nil {
		return team.MailMessage{}, nil
	}
	parentSessionID = strings.TrimSpace(parentSessionID)
	childSessionID = strings.TrimSpace(childSessionID)
	if parentSessionID == "" || childSessionID == "" {
		return team.MailMessage{}, nil
	}
	message := toolbroker.BuildSubagentCompletionMailboxMessage(parentSessionID, childSessionID, childPath, childType, sourceEventType, payload)
	message.ID = "subagent_completed_" + sanitizeAPIAgentPathSegment(childSessionID)
	err := chat.DeliverMailboxEventFirst(ctx, c.handler.getSessionEventStore(), c.handler.getRuntimeEventBus(), c.deliverMailboxToActor, parentSessionID, message)
	return message, err
}

func (c *sessionAgentController) reserveOrRegisterAgentSpawn(ctx context.Context, parentSession *chat.Session, parentSessionID string, childSession *chat.Session, args toolbroker.SpawnAgentArgs, childDepth int) error {
	if c == nil || c.handler == nil || childSession == nil {
		return nil
	}
	store := c.handler.getAgentControlAgentStore()
	if store == nil {
		return nil
	}
	parentSessionID = strings.TrimSpace(parentSessionID)
	childSessionID := strings.TrimSpace(childSession.ID)
	if childSessionID == "" {
		return fmt.Errorf("child session id is required")
	}
	rootRecord := apiRootAgentRecord(parentSession, parentSessionID)
	childRecord := apiChildAgentRecord(parentSession, parentSessionID, childSession, args, childDepth)
	if reserver, ok := store.(agentcontrol.AgentSpawnReservationStore); ok && reserver != nil {
		_, err := reserver.ReserveAgentControlAgentSpawn(ctx, rootRecord, childRecord, c.agentsConfig().MaxThreads)
		return err
	}
	if _, err := store.UpsertAgentControlAgent(ctx, rootRecord); err != nil {
		return err
	}
	if _, err := store.UpsertAgentControlAgent(ctx, childRecord); err != nil {
		return err
	}
	return nil
}

func apiRootAgentRecord(parentSession *chat.Session, parentSessionID string) agentcontrol.AgentRecord {
	parentSessionID = strings.TrimSpace(parentSessionID)
	rootSessionID := apiAgentRootSessionID(parentSession, parentSessionID)
	if rootSessionID == "" {
		rootSessionID = parentSessionID
	}
	return agentcontrol.AgentRecord{
		AgentID:       apiRootAgentID(rootSessionID),
		RootSessionID: rootSessionID,
		SessionID:     rootSessionID,
		AgentPath:     "/root",
		AgentType:     agentcontrol.AgentTypeRoot,
		Status:        agentcontrol.AgentStatusActive,
	}
}

func apiChildAgentRecord(parentSession *chat.Session, parentSessionID string, childSession *chat.Session, args toolbroker.SpawnAgentArgs, childDepth int) agentcontrol.AgentRecord {
	childSessionID := ""
	if childSession != nil {
		childSessionID = strings.TrimSpace(childSession.ID)
	}
	rootSessionID := apiAgentRootSessionID(parentSession, strings.TrimSpace(parentSessionID))
	agentType := firstNonEmptyString(strings.TrimSpace(args.AgentType), agentcontrol.AgentTypeChild)
	return agentcontrol.AgentRecord{
		AgentID:         childSessionID,
		RootSessionID:   rootSessionID,
		ParentAgentID:   apiAgentIDForSession(parentSession, strings.TrimSpace(parentSessionID)),
		ParentSessionID: strings.TrimSpace(parentSessionID),
		SessionID:       childSessionID,
		AgentPath:       apiAgentChildPath(parentSession, childSessionID),
		Depth:           childDepth,
		AgentType:       agentType,
		Workflow:        agentcontrol.WorkflowSpawnAgent,
		Status:          agentcontrol.AgentStatusActive,
	}
}

func apiAgentIDForSession(session *chat.Session, sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if session == nil || !isAPIAgentSession(session) {
		rootSessionID := apiAgentRootSessionID(session, sessionID)
		return apiRootAgentID(rootSessionID)
	}
	return sessionID
}

func apiRootAgentID(rootSessionID string) string {
	rootSessionID = strings.TrimSpace(rootSessionID)
	if rootSessionID == "" {
		return "root"
	}
	return "root:" + rootSessionID
}

func firstNonZeroInt(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func (c *sessionAgentController) listAgentsFromRegistry(ctx context.Context, parentSessionID string, args toolbroker.ListAgentsArgs, store agentcontrol.AgentRegistryStore) (*toolbroker.AgentListResult, error) {
	if store == nil {
		return nil, nil
	}
	rootSessionID, parentPath, err := c.agentRegistryRootAndPath(ctx, parentSessionID)
	if err != nil {
		return nil, err
	}
	if rootSessionID == "" {
		return nil, nil
	}
	pathPrefix := strings.TrimSpace(args.PathPrefix)
	if pathPrefix == "" && parentPath != "" && parentPath != "/root" {
		pathPrefix = parentPath
	}
	records, err := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
		RootSessionID: rootSessionID,
		PathPrefix:    pathPrefix,
		IncludeClosed: args.IncludeClosed,
	})
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}
	agents := make([]toolbroker.AgentStatusResult, 0, len(records))
	for _, record := range records {
		record = record.Normalize()
		if strings.EqualFold(record.AgentType, agentcontrol.AgentTypeRoot) || record.AgentPath == "/root" {
			continue
		}
		status, err := c.agentStatusFromRecord(ctx, record)
		if err != nil {
			return nil, err
		}
		agents = append(agents, status)
	}
	sort.SliceStable(agents, func(i, j int) bool {
		left := firstNonEmptyString(agents[i].Path, agents[i].SessionID, agents[i].ID)
		right := firstNonEmptyString(agents[j].Path, agents[j].SessionID, agents[j].ID)
		return left < right
	})
	return &toolbroker.AgentListResult{Agents: agents, Count: len(agents)}, nil
}

func (c *sessionAgentController) agentRegistryRootAndPath(ctx context.Context, parentSessionID string) (string, string, error) {
	parentSessionID = strings.TrimSpace(parentSessionID)
	if parentSessionID == "" {
		return "", "", nil
	}
	if strings.HasPrefix(parentSessionID, "/") {
		if store := c.handler.getAgentControlAgentStore(); store != nil {
			records, err := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
				AgentPath:     parentSessionID,
				IncludeClosed: true,
				Limit:         1,
			})
			if err != nil {
				return "", "", err
			}
			if len(records) > 0 {
				return records[0].RootSessionID, records[0].AgentPath, nil
			}
		}
		return "", parentSessionID, nil
	}
	parent, err := c.handler.sessionManager.Get(ctx, parentSessionID)
	if err != nil {
		if stderrors.Is(err, chat.ErrSessionNotFound) || strings.Contains(strings.ToLower(err.Error()), "not found") {
			return parentSessionID, "", nil
		}
		return "", "", err
	}
	return apiAgentRootSessionID(parent, parentSessionID), apiAgentSessionPath(parent), nil
}

func (c *sessionAgentController) agentStatusFromRecord(ctx context.Context, record agentcontrol.AgentRecord) (toolbroker.AgentStatusResult, error) {
	sessionID := strings.TrimSpace(record.SessionID)
	var result *toolbroker.AgentStatusResult
	if sessionID != "" {
		snapshot, err := c.snapshot(ctx, sessionID)
		if err != nil {
			return toolbroker.AgentStatusResult{}, err
		}
		result = snapshot
	}
	if result == nil {
		result = &toolbroker.AgentStatusResult{
			ID:        firstNonEmptyString(record.AgentID, record.SessionID),
			SessionID: sessionID,
			Status:    "missing",
		}
	}
	result.ID = firstNonEmptyString(strings.TrimSpace(result.ID), record.AgentID, sessionID)
	result.SessionID = firstNonEmptyString(strings.TrimSpace(result.SessionID), sessionID)
	result.ParentSessionID = firstNonEmptyString(strings.TrimSpace(result.ParentSessionID), record.ParentSessionID)
	result.Path = firstNonEmptyString(strings.TrimSpace(result.Path), record.AgentPath)
	result.Depth = firstNonZeroInt(result.Depth, record.Depth)
	result.AgentType = firstNonEmptyString(strings.TrimSpace(result.AgentType), record.AgentType)
	result.TeamID = firstNonEmptyString(strings.TrimSpace(result.TeamID), record.TeamID)
	result.TeammateID = firstNonEmptyString(strings.TrimSpace(result.TeammateID), record.TeammateID)
	if record.Closed() {
		result.Status = string(chat.SessionStopped)
		if result.SessionState == "" {
			result.SessionState = string(chat.StateClosed)
		}
	}
	return *result, nil
}

func (c *sessionAgentController) List(ctx context.Context, parentSessionID string, args toolbroker.ListAgentsArgs) (*toolbroker.AgentListResult, error) {
	if c == nil || c.handler == nil || c.handler.sessionManager == nil {
		return nil, fmt.Errorf("session manager not configured")
	}
	parentSessionID = firstNonEmptyString(strings.TrimSpace(args.ParentSessionID), strings.TrimSpace(parentSessionID))
	if store := c.handler.getAgentControlAgentStore(); store != nil {
		result, err := c.listAgentsFromRegistry(ctx, parentSessionID, args, store)
		if err != nil {
			return nil, err
		}
		if result != nil {
			return result, nil
		}
	}
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
	delivered := false
	triggered := false
	if trigger && !c.apiAgentSessionBusy(ctx, sessionID) {
		if actor := c.apiAgentActor(ctx, sessionID); actor != nil && !apiAgentActorBusy(actor.State()) {
			if err := actor.SubmitPromptAsync(ctx, message, nil); err != nil {
				return nil, err
			}
			triggered = true
		}
	}
	if !triggered {
		mail := toolbroker.BuildAgentMailboxMessage(fromSessionID, sessionID, message, trigger)
		if err := c.deliverAgentMailboxEvent(ctx, sessionID, mail); err != nil {
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

func (c *sessionAgentController) apiAgentSessionBusy(ctx context.Context, sessionID string) bool {
	if c == nil || c.handler == nil {
		return false
	}
	if hub := c.handler.getSessionHub(); hub != nil {
		if actor, ok := hub.Get(strings.TrimSpace(sessionID)); ok && actor != nil && apiAgentActorBusy(actor.State()) {
			return true
		}
	}
	if store := c.handler.getSessionRuntimeStore(); store != nil {
		state, err := store.LoadState(ctx, strings.TrimSpace(sessionID))
		if err == nil && apiAgentActorBusy(state) {
			return true
		}
	}
	return false
}

func (c *sessionAgentController) apiAgentActor(ctx context.Context, sessionID string) *chat.SessionActor {
	if c == nil || c.handler == nil {
		return nil
	}
	hub := c.handler.getSessionHub()
	if hub == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	if actor, ok := hub.Get(sessionID); ok && actor != nil {
		return actor
	}
	actor, err := hub.GetOrCreate(sessionID)
	if err != nil {
		return nil
	}
	return actor
}

func (c *sessionAgentController) deliverAgentMailboxEvent(ctx context.Context, sessionID string, mail team.MailMessage) error {
	if c == nil || c.handler == nil {
		return fmt.Errorf("handler not configured")
	}
	return chat.DeliverMailboxEventFirst(ctx, c.handler.getSessionEventStore(), c.handler.getRuntimeEventBus(), c.deliverMailboxToActor, sessionID, mail)
}

func (c *sessionAgentController) deliverMailboxToActor(ctx context.Context, sessionID string, mail team.MailMessage) error {
	actor := c.apiAgentActor(ctx, sessionID)
	if actor == nil {
		return fmt.Errorf("session hub not configured")
	}
	return actor.DeliverMailboxMessage(ctx, mail)
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
	if args.MailboxOnly {
		return c.waitForMailboxEvent(ctx, args)
	}
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
	wakeCh, unsubscribe := c.subscribeWaitEvents(waitCtx, sessionIDs)
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

func (c *sessionAgentController) waitForMailboxEvent(ctx context.Context, args toolbroker.WaitAgentArgs) (*toolbroker.AgentWaitResult, error) {
	if c == nil || c.handler == nil {
		return nil, fmt.Errorf("handler not configured")
	}
	sessionID := firstNonEmptyString(strings.TrimSpace(args.ID), strings.TrimSpace(args.SessionID))
	if sessionID == "" {
		return nil, fmt.Errorf("session id is required")
	}
	store := c.handler.getSessionEventStore()
	if store == nil {
		return nil, fmt.Errorf("session event store not configured")
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
	wakeCh, unsubscribe := c.subscribeMailboxEvents(waitCtx, store, sessionID)
	defer unsubscribe()
	for {
		if events, ok, hasMailboxRows, err := listAPIMailboxEvents(waitCtx, store, sessionID, args.AfterSeq, 64); err != nil {
			return nil, err
		} else if ok {
			if result := buildMailboxWaitResult(sessionID, events); result != nil {
				return result, nil
			}
			if hasMailboxRows {
				select {
				case <-waitCtx.Done():
					return &toolbroker.AgentWaitResult{TimedOut: true}, nil
				case <-wakeCh:
				case <-time.After(500 * time.Millisecond):
				}
				continue
			}
		}
		events, err := store.ListEvents(waitCtx, sessionID, args.AfterSeq, 64)
		if err != nil {
			return nil, err
		}
		if result := buildMailboxWaitResult(sessionID, events); result != nil {
			return result, nil
		}
		select {
		case <-waitCtx.Done():
			return &toolbroker.AgentWaitResult{TimedOut: true}, nil
		case <-wakeCh:
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (c *sessionAgentController) subscribeWaitEvents(ctx context.Context, sessionIDs []string) (<-chan struct{}, func()) {
	if c == nil || c.handler == nil || len(sessionIDs) == 0 {
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
	wake := func() {
		select {
		case wakeCh <- struct{}{}:
		default:
		}
	}
	var unsubscribeStores []func()
	if store := c.handler.getSessionEventStore(); store != nil {
		if watcher, ok := store.(chat.EventWatcherStore); ok && watcher != nil {
			for sessionID := range targets {
				eventCh, unwatch := watcher.WatchEvents(ctx, sessionID)
				unsubscribeStores = append(unsubscribeStores, unwatch)
				go func(target string, ch <-chan runtimeevents.Event) {
					for {
						select {
						case <-ctx.Done():
							return
						case event := <-ch:
							if !strings.EqualFold(strings.TrimSpace(event.SessionID), target) {
								continue
							}
							if !isAgentWaitWakeEvent(event.Type) {
								continue
							}
							wake()
						}
					}
				}(sessionID, eventCh)
			}
		}
	}
	bus := c.handler.getRuntimeEventBus()
	if bus == nil {
		return wakeCh, func() {
			for _, unwatch := range unsubscribeStores {
				if unwatch != nil {
					unwatch()
				}
			}
		}
	}
	handler := func(event runtimeevents.Event) {
		if _, ok := targets[strings.TrimSpace(event.SessionID)]; !ok {
			return
		}
		if !isAgentWaitWakeEvent(event.Type) {
			return
		}
		wake()
	}
	unsubscribeBus := bus.SubscribeCancelable("", handler)
	return wakeCh, func() {
		for _, unwatch := range unsubscribeStores {
			if unwatch != nil {
				unwatch()
			}
		}
		if unsubscribeBus != nil {
			unsubscribeBus()
		}
	}
}

func (c *sessionAgentController) ReadEvents(ctx context.Context, args toolbroker.ReadAgentEventsArgs) (*toolbroker.AgentEventsResult, error) {
	sessionID := firstNonEmptyString(strings.TrimSpace(args.ID), strings.TrimSpace(args.SessionID))
	if sessionID == "" {
		return nil, fmt.Errorf("id is required")
	}
	if !args.MailboxOnly {
		resolvedSessionID, err := c.resolveTargetSessionID(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		sessionID = resolvedSessionID
	}
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
	wakeCh, unsubscribe := c.subscribeReadEvents(readCtx, store, sessionID)
	if args.MailboxOnly {
		unsubscribe()
		wakeCh, unsubscribe = c.subscribeMailboxEvents(readCtx, store, sessionID)
	}
	defer unsubscribe()
	for {
		if args.MailboxOnly {
			if events, ok, hasMailboxRows, err := listAPIMailboxEvents(readCtx, store, sessionID, args.AfterSeq, limit); err != nil {
				return nil, err
			} else if ok {
				if len(events) > 0 || (hasMailboxRows && waitMs == 0) {
					return buildAgentEventsResult(sessionID, events), nil
				}
				if hasMailboxRows {
					select {
					case <-readCtx.Done():
						result := buildAgentEventsResult(sessionID, nil)
						result.TimedOut = true
						return result, nil
					case <-wakeCh:
					case <-time.After(500 * time.Millisecond):
					}
					continue
				}
			}
		}
		events, err := store.ListEvents(readCtx, sessionID, args.AfterSeq, limit)
		if err != nil {
			return nil, err
		}
		if args.MailboxOnly {
			events = filterAPIMailboxWaitEvents(events)
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

func filterAPIMailboxWaitEvents(events []runtimeevents.Event) []runtimeevents.Event {
	filtered := make([]runtimeevents.Event, 0, len(events))
	for _, event := range events {
		if !isAPIMailboxWaitEvent(event) {
			continue
		}
		filtered = append(filtered, event)
	}
	return filtered
}

func listAPIMailboxEvents(ctx context.Context, store chat.EventStore, sessionID string, afterSeq int64, limit int) ([]runtimeevents.Event, bool, bool, error) {
	messages, ok, hasMailboxRows, err := chat.ListMailboxAgentControlFirst(ctx, store, sessionID, afterSeq, limit)
	if err != nil {
		return nil, ok, false, err
	}
	if !ok {
		return nil, false, false, nil
	}
	return apiMailboxMessagesToEvents(sessionID, messages), true, hasMailboxRows, nil
}

func apiMailboxMessagesToEvents(sessionID string, messages []team.MailMessage) []runtimeevents.Event {
	events := make([]runtimeevents.Event, 0, len(messages))
	for _, message := range messages {
		event := chat.NewMailboxReceivedEvent(sessionID, message)
		if event.Payload == nil {
			event.Payload = map[string]interface{}{}
		}
		presentationSeq := message.Seq
		if message.SessionMailboxSeq > 0 {
			presentationSeq = message.SessionMailboxSeq
		}
		event.Payload["seq"] = presentationSeq
		if message.ControlSeq > 0 {
			event.Payload["control_seq"] = message.ControlSeq
		}
		if message.SessionMailboxSeq > 0 {
			event.Payload["session_mailbox_seq"] = message.SessionMailboxSeq
			event.Payload["mailbox_seq"] = message.SessionMailboxSeq
		} else {
			event.Payload["mailbox_seq"] = message.Seq
		}
		events = append(events, event)
	}
	return events
}

func (c *sessionAgentController) subscribeMailboxEvents(ctx context.Context, store chat.EventStore, sessionID string) (<-chan struct{}, func()) {
	if c == nil || c.handler == nil {
		return nil, func() {}
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, func() {}
	}
	wakeCh := make(chan struct{}, 1)
	wake := func() {
		select {
		case wakeCh <- struct{}{}:
		default:
		}
	}
	unsubscribes := make([]func(), 0, 2)
	if mailCh, unwatch, ok := chat.WatchMailboxAgentControlFirst(ctx, store, sessionID); ok {
		unsubscribes = append(unsubscribes, unwatch)
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case message, open := <-mailCh:
					if !open {
						return
					}
					if strings.TrimSpace(message.ToAgent) != "" || message.Seq > 0 {
						wake()
					}
				}
			}
		}()
	}
	if watcher, ok := store.(chat.EventWatcherStore); ok && watcher != nil {
		eventCh, unwatch := watcher.WatchEvents(ctx, sessionID)
		unsubscribes = append(unsubscribes, unwatch)
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case event := <-eventCh:
					if strings.EqualFold(strings.TrimSpace(event.SessionID), sessionID) && isAPIMailboxWaitEvent(event) {
						wake()
					}
				}
			}
		}()
	}
	if len(unsubscribes) > 0 {
		return wakeCh, func() {
			for _, unsubscribe := range unsubscribes {
				if unsubscribe != nil {
					unsubscribe()
				}
			}
		}
	}
	return c.subscribeReadEvents(ctx, store, sessionID)
}

func (c *sessionAgentController) subscribeReadEvents(ctx context.Context, store chat.EventStore, sessionID string) (<-chan struct{}, func()) {
	if c == nil || c.handler == nil {
		return nil, func() {}
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, func() {}
	}
	wakeCh := make(chan struct{}, 1)
	wake := func() {
		select {
		case wakeCh <- struct{}{}:
		default:
		}
	}
	var unsubscribeStore func()
	if watcher, ok := store.(chat.EventWatcherStore); ok && watcher != nil {
		eventCh, unwatch := watcher.WatchEvents(ctx, sessionID)
		unsubscribeStore = unwatch
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case event := <-eventCh:
					if strings.EqualFold(strings.TrimSpace(event.SessionID), sessionID) {
						wake()
					}
				}
			}
		}()
	}
	bus := c.handler.getRuntimeEventBus()
	if bus == nil {
		return wakeCh, func() {
			if unsubscribeStore != nil {
				unsubscribeStore()
			}
		}
	}
	handler := func(event runtimeevents.Event) {
		if !strings.EqualFold(strings.TrimSpace(event.SessionID), sessionID) {
			return
		}
		wake()
	}
	unsubscribeBus := bus.SubscribeCancelable("", handler)
	return wakeCh, func() {
		if unsubscribeStore != nil {
			unsubscribeStore()
		}
		if unsubscribeBus != nil {
			unsubscribeBus()
		}
	}
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
	if store := c.handler.getAgentControlAgentStore(); store != nil {
		rootSessionID, targetPath, err := c.closeTargetRegistryRootAndPath(ctx, target, targetSessionID)
		if err != nil {
			return nil, err
		}
		if rootSessionID != "" && targetPath != "" {
			_, err = store.CloseAgentControlAgentSubtree(ctx, rootSessionID, targetPath, time.Now().UTC())
			if err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}

func (c *sessionAgentController) resolveCloseTargets(ctx context.Context, target string) (string, []string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", nil, fmt.Errorf("id is required")
	}
	if targetSessionID, closeIDs, ok, err := c.resolveCloseTargetsFromRegistry(ctx, target); err != nil || ok {
		return targetSessionID, closeIDs, err
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

func (c *sessionAgentController) resolveCloseTargetsFromRegistry(ctx context.Context, target string) (string, []string, bool, error) {
	if c == nil || c.handler == nil {
		return "", nil, false, nil
	}
	store := c.handler.getAgentControlAgentStore()
	if store == nil {
		return "", nil, false, nil
	}
	record, ok, err := c.resolveAgentRecord(ctx, target, true)
	if err != nil || !ok {
		return "", nil, ok, err
	}
	records, err := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
		RootSessionID: record.RootSessionID,
		PathPrefix:    record.AgentPath,
		IncludeClosed: true,
	})
	if err != nil {
		return "", nil, true, err
	}
	if len(records) == 0 {
		records = []agentcontrol.AgentRecord{record}
	}
	sort.SliceStable(records, func(i, j int) bool {
		if strings.EqualFold(records[i].AgentPath, record.AgentPath) {
			return true
		}
		if strings.EqualFold(records[j].AgentPath, record.AgentPath) {
			return false
		}
		return records[i].AgentPath < records[j].AgentPath
	})
	closeIDs := make([]string, 0, len(records))
	seen := map[string]struct{}{}
	for _, item := range records {
		itemPath := strings.TrimSpace(item.AgentPath)
		targetPath := strings.TrimRight(strings.TrimSpace(record.AgentPath), "/")
		if targetPath != "" && itemPath != targetPath && !strings.HasPrefix(itemPath, targetPath+"/") {
			continue
		}
		sessionID := strings.TrimSpace(item.SessionID)
		if sessionID == "" {
			continue
		}
		if _, exists := seen[strings.ToLower(sessionID)]; exists {
			continue
		}
		seen[strings.ToLower(sessionID)] = struct{}{}
		closeIDs = append(closeIDs, sessionID)
	}
	targetSessionID := firstNonEmptyString(record.SessionID, record.AgentID, target)
	if len(closeIDs) == 0 && targetSessionID != "" {
		closeIDs = append(closeIDs, targetSessionID)
	}
	return targetSessionID, closeIDs, true, nil
}

func (c *sessionAgentController) closeTargetRegistryRootAndPath(ctx context.Context, target, targetSessionID string) (string, string, error) {
	record, ok, err := c.resolveAgentRecord(ctx, firstNonEmptyString(target, targetSessionID), true)
	if err != nil || !ok {
		return "", "", err
	}
	return record.RootSessionID, record.AgentPath, nil
}

func (c *sessionAgentController) resolveTargetSessionID(ctx context.Context, target string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return target, nil
	}
	if record, ok, err := c.resolveAgentRecord(ctx, target, false); err != nil || ok {
		if err != nil {
			return "", err
		}
		if sessionID := strings.TrimSpace(record.SessionID); sessionID != "" {
			return sessionID, nil
		}
	}
	if !strings.HasPrefix(target, "/") {
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

func (c *sessionAgentController) resolveAgentRecord(ctx context.Context, target string, includeClosed bool) (agentcontrol.AgentRecord, bool, error) {
	if c == nil || c.handler == nil {
		return agentcontrol.AgentRecord{}, false, nil
	}
	store := c.handler.getAgentControlAgentStore()
	if store == nil {
		return agentcontrol.AgentRecord{}, false, nil
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return agentcontrol.AgentRecord{}, false, nil
	}
	filter := agentcontrol.AgentFilter{IncludeClosed: includeClosed, Limit: 1}
	if strings.HasPrefix(target, "/") {
		filter.AgentPath = target
	} else {
		filter.SessionID = target
	}
	records, err := store.ListAgentControlAgents(ctx, filter)
	if err != nil {
		return agentcontrol.AgentRecord{}, false, err
	}
	if len(records) == 0 && !strings.HasPrefix(target, "/") {
		records, err = store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
			AgentID:       target,
			IncludeClosed: includeClosed,
			Limit:         1,
		})
		if err != nil {
			return agentcontrol.AgentRecord{}, false, err
		}
	}
	if len(records) == 0 {
		return agentcontrol.AgentRecord{}, false, nil
	}
	return records[0].Normalize(), true, nil
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
		if err := c.enrichAgentTeamProjection(ctx, session, result); err != nil {
			return nil, err
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

func (c *sessionAgentController) enrichAgentTeamProjection(ctx context.Context, session *chat.Session, result *toolbroker.AgentStatusResult) error {
	if session == nil || result == nil {
		return nil
	}
	if value, ok := session.GetContext(toolbroker.AgentSessionContextTeamID); ok {
		if text, ok := value.(string); ok {
			result.TeamID = strings.TrimSpace(text)
		}
	}
	if value, ok := session.GetContext(toolbroker.AgentSessionContextTeammateID); ok {
		if text, ok := value.(string); ok {
			result.TeammateID = strings.TrimSpace(text)
		}
	}
	if c == nil || c.handler == nil || c.handler.teamStore == nil {
		return nil
	}
	if result.TeamID == "" || result.TeammateID == "" {
		record, teammate, err := team.FindTeammateBySession(ctx, c.handler.teamStore, session.ID)
		if err != nil {
			return err
		}
		if record != nil {
			result.TeamID = strings.TrimSpace(record.ID)
		}
		if teammate != nil {
			result.TeammateID = strings.TrimSpace(teammate.ID)
		}
	}
	task, err := team.ActiveAgentControlTaskRecordForAssignee(ctx, c.handler.teamStore, result.TeamID, result.TeammateID)
	if err != nil {
		return err
	}
	if task != nil {
		result.CurrentTaskID = strings.TrimSpace(task.ID)
		result.CurrentTaskStatus = strings.TrimSpace(task.Status)
	}
	return nil
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
	if store := c.handler.getAgentControlAgentStore(); store != nil {
		rootSessionID := apiAgentRootSessionID(parentSession, parentSessionID)
		records, err := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
			RootSessionID: rootSessionID,
		})
		if err != nil {
			return err
		}
		count := 0
		for _, record := range records {
			if record.AgentPath == "/root" || strings.EqualFold(record.AgentType, agentcontrol.AgentTypeRoot) {
				continue
			}
			count++
		}
		if count >= limits.MaxThreads {
			return fmt.Errorf("agent spawn thread limit reached: max_threads=%d active_children=%d", limits.MaxThreads, count)
		}
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
		sessions, err := c.listAllSessions(ctx)
		if err != nil {
			return nil, err
		}
		return c.applyTeamTeammateAgentContexts(ctx, sessions, "")
	}
	userID := "agent"
	if preferredSessionID != "" {
		if session, err := c.handler.sessionManager.Get(ctx, preferredSessionID); err == nil && session != nil && strings.TrimSpace(session.UserID) != "" {
			userID = strings.TrimSpace(session.UserID)
		}
	}
	sessions, err := c.handler.sessionManager.List(ctx, userID)
	if err != nil {
		return nil, err
	}
	return c.applyTeamTeammateAgentContexts(ctx, sessions, preferredSessionID)
}

func (c *sessionAgentController) applyTeamTeammateAgentContexts(ctx context.Context, sessions []*chat.Session, fallbackLeadSessionID string) ([]*chat.Session, error) {
	if c == nil || c.handler == nil || c.handler.teamStore == nil || c.handler.sessionManager == nil {
		return sessions, nil
	}
	for _, session := range sessions {
		changed, err := c.applyTeamTeammateAgentContext(ctx, session, fallbackLeadSessionID)
		if err != nil {
			return nil, err
		}
		if changed {
			if err := c.handler.sessionManager.Update(ctx, session); err != nil {
				return nil, err
			}
		}
	}
	return sessions, nil
}

func (c *sessionAgentController) applyTeamTeammateAgentContext(ctx context.Context, session *chat.Session, fallbackLeadSessionID string) (bool, error) {
	if c == nil || c.handler == nil || c.handler.teamStore == nil || session == nil {
		return false, nil
	}
	sessionID := strings.TrimSpace(session.ID)
	if sessionID == "" {
		return false, nil
	}
	teams, err := c.handler.teamStore.ListTeams(ctx, team.TeamFilter{})
	if err != nil {
		return false, err
	}
	for _, record := range teams {
		teamID := strings.TrimSpace(record.ID)
		if teamID == "" {
			continue
		}
		teammates, err := c.handler.teamStore.ListTeammates(ctx, teamID)
		if err != nil {
			return false, err
		}
		for _, mate := range teammates {
			if !strings.EqualFold(strings.TrimSpace(mate.SessionID), sessionID) {
				continue
			}
			leadSessionID := firstNonEmptyString(strings.TrimSpace(record.LeadSessionID), strings.TrimSpace(fallbackLeadSessionID))
			changed := false
			if leadSessionID != "" {
				changed = agentcontrol.SetContextIfChanged(session, toolbroker.AgentSessionContextParentSessionID, leadSessionID) || changed
				changed = agentcontrol.SetContextIfChanged(session, toolbroker.AgentSessionContextRootSessionID, leadSessionID) || changed
			}
			changed = agentcontrol.SetContextIfChanged(session, toolbroker.AgentSessionContextTeamID, teamID) || changed
			changed = agentcontrol.SetContextIfChanged(session, toolbroker.AgentSessionContextTeammateID, mate.ID) || changed
			path := agentcontrol.TeamTeammatePath(teamID, mate.ID, mate.Name, sessionID)
			changed = agentcontrol.SetContextIfChanged(session, toolbroker.AgentSessionContextPath, path) || changed
			changed = agentcontrol.SetContextIfChanged(session, toolbroker.AgentSessionContextDepth, 1) || changed
			if profile := strings.TrimSpace(mate.Profile); profile != "" {
				changed = agentcontrol.SetContextIfChanged(session, toolbroker.AgentSessionContextAgentType, profile) || changed
			}
			if err := c.upsertTeamTeammateAgentRecord(ctx, record, mate, session, leadSessionID, path); err != nil {
				return false, err
			}
			return changed, nil
		}
	}
	return false, nil
}

func (c *sessionAgentController) upsertTeamTeammateAgentRecord(ctx context.Context, record team.Team, mate team.Teammate, session *chat.Session, leadSessionID, path string) error {
	if c == nil || c.handler == nil || session == nil {
		return nil
	}
	store := c.handler.getAgentControlAgentStore()
	if store == nil {
		return nil
	}
	sessionID := strings.TrimSpace(session.ID)
	teamID := strings.TrimSpace(record.ID)
	rootSessionID := firstNonEmptyString(strings.TrimSpace(leadSessionID), strings.TrimSpace(record.LeadSessionID))
	rootSessionIDIsSynthetic := false
	if rootSessionID == "" {
		rootSessionID = "team:" + teamID
		rootSessionIDIsSynthetic = true
	}
	if rootSessionID == "" {
		return nil
	}
	rootSessionBinding := rootSessionID
	if rootSessionIDIsSynthetic {
		rootSessionBinding = ""
	}
	if _, err := store.UpsertAgentControlAgent(ctx, agentcontrol.AgentRecord{
		AgentID:       apiRootAgentID(rootSessionID),
		RootSessionID: rootSessionID,
		SessionID:     rootSessionBinding,
		AgentPath:     "/root",
		AgentType:     agentcontrol.AgentTypeRoot,
		Status:        agentcontrol.AgentStatusActive,
	}); err != nil {
		return err
	}
	agentID := "team:" + teamID + ":" + firstNonEmptyString(strings.TrimSpace(mate.ID), sessionID)
	_, err := store.UpsertAgentControlAgent(ctx, agentcontrol.AgentRecord{
		AgentID:         agentID,
		RootSessionID:   rootSessionID,
		ParentAgentID:   apiRootAgentID(rootSessionID),
		ParentSessionID: rootSessionID,
		SessionID:       sessionID,
		AgentPath:       path,
		Depth:           1,
		AgentType:       firstNonEmptyString(strings.TrimSpace(mate.Profile), agentcontrol.AgentTypeTeamTeammate),
		Nickname:        strings.TrimSpace(mate.Name),
		Workflow:        agentcontrol.WorkflowSpawnTeam,
		TeamID:          teamID,
		TeammateID:      strings.TrimSpace(mate.ID),
		Status:          agentcontrol.AgentStatusActive,
	})
	return err
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
	return agentcontrol.ChildDepth(parent)
}

func apiAgentSessionDepth(session *chat.Session) int {
	return agentcontrol.SessionDepth(session)
}

func apiAgentRootSessionID(session *chat.Session, fallback string) string {
	sessionID := ""
	if session != nil {
		sessionID = session.ID
	}
	return agentcontrol.RootSessionID(session, sessionID, fallback)
}

func apiAgentChildPath(parent *chat.Session, sessionID string) string {
	parentID := ""
	if parent != nil {
		parentID = parent.ID
	}
	return agentcontrol.ChildPath(parent, parentID, sessionID, parent != nil && isAPIAgentSession(parent))
}

func apiAgentSessionPath(session *chat.Session) string {
	sessionID := ""
	if session != nil {
		sessionID = session.ID
	}
	return agentcontrol.SessionPath(session, sessionID, isAPIAgentSession(session))
}

func sanitizeAPIAgentPathSegment(value string) string {
	return agentcontrol.SanitizePathSegment(value)
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
	if value, ok := payload["seq"]; ok {
		target["source_event_seq"] = value
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

func buildMailboxWaitResult(sessionID string, events []runtimeevents.Event) *toolbroker.AgentWaitResult {
	filtered := filterAPIMailboxWaitEvents(events)
	if len(filtered) == 0 {
		return nil
	}
	agentEvents := buildAgentEventsResult(sessionID, filtered)
	result := &toolbroker.AgentWaitResult{
		Events:    agentEvents.Events,
		LatestSeq: agentEvents.LatestSeq,
	}
	if len(agentEvents.Events) > 0 {
		event := agentEvents.Events[0]
		result.Event = &event
		result.MatchedSessionID = event.SessionID
		result.ReadyCount = 1
	}
	return result
}

func isAPIMailboxWaitEvent(event runtimeevents.Event) bool {
	switch strings.TrimSpace(event.Type) {
	case chat.EventMailboxReceived,
		"subagent.completed",
		"team.completed",
		"team.summary":
		return true
	default:
		return false
	}
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

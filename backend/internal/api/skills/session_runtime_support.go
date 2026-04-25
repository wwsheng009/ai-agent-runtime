package skills

import (
	"context"
	stderrors "errors"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
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

type sessionAgentController struct {
	handler *Handler
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

	forkContext := args.ForkContext != nil && *args.ForkContext
	if forkContext && parentSession != nil {
		childSession = parentSession.Clone()
		childSession.ID = sessionID
		childSession.UserID = userID
		childSession.UpdateState(chat.StateActive)
	}
	childSession.SetContext(toolbroker.AgentSessionContextParentSessionID, strings.TrimSpace(parentSessionID))
	if agentType := strings.TrimSpace(args.AgentType); agentType != "" {
		childSession.SetContext(toolbroker.AgentSessionContextAgentType, agentType)
	}
	if model := strings.TrimSpace(args.Model); model != "" {
		childSession.SetContext(toolbroker.AgentSessionContextRequestedModel, model)
	}
	if err := storage.Save(ctx, childSession); err != nil {
		return nil, err
	}

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

func (c *sessionAgentController) SendInput(ctx context.Context, args toolbroker.SendAgentInputArgs) (*toolbroker.AgentStatusResult, error) {
	if c == nil || c.handler == nil {
		return nil, fmt.Errorf("handler not configured")
	}
	sessionID := firstNonEmptyString(strings.TrimSpace(args.ID), strings.TrimSpace(args.SessionID))
	if sessionID == "" {
		return nil, fmt.Errorf("id is required")
	}
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
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (c *sessionAgentController) ReadEvents(ctx context.Context, args toolbroker.ReadAgentEventsArgs) (*toolbroker.AgentEventsResult, error) {
	sessionID := firstNonEmptyString(strings.TrimSpace(args.ID), strings.TrimSpace(args.SessionID))
	if sessionID == "" {
		return nil, fmt.Errorf("id is required")
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
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (c *sessionAgentController) Close(ctx context.Context, sessionID string) (*toolbroker.AgentStatusResult, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("id is required")
	}
	if hub := c.handler.getSessionHub(); hub != nil {
		hub.Stop(sessionID)
	}
	if c.handler.sessionManager != nil {
		_ = c.handler.sessionManager.Close(ctx, sessionID)
	}
	result, err := c.snapshot(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if result != nil {
		result.Status = string(chat.SessionStopped)
	}
	return result, nil
}

func (c *sessionAgentController) Resume(ctx context.Context, sessionID string) (*toolbroker.AgentStatusResult, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("id is required")
	}
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

func isAgentWaitReady(status string) bool {
	switch strings.TrimSpace(status) {
	case string(chat.SessionIdle), string(chat.SessionWaitingApproval), string(chat.SessionWaitingInput), string(chat.SessionStopped), "missing":
		return true
	default:
		return false
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
	})
	if err != nil {
		return nil, err
	}
	return actor, nil
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

package skills

import (
	"context"
	stderrors "errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentdef"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimehooks "github.com/wwsheng009/ai-agent-runtime/internal/hooks"
	"github.com/wwsheng009/ai-agent-runtime/internal/isolation/worktree"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/modelrouting"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimeprofileinput "github.com/wwsheng009/ai-agent-runtime/internal/profileinput"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolprotocol"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type sessionActorClient struct {
	hub        *chat.SessionHub
	store      team.Store
	eventStore chat.EventStore
	eventBus   *runtimeevents.Bus
	handler    *Handler
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
	return c.submitPrompt(ctx, sessionID, prompt, runMeta)
}

func (c *sessionActorClient) submitPrompt(ctx context.Context, sessionID, prompt string, runMeta *team.RunMeta, opts ...chat.SubmitPromptOption) (*team.SessionResult, error) {
	if c == nil || c.hub == nil {
		return nil, fmt.Errorf("session hub not configured")
	}
	actor, err := c.hub.GetOrCreate(strings.TrimSpace(sessionID))
	if err != nil {
		return nil, err
	}
	if c.handler != nil {
		prompt, err = c.handler.injectSupervisionPreflight(ctx, sessionID, prompt, runMeta)
		if err != nil {
			return nil, err
		}
	}
	result, err := actor.SubmitPrompt(ctx, prompt, runMeta, opts...)
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
	_, _ = team.AppendTaskDispatchStarted(ctx, store, request)
	runCtx := team.DetachedTaskExecutionContext(ctx)
	result, err := c.submitPrompt(runCtx, request.SessionID, request.Prompt, request.RunMeta, chat.SubmitPromptOption{
		RouteOverride: chatRouteOverrideFromTaskExecutionRoute(request.Route),
	})
	_, _ = team.AppendTaskDispatchCompleted(runCtx, store, request, result, err)
	return result, err
}

func chatRouteOverrideFromTaskExecutionRoute(route *team.TaskExecutionRoute) *chat.RunRouteOverride {
	if route == nil {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(route.Source), modelrouting.SourceDisabled) {
		return nil
	}
	override := &chat.RunRouteOverride{
		Provider:        strings.TrimSpace(route.Provider),
		Model:           strings.TrimSpace(route.Model),
		ReasoningEffort: strings.TrimSpace(route.ReasoningEffort),
	}
	if override.Provider == "" && override.Model == "" && override.ReasoningEffort == "" {
		return nil
	}
	return override
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

func (c *sessionAgentController) subagentRoutingConfig() *agentconfig.AICLISubagentRoutingConfig {
	if c == nil || c.handler == nil {
		return nil
	}
	return c.handler.subagentRoutingConfig()
}

func teamExpertConcurrencyLimit(routingConfig *agentconfig.AICLISubagentRoutingConfig) int {
	if !modelrouting.RoutingEnabled(routingConfig) || routingConfig.MaxExpertConcurrency <= 0 {
		return 0
	}
	return routingConfig.MaxExpertConcurrency
}

func (c *sessionAgentController) resolveSpawnAgentRoute(parentSession *chat.Session, sessionID string, args toolbroker.SpawnAgentArgs) (toolbroker.SpawnAgentArgs, error) {
	parent := c.spawnAgentParentDefaults(parentSession)
	routingConfig := c.subagentRoutingConfig()
	task := modelrouting.TaskHint{
		ID:                  strings.TrimSpace(sessionID),
		Role:                strings.TrimSpace(args.AgentType),
		Goal:                strings.TrimSpace(args.Message),
		Difficulty:          strings.TrimSpace(args.Difficulty),
		DifficultyRationale: strings.TrimSpace(args.DifficultyRationale),
		Provider:            strings.TrimSpace(args.Provider),
		Model:               strings.TrimSpace(args.Model),
		ReasoningEffort:     firstNonEmptyString(strings.TrimSpace(args.ReasoningEffort), strings.TrimSpace(args.ThinkingEffort)),
		ReadOnly:            false,
		Warnings:            append([]string(nil), args.RouteWarnings...),
	}
	var catalog modelrouting.ProviderCatalog
	if c != nil && c.handler != nil && c.handler.llmRuntime != nil {
		catalog = modelrouting.NewRuntimeCatalog(c.handler.llmRuntime)
	}
	decision, err := (modelrouting.Resolver{
		Config:  routingConfig,
		Catalog: catalog,
	}).Resolve(parent, task)
	if err != nil {
		return args, err
	}
	if !modelrouting.RoutingEnabled(routingConfig) {
		if strings.TrimSpace(task.Model) != "" {
			args.Model = strings.TrimSpace(decision.Model)
		} else {
			args.Model = ""
		}
		args.Provider = ""
		args.ReasoningEffort = ""
		args.ThinkingEffort = ""
		args.Difficulty = strings.TrimSpace(decision.Difficulty)
		args.DifficultySource = strings.TrimSpace(decision.DifficultySource)
		args.DifficultyRationale = strings.TrimSpace(decision.DifficultyRationale)
		args.RouteSource = strings.TrimSpace(decision.Source)
		args.RouteWarnings = append([]string(nil), decision.Warnings...)
		args.FallbackUsed = decision.FallbackUsed
		args.FallbackReason = strings.TrimSpace(decision.FallbackReason)
		return args, nil
	}
	args.Provider = strings.TrimSpace(decision.Provider)
	args.Model = strings.TrimSpace(decision.Model)
	args.ReasoningEffort = strings.TrimSpace(decision.ReasoningEffort)
	args.Difficulty = strings.TrimSpace(decision.Difficulty)
	args.DifficultySource = strings.TrimSpace(decision.DifficultySource)
	args.DifficultyRationale = strings.TrimSpace(decision.DifficultyRationale)
	args.RouteSource = strings.TrimSpace(decision.Source)
	args.RouteWarnings = append([]string(nil), decision.Warnings...)
	args.FallbackUsed = decision.FallbackUsed
	args.FallbackReason = strings.TrimSpace(decision.FallbackReason)
	return args, nil
}

func (c *sessionAgentController) spawnAgentParentDefaults(parentSession *chat.Session) modelrouting.ParentDefaults {
	parent := modelrouting.ParentDefaults{}
	if c != nil && c.handler != nil {
		runtimeConfig := c.handler.resolveRuntimeConfig(UsageScope{})
		if runtimeConfig != nil {
			parent.Provider = strings.TrimSpace(runtimeConfig.Agent.DefaultProvider)
			parent.Model = strings.TrimSpace(runtimeConfig.Agent.DefaultModel)
			parent.Timeout = runtimeConfig.Agent.Timeout
		}
		if c.handler.llmRuntime != nil {
			runtime := c.handler.llmRuntime
			if strings.TrimSpace(parent.Provider) == "" {
				parent.Provider = strings.TrimSpace(runtime.DefaultProvider())
			}
			if strings.TrimSpace(parent.Model) == "" {
				parent.Model = strings.TrimSpace(runtime.DefaultModel())
			}
		}
	}
	if parentSession != nil {
		if provider := agentcontrol.ContextString(parentSession, sessionmeta.ProviderName); provider != "" {
			parent.Provider = provider
		}
		if model := agentcontrol.ContextString(parentSession, toolbroker.AgentSessionContextRequestedModel); model != "" {
			parent.Model = model
		} else if model := agentcontrol.ContextString(parentSession, sessionmeta.Model); model != "" {
			parent.Model = model
		}
		if effort := agentcontrol.ContextString(parentSession, sessionmeta.ReasoningEffort); effort != "" {
			parent.ReasoningEffort = effort
		}
	}
	return parent
}

type apiTeamTaskRouteResolver struct {
	handler *Handler
}

type apiTeamTaskRouteAuditSink struct {
	store  team.Store
	events *team.TeamEventBus
}

func (h *Handler) newTeamTaskRouteResolver() team.TaskRouteResolver {
	if h == nil {
		return nil
	}
	return &apiTeamTaskRouteResolver{handler: h}
}

func newAPITeamTaskRouteAuditSink(store team.Store, events *team.TeamEventBus) team.TaskRouteAuditSink {
	if store == nil {
		return nil
	}
	return apiTeamTaskRouteAuditSink{
		store:  store,
		events: events,
	}
}

func (s apiTeamTaskRouteAuditSink) RecordTaskRouteAudit(ctx context.Context, audit team.TaskRouteAudit) error {
	err := team.NewAgentControlTaskRegistry(s.store).RecordTaskRouteAudit(ctx, audit)
	if err == nil && s.events != nil {
		event := team.TaskRouteResolvedEventFromAudit(audit)
		if strings.TrimSpace(event.TeamID) != "" {
			s.events.Publish(event)
		}
	}
	return err
}

func (r *apiTeamTaskRouteResolver) ResolveTaskRoute(ctx context.Context, request team.TaskRouteRequest) (*team.TaskRouteResolution, error) {
	if r == nil || r.handler == nil {
		return nil, nil
	}
	routingConfig := r.handler.teamRoutingConfig()
	parent := (&sessionAgentController{handler: r.handler}).spawnAgentParentDefaults(r.parentSession(ctx, request))
	var catalog modelrouting.ProviderCatalog
	if r.handler.llmRuntime != nil {
		catalog = modelrouting.NewRuntimeCatalog(r.handler.llmRuntime)
	}
	decision, err := (modelrouting.Resolver{
		Config:  routingConfig,
		Catalog: catalog,
	}).Resolve(parent, teamTaskRouteHint(request))
	strict := modelrouting.StrictCompatibilityMode(routingConfig)
	if err != nil {
		return &team.TaskRouteResolution{
			Route:  fallbackTaskExecutionRoute(parent, request, routingConfig, err),
			Strict: strict,
		}, err
	}
	return &team.TaskRouteResolution{
		Route:    taskExecutionRouteFromDecision(decision, request.Attempt),
		Disabled: !modelrouting.RoutingEnabled(routingConfig),
		Strict:   strict,
	}, nil
}

func (r *apiTeamTaskRouteResolver) parentSession(ctx context.Context, request team.TaskRouteRequest) *chat.Session {
	if r == nil || r.handler == nil || r.handler.sessionManager == nil {
		return nil
	}
	for _, sessionID := range []string{
		strings.TrimSpace(request.Team.LeadSessionID),
		strings.TrimSpace(request.SessionID),
	} {
		if sessionID == "" {
			continue
		}
		session, err := r.handler.sessionManager.Get(ctx, sessionID)
		if err == nil && session != nil {
			return session
		}
	}
	return nil
}

func teamTaskRouteHint(request team.TaskRouteRequest) modelrouting.TaskHint {
	task := request.Task
	return modelrouting.TaskHint{
		ID:                  strings.TrimSpace(task.ID),
		Role:                teamTaskRouteRole(request.Teammate, task),
		Goal:                firstNonEmptyString(strings.TrimSpace(task.Goal), strings.TrimSpace(task.Title)),
		Difficulty:          strings.TrimSpace(task.Difficulty),
		DifficultyRationale: strings.TrimSpace(task.DifficultyRationale),
		ReadOnly:            !teamTaskHasWritePaths(task),
	}
}

func teamTaskRouteRole(mate team.Teammate, task team.Task) string {
	if profile := strings.TrimSpace(mate.Profile); profile != "" {
		return profile
	}
	if teamTaskHasWritePaths(task) {
		return "writer"
	}
	return "researcher"
}

func teamTaskHasWritePaths(task team.Task) bool {
	for _, path := range task.WritePaths {
		if strings.TrimSpace(path) != "" {
			return true
		}
	}
	return false
}

func taskExecutionRouteFromDecision(decision modelrouting.RouteDecision, attempt int) *team.TaskExecutionRoute {
	route := &team.TaskExecutionRoute{
		Difficulty:          strings.TrimSpace(decision.Difficulty),
		DifficultySource:    strings.TrimSpace(decision.DifficultySource),
		DifficultyRationale: strings.TrimSpace(decision.DifficultyRationale),
		Provider:            strings.TrimSpace(decision.Provider),
		Model:               strings.TrimSpace(decision.Model),
		ReasoningEffort:     strings.TrimSpace(decision.ReasoningEffort),
		Source:              strings.TrimSpace(decision.Source),
		Warnings:            append([]string(nil), decision.Warnings...),
		FallbackUsed:        decision.FallbackUsed,
		FallbackReason:      strings.TrimSpace(decision.FallbackReason),
		ResolvedAt:          time.Now().UTC(),
		Attempt:             attempt,
	}
	if route.Attempt <= 0 {
		route.Attempt = 1
	}
	return route
}

func fallbackTaskExecutionRoute(parent modelrouting.ParentDefaults, request team.TaskRouteRequest, routingConfig *agentconfig.AICLISubagentRoutingConfig, routeErr error) *team.TaskExecutionRoute {
	difficulty, source := modelrouting.DefaultDifficulty(routingConfig), "default"
	if normalized, ok := modelrouting.NormalizeDifficulty(request.Task.Difficulty); ok && normalized != "" {
		difficulty = normalized
		source = "explicit"
	}
	attempt := request.Attempt
	if attempt <= 0 {
		attempt = 1
	}
	route := &team.TaskExecutionRoute{
		Difficulty:          difficulty,
		DifficultySource:    source,
		DifficultyRationale: strings.TrimSpace(request.Task.DifficultyRationale),
		Provider:            strings.TrimSpace(parent.Provider),
		Model:               strings.TrimSpace(parent.Model),
		ReasoningEffort:     strings.TrimSpace(parent.ReasoningEffort),
		Source:              modelrouting.SourceFallback,
		Warnings:            []string{"route_resolution_failed_fallback_parent"},
		FallbackUsed:        true,
		FallbackReason:      "route_resolution_error",
		ResolvedAt:          time.Now().UTC(),
		Attempt:             attempt,
	}
	if routeErr != nil {
		route.Error = truncateLine(routeErr.Error(), 240)
	}
	return route
}

func (c *sessionAgentController) Spawn(ctx context.Context, parentSessionID string, args toolbroker.SpawnAgentArgs) (*toolbroker.AgentStatusResult, error) {
	if c == nil || c.handler == nil || c.handler.sessionManager == nil {
		return nil, fmt.Errorf("session manager not configured")
	}
	normalizedArgs, err := toolbroker.NormalizeOrdinarySpawnAgentArgs(args)
	if err != nil {
		return nil, err
	}
	args = normalizedArgs
	sessionID := firstNonEmptyString(strings.TrimSpace(args.ID), strings.TrimSpace(args.SessionID))
	// 与 aicli 侧 localActorRegistry.Spawn 同源的问题：外部传入的 ID 只有在
	// 规范化下保持不变时才能按原字符串读回（见 chat.IsAddressableSessionID）。
	// 否则下面的 Load 永远 not found（读取先取路径最后一段），而 Save 会把原始
	// 字符串落库，结果是列表里可见、打开即 404 的孤儿记录（如 "/root/p26s3b"）。
	// 与 CLI 侧一致选择快速失败，不做静默改写。
	if sessionID != "" && !chat.IsAddressableSessionID(sessionID) {
		return nil, fmt.Errorf("invalid session id %q: session ids must not contain path separators, trailing separators or nil placeholders", sessionID)
	}
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
	args, err = c.resolveSpawnAgentRoute(parentSession, childSession.ID, args)
	if err != nil {
		_ = storage.Delete(ctx, childSession.ID)
		return nil, err
	}
	childSession.SetContext(toolbroker.AgentSessionContextParentSessionID, strings.TrimSpace(parentSessionID))
	childSession.SetContext(toolbroker.AgentSessionContextRootSessionID, apiAgentRootSessionID(parentSession, parentSessionID))
	childSession.SetContext(toolbroker.AgentSessionContextPath, apiAgentChildPath(parentSession, sessionID))
	childSession.SetContext(toolbroker.AgentSessionContextDepth, childDepth)
	if agentType := strings.TrimSpace(args.AgentType); agentType != "" {
		childSession.SetContext(toolbroker.AgentSessionContextAgentType, agentType)
	}
	toolbroker.ApplySpawnAgentRouteContext(childSession, args)
	if err := c.applyAPISpawnIsolation(ctx, childSession, args); err != nil {
		_ = storage.Delete(ctx, childSession.ID)
		return nil, err
	}
	if err := storage.Save(ctx, childSession); err != nil {
		c.cleanupAPISpawnIsolation(ctx, childSession)
		return nil, err
	}
	if err := c.reserveOrRegisterAgentSpawn(ctx, parentSession, parentSessionID, childSession, args, childDepth); err != nil {
		c.cleanupAPISpawnIsolation(ctx, childSession)
		_ = storage.Delete(ctx, childSession.ID)
		return nil, err
	}
	c.subscribeAgentCompletion(parentSessionID, childSession)

	// The durable reservation exists from this point on. Any failure that
	// leaves the child unusable must compensate it, otherwise the row stays
	// active and keeps counting against agents.maxThreads until a manual
	// consistency audit (N1).
	rollbackSpawnFailure := func(cause error) error {
		c.cleanupAPISpawnIsolation(ctx, childSession)
		if hub := c.handler.getSessionHub(); hub != nil {
			hub.Stop(sessionID)
		}
		if deleteErr := storage.Delete(ctx, childSession.ID); deleteErr != nil && !stderrors.Is(deleteErr, chat.ErrSessionNotFound) {
			cause = fmt.Errorf("%w (spawn session cleanup failed: %v)", cause, deleteErr)
		}
		// 释放预约必须是回滚的最后一次写入：投影刷新（materialize）按会话存储重写
		// registry，若先释放、后删容器，并发投影会按“仍在存储里的 active 子会话”
		// 把刚释放的行重新写回 active，预约就不再被回收（flaky：N1 回滚用例）。
		if releaseErr := c.releaseAgentSpawnReservation(ctx, childSession, cause.Error()); releaseErr != nil {
			cause = fmt.Errorf("%w (spawn reservation release failed: %v)", cause, releaseErr)
		}
		return cause
	}

	actor, err := c.handler.getSessionHub().GetOrCreate(sessionID)
	if err != nil {
		return nil, rollbackSpawnFailure(fmt.Errorf("create child session actor: %w", err))
	}
	queued := false
	if message := strings.TrimSpace(args.Message); message != "" {
		if err := actor.SubmitPromptAsync(ctx, message, toolbroker.SpawnAgentRunMetaFromContext(childSession)); err != nil {
			return nil, rollbackSpawnFailure(fmt.Errorf("queue child prompt: %w", err))
		}
		queued = true
	}
	result, err := c.snapshot(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	result.Created = true
	result.Queued = queued
	c.dispatchAgentHook(runtimehooks.EventSubagentStart, apiSpawnAgentHookPayload(parentSessionID, childSession, map[string]interface{}{
		"queued": queued,
	}))
	return result, nil
}

// applyAPISpawnIsolation creates a git worktree when isolation=worktree and binds
// workspace_path for the child actor. Failures do not fall back to the main tree.
func (c *sessionAgentController) applyAPISpawnIsolation(ctx context.Context, childSession *chat.Session, args toolbroker.SpawnAgentArgs) error {
	if c == nil || childSession == nil {
		return nil
	}
	mode, err := worktree.NormalizeMode(args.Isolation)
	if err != nil {
		return err
	}
	childSession.SetContext(toolbroker.AgentSessionContextIsolation, mode)
	if mode != worktree.ModeWorktree {
		return nil
	}
	repoRoot := c.resolveAPISpawnRepoRoot(childSession)
	handle, err := worktree.Create(ctx, worktree.Options{
		RepoRoot:  repoRoot,
		SessionID: childSession.ID,
	})
	if err != nil {
		return err
	}
	childSession.SetContext(toolbroker.AgentSessionContextWorktreePath, handle.Path)
	childSession.SetContext(toolbroker.AgentSessionContextWorktreeBranch, handle.Branch)
	childSession.SetContext(toolbroker.AgentSessionContextWorktreeRepoRoot, handle.RepoRoot)
	childSession.SetContext(sessionmeta.WorkspacePath, handle.Path)
	// Default claim/write scope: isolation root so path claims / team write_paths
	// can inherit the worktree without falling back to the main tree.
	childSession.SetContext(toolbroker.AgentSessionContextWritePaths, []string{handle.Path})
	return nil
}

func (c *sessionAgentController) resolveAPISpawnRepoRoot(childSession *chat.Session) string {
	if childSession != nil {
		if path := agentcontrol.ContextString(childSession, sessionmeta.WorkspacePath); path != "" {
			return path
		}
		if path := agentcontrol.ContextString(childSession, toolbroker.AgentSessionContextWorktreeRepoRoot); path != "" {
			return path
		}
	}
	if c != nil && c.handler != nil {
		if runtimeConfig := c.handler.resolveRuntimeConfig(UsageScope{}); runtimeConfig != nil {
			if root := strings.TrimSpace(runtimeConfig.Workspace.Root); root != "" {
				return root
			}
		}
	}
	return ""
}

// cleanupAPISpawnIsolation removes a child worktree when isolation=worktree.
// Safe when isolation is none, already applied/discarded, or context is incomplete.
func (c *sessionAgentController) cleanupAPISpawnIsolation(ctx context.Context, childSession *chat.Session) error {
	if childSession == nil {
		return nil
	}
	session := childSession
	if c != nil && c.handler != nil && c.handler.sessionManager != nil {
		if loaded, err := c.handler.sessionManager.Get(ctx, childSession.ID); err == nil && loaded != nil {
			session = loaded
		}
	}
	mode := agentcontrol.ContextString(session, toolbroker.AgentSessionContextIsolation)
	path := agentcontrol.ContextString(session, toolbroker.AgentSessionContextWorktreePath)
	if mode != worktree.ModeWorktree || path == "" {
		return nil
	}
	handle := &worktree.Handle{
		Path:      path,
		Branch:    agentcontrol.ContextString(session, toolbroker.AgentSessionContextWorktreeBranch),
		RepoRoot:  agentcontrol.ContextString(session, toolbroker.AgentSessionContextWorktreeRepoRoot),
		SessionID: strings.TrimSpace(session.ID),
	}
	if err := handle.Remove(ctx); err != nil {
		return err
	}
	clearAPISpawnWorktreeContext(session, "")
	if c != nil && c.handler != nil && c.handler.sessionManager != nil {
		_ = c.handler.sessionManager.Update(ctx, session)
	}
	return nil
}

func clearAPISpawnWorktreeContext(session *chat.Session, disposition string) {
	if session == nil {
		return
	}
	session.SetContext(toolbroker.AgentSessionContextWorktreePath, "")
	session.SetContext(toolbroker.AgentSessionContextWorktreeBranch, "")
	session.SetContext(toolbroker.AgentSessionContextWritePaths, []string{})
	if repoRoot := agentcontrol.ContextString(session, toolbroker.AgentSessionContextWorktreeRepoRoot); repoRoot != "" {
		session.SetContext(sessionmeta.WorkspacePath, repoRoot)
	}
	if disposition = strings.TrimSpace(disposition); disposition != "" {
		session.SetContext(toolbroker.AgentSessionContextWorktreeDisposition, disposition)
	}
}

func apiSpawnWorktreeHandle(session *chat.Session) (*worktree.Handle, error) {
	if session == nil {
		return nil, fmt.Errorf("session is required")
	}
	mode := agentcontrol.ContextString(session, toolbroker.AgentSessionContextIsolation)
	path := agentcontrol.ContextString(session, toolbroker.AgentSessionContextWorktreePath)
	branch := agentcontrol.ContextString(session, toolbroker.AgentSessionContextWorktreeBranch)
	repoRoot := agentcontrol.ContextString(session, toolbroker.AgentSessionContextWorktreeRepoRoot)
	disposition := agentcontrol.ContextString(session, toolbroker.AgentSessionContextWorktreeDisposition)
	if mode != worktree.ModeWorktree {
		return nil, fmt.Errorf("session %s isolation is %q (want worktree)", strings.TrimSpace(session.ID), mode)
	}
	if path == "" {
		if disposition != "" {
			return nil, fmt.Errorf("session %s worktree already %s", strings.TrimSpace(session.ID), disposition)
		}
		return nil, fmt.Errorf("session %s has no worktree path", strings.TrimSpace(session.ID))
	}
	if branch == "" || repoRoot == "" {
		return nil, fmt.Errorf("session %s worktree context incomplete (branch/repo_root required)", strings.TrimSpace(session.ID))
	}
	return &worktree.Handle{
		Path:      path,
		Branch:    branch,
		RepoRoot:  repoRoot,
		SessionID: strings.TrimSpace(session.ID),
	}, nil
}

func (c *sessionAgentController) ApplyWorktree(ctx context.Context, args toolbroker.ApplyAgentWorktreeArgs) (*toolbroker.AgentWorktreeResult, error) {
	sessionRef := strings.TrimSpace(firstNonEmptyAPIString(args.ID, args.SessionID))
	if sessionRef == "" {
		return nil, fmt.Errorf("id is required")
	}
	sessionID, err := c.resolveTargetSessionID(ctx, sessionRef)
	if err != nil {
		return nil, err
	}
	if c == nil || c.handler == nil || c.handler.sessionManager == nil {
		return nil, fmt.Errorf("session manager is not configured")
	}
	session, err := c.handler.sessionManager.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	handle, err := apiSpawnWorktreeHandle(session)
	if err != nil {
		return nil, err
	}
	diffStat, _ := handle.DiffStat(ctx)
	if err := handle.Apply(ctx, worktree.ApplyOptions{Paths: append([]string(nil), args.Paths...)}); err != nil {
		return nil, err
	}
	result := &toolbroker.AgentWorktreeResult{
		ID:             sessionID,
		SessionID:      sessionID,
		Action:         "apply",
		Isolation:      worktree.ModeWorktree,
		WorktreePath:   handle.Path,
		WorktreeBranch: handle.Branch,
		RepoRoot:       handle.RepoRoot,
		DiffStat:       diffStat,
		Paths:          append([]string(nil), args.Paths...),
		Applied:        true,
		Kept:           args.Keep,
	}
	if !args.Keep {
		if err := handle.Remove(ctx); err != nil {
			return nil, fmt.Errorf("apply succeeded but worktree remove failed: %w", err)
		}
		clearAPISpawnWorktreeContext(session, toolbroker.WorktreeDispositionApplied)
		result.Removed = true
		result.WorktreePath = ""
	} else {
		session.SetContext(toolbroker.AgentSessionContextWorktreeDisposition, toolbroker.WorktreeDispositionApplied)
	}
	if err := c.handler.sessionManager.Update(ctx, session); err != nil {
		return nil, err
	}
	if status, statusErr := c.snapshot(ctx, sessionID); statusErr == nil {
		result.Status = status
	}
	return result, nil
}

func (c *sessionAgentController) DiscardWorktree(ctx context.Context, args toolbroker.DiscardAgentWorktreeArgs) (*toolbroker.AgentWorktreeResult, error) {
	sessionRef := strings.TrimSpace(firstNonEmptyAPIString(args.ID, args.SessionID))
	if sessionRef == "" {
		return nil, fmt.Errorf("id is required")
	}
	sessionID, err := c.resolveTargetSessionID(ctx, sessionRef)
	if err != nil {
		return nil, err
	}
	if c == nil || c.handler == nil || c.handler.sessionManager == nil {
		return nil, fmt.Errorf("session manager is not configured")
	}
	session, err := c.handler.sessionManager.Get(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	handle, err := apiSpawnWorktreeHandle(session)
	if err != nil {
		return nil, err
	}
	diffStat, _ := handle.DiffStat(ctx)
	if err := handle.Remove(ctx); err != nil {
		return nil, err
	}
	clearAPISpawnWorktreeContext(session, toolbroker.WorktreeDispositionDiscarded)
	if err := c.handler.sessionManager.Update(ctx, session); err != nil {
		return nil, err
	}
	result := &toolbroker.AgentWorktreeResult{
		ID:             sessionID,
		SessionID:      sessionID,
		Action:         "discard",
		Isolation:      worktree.ModeWorktree,
		WorktreeBranch: handle.Branch,
		RepoRoot:       handle.RepoRoot,
		DiffStat:       diffStat,
		Discarded:      true,
		Removed:        true,
	}
	if status, statusErr := c.snapshot(ctx, sessionID); statusErr == nil {
		result.Status = status
	}
	return result, nil
}

func firstNonEmptyAPIString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func annotateAPISpawnWorktreeCompletion(ctx context.Context, c *sessionAgentController, childSession *chat.Session, payload map[string]interface{}) {
	if payload == nil || childSession == nil {
		return
	}
	session := childSession
	if c != nil && c.handler != nil && c.handler.sessionManager != nil {
		if loaded, err := c.handler.sessionManager.Get(ctx, childSession.ID); err == nil && loaded != nil {
			session = loaded
		}
	}
	mode := agentcontrol.ContextString(session, toolbroker.AgentSessionContextIsolation)
	if mode != worktree.ModeWorktree {
		return
	}
	path := agentcontrol.ContextString(session, toolbroker.AgentSessionContextWorktreePath)
	branch := agentcontrol.ContextString(session, toolbroker.AgentSessionContextWorktreeBranch)
	repoRoot := agentcontrol.ContextString(session, toolbroker.AgentSessionContextWorktreeRepoRoot)
	disposition := agentcontrol.ContextString(session, toolbroker.AgentSessionContextWorktreeDisposition)
	if path != "" {
		payload["worktree_path"] = path
		payload["worktree_pending"] = disposition == ""
		payload["isolation"] = mode
		if branch != "" {
			payload["worktree_branch"] = branch
		}
		if repoRoot != "" {
			payload["worktree_repo_root"] = repoRoot
		}
		handle := &worktree.Handle{Path: path, Branch: branch, RepoRoot: repoRoot, SessionID: strings.TrimSpace(session.ID)}
		if diff, err := handle.DiffStat(ctx); err == nil && strings.TrimSpace(diff) != "" {
			payload["worktree_diff_stat"] = diff
		}
		payload["next_action"] = "Call apply_agent_worktree to land changes in the main repo, or discard_agent_worktree to drop them. close_agent also cleans remaining worktrees."
		return
	}
	if disposition != "" {
		payload["worktree_disposition"] = disposition
		payload["worktree_pending"] = false
		payload["isolation"] = mode
	}
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
	// P1-5 方案 2: mirror throttled child tool progress onto the parent stream as
	// the live-only subagent.progress event. The mirror is per child (dropped
	// together with the subscription) and never writes to the event store, so
	// the parent transcript/replay cannot be polluted by child progress.
	progressTarget := supervision.SubagentProgressTarget{
		ParentSessionID: parentSessionID,
		ChildSessionID:  childSessionID,
		Path:            childPath,
		Depth:           childDepth,
		AgentType:       childType,
	}
	progressMirror := supervision.NewSubagentProgressMirror(supervision.DefaultSubagentProgressWindow)
	handler := func(event runtimeevents.Event) {
		if !strings.EqualFold(strings.TrimSpace(event.SessionID), childSessionID) {
			return
		}
		eventType := strings.TrimSpace(event.Type)
		// P0-2: a child blocked on an approval/question must reach the parent
		// even when the parent already ended its turn. Both edges are projected
		// into the durable inbox and the request edge doubles as a controlled
		// wake request.
		switch eventType {
		case chat.EventApprovalRequested, chat.EventQuestionAsked:
			c.projectAgentApproval(context.Background(), parentSessionID, childSessionID, childPath, eventType, event.Payload)
			return
		case chat.EventApprovalResolved, chat.EventQuestionAnswered:
			c.resolveAgentApproval(context.Background(), parentSessionID, childSessionID, childPath, eventType, event.Payload)
			return
		case toolprotocol.EventTypeProgress:
			if mirrored, ok := progressMirror.Observe(progressTarget, event, time.Now().UTC()); ok {
				bus.Publish(mirrored)
			}
			return
		}
		if eventType != chat.EventSessionEnd && eventType != chat.EventSessionInterrupted {
			return
		}
		progressMirror.Forget(childSessionID)
		if unsubscribe != nil {
			unsubscribe()
		}
		payload := map[string]interface{}{
			"agent_id":              childSessionID,
			"session_id":            childSessionID,
			"parent_session_id":     parentSessionID,
			"path":                  childPath,
			"source_event_type":     eventType,
			"source_event_trace_id": strings.TrimSpace(event.TraceID),
			"status":                agentCompletionStatus(event),
		}
		if !event.Timestamp.IsZero() {
			payload["source_event_timestamp"] = event.Timestamp.UTC().Format(time.RFC3339Nano)
		}
		if childDepth > 0 {
			payload["depth"] = childDepth
		}
		if childType != "" {
			payload["agent_type"] = childType
			payload["role"] = childType
		}
		toolbroker.AddSpawnAgentRoutePayload(payload, childSession)
		copyAgentCompletionPayload(payload, event.Payload)
		if registry := c.handler.getAgentControlAgentStore(); registry != nil && childPath != "" {
			rootSessionID := apiAgentRootSessionID(childSession, parentSessionID)
			if _, closeErr := registry.CloseAgentControlAgentSubtree(context.Background(), rootSessionID, childPath, time.Now().UTC()); closeErr != nil {
				payload["lifecycle_close_error"] = closeErr.Error()
			}
		}
		c.projectAgentCompletion(context.Background(), parentSessionID, childSessionID, agentCompletionStatus(event), eventType)
		c.completeSupervisedRun(context.Background(), parentSessionID, childSessionID, agentCompletionStatus(event), eventType)
		// Keep worktree after completion so parent can apply/discard explicitly.
		// close_agent still cleans remaining worktrees.
		annotateAPISpawnWorktreeCompletion(context.Background(), c, childSession, payload)
		completionMessage, mailboxErr := c.deliverSubagentCompletionMailbox(context.Background(), parentSessionID, childSessionID, childPath, childType, eventType, payload)
		payload = toolbroker.AnnotateSubagentCompletionDisplayMirror(payload, completionMessage, mailboxErr)
		c.dispatchAgentHook(runtimehooks.EventSubagentStop, cloneProfileContextValues(payload))
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

// completeSupervisedRun marks the child's supervised execution run terminal
// (P3 doc 7.3 mapping): session_end -> succeeded, interrupted/stopped ->
// canceled, explicit failure -> failed. Best-effort: an absent run (unsupervised
// spawn, already terminal, store outage) is a no-op and never blocks the
// established completion mailbox path.
func (c *sessionAgentController) completeSupervisedRun(ctx context.Context, parentSessionID, childSessionID, completionStatus, sourceEventType string) {
	if c == nil || c.handler == nil {
		return
	}
	supervisor := c.handler.getExecutionSupervisor()
	if supervisor == nil || supervisor.Store == nil {
		return
	}
	runs, err := supervisor.Store.ListExecutionRunsBySession(ctx, strings.TrimSpace(childSessionID), 3)
	if err != nil {
		return
	}
	for _, run := range runs {
		if run.Terminal() {
			continue
		}
		status := supervision.RunStatusSucceeded
		switch strings.TrimSpace(completionStatus) {
		case string(chat.SessionStopped), "interrupted":
			status = supervision.RunStatusCanceled
		case "failed", "error":
			status = supervision.RunStatusFailed
		}
		_ = supervisor.CompleteRun(ctx, run.RunID, status, "", "", map[string]interface{}{
			"source_event_type": sourceEventType,
			"status":            status,
		})
		return
	}
}

// projectAgentCompletion bridges normal AgentControl lifecycle events into the
// durable P2 inbox. It is intentionally best-effort: a supervision-store
// outage must not prevent the established completion mailbox from reaching the
// parent.
func (c *sessionAgentController) projectAgentCompletion(ctx context.Context, parentSessionID, childSessionID, status, sourceEventType string) {
	if c == nil || c.handler == nil {
		return
	}
	store := c.handler.getSupervisionStore()
	if store == nil {
		return
	}
	rootScopeID := strings.TrimSpace(parentSessionID)
	if session, err := c.handler.sessionManager.Get(ctx, childSessionID); err == nil && session != nil {
		rootScopeID = apiAgentRootSessionID(session, parentSessionID)
	}
	if rootScopeID == "" {
		return
	}
	_, _ = supervision.ProjectAgentCompletion(
		ctx,
		store,
		c.handler.getSupervisionWakeScheduler(),
		rootScopeID,
		parentSessionID,
		childSessionID,
		status,
		sourceEventType,
	)
	// P2 closure: a critical completion must be able to start a parent turn
	// without an explicit wait. When the parent is still busy the wake stays
	// durable and the next runnable transition drains it (doc 6.5).
	_ = c.wakeSupervisedParent(ctx, rootScopeID, parentSessionID)
}

// projectAgentApproval bridges a child approval/question edge into the durable
// P0-2 parent inbox. Best-effort: a supervision outage must never block the
// child's own runtime path, and the parent's wait path is unchanged.
func (c *sessionAgentController) projectAgentApproval(ctx context.Context, parentSessionID, childSessionID, childPath, eventType string, payload map[string]interface{}) {
	if c == nil || c.handler == nil {
		return
	}
	store := c.handler.getSupervisionStore()
	if store == nil {
		return
	}
	rootScopeID := strings.TrimSpace(parentSessionID)
	if session, err := c.handler.sessionManager.Get(ctx, childSessionID); err == nil && session != nil {
		rootScopeID = apiAgentRootSessionID(session, parentSessionID)
	}
	if rootScopeID == "" {
		return
	}
	notice, ok := supervision.ApprovalNoticeFromEvent(
		rootScopeID,
		parentSessionID,
		"",
		childSessionID,
		childPath,
		eventType,
		payload,
		time.Now().UTC(),
	)
	if !ok {
		return
	}
	_, _ = supervision.ProjectApprovalRequest(ctx, store, c.handler.getSupervisionWakeScheduler(), notice)
	// The parent may already be idle: start one bounded turn now. Busy parents
	// keep the durable wake for the next runnable transition, and every parent
	// turn additionally sees the pending approval in its preflight digest.
	_ = c.wakeSupervisedParent(ctx, rootScopeID, parentSessionID)
}

// resolveAgentApproval closes the inbox entry once the child received its
// decision, so the parent's preflight stops showing a stale pending item.
func (c *sessionAgentController) resolveAgentApproval(ctx context.Context, parentSessionID, childSessionID, childPath, eventType string, payload map[string]interface{}) {
	if c == nil || c.handler == nil {
		return
	}
	store := c.handler.getSupervisionStore()
	if store == nil {
		return
	}
	rootScopeID := strings.TrimSpace(parentSessionID)
	if session, err := c.handler.sessionManager.Get(ctx, childSessionID); err == nil && session != nil {
		rootScopeID = apiAgentRootSessionID(session, parentSessionID)
	}
	if rootScopeID == "" {
		return
	}
	notice, ok := supervision.ApprovalNoticeFromEvent(
		rootScopeID,
		parentSessionID,
		"",
		childSessionID,
		childPath,
		supervision.ApprovalRequestEventType(eventType),
		payload,
		time.Now().UTC(),
	)
	if !ok {
		return
	}
	_, _ = supervision.ResolveApprovalRequest(ctx, store, notice, supervision.ApprovalResolutionFromPayload(payload))
}

// wakeSupervisedParent drains pending critical-lifecycle wakes for the
// parent session and starts one parent turn when the parent is runnable.
// Busy parents keep the wake durable for the next runnable transition; the
// natural-turn preflight additionally injects the digest as a fallback.
func (c *sessionAgentController) wakeSupervisedParent(ctx context.Context, rootScopeID, parentSessionID string) error {
	if c == nil || c.handler == nil {
		return nil
	}
	scheduler := c.handler.getSupervisionWakeScheduler()
	if scheduler == nil {
		return nil
	}
	return c.supervisionWakeConsumer(scheduler).MaybeWakeParent(ctx, parentSessionID, "", rootScopeID)
}

// supervisionWakeConsumer builds the consumer shared by the auto-wake and the
// turn-end self-check paths: both must use the same runnable gate and the same
// delivery path, otherwise a self-check turn could start while the parent is
// busy or bypass the digest injection.
func (c *sessionAgentController) supervisionWakeConsumer(scheduler *supervision.WakeScheduler) *supervision.WakeConsumer {
	return &supervision.WakeConsumer{
		Wakes: scheduler,
		Runnable: func(ctx context.Context, rootScopeID, parentSessionID, parentTeamID string) bool {
			actor := c.apiAgentActor(ctx, parentSessionID)
			if actor == nil {
				return false
			}
			state, ok := actor.StateSummary()
			return ok && !state.Busy()
		},
		Deliver: func(ctx context.Context, parentSessionID string, digest *supervision.Digest, wakeIDs []string) error {
			actor := c.apiAgentActor(ctx, parentSessionID)
			if actor == nil {
				return fmt.Errorf("parent actor not found")
			}
			// SubmitPromptAsync enqueues the wake turn on the actor loop; it
			// does not block on the full parent turn.
			return actor.SubmitPromptAsync(ctx, supervision.AutoWakePrompt, c.apiAgentRunMeta(ctx, parentSessionID))
		},
	}
}

// selfCheckSupervisedParent is the turn-end self-check (plan P1-6 方案 4). It
// is called only after the auto-wake path reported ErrWakeRateLimited: the
// class budget deferred the wake, so without the self-check the parent goes
// idle with an undelivered digest until the next natural turn. The scheduler
// owns the per-window allowance, so a self-check turn that ends again cannot
// recurse, and the default (0) disables the whole path.
func (c *sessionAgentController) selfCheckSupervisedParent(ctx context.Context, rootScopeID, parentSessionID string) error {
	if c == nil || c.handler == nil {
		return nil
	}
	scheduler := c.handler.getSupervisionWakeScheduler()
	if scheduler == nil {
		return nil
	}
	_, err := c.supervisionWakeConsumer(scheduler).MaybeSelfCheckParent(ctx, parentSessionID, "", rootScopeID)
	return err
}

func (c *sessionAgentController) dispatchAgentHook(event runtimehooks.Event, payload map[string]interface{}) {
	if c == nil || c.handler == nil || len(payload) == 0 {
		return
	}
	runtimeConfig := c.handler.resolveRuntimeConfig(UsageScope{})
	if runtimeConfig == nil || len(runtimeConfig.Hooks) == 0 {
		return
	}
	runtimehooks.NewManager(runtimeConfig.Hooks).DispatchAsync(context.Background(), event, payload)
}

func apiSpawnAgentHookPayload(parentSessionID string, childSession *chat.Session, extra map[string]interface{}) map[string]interface{} {
	if childSession == nil {
		return nil
	}
	childSessionID := strings.TrimSpace(childSession.ID)
	if childSessionID == "" {
		return nil
	}
	payload := map[string]interface{}{
		"agent_id":          childSessionID,
		"session_id":        childSessionID,
		"parent_session_id": strings.TrimSpace(parentSessionID),
		"path":              apiAgentSessionPath(childSession),
	}
	if depth := apiAgentSessionDepth(childSession); depth > 0 {
		payload["depth"] = depth
	}
	if value, ok := childSession.GetContext(toolbroker.AgentSessionContextAgentType); ok {
		if agentType, ok := value.(string); ok && strings.TrimSpace(agentType) != "" {
			payload["agent_type"] = strings.TrimSpace(agentType)
			payload["role"] = strings.TrimSpace(agentType)
		}
	}
	toolbroker.AddSpawnAgentRoutePayload(payload, childSession)
	for key, value := range extra {
		if strings.TrimSpace(key) != "" {
			payload[key] = value
		}
	}
	return payload
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
	// API/CLI parity: a session id can be re-bound inside the same root after a
	// restart. Close stale rows for the same session id first, otherwise the
	// reserve transaction would count two active rows for one container.
	if err := agentcontrol.CloseStaleAgentSessionBindings(ctx, store, rootRecord); err != nil {
		return err
	}
	if err := agentcontrol.CloseStaleAgentSessionBindings(ctx, store, childRecord); err != nil {
		return err
	}
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

// releaseAgentSpawnReservation compensates a durable spawn reservation whose
// child never became runnable. Stores implementing
// agentcontrol.AgentSpawnReservationReleaser move the row to stale in one
// step; older stores degrade to the stale-marker/close path.
func (c *sessionAgentController) releaseAgentSpawnReservation(ctx context.Context, childSession *chat.Session, reason string) error {
	if c == nil || c.handler == nil || childSession == nil {
		return nil
	}
	store := c.handler.getAgentControlAgentStore()
	if store == nil {
		return nil
	}
	agentID := strings.TrimSpace(childSession.ID)
	if agentID == "" {
		return nil
	}
	if releaser, ok := store.(agentcontrol.AgentSpawnReservationReleaser); ok && releaser != nil {
		_, err := releaser.ReleaseAgentControlAgentSpawn(ctx, agentID, reason)
		return err
	}
	records, err := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
		AgentID:       agentID,
		IncludeClosed: true,
		Limit:         1,
	})
	if err != nil || len(records) == 0 {
		return err
	}
	record := records[0].Normalize()
	if record.Closed() || record.RootSessionID == "" || record.AgentPath == "" {
		return nil
	}
	staleAt := time.Now().UTC()
	if marker, ok := store.(agentcontrol.AgentStaleMarker); ok && marker != nil {
		_, err := marker.MarkAgentControlAgentSubtreeStale(ctx, record.RootSessionID, record.AgentPath, staleAt)
		return err
	}
	_, err = store.CloseAgentControlAgentSubtree(ctx, record.RootSessionID, record.AgentPath, staleAt)
	return err
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
	record := agentcontrol.AgentRecord{
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
	toolbroker.ApplySpawnAgentRouteRecord(&record, args)
	return record
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
	toolbroker.ApplySpawnAgentRouteStatusRecord(result, record)
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
		if pathPrefix != "" && !agentcontrol.AgentPathMatchesPrefix(path, pathPrefix) {
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
		if actor := c.apiAgentActor(ctx, sessionID); actor != nil {
			state, ok := actor.StateSummary()
			if !ok || !state.Busy() {
				if err := actor.SubmitPromptAsync(ctx, message, c.apiAgentRunMeta(ctx, sessionID)); err != nil {
					return nil, err
				}
				triggered = true
			}
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
		if actor, exists := hub.Get(strings.TrimSpace(sessionID)); exists && actor != nil {
			if state, ok := actor.StateSummary(); ok && state.Busy() {
				return true
			}
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

func (c *sessionAgentController) apiAgentRunMeta(ctx context.Context, sessionID string) *team.RunMeta {
	if c == nil || c.handler == nil || c.handler.sessionManager == nil {
		return nil
	}
	session, err := c.handler.sessionManager.Get(ctx, strings.TrimSpace(sessionID))
	if err != nil || session == nil {
		return nil
	}
	// Child follow-up/resume must rebuild RunMeta from the child session only.
	// SpawnAgentRunMetaFromContext forces completion_requirement=none for
	// ordinary spawn_agent children so a legacy complete_task context value
	// cannot re-enter the run.
	return toolbroker.SpawnAgentRunMetaFromContext(session)
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
	if state, ok := actor.StateSummary(); ok {
		if state.Busy() {
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
	if err := actor.SubmitPromptAsync(ctx, message, c.apiAgentRunMeta(ctx, sessionID)); err != nil {
		return nil, err
	}
	result, err := c.snapshot(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	result.Queued = true
	return result, nil
}

func (c *sessionAgentController) ResolveApproval(ctx context.Context, args toolbroker.ResolveAgentApprovalArgs) (*toolbroker.AgentApprovalResult, error) {
	if c == nil || c.handler == nil {
		return nil, fmt.Errorf("handler not configured")
	}
	sessionID := firstNonEmptyString(strings.TrimSpace(args.ID), strings.TrimSpace(args.SessionID))
	if sessionID == "" {
		return nil, fmt.Errorf("id is required")
	}
	requestID := strings.TrimSpace(args.RequestID)
	if requestID == "" {
		return nil, fmt.Errorf("request_id is required")
	}
	resolvedSessionID, err := c.resolveTargetSessionID(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	sessionID = resolvedSessionID
	actor, err := c.handler.getSessionHub().GetOrCreate(sessionID)
	if err != nil {
		return nil, err
	}
	if err := actor.ApproveToolWithArgs(ctx, requestID, args.Allow, args.PatchedArgs); err != nil {
		return nil, err
	}
	status, err := c.snapshot(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return &toolbroker.AgentApprovalResult{
		SessionID: sessionID,
		RequestID: requestID,
		Allowed:   args.Allow,
		Resolved:  true,
		Status:    status,
	}, nil
}

func (c *sessionAgentController) Wait(ctx context.Context, args toolbroker.WaitAgentArgs) (*toolbroker.AgentWaitResult, error) {
	if args.MailboxOnly {
		return c.waitForMailboxEvent(ctx, args)
	}
	startedAt := time.Now()
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
	resolution, err := agentcontrol.ResolveWaitTimeout(args.TimeoutMs, c.waitTimeoutPolicy())
	if err != nil {
		return nil, err
	}
	timeout := time.Duration(resolution.EffectiveMs) * time.Millisecond
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	wakeCh, unsubscribe := c.subscribeWaitEvents(waitCtx, sessionIDs)
	defer unsubscribe()
	for {
		snapshots := make([]toolbroker.AgentStatusResult, 0, len(sessionIDs))
		readyIDs := make([]string, 0, len(sessionIDs))
		pendingIDs := make([]string, 0, len(sessionIDs))
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
				readyIDs = append(readyIDs, firstNonEmptyString(result.ID, result.SessionID, sessionID))
				if matched == nil {
					cloned := *result
					matched = &cloned
				}
			} else {
				pendingIDs = append(pendingIDs, firstNonEmptyString(result.ID, result.SessionID, sessionID))
			}
		}
		waitResult := &toolbroker.AgentWaitResult{
			Agents:                 snapshots,
			ReadyCount:             readyCount,
			PendingCount:           len(snapshots) - readyCount,
			ReadyIDs:               readyIDs,
			PendingIDs:             pendingIDs,
			WaitTimeoutMs:          int(timeout.Milliseconds()),
			WaitTimeoutRequestedMs: resolution.RequestedMs,
			WaitTimeoutClamped:     resolution.Clamped,
		}
		if matched != nil {
			waitResult.Agent = matched
			waitResult.MatchedID = matched.ID
			waitResult.MatchedSessionID = matched.SessionID
			return toolbroker.FinalizeAgentWaitResult(waitResult, startedAt), nil
		}
		select {
		case <-waitCtx.Done():
			waitResult.TimedOut = true
			return toolbroker.FinalizeAgentWaitResult(waitResult, startedAt), nil
		case <-wakeCh:
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// waitForMailboxEvent bounds the mailbox observation window, then delegates to
// the resolved implementation so the effective window is echoed back.
func (c *sessionAgentController) waitForMailboxEvent(ctx context.Context, args toolbroker.WaitAgentArgs) (*toolbroker.AgentWaitResult, error) {
	resolution, err := agentcontrol.ResolveWaitTimeout(args.TimeoutMs, c.waitTimeoutPolicy())
	if err != nil {
		return nil, err
	}
	args.TimeoutMs = resolution.EffectiveMs
	result, err := c.waitForMailboxEventResolved(ctx, args)
	if err != nil {
		return nil, err
	}
	return toolbroker.ApplyAgentWaitTimeout(result, resolution.RequestedMs, resolution.EffectiveMs, resolution.Clamped), nil
}

func (c *sessionAgentController) waitForMailboxEventResolved(ctx context.Context, args toolbroker.WaitAgentArgs) (*toolbroker.AgentWaitResult, error) {
	if c == nil || c.handler == nil {
		return nil, fmt.Errorf("handler not configured")
	}
	sessionID := firstNonEmptyString(strings.TrimSpace(args.ID), strings.TrimSpace(args.SessionID))
	if sessionID == "" {
		return nil, fmt.Errorf("session id is required")
	}
	startedAt := time.Now()
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
				return toolbroker.FinalizeAgentWaitResult(result, startedAt), nil
			}
			if hasMailboxRows {
				select {
				case <-waitCtx.Done():
					return toolbroker.FinalizeAgentWaitResult(&toolbroker.AgentWaitResult{TimedOut: true}, startedAt), nil
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
			return toolbroker.FinalizeAgentWaitResult(result, startedAt), nil
		}
		select {
		case <-waitCtx.Done():
			return toolbroker.FinalizeAgentWaitResult(&toolbroker.AgentWaitResult{TimedOut: true}, startedAt), nil
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

// ReadEvents bounds wait_ms before delegating to the resolved implementation.
// wait_ms=0 keeps the non-blocking read semantics (it is not a default request).
func (c *sessionAgentController) ReadEvents(ctx context.Context, args toolbroker.ReadAgentEventsArgs) (*toolbroker.AgentEventsResult, error) {
	if args.WaitMs > 0 {
		resolution, err := agentcontrol.ResolveWaitTimeout(args.WaitMs, c.waitTimeoutPolicy())
		if err != nil {
			return nil, err
		}
		args.WaitMs = resolution.EffectiveMs
	}
	return c.readEventsResolved(ctx, args)
}

func (c *sessionAgentController) readEventsResolved(ctx context.Context, args toolbroker.ReadAgentEventsArgs) (*toolbroker.AgentEventsResult, error) {
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
			if events, ok, hasMailboxRows, err := listAPIMailboxEvents(readCtx, store, sessionID, args.AfterSeq, limit+1); err != nil {
				return nil, err
			} else if ok {
				// The extra row only signals that more events are queued; mailbox
				// unread counts stay unset because presentation seq and
				// session-mailbox seq are distinct sequences.
				hasMore := len(events) > limit
				if hasMore {
					events = events[:limit]
				}
				if len(events) > 0 || (hasMailboxRows && waitMs == 0) {
					return toolbroker.ApplyAgentEventsPagination(buildAgentEventsResult(sessionID, events), hasMore, 0), nil
				}
				if hasMailboxRows {
					select {
					case <-readCtx.Done():
						result := buildAgentEventsResult(sessionID, nil)
						result.TimedOut = true
						result.NextAction = ""
						return toolbroker.FinalizeAgentEventsResult(result), nil
					case <-wakeCh:
					case <-time.After(500 * time.Millisecond):
					}
					continue
				}
			}
		}
		events, err := store.ListEvents(readCtx, sessionID, args.AfterSeq, limit+1)
		if err != nil {
			return nil, err
		}
		if args.MailboxOnly {
			events = filterAPIMailboxWaitEvents(events)
		}
		hasMore := len(events) > limit
		if hasMore {
			events = events[:limit]
		}
		if len(events) > 0 || waitMs == 0 {
			result := buildAgentEventsResult(sessionID, events)
			if hasMore {
				return toolbroker.ApplyAgentEventsPagination(result, true, apiAgentEventsUnreadCount(ctx, store, sessionID, result.LatestSeq)), nil
			}
			return result, nil
		}
		select {
		case <-readCtx.Done():
			result := buildAgentEventsResult(sessionID, nil)
			result.TimedOut = true
			result.NextAction = ""
			return toolbroker.FinalizeAgentEventsResult(result), nil
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
			if session, loadErr := c.handler.sessionManager.Get(ctx, closeID); loadErr == nil && session != nil {
				_ = c.cleanupAPISpawnIsolation(ctx, session)
			}
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
		toolbroker.ApplySpawnAgentRouteStatusContext(result, session)
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
			state, exists := actor.StateSummary()
			if exists {
				result.Status = string(state.Status)
				result.PendingApproval = state.PendingApproval
				if state.PendingApproval {
					result.PendingApprovalID = strings.TrimSpace(state.PendingApprovalID)
					result.PendingApprovalReason = strings.TrimSpace(state.PendingApprovalReason)
					result.PendingApprovalRiskLevel = strings.TrimSpace(state.PendingApprovalRiskLevel)
				}
				result.PendingQuestion = state.PendingQuestion
				result.CurrentTurnID = strings.TrimSpace(state.CurrentTurnID)
				if state.PendingTool {
					result.PendingToolName = strings.TrimSpace(state.PendingToolName)
					result.PendingToolCallID = strings.TrimSpace(state.PendingToolCallID)
				}
			}
		}
	}
	c.enrichExecutionRunSupervision(ctx, result)
	return result, nil
}

// enrichExecutionRunSupervision attaches the durable execution run supervision
// fields (P6-2) to a status snapshot: run status, attempt, deadlines, heartbeat
// and progress timestamps. Best-effort: an absent run store or run record
// leaves the fields empty without failing the snapshot.
func (c *sessionAgentController) enrichExecutionRunSupervision(ctx context.Context, result *toolbroker.AgentStatusResult) {
	if result == nil || result.SessionID == "" || c == nil || c.handler == nil {
		return
	}
	supervisor := c.handler.getExecutionSupervisor()
	if supervisor == nil || supervisor.Store == nil {
		return
	}
	runs, err := supervisor.Store.ListExecutionRunsBySession(ctx, strings.TrimSpace(result.SessionID), 1)
	if err != nil || len(runs) == 0 {
		return
	}
	run := runs[0]
	result.RunID = strings.TrimSpace(run.RunID)
	result.RunStatus = strings.TrimSpace(run.Status)
	result.Attempt = run.Attempt
	result.MaxAttempts = run.MaxAttempts
	result.RunOwnerID = strings.TrimSpace(run.OwnerID)
	result.ExecutionDeadlineAt = formatSupervisionTimePtr(run.ExecutionDeadlineAt)
	result.ProgressDeadlineAt = formatSupervisionTimePtr(run.ProgressDeadlineAt)
	result.CancelDeadlineAt = formatSupervisionTimePtr(run.CancelDeadlineAt)
	result.LastHeartbeatAt = formatSupervisionTime(run.LastHeartbeatAt)
	result.LastProgressAt = formatSupervisionTime(run.LastProgressAt)
}

// formatSupervisionTime serializes a supervision timestamp for JSON status.
func formatSupervisionTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// formatSupervisionTimePtr serializes an optional supervision timestamp.
func formatSupervisionTimePtr(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
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

// publishAgentReclaimEvent mirrors one quota eviction pass onto the runtime
// event bus (plan §P2-8 方案 4). The event is scoped to the parent session so it
// travels the stream the parent already observes; durable persistence into the
// parent session event stream is done by the handler event bridge
// (shouldPersistRuntimeSessionEvent), which keeps this call synchronous and
// side-effect free for callers that own no session event store.
func (c *sessionAgentController) publishAgentReclaimEvent(parentSessionID, rootSessionID, source string, outcome agentcontrol.ReclaimOutcome) {
	if c == nil || c.handler == nil || outcome.Reclaimed() == 0 {
		return
	}
	bus := c.handler.getRuntimeEventBus()
	if bus == nil {
		return
	}
	payload := agentcontrol.ReclaimEventPayload(source, outcome)
	if root := strings.TrimSpace(rootSessionID); root != "" {
		payload["root_session_id"] = root
	}
	bus.Publish(runtimeevents.Event{
		Type:      agentcontrol.EventAgentReclaimed,
		SessionID: strings.TrimSpace(parentSessionID),
		Payload:   payload,
	})
}

func (c *sessionAgentController) enforceSpawnLimits(ctx context.Context, parentSession *chat.Session, parentSessionID string, childDepth int) error {
	limits := c.agentsConfig()
	if limits.MaxDepth > 0 && childDepth > limits.MaxDepth {
		return runtimeerrors.Newf(
			runtimeerrors.ErrAgentSpawnDepthLimit,
			"agent spawn depth limit reached: max_depth=%d requested_depth=%d; next_action=complete_locally_or_use_spawn_team — continue the work in the current agent, reuse an existing child, or use spawn_team; do not retry the same spawn_agent",
			limits.MaxDepth, childDepth,
		)
	}
	// P2-8 语义收敛：agents.maxThreads=0 表示“未设置”（回退内置默认 6），
	// 仅 -1 是显式不限；超限文案统一附带 next_action 与 active 子会话摘要。
	maxThreads, unlimited := agentcontrol.ResolveMaxThreads(limits.MaxThreads, runtimecfg.DefaultRuntimeConfig().Agents.MaxThreads)
	if unlimited {
		return nil
	}
	rootSessionID := apiAgentRootSessionID(parentSession, parentSessionID)
	if store := c.handler.getAgentControlAgentStore(); store != nil {
		records, err := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
			RootSessionID: rootSessionID,
		})
		if err != nil {
			return err
		}
		children := agentcontrol.QuotaChildren(records)
		if len(children) < maxThreads {
			return nil
		}
		now := time.Now().UTC()
		observations := c.observeQuotaChildren(ctx, children)
		extras := make([]string, 0, 2)
		if reclaimStore, ok := store.(agentcontrol.AgentReclaimStore); ok && reclaimStore != nil {
			policy := agentcontrol.ReclaimPolicy{
				IdleTimeout: time.Duration(limits.ReclaimIdleMs) * time.Millisecond,
				Now:         now,
			}
			outcome, reclaimErr := agentcontrol.ReclaimAgentQuota(ctx, reclaimStore, rootSessionID, observations, policy)
			if err := ctx.Err(); err != nil {
				return err
			}
			// 与 CLI 闸门一致：拒付文案给人读句（机器计数留在
			// agent.reclaimed payload 与 /debug），否则每次被拦下的 spawn
			// 都会把同一串 `reclaimed=3 reclaimed_rows=3
			// reclaim_reasons=session_terminal` 复读进会话记录。
			if human := outcome.HumanSummary(); human != "" {
				extras = append(extras, human)
			}
			if reclaimErr != nil {
				extras = append(extras, "reclaim_error="+reclaimErr.Error())
			}
			if outcome.Reclaimed() > 0 {
				// P2-8 方案 4：回收动作进入产品事件流（父会话 runtime events），
				// 前端下钻与 /debug 据此解释“子会话为何消失、是谁回收的”。
				c.publishAgentReclaimEvent(parentSessionID, rootSessionID, agentcontrol.ReclaimSourceSpawnGate, outcome)
				records, err = store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{
					RootSessionID: rootSessionID,
				})
				if err != nil {
					return err
				}
				if children = agentcontrol.QuotaChildren(records); len(children) < maxThreads {
					return nil
				}
			}
		} else {
			extras = append(extras, "reclaim=unavailable")
		}
		return runtimeerrors.New(runtimeerrors.ErrAgentThreadLimit, agentcontrol.ThreadLimitMessage(
			maxThreads, len(children), agentcontrol.ThreadOccupants(observations, now), extras...,
		))
	}
	count, err := c.countAgentTree(ctx, rootSessionID)
	if err != nil {
		return err
	}
	if count >= maxThreads {
		return runtimeerrors.New(runtimeerrors.ErrAgentThreadLimit, agentcontrol.ThreadLimitMessage(maxThreads, count, nil))
	}
	return nil
}

// observeQuotaChildren projects the durable child rows plus the host-observed
// session/actor liveness into the P2-8 eviction input. Only children whose
// container is provably gone/terminal (default) or idle beyond the opt-in
// timeout become reclaimable — running, waiting-approval and waiting-input
// children are explicitly protected. A container whose execution lease expired
// while its durable state still claims progress is marked stale (the crash
// signature) so the shared policy can release its quota instead of leaving a
// dead child occupying a thread slot forever.
func (c *sessionAgentController) observeQuotaChildren(ctx context.Context, records []agentcontrol.AgentRecord) []agentcontrol.ReclaimObservation {
	observations := make([]agentcontrol.ReclaimObservation, 0, len(records))
	var hub *chat.SessionHub
	if c != nil && c.handler != nil {
		hub = c.handler.getSessionHub()
	}
	for _, record := range records {
		observation := agentcontrol.ReclaimObservation{
			AgentID:           record.AgentID,
			AgentPath:         record.AgentPath,
			SessionID:         record.SessionID,
			Status:            record.Status,
			RegistryUpdatedAt: record.UpdatedAt,
		}
		sessionID := strings.TrimSpace(record.SessionID)
		if sessionID != "" && c != nil && c.handler != nil && c.handler.sessionManager != nil {
			session, err := c.handler.sessionManager.Get(ctx, sessionID)
			switch {
			case err != nil:
				if stderrors.Is(err, chat.ErrSessionNotFound) || strings.Contains(strings.ToLower(err.Error()), "not found") {
					observation.SessionMissing = true
				}
			case session == nil:
				observation.SessionMissing = true
			default:
				if session.State == chat.StateClosed || session.State == chat.StateArchived {
					observation.SessionTerminal = true
				}
				observation.SessionIdleSince = session.UpdatedAt
			}
		}
		actorLive := false
		if hub != nil && sessionID != "" {
			if actor, ok := hub.Get(sessionID); ok && actor != nil {
				// The actor owns the session inside this process and keeps its
				// lease renewed, so the container is never stale in that case.
				actorLive = true
				if state, exists := actor.StateSummary(); exists {
					if state.Busy() || state.PendingTool || state.ActiveJobCount > 0 {
						observation.SessionBusy = true
					}
				}
			}
		}
		if sessionID != "" && !actorLive && !observation.SessionMissing && !observation.SessionTerminal && c != nil && c.handler != nil {
			if store := c.handler.getSessionRuntimeStore(); store != nil {
				if state, err := chat.LoadRuntimeStateForInspection(ctx, store, sessionID); err == nil && state != nil && apiAgentRegistrySessionClaimsProgress(state.Status) {
					if staleLease, leaseErr := apiAgentRegistryHasExpiredLease(ctx, store, sessionID); leaseErr == nil && staleLease {
						observation.SessionStale = true
					}
				}
			}
		}
		observations = append(observations, observation)
	}
	return observations
}

func (c *sessionAgentController) agentsConfig() runtimecfg.AgentsConfig {
	defaults := runtimecfg.DefaultRuntimeConfig().Agents
	if c == nil || c.handler == nil || c.handler.runtimeConfig == nil {
		return defaults
	}
	cfg := c.handler.runtimeConfig.Agents
	if cfg.MaxThreads == 0 && cfg.MaxDepth == 0 && cfg.DefaultWaitTimeoutMs == 0 &&
		cfg.MinWaitTimeoutMs == 0 && cfg.MaxWaitTimeoutMs == 0 && strings.TrimSpace(cfg.WaitTimeoutMode) == "" &&
		strings.TrimSpace(cfg.DefaultForkTurns) == "" && cfg.RegistryReconcileInterval == 0 &&
		strings.TrimSpace(cfg.RegistryReconcileMode) == "" && cfg.RegistryTerminalRetention == 0 &&
		cfg.ReclaimIdleMs == 0 {
		return defaults
	}
	return cfg
}

// waitTimeoutPolicy maps the agents config onto the shared wait-window bounds
// resolver used by wait_agent and read_agent_events.
func (c *sessionAgentController) waitTimeoutPolicy() agentcontrol.WaitTimeoutPolicy {
	cfg := c.agentsConfig()
	return agentcontrol.WaitTimeoutPolicy{
		DefaultMs: cfg.DefaultWaitTimeoutMs,
		MinMs:     cfg.MinWaitTimeoutMs,
		MaxMs:     cfg.MaxWaitTimeoutMs,
		Mode:      cfg.WaitTimeoutMode,
	}
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
				changed = applyAPITeammateAgentdefSessionDefaults(session, profile) || changed
			}
			if err := c.upsertTeamTeammateAgentRecord(ctx, record, mate, session, leadSessionID, path); err != nil {
				return false, err
			}
			return changed, nil
		}
	}
	return false, nil
}

// applyAPITeammateAgentdefSessionDefaults projects portable agentdef
// permission_mode / read_only onto a teammate session when not already set.
// Explicit session values win; task runs still force complete_task via RunMeta.
func applyAPITeammateAgentdefSessionDefaults(session *chat.Session, profile string) bool {
	if session == nil {
		return false
	}
	profile = strings.TrimSpace(profile)
	if !agentdef.IsPortableAgentName(profile) {
		return false
	}
	workspacePath := ""
	if session.Metadata.Context != nil {
		if path := sessionmeta.String(session.Metadata.Context, toolbroker.AgentSessionContextWorktreePath); path != "" {
			workspacePath = path
		} else if path := sessionmeta.String(session.Metadata.Context, sessionmeta.WorkspacePath); path != "" {
			workspacePath = path
		}
	}
	defaults, ok := agentdef.PortableSessionDefaults(profile, agentdef.DiscoverOptions{
		ProjectRoot: strings.TrimSpace(workspacePath),
	})
	if !ok {
		return false
	}
	changed := false
	if defaults.PermissionMode != "" {
		if existing := agentcontrol.ContextString(session, toolbroker.AgentSessionContextPermissionMode); strings.TrimSpace(existing) == "" {
			changed = agentcontrol.SetContextIfChanged(session, toolbroker.AgentSessionContextPermissionMode, defaults.PermissionMode) || changed
		}
	}
	if defaults.HasReadOnly && defaults.ReadOnly {
		if _, ok := sessionmeta.Bool(session.Metadata.Context, toolbroker.AgentSessionContextReadOnly); !ok {
			changed = agentcontrol.SetContextIfChanged(session, toolbroker.AgentSessionContextReadOnly, true) || changed
		}
	}
	return changed
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
	for _, key := range []string{
		"success",
		"error",
		"duration",
		"steps",
		"trace_id",
		"turn_id",
		"usage_prompt_tokens",
		"usage_completion_tokens",
		"usage_total_tokens",
		"usage_cached_tokens",
		"usage_cache_read_tokens",
		"usage_cache_creation_tokens",
		"usage_cache_read_reported",
		"usage_cache_status",
		"usage_reasoning_tokens",
	} {
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

// apiAgentEventsUnreadCount probes how many events remain beyond a returned
// read window. Probe failures degrade to 0: has_more still tells the parent to
// advance after_seq, so a missing count never hides the pagination signal.
func apiAgentEventsUnreadCount(ctx context.Context, store chat.EventStore, sessionID string, afterSeq int64) int {
	if store == nil || afterSeq <= 0 {
		return 0
	}
	remaining, err := store.ListEvents(ctx, sessionID, afterSeq, toolbroker.AgentEventsUnreadProbeLimit+1)
	if err != nil {
		return 0
	}
	return toolbroker.CapAgentEventsUnread(len(remaining))
}

func buildAgentEventsResult(sessionID string, events []runtimeevents.Event) *toolbroker.AgentEventsResult {
	result := &toolbroker.AgentEventsResult{
		SessionID: strings.TrimSpace(sessionID),
		Count:     len(events),
	}
	if len(events) == 0 {
		return toolbroker.FinalizeAgentEventsResult(result)
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
	return toolbroker.FinalizeAgentEventsResult(result)
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
		h.sessionHub = chat.NewBoundedSessionHub(func(sessionID string) (*chat.SessionActor, error) {
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
	requestedProvider := ""
	requestedChildModel := ""
	requestedReasoningEffort := ""
	childCompletionRequirement := ""
	streamRequested := false
	disableTools := false
	childReadOnly := false
	childDepth := 0
	// 会话存储（session_history.sqlite）可能与并发 aicli CLI 进程共享，被写锁
	// 占用时 Get 会阻塞在 sqlite busy_timeout / 应用层重试。带截止时间查询，
	// 失败时降级为默认配置，而不是让请求无限挂起（前端表现为 "signal timed out"）。
	getCtx, getCancel := context.WithTimeout(context.Background(), sessionStoreQueryTimeout)
	defer getCancel()
	if session, err := h.sessionManager.Get(getCtx, sessionID); err == nil && session != nil {
		getContextString := func(key string) string {
			return sessionmeta.String(session.Metadata.Context, key)
		}
		profileRef := getContextString(apiProfileContextReference)
		agentID := getContextString(apiProfileContextAgent)
		workspacePath = getContextString(sessionmeta.WorkspacePath)
		if worktreePath := getContextString(toolbroker.AgentSessionContextWorktreePath); worktreePath != "" {
			workspacePath = worktreePath
		}
		if profileRef != "" {
			if resolved, err := h.resolveProfileSessionState(profileRef, agentID, workspacePath); err == nil {
				profileState = resolved
			}
		}
		childAgentType = getContextString(toolbroker.AgentSessionContextAgentType)
		childCompletionRequirement = getContextString(toolbroker.AgentSessionContextCompletionRequirement)
		requestedProvider = getContextString(sessionmeta.ProviderName)
		requestedChildModel = getContextString(toolbroker.AgentSessionContextRequestedModel)
		if requestedChildModel == "" {
			requestedChildModel = getContextString(sessionmeta.Model)
		}
		requestedReasoningEffort = getContextString(sessionmeta.ReasoningEffort)
		if value, ok := sessionmeta.Bool(session.Metadata.Context, sessionmeta.Stream); ok {
			streamRequested = value
		}
		if value, ok := sessionmeta.Bool(session.Metadata.Context, sessionmeta.DisableTools); ok {
			disableTools = value
		}
		if value, ok := sessionmeta.Bool(session.Metadata.Context, toolbroker.AgentSessionContextReadOnly); ok {
			childReadOnly = value
		}
		if value, ok := sessionmeta.Int(session.Metadata.Context, toolbroker.AgentSessionContextDepth); ok {
			childDepth = value
		}
	}

	selectedConfig := runtimeConfig
	if profileState != nil && profileState.RuntimeConfig != nil {
		selectedConfig = profileState.RuntimeConfig
	}

	agentProvider := resolveAgentProvider(profileState, selectedConfig, h.llmRuntime)
	if strings.TrimSpace(requestedProvider) != "" {
		agentProvider = strings.TrimSpace(requestedProvider)
	}
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
		agentConfig.MaxToolCalls = selectedConfig.Agent.MaxToolCalls
		agentConfig.MaxRunDuration = selectedConfig.Agent.Timeout
		agentConfig.MaxExplorationSteps = selectedConfig.Agent.MaxExplorationSteps
		agentConfig.MaxRepeatedToolCalls = selectedConfig.Agent.MaxRepeatedToolCalls
		agentConfig.MaxRepeatedPollCalls = selectedConfig.Agent.MaxRepeatedPollCalls
		agentConfig.Options = contextOptionsFromRuntimeConfig(selectedConfig)
	}
	if streamRequested || strings.TrimSpace(requestedReasoningEffort) != "" {
		if agentConfig.Options == nil {
			agentConfig.Options = make(map[string]interface{})
		}
		if streamRequested {
			agentConfig.Options["stream"] = true
		}
		if reasoningEffort := runtimetypes.NormalizeReasoningEffort(requestedReasoningEffort); reasoningEffort != "" {
			agentConfig.Options["reasoning_effort"] = reasoningEffort
		}
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
	if disableTools {
		if agentConfig.Options == nil {
			agentConfig.Options = make(map[string]interface{})
		}
		agentConfig.Options[llm.MetadataKeyDisableTools] = true
	}

	// Always construct the request catalog. disable_tools is an execution/choice
	// decision and must not remove definitions from an existing cache lane.
	sessionMCPManager := h.runtimeServerToolSurfaceForSession(context.Background(), sessionID, h.mcpManager, true)
	apiAgent := h.newAPIAgentWithRuntime(agentConfig, &agentRuntimeComponents{
		registry:        h.skillRegistry,
		embeddingRouter: h.embeddingRouter,
		mcpManager:      sessionMCPManager,
		llmRuntime:      h.llmRuntime,
	})
	if disableTools {
		apiAgent.SetToolExecutionPolicy(agent.NewToolExecutionPolicy([]string{}, false))
	} else {
		h.applyAgentExecutionPolicy(apiAgent, workspacePath, selectedConfig, profileStateToolPolicy(profileState))
	}
	if strings.TrimSpace(childAgentType) != "" {
		applyAPIChildAgentdefToolPolicy(apiAgent, childAgentType, workspacePath)
	}
	if childReadOnly {
		toolPolicy := apiAgent.GetToolExecutionPolicy()
		if toolPolicy == nil {
			toolPolicy = agent.NewToolExecutionPolicy(nil, true)
		} else {
			toolPolicy = toolPolicy.Clone()
			toolPolicy.ReadOnly = true
		}
		toolPolicy.SetCapabilityScope(runtimepolicy.ReadOnlyChildCapabilities())
		apiAgent.SetToolExecutionPolicy(toolPolicy)
	}
	maxDepth := 0
	if selectedConfig != nil {
		maxDepth = selectedConfig.Agents.MaxDepth
	}
	applyAPIAgentChildDepthPolicy(apiAgent, childDepth, maxDepth)
	h.applyAgentHooks(apiAgent, selectedConfig)
	h.applyAgentRuntimeServices(apiAgent, selectedConfig)

	stateStore := h.getSessionRuntimeStore()
	eventStore := h.getSessionEventStore()
	// NoWait: this runs inside the session hub factory while the hub lock is
	// held; queueing behind a same-process holder here would stall every
	// session in the hub. Construction-time conflicts surface immediately and
	// the caller (GetOrCreate) maps them to 409.
	leaseHandle, leaseErr := h.acquireSessionLeaseNoWait(context.Background(), sessionID, sessionActorLeaseOwnerKind, sessionID)
	if leaseErr != nil {
		return nil, leaseErr
	}
	loopConfig := buildSessionLoopConfig(selectedConfig, requestedReasoningEffort)
	applyAPISessionCompletionRequirement(loopConfig, profileState, childAgentType, childCompletionRequirement, workspacePath)

	// The session lease is scoped to each run rather than to the actor
	// lifetime. The initial acquisition above guards actor construction
	// (loadState/RecoverStale) against concurrent cross-process writers; it is
	// released immediately after construction. Each subsequent run acquires
	// the lease in PrepareRun and releases it in OnRunFinished / OnRunStalled /
	// OnStop. This prevents an idle actor from blocking web agent-chat turns
	// on the same session for up to the hub idle TTL, which previously caused
	// same-session 409 self-conflicts even without any concurrent execution.
	var leaseMu sync.Mutex
	ensureLease := func(ctx context.Context) error {
		leaseMu.Lock()
		defer leaseMu.Unlock()
		if leaseHandle != nil {
			return nil
		}
		h2, err := h.acquireSessionLease(ctx, sessionID, sessionActorLeaseOwnerKind, sessionID)
		if err != nil {
			return err
		}
		leaseHandle = h2
		return nil
	}
	releaseLease := func() {
		leaseMu.Lock()
		defer leaseMu.Unlock()
		if leaseHandle != nil {
			_ = leaseHandle.Release(context.Background())
			leaseHandle = nil
		}
	}
	actor, err := chat.NewSessionActor(sessionID, chat.SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   h.llmRuntime,
		SessionStore: sessionStore,
		StateStore:   stateStore,
		EventStore:   eventStore,
		EventBus:     h.getRuntimeEventBus(),
		LoopConfig:   loopConfig,
		PersistHook:  h.runtimeServerGoalPersistHook,
		RecoverStale: true,
		PrepareRun: func(ctx context.Context, session *chat.Session, resume bool) error {
			return ensureLease(ctx)
		},
		OnRunStalled: func(turnID string) {
			releaseLease()
		},
		OnRunFinished: func() {
			releaseLease()
		},
		OnStop: func() {
			releaseLease()
		},
	})
	if err != nil {
		releaseLease()
		return nil, err
	}
	// Actor construction (including loadState/RecoverStale) is complete and
	// no run is executing yet: drop the construction lease so an idle actor
	// does not block other channels. The next run re-acquires via PrepareRun.
	releaseLease()
	return actor, nil
}

func applyAPIAgentChildDepthPolicy(apiAgent *agent.Agent, depth, maxDepth int) {
	if apiAgent == nil || maxDepth <= 0 || depth < maxDepth {
		return
	}
	toolPolicy := apiAgent.GetToolExecutionPolicy()
	if toolPolicy == nil {
		toolPolicy = agent.NewToolExecutionPolicy(nil, false)
	} else {
		toolPolicy = toolPolicy.Clone()
	}
	if toolPolicy.DeniedTools == nil {
		toolPolicy.DeniedTools = map[string]bool{}
	}
	for _, toolName := range []string{"spawn_agent", "spawn_subagents", "spawn_team"} {
		toolPolicy.DeniedTools[toolName] = true
	}
	apiAgent.SetToolExecutionPolicy(toolPolicy)
}

// applyAPIChildAgentdefToolPolicy overlays agentdef allow/deny/read-only/sandbox
// onto an API session actor when agent_type resolves to a portable definition.
// Mirrors applyLocalChildAgentdefToolPolicy for spawn_agent / teammate parity.
func applyAPIChildAgentdefToolPolicy(apiAgent *agent.Agent, agentType, workspaceRoot string) {
	if apiAgent == nil {
		return
	}
	agentType = strings.TrimSpace(agentType)
	if !agentdef.IsPortableAgentName(agentType) {
		return
	}
	binding, err := agentdef.ResolvePortableBinding(agentType, agentdef.DiscoverOptions{
		ProjectRoot: strings.TrimSpace(workspaceRoot),
	})
	if err != nil || binding == nil {
		return
	}
	hasSandbox := len(binding.Sandbox) > 0
	if len(binding.ToolAllowlist) == 0 && len(binding.ToolDenylist) == 0 && (binding.ReadOnly == nil || !*binding.ReadOnly) && !hasSandbox {
		return
	}

	readOnly := binding.ReadOnly != nil && *binding.ReadOnly
	var allowlist []string
	if len(binding.ToolAllowlist) > 0 {
		allowlist = append([]string(nil), binding.ToolAllowlist...)
	}
	toolPolicy := apiAgent.GetToolExecutionPolicy()
	if toolPolicy == nil {
		toolPolicy = agent.NewToolExecutionPolicy(allowlist, readOnly)
	} else {
		toolPolicy = toolPolicy.DeriveChild(allowlist, readOnly)
	}
	if len(binding.ToolDenylist) > 0 {
		if toolPolicy.DeniedTools == nil {
			toolPolicy.DeniedTools = map[string]bool{}
		}
		for _, name := range binding.ToolDenylist {
			name = strings.TrimSpace(name)
			if name != "" {
				toolPolicy.DeniedTools[name] = true
			}
		}
	}
	if hasSandbox {
		warnings, err := runtimeprofileinput.MaterializeSandboxForWorkspace(toolPolicy, binding.Sandbox, workspaceRoot)
		if err != nil {
			logger.Warnf("api child agentdef sandbox materialize failed for %s: %v", agentType, err)
		}
		for _, warning := range warnings {
			logger.Warnf("api child agentdef sandbox: %s", warning)
		}
	}
	apiAgent.SetToolExecutionPolicy(toolPolicy)

	if binding.PermissionMode == runtimepolicy.ModePlan {
		engine := apiAgent.GetPermissionEngine()
		if engine == nil {
			engine = agent.NewPermissionEngine()
			apiAgent.SetPermissionEngine(engine)
		}
		engine.Mode = runtimepolicy.ModePlan
		runtimepolicy.EnsurePlanWriteAllowPaths(engine)
	}
}

func buildSessionLoopConfig(selectedConfig *runtimecfg.RuntimeConfig, requestedReasoningEffort ...string) *agent.LoopReActConfig {
	config := &agent.LoopReActConfig{
		MaxSteps:             0,
		EnableThought:        true,
		EnableToolCalls:      true,
		EnableParallelTools:  true,
		MaxParallelToolCalls: 4,
		Temperature:          0.7,
	}
	if selectedConfig != nil {
		config.MaxSteps = agent.NormalizeMaxSteps(selectedConfig.Agent.MaxMaxSteps)
		config.MaxToolCalls = selectedConfig.Agent.MaxToolCalls
		config.MaxRunDuration = selectedConfig.Agent.Timeout
		config.MaxExplorationSteps = selectedConfig.Agent.MaxExplorationSteps
		config.MaxRepeatedToolCalls = selectedConfig.Agent.MaxRepeatedToolCalls
		config.MaxRepeatedPollCalls = selectedConfig.Agent.MaxRepeatedPollCalls
		config.EnableParallelTools = selectedConfig.Agent.EnableParallelTools
		if selectedConfig.Agent.MaxParallelToolCalls > 0 {
			config.MaxParallelToolCalls = selectedConfig.Agent.MaxParallelToolCalls
		}
	}
	if len(requestedReasoningEffort) > 0 {
		if reasoningEffort := runtimetypes.NormalizeReasoningEffort(requestedReasoningEffort[0]); reasoningEffort != "" {
			config.ReasoningEffort = reasoningEffort
		}
	}
	return config
}

// applyAPISessionCompletionRequirement sets loop completion from explicit session
// context, then profile agent id / child agent_type agentdef lookup.
func applyAPISessionCompletionRequirement(config *agent.LoopReActConfig, profileState *profileRuntimeState, agentType, explicitRequirement, workspacePath string) {
	if config == nil {
		return
	}
	requirement := strings.TrimSpace(explicitRequirement)
	profileRoot := ""
	profileAgent := ""
	if profileState != nil && profileState.Resolved != nil {
		profileRoot = strings.TrimSpace(profileState.Resolved.ProfileRoot)
		profileAgent = strings.TrimSpace(profileState.Resolved.AgentID)
	}
	if requirement == "" && profileAgent != "" {
		requirement = resolveAPIAgentdefCompletionRequirement(profileAgent, profileRoot, workspacePath)
	}
	if requirement == "" && strings.TrimSpace(agentType) != "" {
		requirement = resolveAPIAgentdefCompletionRequirement(agentType, profileRoot, workspacePath)
	}
	config.CompletionRequirement = agent.NormalizeCompletionRequirement(requirement)
}

func resolveAPIAgentdefCompletionRequirement(agentName, profileRoot, projectRoot string) string {
	agentName = strings.TrimSpace(agentName)
	if agentName == "" {
		return ""
	}
	def, err := agentdef.Resolve(agentName, agentdef.DiscoverOptions{
		ProjectRoot: strings.TrimSpace(projectRoot),
		ProfileRoot: strings.TrimSpace(profileRoot),
	})
	if err != nil || def == nil {
		return ""
	}
	return string(def.CompletionRequirement)
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

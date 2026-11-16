package runtimeapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
	"github.com/wwsheng009/ai-agent-runtime/internal/workspace"
)

type agentChatActorRun struct {
	Session               *chat.Session
	Request               *agentChatRequest
	ProfileState          *profileRuntimeState
	SelectedConfig        *runtimecfg.RuntimeConfig
	WorkspacePath         string
	AgentContext          map[string]interface{}
	WorkspaceContext      *workspace.WorkspaceContext
	Prompt                string
	TurnID                string
	Provider              string
	Model                 string
	ReasoningEffort       string
	Route                 executionRouteTransparency
	UsageScope            UsageScope
	EstimatedPromptTokens int
	TraceID               string
	Streaming             bool
	RouteAttempted        bool
	RouteCandidates       []*skill.RouteResult
	ReplaceHistory        []runtimetypes.Message
}

// runAgentChatViaSessionActor is the Web adapter for the durable actor core.
// It owns only wire concerns (SSE projection, HTTP envelope, usage ledger);
// history, approvals, compaction, goal driving, and run-scoped leases stay in
// the shared SessionActor implementation.
func (h *Handler) runAgentChatViaSessionActor(ctx context.Context, w http.ResponseWriter, r *http.Request, run agentChatActorRun) {
	if h == nil {
		http.Error(w, "agent chat handler is not configured", http.StatusInternalServerError)
		return
	}
	if run.Session == nil || run.Request == nil {
		h.writeError(w, http.StatusInternalServerError, fmt.Errorf("agent chat actor run is not configured"))
		return
	}
	sid := sessionID(run.Session)
	if sid == "" {
		h.writeError(w, http.StatusInternalServerError, fmt.Errorf("session id is required"))
		return
	}
	if run.Streaming && agentChatStreamFrom(w) == nil {
		opened, err := h.openAgentChatStream(w, r, true)
		if err != nil {
			h.writeError(w, http.StatusBadRequest, err)
			return
		}
		w = opened
	}

	h.reconcilePendingActorRebuild(sid)
	hub := h.getSessionHub()
	if hub == nil {
		h.writeError(w, http.StatusServiceUnavailable, fmt.Errorf("session hub not configured"))
		return
	}
	actor, err := hub.GetOrCreate(sid)
	if err != nil {
		if h.writeSessionLeaseConflict(w, err) {
			return
		}
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	actorName := strings.TrimSpace(actor.AgentName())
	agentName := "api-agent"
	if actorName != "" && actorName != "session-actor" {
		agentName = actorName
	}
	if run.Streaming {
		actor.EnableStreaming()
	} else {
		actor.DisableStreaming()
	}

	skillCfg := h.skillsRuntimeConfigFor(run.ProfileState)
	skillMessages, skillErr := buildSkillExposureMessages(h.skillRegistry, run.Request.ExposeSkills, skillCfg, skillExposureOptions{
		Args:                run.Request.SkillArgs,
		ProjectDir:          run.WorkspacePath,
		SessionID:           sid,
		Effort:              run.ReasoningEffort,
		SubstitutionEnabled: skillCfg.ArgumentSubstitutionEnabled(),
	})
	if skillErr != nil {
		h.writeAgentChatExecutionError(ctx, w, http.StatusBadRequest, skillErr, run.Session, run.TraceID)
		return
	}
	turnSystemMessages := buildAgentContextMessages(run.AgentContext, run.WorkspaceContext)
	turnSystemMessages = append(turnSystemMessages, skillMessages...)

	if value := strings.TrimSpace(run.Provider); value != "" {
		ctx = logger.WithProvider(ctx, value)
	}
	if value := strings.TrimSpace(run.Model); value != "" {
		ctx = logger.WithModel(ctx, value)
	}
	ctx = llm.WithHTTPDebugReporter(ctx, runtimeHTTPDebugReporter(ctx))
	baseRunCtx := ctx
	if run.Request.ResumeOnDisconnect {
		baseRunCtx = context.WithoutCancel(ctx)
	}
	runCtx, cancelRun := context.WithCancel(baseRunCtx)
	defer cancelRun()
	if run.Request.ResumeOnDisconnect {
		runTimeout := time.Duration(0)
		if run.SelectedConfig != nil {
			runTimeout = run.SelectedConfig.Agent.Timeout
		}
		if runTimeout <= 0 {
			runTimeout = defaultDetachedAgentChatRunTimeout
		}
		var cancelTimeout context.CancelFunc
		runCtx, cancelTimeout = context.WithTimeout(runCtx, runTimeout+detachedAgentChatRunGrace)
		defer cancelTimeout()
	}
	runCtx = agent.WithTurnID(runCtx, run.TurnID)

	var turnInterrupted atomic.Bool
	releaseActiveTurn := h.getActiveTurnRegistry().beginCancelable(
		sid,
		run.TurnID,
		agentChatActiveTurnSource,
		run.Request.ResumeOnDisconnect,
		func(string) bool {
			state := actor.StateForInspection()
			if state == nil || strings.TrimSpace(state.CurrentTurnID) != run.TurnID {
				return false
			}
			turnInterrupted.Store(true)
			_ = actor.Interrupt(context.Background())
			return true
		},
	)

	runMeta := &team.RunMeta{
		Source:         "agent_chat",
		PermissionMode: strings.TrimSpace(sessionPermissionMode(run.Session)),
		BudgetTokens:   run.Request.BudgetTokens,
	}
	h.goalDriverMaybeRearmForUserIntent(run.Session, run.Prompt)
	planningMode := h.resolvePlanningMode(run.Request.PlanningMode, run.SelectedConfig)
	routeRequested := run.Request.EnableRoute ||
		strings.EqualFold(strings.TrimSpace(planningMode), string(agent.OrchestrationPlannerPreferred)) ||
		strings.EqualFold(strings.TrimSpace(planningMode), string(agent.OrchestrationRoutePreferred))
	routeOverride := &chat.RunRouteOverride{
		Provider:        strings.TrimSpace(run.Provider),
		Model:           strings.TrimSpace(run.Model),
		ReasoningEffort: strings.TrimSpace(run.ReasoningEffort),
		MaxSteps:        run.Request.MaxSteps,
		Thinking:        runtimetypes.CloneThinkingConfig(run.Request.Thinking),
		TeamID:          strings.TrimSpace(run.Request.TeamID),
		TaskID:          strings.TrimSpace(run.Request.TaskID),
		DisableTools:    run.Request.DisableTools,
		// The direct AgentChat path only attempts skill routing when the request
		// asks for it. Preserve that contract in the actor path; CLI/TUI calls
		// leave this false and keep their existing auto-route behavior.
		DisableSkillRoute: !routeRequested,
	}
	opts := []chat.SubmitPromptOption{{
		RouteOverride:      routeOverride,
		TurnSystemMessages: turnSystemMessages,
		ReplaceHistory:     run.ReplaceHistory,
	}}

	if run.Streaming {
		stream := agentChatStreamFrom(w)
		if stream == nil {
			releaseActiveTurn()
			h.writeError(w, http.StatusInternalServerError, fmt.Errorf("agent chat stream is not available"))
			return
		}
		emitter := h.newTrajectoryEmitter(stream, run.Session, run.TurnID)
		projector := newAgentChatActorStreamProjector(emitter)
		unsubscribeProjector := subscribeAgentChatActorEvents(
			h.getRuntimeEventBus(),
			sid,
			run.TurnID,
			projector.Project,
		)
		defer unsubscribeProjector()

		liveTools := newLiveToolStreamTracker()
		unsubscribeLiveTools := subscribeLiveToolStream(
			h.getRuntimeEventBus(),
			sid,
			liveTools,
			projector.EmitLive,
		)
		defer unsubscribeLiveTools()

		// Close the paced writer before clearing active_turn, matching the
		// direct path's final-frame/registry ordering contract.
		defer func() {
			stream.Close()
			releaseActiveTurn()
		}()

		emitter.Emit("meta", map[string]interface{}{
			"session_id": sid,
			"agent_id":   agentName,
			"source":     "agent_react",
			"kind":       "agent",
			"model":      strings.TrimSpace(run.Model),
			"status":     "streaming",
			"turn_id":    run.TurnID,
			"orchestration": buildOrchestrationPayload(
				"agent_react",
				run.RouteAttempted,
				run.RouteCandidates,
				nil,
				nil,
				"",
			),
		})

		result, submitErr := h.submitAgentChatActorPrompt(runCtx, hub, sid, run.Prompt, runMeta, opts...)
		if submitErr != nil {
			h.writeAgentChatExecutionError(
				ctx,
				w,
				agentChatErrorStatus(submitErr, turnInterrupted.Load()),
				submitErr,
				run.Session,
				run.TraceID,
			)
			return
		}

		resultPayload := h.agentChatActorResultPayload(run, result)
		emitter.Emit("orchestration", resultPayload["orchestration"])
		if routePayload, ok := buildAgentRouteEventPayload(resultPayload); ok {
			emitter.Emit("route", routePayload)
		}
		for _, toolEvent := range buildObservedToolEventPayloadsWithLive(resultPayload, liveTools) {
			emitter.Emit(toolEvent.Event, toolEvent.Payload)
		}
		for _, observationPayload := range buildObservationEventPayloads(resultPayload) {
			emitter.Emit("observation", observationPayload)
		}
		for _, subagentPayload := range buildSubagentEventPayloads(resultPayload) {
			emitter.Emit("subagent", subagentPayload)
		}
		emitter.Emit("result", resultPayload)
		emitter.Emit("done", map[string]interface{}{
			"session_id": sid,
			"agent_id":   agentName,
			"source":     responseResultSource(resultPayload),
			"status":     finalResultStatus(resultPayload),
			"content":    resultPayload["output"],
			"result":     resultPayload,
			"turn_id":    run.TurnID,
		})
		return
	}

	defer releaseActiveTurn()
	result, submitErr := h.submitAgentChatActorPrompt(runCtx, hub, sid, run.Prompt, runMeta, opts...)
	if submitErr != nil {
		h.writeAgentChatExecutionError(
			ctx,
			w,
			agentChatErrorStatus(submitErr, turnInterrupted.Load()),
			submitErr,
			run.Session,
			run.TraceID,
		)
		return
	}
	resultPayload := h.agentChatActorResultPayload(run, result)
	response := map[string]interface{}{
		"session_id": sid,
		"agent_id":   agentName,
		"result":     resultPayload,
		"source":     responseResultSource(resultPayload),
		"status":     finalResultStatus(resultPayload),
		"turn_id":    run.TurnID,
	}
	attachExecutionRouteTransparency(response, run.Route)
	h.writeJSON(w, http.StatusOK, response)
}

func (h *Handler) submitAgentChatActorPrompt(ctx context.Context, hub *chat.SessionHub, sessionID, prompt string, runMeta *team.RunMeta, opts ...chat.SubmitPromptOption) (*agent.Result, error) {
	client := &sessionActorClient{
		handler: h,
		hub:     hub,
	}
	return client.submitPromptResult(ctx, sessionID, prompt, runMeta, opts...)
}

func (h *Handler) agentChatActorResultPayload(run agentChatActorRun, result *agent.Result) map[string]interface{} {
	resultPayload := buildAgentResultPayload("agent_react", result)
	attachAgentChatTurnID(resultPayload, run.TurnID)
	attachExecutionRouteTransparency(resultPayload, run.Route)
	resultPayload["orchestration"] = buildOrchestrationPayload(
		"agent_react",
		run.RouteAttempted,
		run.RouteCandidates,
		result,
		nil,
		"",
	)
	if result != nil {
		h.recordUsage(
			run.UsageScope,
			"agent_chat",
			result.Skill,
			result.Success,
			run.EstimatedPromptTokens,
			result.Usage,
			result.Output,
		)
	}
	return resultPayload
}

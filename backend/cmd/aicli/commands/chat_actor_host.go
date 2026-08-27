package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentdef"
	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	runtimebootstrap "github.com/wwsheng009/ai-agent-runtime/internal/bootstrap"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/contextmgr"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimehooks "github.com/wwsheng009/ai-agent-runtime/internal/hooks"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimeprofileinput "github.com/wwsheng009/ai-agent-runtime/internal/profileinput"
	runtimeserver "github.com/wwsheng009/ai-agent-runtime/internal/runtimeserver"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionruntime"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	toolbrokersessionctx "github.com/wwsheng009/ai-agent-runtime/internal/toolbroker/sessionctx"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const (
	localChatSessionActorLeaseOwnerKind   = "aicli-actor"
	localSubagentBatchRestartGrace        = 5 * time.Minute
	localSubagentBatchRecoveryPassTimeout = 15 * time.Second
	// defaultLocalChatRunStallTimeout 是 run 无进展 watchdog 的默认阈值：
	// run 启动后超过该时长没有任何进展事件（assistant_delta /
	// assistant.reasoning / tool.* / 状态更新）即判定挂死并强制中止。
	// 15 分钟对正常长任务（多步工具调用、长流式输出）足够，同时远小于
	// 用户遇到的"上游挂死 30+ 分钟无事件"场景。
	defaultLocalChatRunStallTimeout = 15 * time.Minute
)

// runLocalSubagentStartupRecovery runs the bounded startup pass immediately
// and once more after restartGrace. The delayed pass catches rows that were
// still inside the stale-worker grace window when this process started.
func runLocalSubagentStartupRecovery(
	ctx context.Context,
	restartGrace time.Duration,
	passTimeout time.Duration,
	recoverOnce func(context.Context),
) {
	if recoverOnce == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	timer := time.NewTimer(restartGrace)
	defer timer.Stop()
	runOnce := func() bool {
		if ctx.Err() != nil {
			return false
		}
		passCtx := ctx
		cancel := func() {}
		if passTimeout > 0 {
			passCtx, cancel = context.WithTimeout(ctx, passTimeout)
		}
		recoverOnce(passCtx)
		cancel()
		return ctx.Err() == nil
	}
	if !runOnce() {
		return
	}
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
		runOnce()
	}
}

type localChatRuntimeHost struct {
	Bootstrap            *runtimebootstrap.Manager
	RuntimeConfig        *runtimecfg.RuntimeConfig
	SessionHub           *runtimechat.SessionHub
	RuntimeStore         runtimechat.RuntimeStateStore
	EventStore           runtimechat.EventStore
	ReceiptStore         runtimechat.ToolReceiptStore
	TeamStore            team.Store
	AgentControl         *agentcontrol.RegistryService
	AgentRegistryStore   agentcontrol.AgentRegistryStore
	Background           *background.Manager
	TeamClaims           *team.PathClaimManager
	Orchestrator         *team.Orchestrator
	ToolSurface          runtimeskill.MCPManager
	EventBus             *runtimeevents.Bus
	SessionStore         runtimechat.SessionStorage
	SessionUser          string
	BaseSession          *ChatSession
	TeamLifecycle        teamLifecycleService
	ActorRegistry        *localActorRegistry
	Supervision          *runtimeserver.SupervisionControlPlane
	SubagentBatches      subagentbatch.BatchStore
	supervisionWake      *supervision.WakeConsumer
	supervisionConfig    supervision.Config
	cleanupFns           []func()
	closeOnce            sync.Once
	subagentMu           sync.Mutex
	subagentCoordinators map[*agent.SubagentBatchCoordinator]struct{}
	subagentOps          int
	subagentIdle         chan struct{}
	closing              bool
	lifecycleCtx         context.Context
	lifecycleCancel      context.CancelFunc
	asyncWG              sync.WaitGroup
}

func (h *localChatRuntimeHost) Close() {
	if h == nil {
		return
	}
	h.closeOnce.Do(func() {
		h.subagentMu.Lock()
		h.closing = true
		subagentIdle := h.subagentIdle
		waitForSubagentOps := h.subagentOps > 0
		h.subagentMu.Unlock()
		if h.lifecycleCancel != nil {
			h.lifecycleCancel()
		}
		h.waitForWarmup()
		h.asyncWG.Wait()
		if waitForSubagentOps && subagentIdle != nil {
			<-subagentIdle
		}
		for i := len(h.cleanupFns) - 1; i >= 0; i-- {
			if h.cleanupFns[i] != nil {
				h.cleanupFns[i]()
			}
		}
	})
}

func (h *localChatRuntimeHost) registerSubagentCoordinator(coordinator *agent.SubagentBatchCoordinator) bool {
	if h == nil || coordinator == nil {
		return false
	}
	h.subagentMu.Lock()
	if h.closing {
		h.subagentMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = coordinator.Shutdown(ctx, "host already closing")
		return false
	}
	if h.subagentCoordinators == nil {
		h.subagentCoordinators = make(map[*agent.SubagentBatchCoordinator]struct{})
	}
	h.subagentCoordinators[coordinator] = struct{}{}
	h.subagentMu.Unlock()
	return true
}

// beginSubagentOperation serializes coordinator setup/replay with host
// shutdown. Close waits for the returned release before it can close the
// durable batch store, preventing a replay from racing store teardown.
func (h *localChatRuntimeHost) beginSubagentOperation() (func(), bool) {
	if h == nil {
		return func() {}, false
	}
	h.subagentMu.Lock()
	if h.closing {
		h.subagentMu.Unlock()
		return func() {}, false
	}
	if h.subagentOps == 0 {
		h.subagentIdle = make(chan struct{})
	}
	h.subagentOps++
	idle := h.subagentIdle
	h.subagentMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			h.subagentMu.Lock()
			h.subagentOps--
			if h.subagentOps == 0 && idle != nil {
				close(idle)
			}
			h.subagentMu.Unlock()
		})
	}, true
}

func (h *localChatRuntimeHost) shutdownSubagentCoordinators() {
	if h == nil {
		return
	}
	h.subagentMu.Lock()
	coordinators := make([]*agent.SubagentBatchCoordinator, 0, len(h.subagentCoordinators))
	for coordinator := range h.subagentCoordinators {
		coordinators = append(coordinators, coordinator)
	}
	h.subagentMu.Unlock()
	for _, coordinator := range coordinators {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		_ = coordinator.Shutdown(ctx, "host shutdown")
		cancel()
	}
}

func localSubagentBatchEmitter(host *localChatRuntimeHost) agent.BatchEmitter {
	return func(eventType string, payload map[string]interface{}) {
		if host == nil || host.EventBus == nil {
			return
		}
		sessionID := ""
		if payload != nil {
			if value, ok := payload["parent_session_id"].(string); ok {
				sessionID = strings.TrimSpace(value)
			}
		}
		host.EventBus.Publish(runtimeevents.Event{Type: eventType, SessionID: sessionID, ToolName: "spawn_subagents", Payload: payload})
	}
}

// localSubagentBatchLifecycleProjector is the single host bridge for both
// synchronous and durable background spawn_subagents terminal states. The
// agent package stays independent from supervision; this adapter turns the
// host-neutral record into a durable lifecycle notification and then offers
// the wake consumer a runnable transition point.
func localSubagentBatchLifecycleProjector(host *localChatRuntimeHost) agent.BatchLifecycleProjector {
	return func(ctx context.Context, terminal agent.BatchTerminalLifecycle) error {
		if host == nil || host.Supervision == nil || host.Supervision.Store == nil {
			return fmt.Errorf("local supervision control plane is not configured")
		}
		if ctx == nil {
			ctx = context.Background()
		}
		rootScopeID := strings.TrimSpace(terminal.RootScopeID)
		if rootScopeID == "" {
			rootScopeID = strings.TrimSpace(terminal.ParentSessionID)
		}
		parentSessionID := strings.TrimSpace(terminal.ParentSessionID)
		if rootScopeID == "" || parentSessionID == "" || strings.TrimSpace(terminal.BatchID) == "" {
			return fmt.Errorf("subagent batch lifecycle requires root, parent and batch ids")
		}

		severity := supervision.SeverityInfo
		supervisionState := supervision.SupervisionTerminated
		resolution := supervision.ResolutionClosed
		recommended := string(supervision.ActionInspect)
		reason := fmt.Sprintf("subagent batch %s finished with status %s (%d/%d completed)", terminal.BatchID, terminal.Status, terminal.CompletedCount, terminal.TaskCount)
		switch terminal.Status {
		case subagentbatch.BatchFailed:
			severity = supervision.SeverityCritical
			supervisionState = supervision.SupervisionBlocked
			resolution = supervision.ResolutionUnresolved
			recommended = string(supervision.ActionInspect)
		case subagentbatch.BatchTimedOut:
			severity = supervision.SeverityCritical
			supervisionState = supervision.SupervisionTimedOut
			resolution = supervision.ResolutionUnresolved
			recommended = string(supervision.ActionCancel)
		case subagentbatch.BatchOrphaned:
			severity = supervision.SeverityCritical
			supervisionState = supervision.SupervisionOrphaned
			resolution = supervision.ResolutionUnresolved
			recommended = string(supervision.ActionCancel)
		case subagentbatch.BatchCanceled:
			severity = supervision.SeverityWarning
			supervisionState = supervision.SupervisionTerminated
			resolution = supervision.ResolutionClosed
		}
		if strings.TrimSpace(terminal.Error) != "" {
			reason += ": " + strings.TrimSpace(terminal.Error)
		}
		_, err := supervision.ProjectLifecycle(ctx, host.Supervision.Store, host.Supervision.Wakes, supervision.LifecycleProjection{
			RootScopeID:           rootScopeID,
			TargetParentSessionID: parentSessionID,
			SubjectKind:           supervision.SubjectAgentRun,
			SubjectID:             terminal.BatchID,
			SubjectVersion:        terminal.SubjectVersion,
			EventType:             terminal.EventType,
			Severity:              severity,
			SupervisionState:      supervisionState,
			Reason:                reason,
			RecommendedAction:     recommended,
			AllowedActions:        []string{string(supervision.ActionInspect), string(supervision.ActionCancel), string(supervision.ActionClose)},
			ResolutionState:       resolution,
		})
		if err != nil {
			return err
		}
		// ProjectLifecycle schedules a wake for unresolved critical states. This
		// call also drains any existing wake after a successful informational or
		// warning projection, without creating a second wake row.
		if err := host.wakeSupervisedParent(ctx, parentSessionID, rootScopeID); err != nil {
			// A busy parent or an exhausted auto-wake budget is an expected
			// durable-control-plane outcome: the wake remains pending for the
			// next turn-end/runnable transition and must not be reported as a
			// failed lifecycle projection.
			if errors.Is(err, supervision.ErrWakeParentBusy) || errors.Is(err, supervision.ErrWakeRateLimited) {
				return nil
			}
			return err
		}
		return nil
	}
}

func localSubagentBatchTerminalSink(host *localChatRuntimeHost) agent.BatchTerminalSink {
	return func(ctx context.Context, notification agent.BatchTerminalNotification) agent.BatchTerminalDelivery {
		if host == nil {
			return agent.BatchTerminalDelivery{Status: agent.BatchTerminalDeliveryFailed, Err: fmt.Errorf("local chat host is nil")}
		}
		parentSessionID := strings.TrimSpace(notification.Batch.ParentSessionID)
		message := toolbroker.BuildSubagentBatchTerminalMailboxMessage(parentSessionID, notification.Batch.BatchID, notification.EventType, notification.DeliveryKey, notification.Payload)
		result, err := runtimechat.DeliverMailboxEventFirstResult(ctx, host.EventStore, host.EventBus, nil, parentSessionID, message)
		if err != nil {
			return agent.BatchTerminalDelivery{Status: agent.BatchTerminalDeliveryFailed, DeliveryKey: notification.DeliveryKey, Err: err}
		}
		return agent.BatchTerminalDelivery{
			Status:           agent.BatchTerminalDeliveryPersisted,
			DeliveryKey:      notification.DeliveryKey,
			AlreadyDelivered: result.Duplicate,
		}
	}
}

// wireLocalSupervisionExecutor installs the concrete runtime executor after
// the actor registry (which owns the session hub close adapter) is ready. It
// is a no-op when the durable control plane is not configured.
func (h *localChatRuntimeHost) wireLocalSupervisionExecutor() {
	if h == nil || h.Supervision == nil || h.Supervision.Actions == nil {
		return
	}
	executor := runtimeserver.SupervisionRuntimeExecutor{
		Store:               h.Supervision.Store,
		TeamStore:           h.TeamStore,
		AgentRegistry:       h.AgentRegistryStore,
		AgentRegistryWriter: h.AgentRegistryStore,
		CloseAgent: func(ctx context.Context, sessionID string) error {
			if h == nil || h.ActorRegistry == nil {
				return fmt.Errorf("actor registry is not ready")
			}
			_, err := h.ActorRegistry.Close(ctx, sessionID)
			return err
		},
	}
	h.Supervision.SetActionExecutor(executor)
	h.wireLocalSupervisionWakeConsumer()
}

// wireLocalSupervisionWakeConsumer installs the wake consumer that turns
// durable critical-lifecycle wakes into real parent turns (doc 6.5). The
// consumer is invoked only at runnable state-transition points; the parent
// runnable check prevents a second concurrent turn while the parent is
// running / waiting approval / waiting input / rewinding.
func (h *localChatRuntimeHost) wireLocalSupervisionWakeConsumer() {
	if h == nil || h.Supervision == nil || h.Supervision.Wakes == nil || h.ActorRegistry == nil {
		return
	}
	h.supervisionWake = &supervision.WakeConsumer{
		Wakes: h.Supervision.Wakes,
		Runnable: func(ctx context.Context, rootScopeID, parentSessionID, parentTeamID string) bool {
			if h == nil || h.RuntimeStore == nil {
				return false
			}
			state, err := h.RuntimeStore.LoadState(ctx, parentSessionID)
			if err != nil || state == nil {
				// No durable state yet (parent never started a turn): keep
				// the wake pending until the parent reaches a known state.
				return false
			}
			return !state.Summary().Busy()
		},
		Deliver: func(ctx context.Context, parentSessionID string, digest *supervision.Digest, wakeIDs []string) error {
			if h == nil || h.ActorRegistry == nil {
				return fmt.Errorf("actor registry is not ready")
			}
			// Deliver asynchronously: the caller is a projection / event
			// handler and must not block on a full parent turn. The wake
			// prompt only references the lifecycle digest; the digest itself
			// is injected by the turn preflight (doc 6.5 rule 5).
			go func() {
				runCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
				defer cancel()
				_, _ = h.ActorRegistry.SubmitPrompt(runCtx, parentSessionID, supervision.AutoWakePrompt, nil)
			}()
			return nil
		},
	}
	h.bindSupervisionWakeConsumer()
	// Recovery runs concurrently during host initialization and may have
	// projected a critical terminal batch before the actor registry/wake
	// consumer was ready. Drain any such durable wake once the consumer is
	// wired; the scheduler keeps it pending if the parent is still busy.
	if h.BaseSession != nil && h.BaseSession.RuntimeSession != nil {
		rootSessionID := strings.TrimSpace(h.BaseSession.RuntimeSession.ID)
		if rootSessionID != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = h.wakeSupervisedParent(ctx, rootSessionID, rootSessionID)
		}
	}
}

// bindSupervisionWakeConsumer subscribes the parent root session turn end so
// wakes accumulated while the parent was busy are drained as soon as the
// parent becomes idle again (doc 6.5 rule 2 closure).
func (h *localChatRuntimeHost) bindSupervisionWakeConsumer() {
	if h == nil || h.supervisionWake == nil || h.EventBus == nil || h.BaseSession == nil || h.BaseSession.RuntimeSession == nil {
		return
	}
	rootSessionID := strings.TrimSpace(h.BaseSession.RuntimeSession.ID)
	if rootSessionID == "" {
		return
	}
	h.EventBus.SubscribeCancelable(runtimechat.EventSessionEnd, func(event runtimeevents.Event) {
		if !strings.EqualFold(strings.TrimSpace(event.SessionID), rootSessionID) {
			return
		}
		if event.Type != runtimechat.EventSessionEnd {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = h.wakeSupervisedParent(ctx, rootSessionID, rootSessionID)
	})
}

// wakeSupervisedParent drains pending wakes for a parent and delivers one
// parent turn when the parent is runnable. It is a no-op when the durable
// control plane is not configured.
func (h *localChatRuntimeHost) wakeSupervisedParent(ctx context.Context, parentSessionID, rootScopeID string) error {
	if h == nil || h.supervisionWake == nil {
		return nil
	}
	return h.supervisionWake.MaybeWakeParent(ctx, parentSessionID, "", rootScopeID)
}

func (h *localChatRuntimeHost) waitForWarmup() {
	if h == nil || h.BaseSession == nil || h.BaseSession.RuntimeSession == nil {
		return
	}
	warmup := currentChatActorWarmup(h.BaseSession, h.BaseSession.RuntimeSession.ID)
	if warmup == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, _ = warmup.wait(ctx)
	cancel()
	setChatActorWarmup(h.BaseSession, nil)
}

func initializeLocalChatRuntimeHost(cfg *config.Config, session *ChatSession, toolManager *runtimetools.Manager) (*localChatRuntimeHost, error) {
	if session == nil {
		return nil, fmt.Errorf("chat session is nil")
	}
	if session.SessionManager == nil || session.RuntimeSession == nil {
		return nil, fmt.Errorf("chat session persistence is not initialized")
	}
	sessionStore := session.SessionManager.GetStorage()
	if sessionStore == nil {
		return nil, fmt.Errorf("chat session storage is not configured")
	}

	runtimeConfig, err := loadLocalChatRuntimeConfig(cfg, session)
	if err != nil {
		return nil, err
	}

	var runtimeMCP runtimeskill.MCPManager
	if toolManager != nil {
		runtimeMCP = runtimetools.NewAgentAdapter(toolManager)
	}
	runtimeMCP = wrapGoalToolSurface(session, runtimeMCP)

	bootstrapManager, err := runtimebootstrap.NewManager(&runtimebootstrap.Options{
		Config:          runtimeConfig,
		SkillDirs:       resolveChatSkillDirs(cfg, session, nil),
		DiscoverOnly:    true,
		MCPManager:      runtimeMCP,
		ProviderConfigs: buildSkillsProviderConfigs(cfg),
	})
	if err != nil {
		return nil, err
	}
	if err := ensureLocalRuntimeProvider(bootstrapManager.LLMRuntime(), session); err != nil {
		_ = bootstrapManager.Stop()
		return nil, err
	}

	runtimeStore, eventStore := buildLocalChatRuntimeStores(session, runtimeConfig)
	receiptStore, _ := runtimeStore.(runtimechat.ToolReceiptStore)
	batchStore, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{
		Path: resolveLocalChatSubagentBatchStorePath(session, runtimeConfig),
	})
	if err != nil {
		_ = bootstrapManager.Stop()
		closeLocalRuntimeStores(runtimeStore, eventStore)
		return nil, fmt.Errorf("initialize subagent batch store: %w", err)
	}
	agentControlRegistry := buildLocalChatAgentControlRegistryService(runtimeConfig)
	backgroundManager := buildLocalChatBackgroundManager(runtimeConfig)
	var globalMailboxStore agentcontrol.GlobalMailboxRegistryStore
	var globalAgentStore agentcontrol.AgentRegistryStore
	if agentControlRegistry != nil {
		globalMailboxStore = agentControlRegistry.MailboxStore
		globalAgentStore = agentControlRegistry.AgentStore
	}
	supervisionConfig := cfg.Supervision.WithDefaults()
	supervisionPlane, err := runtimeserver.BuildSupervisionControlPlane(
		resolveLocalChatSupervisionDataDir(session, runtimeConfig),
		supervisionConfig,
		runtimeserver.SupervisionRuntimeHooks{
			AgentRegistry: globalAgentStore,
			TeamStore:     bootstrapManager.TeamStore(),
		},
	)
	if err != nil {
		_ = bootstrapManager.Stop()
		closeLocalRuntimeStores(runtimeStore, eventStore)
		_ = batchStore.Close()
		if agentControlRegistry != nil {
			_ = agentControlRegistry.Close()
		}
		if backgroundManager != nil {
			backgroundManager.Close()
		}
		return nil, fmt.Errorf("initialize supervision control plane: %w", err)
	}
	configureLocalChatMailboxWriteThrough(globalMailboxStore, runtimeStore, bootstrapManager.TeamStore())
	eventBus := runtimeevents.NewBusWithRetention(2048)
	host := &localChatRuntimeHost{
		Bootstrap:          bootstrapManager,
		RuntimeConfig:      runtimeConfig,
		RuntimeStore:       runtimeStore,
		EventStore:         eventStore,
		ReceiptStore:       receiptStore,
		TeamStore:          bootstrapManager.TeamStore(),
		AgentControl:       agentControlRegistry,
		AgentRegistryStore: globalAgentStore,
		Background:         backgroundManager,
		ToolSurface:        runtimeMCP,
		EventBus:           eventBus,
		SessionStore:       sessionStore,
		SessionUser:        session.SessionUserID,
		BaseSession:        session,
		Supervision:        supervisionPlane,
		SubagentBatches:    batchStore,
		supervisionConfig:  supervisionConfig,
	}
	host.lifecycleCtx, host.lifecycleCancel = context.WithCancel(context.Background())
	// Recover only rows whose heartbeat is older than the restart grace period.
	// Run a second pass after that grace expires: rows written immediately before
	// the previous process died are intentionally too fresh for the first pass,
	// but must not remain queued/running forever when no actor is materialized.
	// The recovery coordinator is deliberately not registered because it owns no
	// worker; lifecycleCtx still cancels the delayed pass before store teardown.
	host.asyncWG.Add(1)
	go func(batchStore subagentbatch.BatchStore, lifecycleCtx context.Context) {
		defer host.asyncWG.Done()
		runLocalSubagentStartupRecovery(
			lifecycleCtx,
			localSubagentBatchRestartGrace,
			localSubagentBatchRecoveryPassTimeout,
			func(recoveryCtx context.Context) {
				coordinator := agent.NewSubagentBatchCoordinator(agent.SubagentBatchCoordinatorConfig{
					Store:              batchStore,
					Emitter:            localSubagentBatchEmitter(host),
					TerminalSink:       localSubagentBatchTerminalSink(host),
					LifecycleProjector: localSubagentBatchLifecycleProjector(host),
				})
				_, _ = coordinator.RecoverStaleBatches(recoveryCtx, localSubagentBatchRestartGrace, "", 512)
				_, _ = coordinator.ReplayTerminalDeliveries(recoveryCtx, "", 512)
			},
		)
	}(batchStore, host.lifecycleCtx)
	host.TeamLifecycle = newLocalTeamLifecycleService(host)

	workspaceRoot := resolveLocalWorkspacePath(runtimeConfig, session)
	// Re-apply project permissions against the resolved workspace root (cwd bootstrap may differ).
	applyChatPermissionsOverlay(session, workspaceRoot)
	claims := team.NewPathClaimManager(host.TeamStore, workspaceRoot)
	host.TeamClaims = claims
	host.Orchestrator = team.NewOrchestrator(host.TeamStore, claims, nil)
	host.Orchestrator.ExpertConcurrencyLimit = localChatTeamExpertConcurrencyLimit(session)
	if globalMailboxStore != nil {
		host.Orchestrator.MailboxWake = globalMailboxStore
	}
	host.ActorRegistry = newLocalActorRegistry(host)
	host.wireLocalSupervisionExecutor()
	if host.Orchestrator != nil {
		mailbox := team.NewMailboxService(host.TeamStore)
		host.Orchestrator.Mailbox = mailbox
		host.Orchestrator.Dispatcher = host.ActorRegistry
		host.Orchestrator.Runner = &team.TeammateRunner{
			Sessions:      host.ActorRegistry,
			AgentControl:  host.ActorRegistry,
			Mailbox:       mailbox,
			Context:       team.NewContextBuilder(host.TeamStore),
			RouteResolver: newLocalTeamTaskRouteResolver(host),
			RouteAudit:    newLocalTeamTaskRouteAuditSink(host),
		}
		host.Orchestrator.LeadPlanner = &team.LeadPlanner{
			Sessions:    host.ActorRegistry,
			Store:       host.TeamStore,
			Mailbox:     mailbox,
			AutoPersist: true,
		}
		host.Orchestrator.LeaseManager = team.NewLeaseManager(host.TeamStore, claims)
		host.Orchestrator.LeaseManager.Mailbox = mailbox
	}
	host.bindTeamLifecycleEvents()
	host.syncTeamLifecycleLoops()
	host.SessionHub = runtimechat.NewBoundedSessionHub(func(sessionID string) (*runtimechat.SessionActor, error) {
		return host.buildSessionActor(sessionID, session, sessionStore, runtimeConfig, workspaceRoot)
	})
	host.cleanupFns = []func(){
		func() {
			_ = bootstrapManager.Stop()
		},
		func() {
			if backgroundManager != nil {
				backgroundManager.Close()
			}
		},
		func() {
			closeLocalRuntimeStores(runtimeStore, eventStore)
		},
		func() {
			if batchStore != nil {
				_ = batchStore.Close()
			}
		},
		func() {
			if host.Orchestrator != nil && globalMailboxStore != nil {
				host.Orchestrator.MailboxWake = nil
			}
			configureLocalChatMailboxWriteThrough(nil, runtimeStore, bootstrapManager.TeamStore())
			if agentControlRegistry != nil {
				_ = agentControlRegistry.Close()
			}
		},
		func() {
			if host.SessionHub != nil {
				host.SessionHub.StopAll()
			}
		},
		func() {
			host.stopTeamLifecycleLoops()
		},
		func() {
			if supervisionPlane != nil {
				_ = supervisionPlane.Close()
			}
		},
		func() {
			host.shutdownSubagentCoordinators()
		},
	}

	return host, nil
}

func refreshLocalRuntimeAfterModelSelection(session *ChatSession) error {
	if session == nil {
		return nil
	}

	setChatActorWarmup(session, nil)
	var errs []string
	if session.LocalRuntimeHost != nil && session.LocalRuntimeHost.SessionHub != nil && session.RuntimeSession != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), chatInterruptCleanupTimeout)
		_ = session.LocalRuntimeHost.SessionHub.StopContext(stopCtx, session.RuntimeSession.ID)
		stopCancel()
	}
	if session.LocalRuntimeHost != nil && session.LocalRuntimeHost.Bootstrap != nil && session.Config != nil {
		if err := session.LocalRuntimeHost.Bootstrap.ReloadProviderConfigs(buildSkillsProviderConfigs(session.Config)); err != nil {
			errs = append(errs, fmt.Sprintf("reload providers: %v", err))
		}
	}
	if session.LocalRuntimeHost != nil && session.LocalRuntimeHost.Bootstrap != nil {
		if err := ensureLocalRuntimeProvider(session.LocalRuntimeHost.Bootstrap.LLMRuntime(), session); err != nil {
			errs = append(errs, fmt.Sprintf("ensure session provider: %v", err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	startChatActorWarmup(session)
	return nil
}

func (h *localChatRuntimeHost) buildSessionActor(sessionID string, session *ChatSession, sessionStore runtimechat.SessionStorage, runtimeConfig *runtimecfg.RuntimeConfig, workspaceRoot string) (*runtimechat.SessionActor, error) {
	childAgentType := ""
	requestedProvider := ""
	requestedModel := ""
	requestedReasoningEffort := ""
	childCompletionRequirement := ""
	childReadOnly := false
	childDepth := 0
	baseSessionID := ""
	if session != nil && session.RuntimeSession != nil {
		baseSessionID = strings.TrimSpace(session.RuntimeSession.ID)
	}
	isBaseSession := baseSessionID != "" && strings.EqualFold(strings.TrimSpace(sessionID), baseSessionID)
	if sessionStore != nil {
		if runtimeSession, err := sessionStore.Load(context.Background(), sessionID); err == nil && runtimeSession != nil {
			if value, ok := runtimeSession.GetContext(toolbroker.AgentSessionContextAgentType); ok {
				if text, ok := value.(string); ok {
					childAgentType = strings.TrimSpace(text)
				}
			}
			childCompletionRequirement = agentcontrol.ContextString(runtimeSession, toolbroker.AgentSessionContextCompletionRequirement)
			if !isBaseSession {
				// Prefer per-child worktree path so tools and system prompt cwd stay isolated.
				if path := agentcontrol.ContextString(runtimeSession, toolbroker.AgentSessionContextWorktreePath); path != "" {
					workspaceRoot = path
				} else if path := agentcontrol.ContextString(runtimeSession, sessionmeta.WorkspacePath); path != "" {
					workspaceRoot = path
				}
				requestedProvider = agentcontrol.ContextString(runtimeSession, sessionmeta.ProviderName)
				requestedModel = agentcontrol.ContextString(runtimeSession, toolbroker.AgentSessionContextRequestedModel)
				if requestedModel == "" {
					requestedModel = agentcontrol.ContextString(runtimeSession, sessionmeta.Model)
				}
				requestedReasoningEffort = agentcontrol.ContextString(runtimeSession, sessionmeta.ReasoningEffort)
				if value, ok := runtimeSessionContextBool(runtimeSession, toolbroker.AgentSessionContextReadOnly); ok {
					childReadOnly = value
				}
				childDepth = localAgentSessionDepth(runtimeSession)
			}
		}
	}
	apiAgent := buildLocalChatAgent(session, h, runtimeConfig, workspaceRoot, childAgentType, requestedModel, requestedProvider, requestedReasoningEffort)
	if !isBaseSession && strings.TrimSpace(childAgentType) != "" {
		applyLocalChildAgentdefToolPolicy(apiAgent, childAgentType, session, workspaceRoot)
	}
	applyLocalChildReadOnlyPolicy(apiAgent, childReadOnly)
	maxDepth := 0
	if runtimeConfig != nil {
		maxDepth = runtimeConfig.Agents.MaxDepth
	} else if h != nil && h.RuntimeConfig != nil {
		maxDepth = h.RuntimeConfig.Agents.MaxDepth
	}
	applyLocalChildDepthPolicy(apiAgent, childDepth, maxDepth)
	leaseHandle, leaseErr := acquireLocalChatSessionLease(context.Background(), h.RuntimeStore, sessionID)
	if leaseErr != nil {
		return nil, leaseErr
	}
	loopConfig := buildLocalChatLoopConfig(runtimeConfig, session, requestedReasoningEffort)
	applyLocalChatCompletionRequirement(loopConfig, session, childAgentType, childCompletionRequirement, workspaceRoot)
	actor, err := runtimechat.NewSessionActor(sessionID, runtimechat.SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   h.Bootstrap.LLMRuntime(),
		SessionStore: sessionStore,
		StateStore:   h.RuntimeStore,
		EventStore:   h.EventStore,
		EventBus:     h.EventBus,
		LoopConfig:   loopConfig,
		PrepareRun:   localChatPrepareRunHook(apiAgent, session, workspaceRoot, isBaseSession),
		PersistHook:  localGoalPersistHook(sessionStore),
		RecoverStale: true,
		// 上游挂死/网络卡死时 run 可能长时间无任何进展（无 delta、无工具
		// 事件、无状态更新），状态卡在 running，busy 锁让用户无法继续也无法
		// 重启接管。watchdog 超时后强制中止并释放 lease，让会话可恢复。
		RunStallTimeout: defaultLocalChatRunStallTimeout,
		OnRunStalled: func(turnID string) {
			if leaseHandle != nil {
				_ = leaseHandle.Release(context.Background())
			}
		},
		OnStop: func() {
			if leaseHandle != nil {
				_ = leaseHandle.Release(context.Background())
			}
		},
	})
	if err != nil {
		if leaseHandle != nil {
			_ = leaseHandle.Release(context.Background())
		}
		return nil, err
	}
	return actor, nil
}

// applyLocalChildAgentdefToolPolicy overlays agentdef allow/deny/read-only onto
// a child actor when spawn_agent agent_type resolves to a portable definition.
func applyLocalChildAgentdefToolPolicy(apiAgent *agent.Agent, agentType string, session *ChatSession, workspaceRoot string) {
	if apiAgent == nil {
		return
	}
	agentType = strings.TrimSpace(agentType)
	if !agentdef.IsPortableAgentName(agentType) {
		return
	}
	profileRoot := ""
	if session != nil {
		profileRoot = strings.TrimSpace(session.ProfileRoot)
	}
	def, err := agentdef.Resolve(agentType, agentdefDiscoverOptions(
		strings.TrimSpace(workspaceRoot),
		profileRoot,
		nil,
	))
	if err != nil || def == nil {
		return
	}
	binding, err := agentdef.BuildBinding(def)
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
			logpkg.Warnf("child agentdef sandbox materialize failed for %s: %v", agentType, err)
		}
		for _, warning := range warnings {
			logpkg.Warnf("child agentdef sandbox: %s", warning)
		}
	}
	apiAgent.SetToolExecutionPolicy(toolPolicy)

	// Plan-role children get plan write allow paths even without /plan state.
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

func applyLocalChildReadOnlyPolicy(apiAgent *agent.Agent, readOnly bool) {
	if apiAgent == nil || !readOnly {
		return
	}
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

func applyLocalChildDepthPolicy(apiAgent *agent.Agent, depth, maxDepth int) {
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

func acquireLocalChatSessionLease(ctx context.Context, store runtimechat.RuntimeStateStore, sessionID string) (*runtimechat.SessionLeaseHandle, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, nil
	}
	leaseStore, ok := store.(runtimechat.SessionLeaseStore)
	if !ok || leaseStore == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	req := runtimechat.LeaseRequest{
		SessionID: sessionID,
		OwnerID:   localChatSessionLeaseOwnerID(localChatSessionActorLeaseOwnerKind, sessionID),
		OwnerKind: localChatSessionActorLeaseOwnerKind,
		PID:       os.Getpid(),
		Hostname:  localChatHostname(),
	}
	handle, err := runtimechat.AcquireSessionLease(ctx, leaseStore, req)
	if err == nil {
		return handle, nil
	}
	var conflict *runtimechat.LeaseConflictError
	if !errors.As(err, &conflict) || !localChatSessionLeaseOwnerStopped(conflict.Lease) {
		return nil, err
	}
	// Release is owner-conditional, so a concurrent live takeover cannot be
	// deleted between the conflict read and this stale-owner cleanup.
	if releaseErr := leaseStore.ReleaseLease(ctx, sessionID, conflict.Lease.OwnerID); releaseErr != nil {
		return nil, fmt.Errorf("release stale local session lease: %w", releaseErr)
	}
	return runtimechat.AcquireSessionLease(ctx, leaseStore, req)
}

func localChatSessionLeaseOwnerStopped(lease *runtimechat.SessionLease) bool {
	if lease == nil || lease.PID <= 0 || !strings.EqualFold(strings.TrimSpace(lease.OwnerKind), localChatSessionActorLeaseOwnerKind) {
		return false
	}
	leaseHost := strings.TrimSpace(lease.Hostname)
	localHost := localChatHostname()
	if leaseHost == "" || localHost == "" || !strings.EqualFold(leaseHost, localHost) {
		return false
	}
	running, known := localChatProcessRunning(lease.PID)
	return known && !running
}

func localChatSessionLeaseOwnerID(ownerKind, scope string) string {
	parts := []string{sanitizeLocalChatLeaseOwnerPart(ownerKind)}
	if hostname := localChatHostname(); hostname != "" {
		parts = append(parts, sanitizeLocalChatLeaseOwnerPart(hostname))
	}
	parts = append(parts, strconv.Itoa(os.Getpid()))
	if scope = strings.TrimSpace(scope); scope != "" {
		parts = append(parts, sanitizeLocalChatLeaseOwnerPart(scope))
	}
	return strings.Join(parts, ":")
}

func localChatHostname() string {
	hostname, err := os.Hostname()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(hostname)
}

func sanitizeLocalChatLeaseOwnerPart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	value = strings.ReplaceAll(value, ":", "_")
	value = strings.ReplaceAll(value, " ", "_")
	return value
}

func buildLocalChatAgent(session *ChatSession, host *localChatRuntimeHost, runtimeConfig *runtimecfg.RuntimeConfig, workspaceRoot string, childAgentType string, requestedModel string, requestedRoute ...string) *agent.Agent {
	requestedProvider := ""
	requestedReasoningEffort := ""
	if len(requestedRoute) > 0 {
		requestedProvider = strings.TrimSpace(requestedRoute[0])
	}
	if len(requestedRoute) > 1 {
		requestedReasoningEffort = strings.TrimSpace(requestedRoute[1])
	}
	agentConfig := &agent.Config{
		Name:         firstNonEmptyChatValue(strings.TrimSpace(childAgentType), "aicli-chat"),
		Provider:     resolveLocalChatAgentProvider(session, host),
		Model:        resolveLocalChatAgentModel(session, host),
		SystemPrompt: composeLocalChatSystemPrompt(session, workspaceRoot),
		MaxSteps:     0,
	}
	if session != nil {
		// Use the Claude Code-style capped request default, not the provider
		// hard ceiling (max_tokens_limit). The ceiling still clamps via the
		// LLM adapter request builder.
		protocol := session.Provider.GetProtocol()
		model := resolveLocalChatAgentModel(session, host)
		providerLimit := session.Provider.GetMaxTokensLimit()
		capability, hasCapability := config.ModelCapabilitySpec{}, false
		if model != "" && len(session.Provider.ModelCapabilities) > 0 {
			if cap, ok := session.Provider.ModelCapabilities[model]; ok {
				capability, hasCapability = cap, true
			} else if cap, ok := session.Provider.ModelCapabilities["*"]; ok {
				capability, hasCapability = cap, true
			}
		}
		resolved := runtimellm.ResolveRequestMaxTokens(protocol, model, 0, capability, hasCapability, providerLimit)
		if resolved.Default > 0 {
			agentConfig.DefaultMaxTokens = resolved.Default
		}
	}
	if requestedProvider != "" {
		agentConfig.Provider = requestedProvider
	}
	if strings.TrimSpace(requestedModel) != "" {
		agentConfig.Model = strings.TrimSpace(requestedModel)
	}
	if runtimeConfig != nil {
		agentConfig.MaxSteps = agent.NormalizeMaxSteps(runtimeConfig.Agent.MaxMaxSteps)
		agentConfig.MaxToolCalls = runtimeConfig.Agent.MaxToolCalls
		agentConfig.MaxRunDuration = runtimeConfig.Agent.Timeout
		agentConfig.MaxExplorationSteps = runtimeConfig.Agent.MaxExplorationSteps
		agentConfig.MaxRepeatedToolCalls = runtimeConfig.Agent.MaxRepeatedToolCalls
	}
	workspaceMode := resolveLocalChatWorkspaceMode(runtimeConfig)
	workspaceContextEnabled := workspaceMode != "" && !strings.EqualFold(workspaceMode, contextmgr.WorkspaceModeDisabled)
	stream := session != nil && session.Stream
	var profileContext map[string]interface{}
	sessionReasoningEffort := ""
	if session != nil {
		profileContext = session.ProfileContext
		sessionReasoningEffort = session.ReasoningEffort
	}
	reasoningEffort := firstNonEmptyChatValue(requestedReasoningEffort, sessionReasoningEffort)
	// tool_base_path is always set when a workspace root is known so preflight /
	// relative path resolution match toolkit SetBasePath even when workspace
	// context scanning remains disabled by default.
	if stream || strings.TrimSpace(reasoningEffort) != "" || workspaceRoot != "" || len(profileContext) > 0 {
		agentConfig.Options = make(map[string]interface{})
		if stream {
			agentConfig.Options["stream"] = true
		}
		if reasoningEffort := runtimetypes.NormalizeReasoningEffort(reasoningEffort); reasoningEffort != "" {
			agentConfig.Options["reasoning_effort"] = reasoningEffort
		}
		if workspaceRoot != "" {
			agentConfig.Options["tool_base_path"] = workspaceRoot
		}
		if workspaceRoot != "" && workspaceContextEnabled {
			agentConfig.Options["workspace_path"] = workspaceRoot
			agentConfig.Options["context_workspace_mode"] = workspaceMode
			agentConfig.Options["context_min_workspace_query_length"] = 4
		}
		if len(profileContext) > 0 {
			agentConfig.Options["profile_context"] = cloneSkillContextMap(profileContext)
		}
	}
	applyLocalChatContextOptions(agentConfig, runtimeConfig)
	if guidance := strings.TrimSpace(renderActiveGoalGuidance(session)); guidance != "" {
		if agentConfig.Options == nil {
			agentConfig.Options = make(map[string]interface{})
		}
		agentConfig.Options["active_goal_guidance"] = guidance
	}
	if session != nil && session.RetryConfig.DisableRetries {
		if agentConfig.Options == nil {
			agentConfig.Options = make(map[string]interface{})
		}
		agentConfig.Options[runtimellm.MetadataKeyDisableRetries] = true
	}

	apiAgent := agent.NewAgentWithLLM(agentConfig, host.ToolSurface, host.Bootstrap.LLMRuntime())
	apiAgent.SetBatchLifecycleProjector(localSubagentBatchLifecycleProjector(host))
	scheduler := agent.NewSubagentScheduler(apiAgent, agent.SubagentSchedulerConfig{
		Routing: localChatSubagentRoutingConfig(session),
	})
	apiAgent.SetSubagentScheduler(scheduler)
	var batchCoordinator *agent.SubagentBatchCoordinator
	if host.SubagentBatches != nil {
		coordinator := agent.NewSubagentBatchCoordinator(agent.SubagentBatchCoordinatorConfig{
			Store:              host.SubagentBatches,
			Scheduler:          scheduler,
			Emitter:            localSubagentBatchEmitter(host),
			TerminalSink:       localSubagentBatchTerminalSink(host),
			LifecycleProjector: localSubagentBatchLifecycleProjector(host),
		})
		if release, active := host.beginSubagentOperation(); active {
			if host.registerSubagentCoordinator(coordinator) {
				batchCoordinator = coordinator
				apiAgent.SetSubagentBatchCoordinator(coordinator)
				parentSessionID := ""
				if session != nil && session.RuntimeSession != nil && strings.TrimSpace(session.RuntimeSession.ID) != "" {
					if parentSessionID == "" {
						parentSessionID = strings.TrimSpace(session.RuntimeSession.ID)
					}
				}
				replayCtx, replayCancel := context.WithTimeout(context.Background(), 10*time.Second)
				_, _ = coordinator.SetTerminalSinkAndReplay(replayCtx, localSubagentBatchTerminalSink(host), parentSessionID, 512)
				replayCancel()
			}
			release()
		}
	}
	if registry := host.Bootstrap.Registry(); registry != nil {
		for _, summary := range registry.ListSummaries() {
			if summary == nil {
				continue
			}
			_ = apiAgent.RegisterSkill(summary.ToSkillStub())
		}
	}
	if embeddingRouter := host.Bootstrap.EmbeddingRouter(); embeddingRouter != nil {
		if cloned, err := embeddingRouter.CloneForRegistry(apiAgent.GetSkillRouter().Registry()); err == nil {
			apiAgent.GetSkillRouter().SetEmbeddingRouter(cloned)
		}
	}
	if host.EventBus != nil {
		apiAgent.SetEventBus(host.EventBus)
	}
	if batchCoordinator != nil {
		batchCoordinator.SetTerminalSink(localSubagentBatchTerminalSink(host))
	}
	checkpointConfig := runtimecfg.DefaultRuntimeConfig().Checkpoint
	if runtimeConfig != nil {
		checkpointConfig = runtimeConfig.Checkpoint
	}
	apiAgent.ApplyCheckpointConfig(
		checkpointConfig.Enabled,
		checkpointConfig.MaxFileBytes,
		agent.CheckpointStorageOptions{
			StoreMode:                checkpointConfig.StoreMode,
			ConversationSnapshot:     checkpointConfig.ConversationSnapshot,
			MaxDiffBytes:             checkpointConfig.MaxDiffBytes,
			MaxCheckpointsPerSession: checkpointConfig.MaxCheckpointsPerSession,
		},
	)
	if host.TeamStore != nil {
		if ctxMgr := apiAgent.GetContextManager(); ctxMgr != nil {
			ctxMgr.TeamContext = team.NewContextBuilder(host.TeamStore)
		}
	}
	if host.TeamStore != nil {
		broker := apiAgent.GetToolBroker()
		if broker == nil {
			broker = &toolbroker.Broker{}
			apiAgent.SetToolBroker(broker)
		}
		if broker.SessionContextStore == nil {
			broker.SessionContextStore = toolbrokersessionctx.New(host.SessionStore)
		}
		broker.AgentSessions = host.ActorRegistry
		broker.TeamStore = host.TeamStore
		broker.TeamClaims = host.TeamClaims
		broker.TeamDispatcher = host.ActorRegistry
		broker.TeamLifecycleChanged = host.syncTeamLifecycleLoops
		if host.Orchestrator != nil {
			broker.TeamPlanner = host.Orchestrator.LeadPlanner
			broker.TeamEvents = host.Orchestrator.Events
		}
	}
	if apiAgent.GetToolBroker() == nil && host.ActorRegistry != nil {
		apiAgent.SetToolBroker(&toolbroker.Broker{
			AgentSessions:       host.ActorRegistry,
			SessionContextStore: toolbrokersessionctx.New(host.SessionStore),
		})
	} else if broker := apiAgent.GetToolBroker(); broker != nil && broker.AgentSessions == nil && host.ActorRegistry != nil {
		broker.AgentSessions = host.ActorRegistry
	}
	if broker := apiAgent.GetToolBroker(); broker != nil && broker.SessionContextStore == nil {
		broker.SessionContextStore = toolbrokersessionctx.New(host.SessionStore)
	}
	if host.Background != nil {
		broker := apiAgent.GetToolBroker()
		if broker == nil {
			broker = &toolbroker.Broker{}
			apiAgent.SetToolBroker(broker)
		}
		if broker.SessionContextStore == nil && host.SessionStore != nil {
			broker.SessionContextStore = toolbrokersessionctx.New(host.SessionStore)
		}
		broker.Background = host.Background
	}
	if toolPolicy := buildLocalChatToolPolicy(session, host.ToolSurface, apiAgent.GetToolBroker()); toolPolicy != nil {
		apiAgent.SetToolExecutionPolicy(toolPolicy)
	}
	// Product permission overlay rules on the actor permission engine.
	applyChatPermissionsOverlayToAgent(apiAgent, session)
	var baseHooks []runtimehooks.HookConfig
	if runtimeConfig != nil {
		baseHooks = runtimeConfig.Hooks
	}
	if mergedHooks := mergeActivePluginHooks(baseHooks); len(mergedHooks) > 0 {
		apiAgent.SetHookManager(runtimehooks.NewManager(mergedHooks))
	}

	return apiAgent
}

func localChatPrepareRunHook(apiAgent *agent.Agent, session *ChatSession, workspaceRoot string, isBaseSession bool) func(context.Context, *runtimechat.Session, bool) error {
	if apiAgent == nil || session == nil || !isBaseSession {
		return nil
	}
	return func(ctx context.Context, runtimeSession *runtimechat.Session, resume bool) error {
		ensureChatSystemPromptMessage(session)
		if cfg := apiAgent.GetConfig(); cfg != nil {
			// Provider prompt caching requires the outbound instruction head
			// (messages[0]) to stay byte-identical for the whole session.
			// Compose it once and freeze it on the session: re-deriving it here
			// on every run would silently rewrite the cached prefix whenever a
			// later run resolves the workspace root (or any other compose input)
			// differently from session start. Session-scoped changes belong in
			// turn-context form (e.g. active_goal_guidance below), never in the
			// frozen instruction head.
			composed := loadFrozenChatSystemPrompt(runtimeSession, session)
			if composed == "" {
				composed = composeLocalChatSystemPrompt(session, workspaceRoot)
				storeFrozenChatSystemPrompt(runtimeSession, session, composed)
			}
			cfg.SystemPrompt = composed
			if cfg.Options == nil {
				cfg.Options = make(map[string]interface{})
			}
			if guidance := strings.TrimSpace(renderActiveGoalGuidance(session)); guidance != "" {
				cfg.Options["active_goal_guidance"] = guidance
			} else {
				delete(cfg.Options, "active_goal_guidance")
			}
		}
		// Plan mode may recreate/mutate the engine; re-apply product overlay after it.
		applyChatPlanModeToAgent(apiAgent, session, runtimeSession)
		applyChatPermissionsOverlayToAgent(apiAgent, session)
		return nil
	}
}

// frozenChatSystemPromptContext returns the durable session context map used to
// anchor the frozen outbound system prompt, initializing it when missing.
func frozenChatSystemPromptContext(runtimeSession *runtimechat.Session, session *ChatSession) map[string]interface{} {
	if runtimeSession != nil {
		if runtimeSession.Metadata.Context == nil {
			runtimeSession.Metadata.Context = make(map[string]interface{})
		}
		return runtimeSession.Metadata.Context
	}
	if session != nil && session.RuntimeSession != nil {
		if session.RuntimeSession.Metadata.Context == nil {
			session.RuntimeSession.Metadata.Context = make(map[string]interface{})
		}
		return session.RuntimeSession.Metadata.Context
	}
	return nil
}

// loadFrozenChatSystemPrompt returns the session-frozen outbound system prompt
// anchored by the first prepare run, or "" when the session has not anchored
// one yet.
func loadFrozenChatSystemPrompt(runtimeSession *runtimechat.Session, session *ChatSession) string {
	if ctx := frozenChatSystemPromptContext(runtimeSession, session); ctx != nil {
		return strings.TrimSpace(sessionmeta.String(ctx, sessionmeta.SystemPromptFrozen))
	}
	return ""
}

// storeFrozenChatSystemPrompt anchors the composed outbound system prompt for
// the life of the session so later prepare runs reuse the identical head and
// never invalidate the provider prompt cache mid-session.
func storeFrozenChatSystemPrompt(runtimeSession *runtimechat.Session, session *ChatSession, composed string) {
	if ctx := frozenChatSystemPromptContext(runtimeSession, session); ctx != nil {
		sessionmeta.Set(ctx, sessionmeta.SystemPromptFrozen, strings.TrimSpace(composed))
	}
}

// applyChatPlanModeToAgent configures the permission engine for active plan mode.
func applyChatPlanModeToAgent(apiAgent *agent.Agent, session *ChatSession, runtimeSession *runtimechat.Session) {
	if apiAgent == nil {
		return
	}
	engine := apiAgent.GetPermissionEngine()
	if engine == nil {
		engine = agent.NewPermissionEngine()
		apiAgent.SetPermissionEngine(engine)
	}
	runtimepolicy.EnsurePlanWriteAllowPaths(engine)

	state := planmode.State{Status: planmode.StatusInactive}
	if runtimeSession != nil {
		state = planmode.Load(runtimeSession)
	} else if session != nil && session.RuntimeSession != nil {
		state = planmode.Load(session.RuntimeSession)
	}
	if planmode.IsActive(state) {
		planmode.ApplyToEngine(engine, state)
		return
	}

	// Reconcile the live engine on every run, including the transition out of
	// plan mode.  Previously this function only forced ModePlan on entry and
	// left the engine untouched on exit, so the durable/session state could say
	// default or accept_edits while the next model turn was still evaluated as
	// plan.
	modeText := planmode.EffectivePermissionMode(state)
	if state.Status == planmode.StatusExited {
		modeText = planmode.ResumeModeAfterExit(state)
	}
	if session != nil {
		if permissionMode := chatSessionPermissionMode(session); strings.TrimSpace(string(permissionMode)) != "" {
			modeText = string(permissionMode)
		}
	}
	mode, err := parseChatPermissionMode(modeText, false)
	if err != nil {
		mode = runtimepolicy.ModeDefault
	}
	engine.Mode = mode
}

func resolveLocalChatWorkspaceMode(runtimeConfig *runtimecfg.RuntimeConfig) string {
	if runtimeConfig == nil {
		return ""
	}
	if mode := strings.TrimSpace(runtimeConfig.Context.WorkspaceMode); mode != "" {
		return strings.ToLower(mode)
	}
	if mode := strings.TrimSpace(runtimeConfig.Workspace.Mode); mode != "" {
		return strings.ToLower(mode)
	}
	if runtimeConfig.Workspace.Enabled {
		return contextmgr.WorkspaceModeSignals
	}
	return ""
}

func composeLocalChatSystemPrompt(session *ChatSession, workspaceRoot string) string {
	promptCWD := strings.TrimSpace(workspaceRoot)
	if promptCWD == "" {
		promptCWD, _ = os.Getwd()
	}
	base := strings.TrimSpace(composeChatSystemPromptWithGuidanceForCWD(session, promptCWD))

	lines := []string{}
	if base != "" {
		lines = append(lines, base)
	}
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot != "" {
		lines = append(lines,
			fmt.Sprintf("Current workspace root: %s", workspaceRoot),
			"Interpret \"当前目录\", \".\", and relative paths as relative to the current workspace root unless the user explicitly says otherwise.",
			"If the user asks to inspect or search the current workspace, do that directly instead of asking which current directory they mean.",
			"When planning file or directory work, only use paths that you directly confirmed from tool output in the current workspace. Do not invent sibling directories or extrapolate missing paths from naming patterns.",
			"Team collaboration tools such as read_task_spec, read_task_context, send_team_message, read_mailbox_digest, report_task_outcome, and block_current_task require an active team run. Ordinary spawn_agent children only support completion_requirement=none; use spawn_team or a Team assignment when a structured complete_task outcome is required.",
			"When calling team tools, leave teammate session_id unset unless you truly need a fixed explicit session. Never use session_id=\"current\" for teammates.",
			"For simple single-command checks such as `git status`, inspect them directly in the parent session; do not spawn a child agent unless the user explicitly asks for subagents or the task benefits from parallel delegation.",
			"When the user explicitly requests a trusted bounded child agent task that must run local tools, pass spawn_agent permission_mode=\"bypass_permissions\" only if the task is safe and scoped; otherwise keep the default approval behavior and expect the child may wait for approval.",
			"If a spawn_agent child reaches waiting_approval, inspect pending_approval_id or the approval_requested event and call resolve_agent_approval with allow=true or allow=false; do not repeatedly wait, poll, rerun the same tool in the parent, or start a fallback agent for that approval.",
			"When calling spawn_team from the current chat, do not set lead_session_id unless the user explicitly asked for a different lead session. The current session will be used automatically.",
			"When you call spawn_team with auto_start=true, treat the delegated work as already in progress. Do not ask the user to choose the next step while the team is running; instead briefly state that the team is working in the background and that you will summarize when it finishes.",
			"After spawn_team auto_start=true, call wait_team with the returned team_id to wait for durable team.completed/team.summary. Do not use wait_agent or read_agent_events for spawn_team teammate ids such as member-1; those tools are only for spawn_agent child sessions.",
		)
	}
	return strings.Join(lines, "\n\n")
}

func buildLocalChatToolPolicy(session *ChatSession, toolSurface runtimeskill.MCPManager, broker *toolbroker.Broker) *runtimepolicy.ToolExecutionPolicy {
	if session == nil {
		return nil
	}
	policy := session.ToolPolicy.Clone()
	if policy == nil {
		switch {
		case session.DisableTools:
			policy = runtimepolicy.NewToolExecutionPolicy([]string{}, false)
		case toolSurface != nil || broker != nil:
			var allowedTools []string
			if toolSurface != nil {
				allowedTools = runtimeToolNames(toolSurface.ListTools())
			}
			allowedTools = append(allowedTools, brokerToolNames(broker.Definitions())...)
			// The scheduler contributes a runtime-owned tool outside the MCP and
			// broker catalogs, so include it in the synthesized default policy.
			// Explicit profile/permissions allowlists are intentionally not widened.
			allowedTools = append(allowedTools, agent.SpawnSubagentsToolName)
			if allowedTools == nil {
				allowedTools = []string{}
			}
			policy = runtimepolicy.NewToolExecutionPolicy(allowedTools, false)
			// When policy was synthesized from tool surface (no profile policy),
			// still apply product allow/deny hard gates from the overlay.
			policy = runtimepolicy.ApplyPermissionsOverlayToPolicy(policy, session.PermissionsOverlay)
		}
	}
	if session.DisableTools {
		if policy == nil {
			policy = runtimepolicy.NewToolExecutionPolicy([]string{}, false)
		}
		policy.AllowlistEnabled = true
		policy.AllowedTools = map[string]bool{}
	}
	return policy
}

func buildLocalChatLoopConfig(runtimeConfig *runtimecfg.RuntimeConfig, session *ChatSession, requestedReasoningEffort ...string) *agent.LoopReActConfig {
	config := &agent.LoopReActConfig{
		MaxSteps:             0,
		EnableThought:        true,
		EnableToolCalls:      true,
		EnableParallelTools:  true,
		MaxParallelToolCalls: 4,
		Temperature:          0.7,
	}
	if runtimeConfig != nil {
		config.MaxSteps = agent.NormalizeMaxSteps(runtimeConfig.Agent.MaxMaxSteps)
		config.MaxToolCalls = runtimeConfig.Agent.MaxToolCalls
		config.MaxRunDuration = runtimeConfig.Agent.Timeout
		config.MaxExplorationSteps = runtimeConfig.Agent.MaxExplorationSteps
		config.MaxRepeatedToolCalls = runtimeConfig.Agent.MaxRepeatedToolCalls
		config.EnableParallelTools = runtimeConfig.Agent.EnableParallelTools
		if runtimeConfig.Agent.MaxParallelToolCalls > 0 {
			config.MaxParallelToolCalls = runtimeConfig.Agent.MaxParallelToolCalls
		}
	}
	if len(requestedReasoningEffort) > 0 {
		if reasoningEffort := runtimetypes.NormalizeReasoningEffort(requestedReasoningEffort[0]); reasoningEffort != "" {
			config.ReasoningEffort = reasoningEffort
			return config
		}
	}
	if session != nil {
		if reasoningEffort := runtimetypes.NormalizeReasoningEffort(session.ReasoningEffort); reasoningEffort != "" {
			config.ReasoningEffort = reasoningEffort
		}
	}
	return config
}

// applyLocalChatCompletionRequirement sets LoopReActConfig.CompletionRequirement from,
// in order: explicit child session context, ProfileAgent agentdef, agent_type agentdef.
// Empty remains none; RunMeta still overrides at cloneLoopConfigForRun time.
func applyLocalChatCompletionRequirement(config *agent.LoopReActConfig, session *ChatSession, agentType, explicitRequirement, workspaceRoot string) {
	if config == nil {
		return
	}
	requirement := strings.TrimSpace(explicitRequirement)
	if requirement == "" && session != nil {
		if profileAgent := strings.TrimSpace(session.ProfileAgent); profileAgent != "" {
			requirement = resolveLocalAgentdefCompletionRequirement(profileAgent, session.ProfileRoot, workspaceRoot)
		}
	}
	if requirement == "" {
		if agentType = strings.TrimSpace(agentType); agentType != "" {
			profileRoot := ""
			if session != nil {
				profileRoot = session.ProfileRoot
			}
			requirement = resolveLocalAgentdefCompletionRequirement(agentType, profileRoot, workspaceRoot)
		}
	}
	config.CompletionRequirement = agent.NormalizeCompletionRequirement(requirement)
}

func resolveLocalAgentdefCompletionRequirement(agentName, profileRoot, projectRoot string) string {
	agentName = strings.TrimSpace(agentName)
	if agentName == "" {
		return ""
	}
	def, err := agentdef.Resolve(agentName, agentdefDiscoverOptions(
		strings.TrimSpace(projectRoot),
		strings.TrimSpace(profileRoot),
		nil,
	))
	if err != nil || def == nil {
		return ""
	}
	return string(def.CompletionRequirement)
}

func localChatSubagentRoutingConfig(session *ChatSession) *config.AICLISubagentRoutingConfig {
	if session == nil || session.Config == nil || session.Config.AICLI == nil || session.Config.AICLI.Subagents == nil {
		return nil
	}
	return session.Config.AICLI.Subagents.Routing
}

func localChatTeamRoutingConfig(session *ChatSession) *config.AICLISubagentRoutingConfig {
	if session == nil {
		return nil
	}
	return config.EffectiveTeamRoutingConfig(session.Config)
}

func applyLocalChatContextOptions(agentConfig *agent.Config, runtimeConfig *runtimecfg.RuntimeConfig) {
	if agentConfig == nil || runtimeConfig == nil {
		return
	}
	ctxCfg := runtimeConfig.Context
	wsCfg := runtimeConfig.Workspace
	hasContextOptions := strings.TrimSpace(ctxCfg.Profile) != "" ||
		strings.TrimSpace(ctxCfg.CompactionMode) != "" ||
		strings.TrimSpace(ctxCfg.RecallMode) != "" ||
		strings.TrimSpace(ctxCfg.ObservationMode) != "" ||
		strings.TrimSpace(ctxCfg.WorkspaceMode) != "" ||
		ctxCfg.MinCompactionMessages > 0 ||
		ctxCfg.MinRecallQueryLength > 0 ||
		ctxCfg.LedgerLoadLimit > 0 ||
		ctxCfg.MaxPromptTokens > 0 ||
		ctxCfg.FallbackMaxPromptTokens > 0 ||
		ctxCfg.MaxMessages > 0 ||
		ctxCfg.KeepRecentMessages > 0 ||
		ctxCfg.MaxRecallResults > 0 ||
		ctxCfg.MaxObservationItems > 0
	hasWorkspaceOptions := wsCfg.MaxFileSize > 0 ||
		strings.TrimSpace(wsCfg.Mode) != "" ||
		wsCfg.MaxChunkSize > 0 ||
		wsCfg.ChunkOverlap > 0 ||
		len(wsCfg.Include) > 0 ||
		len(wsCfg.Exclude) > 0
	if !hasContextOptions && !hasWorkspaceOptions {
		return
	}
	if agentConfig.Options == nil {
		agentConfig.Options = make(map[string]interface{})
	}
	if strings.TrimSpace(ctxCfg.Profile) != "" {
		agentConfig.Options["context_profile"] = strings.TrimSpace(ctxCfg.Profile)
	}
	if strings.TrimSpace(ctxCfg.CompactionMode) != "" {
		agentConfig.Options["context_compaction_mode"] = strings.TrimSpace(ctxCfg.CompactionMode)
	}
	if strings.TrimSpace(ctxCfg.RecallMode) != "" {
		agentConfig.Options["context_recall_mode"] = strings.TrimSpace(ctxCfg.RecallMode)
	}
	if strings.TrimSpace(ctxCfg.ObservationMode) != "" {
		agentConfig.Options["context_observation_mode"] = strings.TrimSpace(ctxCfg.ObservationMode)
	}
	if strings.TrimSpace(ctxCfg.WorkspaceMode) != "" {
		agentConfig.Options["context_workspace_mode"] = strings.ToLower(strings.TrimSpace(ctxCfg.WorkspaceMode))
	} else if strings.TrimSpace(wsCfg.Mode) != "" {
		agentConfig.Options["context_workspace_mode"] = strings.ToLower(strings.TrimSpace(wsCfg.Mode))
	}
	if ctxCfg.MinCompactionMessages > 0 {
		agentConfig.Options["context_min_compaction_messages"] = ctxCfg.MinCompactionMessages
	}
	if ctxCfg.MinRecallQueryLength > 0 {
		agentConfig.Options["context_min_recall_query_length"] = ctxCfg.MinRecallQueryLength
	}
	if ctxCfg.LedgerLoadLimit > 0 {
		agentConfig.Options["context_ledger_load_limit"] = ctxCfg.LedgerLoadLimit
	}
	if ctxCfg.MaxPromptTokens > 0 {
		agentConfig.Options["context_max_prompt_tokens"] = ctxCfg.MaxPromptTokens
	}
	if ctxCfg.FallbackMaxPromptTokens > 0 {
		agentConfig.Options["context_fallback_max_prompt_tokens"] = ctxCfg.FallbackMaxPromptTokens
	}
	if ctxCfg.MaxMessages > 0 {
		agentConfig.Options["context_max_messages"] = ctxCfg.MaxMessages
	}
	if ctxCfg.KeepRecentMessages > 0 {
		agentConfig.Options["context_keep_recent_messages"] = ctxCfg.KeepRecentMessages
	}
	if ctxCfg.MaxRecallResults > 0 {
		agentConfig.Options["context_max_recall_results"] = ctxCfg.MaxRecallResults
	}
	if ctxCfg.MaxObservationItems > 0 {
		agentConfig.Options["context_max_observation_items"] = ctxCfg.MaxObservationItems
	}

	if wsCfg.MaxFileSize > 0 {
		agentConfig.Options["workspace_max_file_size"] = wsCfg.MaxFileSize
	}
	if wsCfg.MaxChunkSize > 0 {
		agentConfig.Options["workspace_max_chunk_size"] = wsCfg.MaxChunkSize
	}
	if wsCfg.ChunkOverlap > 0 {
		agentConfig.Options["workspace_chunk_overlap"] = wsCfg.ChunkOverlap
	}
	if len(wsCfg.Include) > 0 {
		agentConfig.Options["workspace_include"] = append([]string(nil), wsCfg.Include...)
	}
	if len(wsCfg.Exclude) > 0 {
		agentConfig.Options["workspace_exclude"] = append([]string(nil), wsCfg.Exclude...)
	}
	if path := strings.TrimSpace(runtimeConfig.Artifact.StorePath); path != "" {
		agentConfig.Options["artifact_store_path"] = path
	}
	if dsn := strings.TrimSpace(runtimeConfig.Artifact.StoreDSN); dsn != "" {
		agentConfig.Options["artifact_store_dsn"] = dsn
	}
}

func loadLocalChatRuntimeConfig(cfg *config.Config, session *ChatSession) (*runtimecfg.RuntimeConfig, error) {
	configPath := resolveChatRuntimeConfigPath(cfg, session)
	if strings.TrimSpace(configPath) == "" {
		config := runtimecfg.DefaultRuntimeConfig()
		applyLocalChatRuntimePersistenceDefaults(config, session, "")
		return config, nil
	}
	config, loadedPath, err := loadCachedRuntimeConfig(configPath)
	if err != nil || config == nil {
		reason := formatRuntimeConfigLoadFallback(configPath, err)
		fmt.Fprintf(os.Stderr, "Warning: 加载 actor runtime 配置失败，已退回默认配置: %s\n", reason)
		config := runtimecfg.DefaultRuntimeConfig()
		applyLocalChatRuntimePersistenceDefaults(config, session, configPath)
		return config, nil
	}
	if session != nil && session.Model != "" {
		config.Agent.DefaultModel = session.Model
	}
	if strings.TrimSpace(loadedPath) == "" {
		loadedPath = configPath
	}
	applyLocalChatRuntimePersistenceDefaults(config, session, loadedPath)
	return config, nil
}

func applyLocalChatRuntimePersistenceDefaults(config *runtimecfg.RuntimeConfig, session *ChatSession, configPath string) {
	if config == nil || session == nil {
		return
	}
	if session.Ephemeral {
		config.Sessions.Dir = ""
		config.SessionRuntime.StorePath = ""
		config.SessionRuntime.StoreDSN = ""
		config.SessionRuntime.DefaultPersistence = sessionruntime.PersistenceMemory
		config.Team.StorePath = ""
		config.Team.StoreDSN = ""
		config.AgentControl.StorePath = ""
		config.AgentControl.StoreDSN = ""
		config.AgentControl.MailboxStorePath = ""
		config.AgentControl.MailboxStoreDSN = ""
		config.AgentControl.AgentStorePath = ""
		config.AgentControl.AgentStoreDSN = ""
		config.Artifact.StorePath = ""
		config.Artifact.StoreDSN = ""
		config.Background.StorePath = ""
		config.Background.StoreDSN = ""
		config.Background.LogDir = ""
		return
	}
	sessionruntime.ApplyDefaults(config, sessionruntime.ResolveOptions{
		Config:     config,
		ConfigFile: configPath,
		SessionDir: session.SessionDir,
		Mode:       sessionruntime.ModeCLILocal,
	})
}

func ensureLocalRuntimeProvider(runtime *runtimellm.LLMRuntime, session *ChatSession) error {
	if runtime == nil || session == nil {
		return nil
	}
	providerName := strings.TrimSpace(session.ProviderName)
	if providerName == "" {
		return nil
	}
	retryTuning := runtimellm.RetryTuningFromAgentConfig(session.Config)
	retryRules := runtimellm.RetryRulesFromAgentConfig(session.Config)
	maxRetries := runtimellm.ProviderMaxRetriesFromAgentConfig(session.Config)
	if _, err := runtime.GetProvider(providerName); err != nil {
		provider, buildErr := runtimellm.NewProvider(&runtimellm.ProviderConfig{
			Type:                  session.Provider.GetType(),
			APIKey:                session.Provider.GetAPIKey(),
			BaseURL:               session.Provider.BaseURL,
			APIPath:               session.Provider.APIPath,
			CompatibilityProfile:  session.Provider.Compatibility.Profile,
			Timeout:               session.Provider.Timeout,
			MaxRetries:            maxRetries,
			RetryTuning:           retryTuning,
			RetryRules:            retryRules,
			DefaultModel:          session.Provider.DefaultModel,
			SupportedModels:       append([]string(nil), session.Provider.SupportedModels...),
			ModelMappings:         cloneStringMap(session.Provider.ModelMappings),
			ModelCapabilities:     cloneProviderModelCapabilities(session.Provider.ModelCapabilities),
			EnableImageGeneration: session.Provider.EnableImageGeneration,
			Headers:               effectiveChatProviderHeaders(session),
			HeaderMappings:        cloneStringMap(session.Provider.HeaderMappings),
			Proxy:                 session.Provider.Proxy.Clone(),
			RequestsPerMinute:     session.Provider.RequestsPerMinute,
			StreamReadTimeout:     runtimellm.ProviderStreamReadTimeoutFromAgentConfig(session.Config),
			ResponseHeaderTimeout: runtimellm.ProviderResponseHeaderTimeoutFromAgentConfig(session.Config),
		})
		if buildErr != nil {
			return buildErr
		}
		if registerErr := runtime.RegisterProvider(providerName, provider); registerErr != nil {
			return registerErr
		}
	}
	aliases := []string{session.Model, session.Provider.DefaultModel}
	aliases = append(aliases, session.Provider.SupportedModels...)
	for _, alias := range aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			continue
		}
		_ = runtime.RegisterProviderAlias(alias, providerName)
	}
	return nil
}

func buildLocalChatRuntimeStores(session *ChatSession, runtimeConfig *runtimecfg.RuntimeConfig) (runtimechat.RuntimeStateStore, runtimechat.EventStore) {
	storePath := resolveLocalChatRuntimeStorePath(session, runtimeConfig)
	if storePath != "" {
		store, err := runtimechat.NewSQLiteRuntimeStore(&runtimechat.RuntimeStoreConfig{Path: storePath})
		if err == nil {
			return store, store
		}
		fmt.Fprintf(os.Stderr, "Warning: 初始化 actor runtime store 失败，已退回内存模式: %v\n", err)
	}
	memoryStore := runtimechat.NewInMemoryRuntimeStore(2048)
	return memoryStore, memoryStore
}

func buildLocalChatBackgroundManager(runtimeConfig *runtimecfg.RuntimeConfig) *background.Manager {
	if runtimeConfig == nil {
		return nil
	}
	cfg := runtimeConfig.Background
	if strings.TrimSpace(cfg.StorePath) == "" && strings.TrimSpace(cfg.StoreDSN) == "" && strings.TrimSpace(cfg.LogDir) == "" {
		return nil
	}
	return background.NewManager(background.Config{
		MaxOutputBytes:          cfg.MaxOutputBytes,
		DefaultTimeout:          cfg.DefaultTimeout,
		MonitorInterval:         cfg.MonitorInterval,
		HeartbeatTimeout:        cfg.HeartbeatTimeout,
		LaunchMaxAttempts:       cfg.LaunchMaxAttempts,
		RetryBackoff:            cfg.RetryBackoff,
		RecoveryMaxAttempts:     cfg.RecoveryMaxAttempts,
		RecoveryBackoffSchedule: append([]time.Duration(nil), cfg.RecoveryBackoffSchedule...),
		StorePath:               strings.TrimSpace(cfg.StorePath),
		StoreDSN:                strings.TrimSpace(cfg.StoreDSN),
		LogDir:                  strings.TrimSpace(cfg.LogDir),
		MaxConcurrentJobs:       cfg.MaxConcurrentJobs,
	})
}

func buildLocalChatAgentControlRegistryService(runtimeConfig *runtimecfg.RuntimeConfig) *agentcontrol.RegistryService {
	if runtimeConfig == nil {
		return nil
	}
	cfg := agentcontrol.RegistryServiceConfig{
		StorePath:        strings.TrimSpace(runtimeConfig.AgentControl.StorePath),
		StoreDSN:         strings.TrimSpace(runtimeConfig.AgentControl.StoreDSN),
		MailboxStorePath: strings.TrimSpace(runtimeConfig.AgentControl.MailboxStorePath),
		MailboxStoreDSN:  strings.TrimSpace(runtimeConfig.AgentControl.MailboxStoreDSN),
		AgentStorePath:   strings.TrimSpace(runtimeConfig.AgentControl.AgentStorePath),
		AgentStoreDSN:    strings.TrimSpace(runtimeConfig.AgentControl.AgentStoreDSN),
	}
	if cfg.Empty() {
		return nil
	}
	service, err := agentcontrol.NewRegistryService(context.Background(), cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: 初始化 AgentControl registry service 失败，已跳过 global registry write-through: %v\n", err)
		return nil
	}
	return service
}

func buildLocalChatGlobalMailboxStore(runtimeConfig *runtimecfg.RuntimeConfig) agentcontrol.GlobalMailboxRegistryStore {
	if runtimeConfig == nil {
		return nil
	}
	path := firstNonEmptyChatValue(runtimeConfig.AgentControl.MailboxStorePath, runtimeConfig.AgentControl.StorePath)
	dsn := firstNonEmptyChatValue(runtimeConfig.AgentControl.MailboxStoreDSN, runtimeConfig.AgentControl.StoreDSN)
	if path == "" && dsn == "" {
		return nil
	}
	store, err := agentcontrol.NewSQLiteGlobalMailboxRegistryStore(&agentcontrol.GlobalMailboxStoreConfig{
		Path: path,
		DSN:  dsn,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: 初始化 AgentControl global mailbox store 失败，已跳过 global write-through: %v\n", err)
		return nil
	}
	return store
}

func buildLocalChatGlobalAgentStore(runtimeConfig *runtimecfg.RuntimeConfig) agentcontrol.AgentRegistryStore {
	if runtimeConfig == nil {
		return nil
	}
	path := firstNonEmptyChatValue(runtimeConfig.AgentControl.AgentStorePath, runtimeConfig.AgentControl.StorePath)
	dsn := firstNonEmptyChatValue(runtimeConfig.AgentControl.AgentStoreDSN, runtimeConfig.AgentControl.StoreDSN)
	if path == "" && dsn == "" {
		return nil
	}
	store, err := agentcontrol.NewSQLiteGlobalAgentRegistryStore(&agentcontrol.GlobalAgentStoreConfig{
		Path: path,
		DSN:  dsn,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: 初始化 AgentControl global agent registry store 失败，已跳过 agent registry write-through: %v\n", err)
		return nil
	}
	return store
}

func configureLocalChatMailboxWriteThrough(writer agentcontrol.GlobalMailboxWriter, stores ...interface{}) {
	for _, store := range stores {
		if setter, ok := store.(interface {
			SetGlobalMailboxWriter(agentcontrol.GlobalMailboxWriter)
		}); ok && setter != nil {
			setter.SetGlobalMailboxWriter(writer)
		}
	}
	if writer != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, _ = agentcontrol.ReconcileMailboxProjections(ctx, agentcontrol.MailboxRecordFilter{}, stores...)
		}()
	}
}

func closeLocalRuntimeStores(store runtimechat.RuntimeStateStore, eventStore runtimechat.EventStore) {
	seen := map[interface{}]struct{}{}
	closeStore := func(value interface{}) {
		if value == nil {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		if closer, ok := value.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}
	closeStore(store)
	closeStore(eventStore)
}

func resolveLocalChatRuntimeStorePath(session *ChatSession, runtimeConfig *runtimecfg.RuntimeConfig) string {
	if session != nil && session.Ephemeral {
		return ""
	}
	if runtimeConfig != nil && strings.TrimSpace(runtimeConfig.SessionRuntime.StorePath) != "" {
		return strings.TrimSpace(runtimeConfig.SessionRuntime.StorePath)
	}
	if session == nil || strings.TrimSpace(session.SessionDir) == "" {
		return ""
	}
	paths := sessionruntime.ResolvePaths(sessionruntime.ResolveOptions{
		Config:     runtimeConfig,
		SessionDir: session.SessionDir,
		Mode:       sessionruntime.ModeCLILocal,
	})
	return paths.SessionRuntimeStorePath
}

func resolveLocalChatSubagentBatchStorePath(session *ChatSession, runtimeConfig *runtimecfg.RuntimeConfig) string {
	if session != nil && session.Ephemeral {
		return ""
	}
	// 按会话分文件：subagent batch 数据是会话局部的（BatchFilter 按
	// ParentSessionID 过滤），多 aicli 实例同时跑不同会话时互不争锁；
	// 同一会话的恢复/续跑仍命中同一文件。
	if session != nil && strings.TrimSpace(currentRuntimeSessionID(session)) != "" {
		return filepath.Join(session.SessionDir, "runtime", "subagent_batches",
			sanitizeLocalFileName(currentRuntimeSessionID(session))+".sqlite")
	}
	if session != nil && strings.TrimSpace(session.SessionDir) != "" {
		return filepath.Join(session.SessionDir, "runtime", "subagent_batches", "default.sqlite")
	}
	if runtimeConfig != nil && strings.TrimSpace(runtimeConfig.SessionRuntime.StorePath) != "" {
		return filepath.Join(filepath.Dir(strings.TrimSpace(runtimeConfig.SessionRuntime.StorePath)), "subagent_batches", "default.sqlite")
	}
	return ""
}

// sanitizeLocalFileName 把会话 ID 规整为安全的文件名字段
// （保留字母数字与 - _ ，其余字符替换为 _ ）。
func sanitizeLocalFileName(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "default"
	}
	return b.String()
}

// resolveLocalChatSupervisionDataDir returns a per-session durable directory
// for the P2 control plane. Keeping it beside the actor/team stores gives
// restart recovery the same lifecycle as a local chat runtime.
func resolveLocalChatSupervisionDataDir(session *ChatSession, runtimeConfig *runtimecfg.RuntimeConfig) string {
	if session != nil && !session.Ephemeral && strings.TrimSpace(session.SessionDir) != "" {
		return filepath.Join(session.SessionDir, "runtime", "supervision")
	}
	if runtimeConfig != nil && strings.TrimSpace(runtimeConfig.SessionRuntime.StorePath) != "" {
		return filepath.Join(filepath.Dir(strings.TrimSpace(runtimeConfig.SessionRuntime.StorePath)), "supervision")
	}
	return filepath.Join(os.TempDir(), "ai-agent-runtime", "supervision")
}

// injectLocalSupervisionPreflight is the CLI-equivalent parent/lead turn hook.
// It deliberately marks a visible digest delivered+seen, never acknowledged.
// Child worker turns are excluded: only the registered Team lead consumes a
// Team's inbox; ordinary sessions consume their own root-session inbox.
func injectLocalSupervisionPreflight(ctx context.Context, host *localChatRuntimeHost, sessionID, prompt string, runMeta *team.RunMeta) (string, error) {
	if host == nil || host.Supervision == nil || host.Supervision.Store == nil {
		return prompt, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return prompt, nil
	}
	rootScopeID := sessionID
	targetTeamID := ""
	if runMeta != nil && runMeta.Team != nil && strings.TrimSpace(runMeta.Team.TeamID) != "" && host.TeamStore != nil {
		candidateTeamID := strings.TrimSpace(runMeta.Team.TeamID)
		if record, err := host.TeamStore.GetTeam(ctx, candidateTeamID); err == nil && record != nil && strings.TrimSpace(record.LeadSessionID) == sessionID {
			rootScopeID = candidateTeamID
			targetTeamID = candidateTeamID
		}
	}
	digest, err := supervision.BuildDigest(ctx, host.Supervision.Store, supervision.DigestRequest{
		RootScopeID:           rootScopeID,
		TargetParentSessionID: sessionID,
		TargetParentTeamID:    targetTeamID,
		Limit:                 host.supervisionConfig.WithDefaults().DigestMaxItems,
		IncludeResolvedSince:  true,
	})
	if err != nil {
		return "", fmt.Errorf("build supervision preflight digest: %w", err)
	}
	if digest == nil || len(digest.Items) == 0 {
		return prompt, nil
	}
	now := time.Now().UTC()
	for _, item := range digest.Items {
		if item.NotificationID == "" {
			continue
		}
		if err := host.Supervision.Store.MarkNotificationDelivered(ctx, item.NotificationID, now); err != nil {
			return "", fmt.Errorf("mark supervision notification delivered: %w", err)
		}
		if err := host.Supervision.Store.MarkNotificationSeen(ctx, item.NotificationID, now); err != nil {
			return "", fmt.Errorf("mark supervision notification seen: %w", err)
		}
	}
	return strings.TrimSpace(digest.Text) + "\n\n" + prompt, nil
}

func resolveLocalChatTeamStorePath(session *ChatSession) string {
	if session == nil || strings.TrimSpace(session.SessionDir) == "" {
		return ""
	}
	return filepath.Join(session.SessionDir, "runtime", "team_store.sqlite")
}

func resolveLocalChatAgentControlStorePath(session *ChatSession) string {
	if session == nil || strings.TrimSpace(session.SessionDir) == "" {
		return ""
	}
	return filepath.Join(session.SessionDir, "runtime", "agent_control.sqlite")
}

func resolveLocalChatGlobalMailboxStorePath(session *ChatSession) string {
	if session == nil || strings.TrimSpace(session.SessionDir) == "" {
		return ""
	}
	return filepath.Join(session.SessionDir, "runtime", "agent_control_mailbox.sqlite")
}

func resolveLocalChatGlobalAgentStorePath(session *ChatSession) string {
	if session == nil || strings.TrimSpace(session.SessionDir) == "" {
		return ""
	}
	return filepath.Join(session.SessionDir, "runtime", "agent_control_agents.sqlite")
}

func resolveLocalWorkspacePath(runtimeConfig *runtimecfg.RuntimeConfig, session *ChatSession) string {
	if runtimeConfig != nil && strings.TrimSpace(runtimeConfig.Workspace.Root) != "" {
		root := strings.TrimSpace(runtimeConfig.Workspace.Root)
		if filepath.IsAbs(root) {
			return root
		}
		if session != nil && strings.TrimSpace(session.ProfileRoot) != "" {
			return filepath.Clean(filepath.Join(strings.TrimSpace(session.ProfileRoot), root))
		}
		if cwd, err := os.Getwd(); err == nil {
			return filepath.Clean(filepath.Join(cwd, root))
		}
		return root
	}
	if session != nil && strings.TrimSpace(session.ProfileRoot) != "" {
		return strings.TrimSpace(session.ProfileRoot)
	}
	if cwd, err := os.Getwd(); err == nil {
		if gitRoot := findGitRoot(cwd); gitRoot != "" {
			return gitRoot
		}
		return cwd
	}
	return ""
}

// findGitRoot walks upward from start looking for a .git directory or file
// (worktrees use a file). Returns the first ancestor containing .git, or "".
func findGitRoot(start string) string {
	dir := filepath.Clean(start)
	for {
		gitPath := filepath.Join(dir, ".git")
		if _, err := os.Stat(gitPath); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func resolveLocalChatAgentProvider(session *ChatSession, host *localChatRuntimeHost) string {
	if session != nil && strings.TrimSpace(session.ProviderName) != "" {
		return strings.TrimSpace(session.ProviderName)
	}
	if host != nil && host.Bootstrap != nil && host.Bootstrap.Config() != nil && strings.TrimSpace(host.Bootstrap.Config().Agent.DefaultProvider) != "" {
		return strings.TrimSpace(host.Bootstrap.Config().Agent.DefaultProvider)
	}
	if host != nil && host.Bootstrap != nil && host.Bootstrap.LLMRuntime() != nil {
		return strings.TrimSpace(host.Bootstrap.LLMRuntime().DefaultProvider())
	}
	return ""
}

func resolveLocalChatAgentModel(session *ChatSession, host *localChatRuntimeHost) string {
	if session != nil && strings.TrimSpace(session.Model) != "" {
		return strings.TrimSpace(session.Model)
	}
	if host != nil && host.Bootstrap != nil && host.Bootstrap.Config() != nil && strings.TrimSpace(host.Bootstrap.Config().Agent.DefaultModel) != "" {
		return strings.TrimSpace(host.Bootstrap.Config().Agent.DefaultModel)
	}
	if host != nil && host.Bootstrap != nil && host.Bootstrap.LLMRuntime() != nil {
		return strings.TrimSpace(host.Bootstrap.LLMRuntime().DefaultModel())
	}
	return ""
}

func runtimeToolNames(tools []runtimeskill.ToolInfo) []string {
	if len(tools) == 0 {
		return nil
	}
	names := make([]string, 0, len(tools))
	seen := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}

func brokerToolNames(definitions []runtimetypes.ToolDefinition) []string {
	if len(definitions) == 0 {
		return nil
	}
	names := make([]string, 0, len(definitions))
	seen := make(map[string]struct{}, len(definitions))
	for _, def := range definitions {
		name := strings.TrimSpace(def.Name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}

func (h *localChatRuntimeHost) syncTeamLifecycleLoops() {
	if lifecycle := h.teamLifecycleService(); lifecycle != nil {
		lifecycle.SyncLoops()
	}
}

func (h *localChatRuntimeHost) bindTeamLifecycleEvents() {
	if h == nil || h.Orchestrator == nil || h.EventBus == nil {
		return
	}
	events := h.Orchestrator.Events
	if events == nil {
		events = team.NewTeamEventBus()
		h.Orchestrator.Events = events
	}
	events.Subscribe("", h.publishTeamLifecycleEvent)
}

func (h *localChatRuntimeHost) publishTeamLifecycleEvent(event team.TeamEvent) {
	h.dispatchTeamLifecycleEvent(event, true)
}

func (h *localChatRuntimeHost) dispatchTeamLifecycleEvent(event team.TeamEvent, persist bool) {
	if h == nil || strings.TrimSpace(event.Type) == "" {
		return
	}
	payload := make(map[string]interface{}, len(event.Payload)+1)
	for key, value := range event.Payload {
		payload[key] = value
	}
	if strings.TrimSpace(event.TeamID) != "" {
		payload["team_id"] = strings.TrimSpace(event.TeamID)
	}
	baseSessionID := h.baseRuntimeSessionID()
	sessionID := h.teamLifecycleEventSessionID(context.Background(), strings.TrimSpace(event.TeamID), baseSessionID)
	runtimeEvent := runtimeevents.Event{
		Type:      strings.TrimSpace(event.Type),
		AgentName: "team-orchestrator",
		SessionID: sessionID,
		Payload:   payload,
		Timestamp: event.Timestamp,
	}
	if persist && h.EventStore != nil {
		if seq, err := h.EventStore.AppendEvent(context.Background(), runtimeEvent); err == nil {
			if runtimeEvent.Payload == nil {
				runtimeEvent.Payload = map[string]interface{}{}
			}
			runtimeEvent.Payload["seq"] = seq
		}
	}
	if persist {
		h.deliverTeamLifecycleMailbox(context.Background(), sessionID, event)
		h.deliverTeamTaskLifecycleMailbox(context.Background(), event)
	}
	isBaseLifecycleSession := h.isLifecycleEventForBaseSession(sessionID)
	if isBaseLifecycleSession {
		if lifecycle := h.teamLifecycleService(); lifecycle != nil {
			lifecycle.Apply(runtimeEvent)
		}
	}
	if isBaseLifecycleSession || strings.EqualFold(strings.TrimSpace(event.Type), team.TaskRouteResolvedEvent) {
		if h.EventBus != nil {
			h.EventBus.Publish(runtimeEvent)
		}
	}
	if h.BaseSession != nil && isBaseLifecycleSession {
		warnIfChatSessionSyncFails(h.BaseSession, "team lifecycle sync", syncAmbientTeamLifecycleState(h.BaseSession))
	}
}

func (h *localChatRuntimeHost) deliverTeamLifecycleMailbox(ctx context.Context, sessionID string, event team.TeamEvent) {
	if h == nil || h.EventStore == nil {
		return
	}
	switch strings.TrimSpace(event.Type) {
	case "team.completed", "team.summary":
	default:
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	_ = runtimechat.DeliverMailboxEventFirst(ctx, h.EventStore, nil, nil, sessionID, team.BuildTeamLifecycleMailboxMessage(event))
}

func (h *localChatRuntimeHost) deliverTeamTaskLifecycleMailbox(ctx context.Context, event team.TeamEvent) {
	if h == nil || h.EventStore == nil || h.TeamStore == nil {
		return
	}
	switch strings.TrimSpace(event.Type) {
	case "task.completed", "task.failed", "task.cancelled":
	default:
		return
	}
	assignee := payloadStringValue(event.Payload["assignee"])
	if assignee == "" {
		return
	}
	mate, err := h.TeamStore.GetTeammate(ctx, assignee)
	if err != nil || mate == nil || strings.TrimSpace(mate.SessionID) == "" {
		return
	}
	_ = runtimechat.DeliverMailboxEventFirst(ctx, h.EventStore, nil, nil, strings.TrimSpace(mate.SessionID), team.BuildTaskLifecycleMailboxMessage(event))
}

func (h *localChatRuntimeHost) baseRuntimeSessionID() string {
	if h == nil || h.BaseSession == nil || h.BaseSession.RuntimeSession == nil {
		return ""
	}
	return strings.TrimSpace(h.BaseSession.RuntimeSession.ID)
}

func (h *localChatRuntimeHost) teamLifecycleEventSessionID(ctx context.Context, teamID string, fallback string) string {
	teamID = strings.TrimSpace(teamID)
	if h != nil && h.TeamStore != nil && teamID != "" {
		if record, err := h.TeamStore.GetTeam(ctx, teamID); err == nil && record != nil {
			if leadSessionID := strings.TrimSpace(record.LeadSessionID); leadSessionID != "" {
				return leadSessionID
			}
		}
	}
	return strings.TrimSpace(fallback)
}

func (h *localChatRuntimeHost) isLifecycleEventForBaseSession(sessionID string) bool {
	baseSessionID := h.baseRuntimeSessionID()
	sessionID = strings.TrimSpace(sessionID)
	return baseSessionID == "" || sessionID == "" || strings.EqualFold(sessionID, baseSessionID)
}

func (h *localChatRuntimeHost) teamLifecycleService() teamLifecycleService {
	if h == nil {
		return nil
	}
	if h.TeamLifecycle != nil {
		return h.TeamLifecycle
	}
	h.TeamLifecycle = newLocalTeamLifecycleService(h)
	return h.TeamLifecycle
}

func (h *localChatRuntimeHost) mirrorTeamSummaryToBaseSession(teamID, summary string) {
	if h == nil || h.BaseSession == nil || h.BaseSession.SessionManager == nil || h.BaseSession.RuntimeSession == nil {
		return
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return
	}

	sessionID := strings.TrimSpace(h.BaseSession.RuntimeSession.ID)
	if sessionID == "" {
		return
	}
	ctx := context.Background()
	runtimeSession, err := h.BaseSession.SessionManager.Get(ctx, sessionID)
	if err != nil || runtimeSession == nil {
		return
	}
	for _, message := range runtimeSession.History {
		if strings.TrimSpace(message.Role) != "assistant" {
			continue
		}
		if strings.TrimSpace(message.Content) == summary {
			return
		}
	}

	message := runtimetypes.NewAssistantMessage(summary)
	if strings.TrimSpace(teamID) != "" {
		if message.Metadata == nil {
			message.Metadata = runtimetypes.NewMetadata()
		}
		message.Metadata["team_id"] = strings.TrimSpace(teamID)
	}
	if err := h.BaseSession.SessionManager.AddMessage(ctx, sessionID, *message); err != nil {
		return
	}
	updated, err := h.BaseSession.SessionManager.Get(ctx, sessionID)
	if err != nil || updated == nil {
		return
	}
	_ = restoreChatStateFromRuntimeSession(h.BaseSession, updated)
	inferAmbientTeamBinding(h.BaseSession, updated)
}

func (h *localChatRuntimeHost) replayStoredTerminalTeamLifecycleEvents(teamID string) {
	if lifecycle := h.teamLifecycleService(); lifecycle != nil {
		lifecycle.PublishStoredTerminalEvents(teamID)
	}
}

func (h *localChatRuntimeHost) stopTeamLifecycleLoops() {
	if lifecycle := h.teamLifecycleService(); lifecycle != nil {
		lifecycle.StopLoops()
	}
}

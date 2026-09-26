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
	"sync/atomic"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentdef"
	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	runtimebootstrap "github.com/wwsheng009/ai-agent-runtime/internal/bootstrap"
	cacheanalytics "github.com/wwsheng009/ai-agent-runtime/internal/cacheanalytics"
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
	runtimeobserve "github.com/wwsheng009/ai-agent-runtime/internal/runtimeobserve"
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
	"github.com/wwsheng009/ai-agent-runtime/internal/usageanalytics"
	"github.com/wwsheng009/ai-agent-runtime/internal/usageledger"
)

const (
	localChatSessionActorLeaseOwnerKind   = "aicli-actor"
	localSubagentBatchRestartGrace        = 5 * time.Minute
	localSubagentBatchRecoveryPassTimeout = 15 * time.Second
	// runStallTimeoutEnv 显式启用 run 无进展 watchdog（默认关闭）。
	runStallTimeoutEnv = "AICLI_RUN_STALL_TIMEOUT"
	// defaultLocalChatRunStallTimeout 是 run 无进展 watchdog 的默认阈值：
	// 0 = 关闭（默认）。看门狗触发会以 context.Canceled 中止整个 run/turn，
	// 在用户侧表现为"操作错误: context canceled"，属于意外结束会话；自动化
	// 长任务应当自然执行到结束，因此默认不设 run 级超时。
	// 上游挂死由请求级超时负责（provider 的 response_header / stream_read /
	// 单请求 timeout），失败以类型化错误返回并走重试，不会让会话卡在 busy。
	// 需要 run 级兜底时用 AICLI_RUN_STALL_TIMEOUT 显式启用（Go duration，
	// 如 30m；off/0/disable 关闭）。
	defaultLocalChatRunStallTimeout = time.Duration(0)
)

// localChatRunStallTimeoutFromEnv 解析 run 无进展 watchdog 的阈值。
//
// 未设置返回默认值 0 = 关闭：run 级看门狗一旦触发就以 context.Canceled
// 结束整个 turn，长任务/自动化不应被它意外打断，所以只有显式配置才启用。
// AICLI_RUN_STALL_TIMEOUT 接受 Go duration（如 30m、1h）以及
// off/0/disable（关闭）；非法值（例如漏写单位的 "30"）按默认（关闭）处理，
// 避免误开看门狗把长任务打断。
func localChatRunStallTimeoutFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv(runStallTimeoutEnv))
	if raw == "" {
		return defaultLocalChatRunStallTimeout
	}
	switch strings.ToLower(raw) {
	case "off", "0", "false", "no", "disable", "disabled":
		return 0
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return defaultLocalChatRunStallTimeout
	}
	return value
}

// localChatSessionCheckpointIntervalFromEnv 解析长 turn 中途落库间隔的覆盖值。
//
// 未设置（或非法）返回 0，由 chat.DefaultSessionCheckpointInterval 兜底；
// AICLI_SESSION_CHECKPOINT_INTERVAL 接受 Go duration（如 5s、30s），以及
// off/disable/0（显式关闭中途落库 → actor 侧按负值处理，turn 收尾的 post-turn
// sync 仍照常落库，用于排查落库相关问题时回滚）。
func localChatSessionCheckpointIntervalFromEnv() time.Duration {
	raw := strings.TrimSpace(os.Getenv("AICLI_SESSION_CHECKPOINT_INTERVAL"))
	if raw == "" {
		return 0
	}
	switch strings.ToLower(raw) {
	case "off", "0", "false", "no", "disable", "disabled":
		return -1
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		// 非法值（如漏写单位 "15"）回退默认间隔，而不是静默关闭落库。
		return 0
	}
	if value <= 0 {
		return -1
	}
	return value
}

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
	Bootstrap          *runtimebootstrap.Manager
	RuntimeConfig      *runtimecfg.RuntimeConfig
	SessionHub         *runtimechat.SessionHub
	RuntimeStore       runtimechat.RuntimeStateStore
	EventStore         runtimechat.EventStore
	ReceiptStore       runtimechat.ToolReceiptStore
	TeamStore          team.Store
	AgentControl       *agentcontrol.RegistryService
	AgentRegistryStore agentcontrol.AgentRegistryStore
	Background         *background.Manager
	TeamClaims         *team.PathClaimManager
	Orchestrator       *team.Orchestrator
	ToolSurface        runtimeskill.MCPManager
	EventBus           *runtimeevents.Bus
	SessionStore       runtimechat.SessionStorage
	SessionUser        string
	BaseSession        *ChatSession
	TeamLifecycle      teamLifecycleService
	ActorRegistry      *localActorRegistry
	Supervision        *runtimeserver.SupervisionControlPlane
	SubagentBatches    subagentbatch.BatchStore
	supervisionWake    *supervision.WakeConsumer
	supervisionConfig  supervision.Config
	// executionSupervisor / executionSupervisorStop 是 P0-4 的 CLI 本地
	// child-run 看门狗：与 API 侧共用 supervision.ExecutionRunStore，惰性构建，
	// 关闭 host 时随 lifecycleCtx 一起停止。
	executionSupervisorOnce sync.Once
	// executionSupervisorMu 保护 executionSupervisor 指针本身：/debug 需要在
	// 不触发惰性构建（不启动后台巡检）的前提下读取它。
	executionSupervisorMu   sync.Mutex
	executionSupervisor     *supervision.ExecutionSupervisor
	executionSupervisorCtx  context.Context
	executionSupervisorStop context.CancelFunc
	// registryReconciler / registryReconcilerStop 是 P2-9 的周期一致性对账：
	// 低频审计 durable registry 与实际会话的漂移，并按 observe/enforce 决定
	// 是否收敛；同样随 lifecycleCtx 停止（见 chat_actor_reconcile.go）。
	registryReconcilerOnce sync.Once
	registryReconciler     *agentcontrol.Reconciler
	registryReconcilerStop context.CancelFunc
	// progressCheckOnce / progressCheckStop 是 P2-D 的 opt-in 周期巡查
	//（supervision.progress_check_interval，默认 0 关闭）：与其它后台循环
	// 一样随 lifecycleCtx + asyncWG 停止，见 chat_actor_progress_check.go。
	progressCheckOnce sync.Once
	progressCheckStop context.CancelFunc
	cleanupFns        []func()
	closeOnce         sync.Once
	// subagentLimiterMu / subagentLimiter 缓存进程级子代理并发上限
	//（P1-4/H12）：同一 host 构建的每个 scheduler 共用同一 limiter 实例，
	// 使 agents.maxThreads 成为「全部 batch 合计」的上限，而不是每个 batch
	// 各自一份。首次构建后不再随配置热重载重建（见 subagentGlobalLimiter）。
	subagentLimiterMu    sync.Mutex
	subagentLimiter      *agent.SubagentConcurrencyLimiter
	subagentMu           sync.Mutex
	subagentCoordinators map[*agent.SubagentBatchCoordinator]struct{}
	subagentOps          int
	subagentIdle         chan struct{}
	closing              bool
	actorTurnGateMu      sync.Mutex
	actorTurnGates       map[string]chan struct{}
	lifecycleCtx         context.Context
	lifecycleCancel      context.CancelFunc
	asyncWG              sync.WaitGroup

	// observeOnce / observeSvc 缓存本地 Runtime Observation Plane 服务：
	// ensureLocalObserveService 惰性构建一次，host.Close() 时释放。
	observeOnce sync.Once
	observeSvc  *runtimeobserve.Service

	// cacheOnce / cacheSvc 缓存本地 LLM 缓存分析服务（cache.analytics.v1）：
	// initializeLocalChatRuntimeHost 在启动期即构建一次（避免丢失挂载前的
	// llm.request.* 事件），host.Close() 时释放；
	// /web/api/cache/* 与 TUI /usage 命令共用同一 Source。
	cacheOnce sync.Once
	cacheSvc  *cacheanalytics.Service

	// usageMu / usageSvc 缓存本地统一用量分析服务
	// （usage_analytics.sqlite，EventBus 实时写入；/web/api/cache/* 优先读它）：
	// 与 runtime server 同处形态，启动期挂载一次，host.Close() 时释放。
	// 用互斥锁而非 sync.Once：构建失败不缓存，后续调用可重试。
	usageMu  sync.Mutex
	usageSvc *usageanalytics.Service

	// ledgerSvc / ledgerDriver / ledgerDSN / ledgerEnabled 是本地用量账本
	//（token_usage_history）记录器：挂载在 EventBus 的 Service，捕获 agent loop
	// 发出的 llm.request.finished 事件，与 runtime-server 的
	// skillsapi.Handler.appendUsageLedger 形成统一的通用账本能力
	//（docs §04:69 / §1064 / 06 §338）。
	ledgerSvc     *usageledger.Service
	ledgerDriver  string
	ledgerDSN     string
	ledgerEnabled bool

	// P0-1c: CLI 宿主与 API 宿主对等的 live-only 子代理进度镜像。
	// 一个 host 一个实例（Once + 值），既服务子会话 tool.progress 订阅，
	// 也作为 BatchProgressSource.Messages 的富化源；镜像自身不落库、
	// 不起 goroutine，随 host 生命周期收敛。
	subagentProgressMirrorOnce  sync.Once
	subagentProgressMirrorValue *supervision.SubagentProgressMirror
	// childEventUnsubsMu / childEventUnsubs 收敛 per-child 的事件订阅句柄：
	// 子会话结束时立即释放；host.Close() 兜底释放，避免子会话未结束就关
	// host 时在总线上残留订阅（订阅回调本身无 goroutine）。
	childEventUnsubsMu sync.Mutex
	childEventUnsubs   map[string]func()

	// runtimeEventBridgeOnce / runtimeEventBridgeUnsub 缓存本地 A 通道桥
	// （总线 → session_runtime.sqlite）的订阅句柄，host.Close() 时释放。
	// 方案 §0.2 / G2：aicli 本地 chat 走 localChatRuntimeHost，而 A 通道桥
	// 原先只存在于 runtime-server 的 api/skills handler 内，导致 agent loop
	// 发出的 tool.requested/tool.completed 在本地模式从未落库。
	runtimeEventBridgeOnce  sync.Once
	runtimeEventBridgeUnsub func()
	// runtimeEventBuffer 是 P1.5 批量落盘缓冲（仅当
	// sessionRuntime.eventPersist.batchingEnabled=true 且 store 支持批量时非空）。
	runtimeEventBuffer *runtimechat.EventPersistBuffer
}

// acquireActorTurnGate serializes internally-triggered and foreground turns
// for the same local actor. The SessionActor remains the final concurrency
// authority; this context-aware gate prevents an auto-wake from racing the
// foreground bridge's BeginRun/SubmitPrompt boundary.
func (h *localChatRuntimeHost) acquireActorTurnGate(ctx context.Context, sessionID string) (func(), error) {
	if h == nil || strings.TrimSpace(sessionID) == "" {
		return func() {}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	sessionID = strings.TrimSpace(sessionID)
	h.actorTurnGateMu.Lock()
	if h.actorTurnGates == nil {
		h.actorTurnGates = make(map[string]chan struct{})
	}
	gate := h.actorTurnGates[sessionID]
	if gate == nil {
		gate = make(chan struct{}, 1)
		gate <- struct{}{}
		h.actorTurnGates[sessionID] = gate
	}
	h.actorTurnGateMu.Unlock()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-gate:
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			gate <- struct{}{}
		})
	}, nil
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
		// P0-1c 兜底：清理清理函数不会覆盖的 per-child 子会话事件订阅
		// （正常情况下已随 actor 停止时的 session_end 释放，这里保证
		// 子会话未结束就关闭 host 也不残留订阅）。
		h.releaseAllChildEventSubscriptions()
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

// subagentTaskProgressCounts aggregates the M1 task-level progress write-back
// counters over this host's registered batch coordinators. enabled reports
// whether the write-back is explicitly configured (interval > 0), so the debug
// view can distinguish "feature off" from "on but nothing written yet".
// Best-effort read-only projection: it never mutates a coordinator.
func (h *localChatRuntimeHost) subagentTaskProgressCounts() (writes, windowSkipped, conflictsDropped, errors int64, enabled bool) {
	if h == nil {
		return 0, 0, 0, 0, false
	}
	h.subagentMu.Lock()
	coordinators := make([]*agent.SubagentBatchCoordinator, 0, len(h.subagentCoordinators))
	for coordinator := range h.subagentCoordinators {
		if coordinator != nil {
			coordinators = append(coordinators, coordinator)
		}
	}
	h.subagentMu.Unlock()
	for _, coordinator := range coordinators {
		counts := coordinator.TaskProgressWriteCounts()
		writes += counts.Writes
		windowSkipped += counts.WindowSkipped
		conflictsDropped += counts.ConflictsDropped
		errors += counts.Errors
	}
	enabled = h.supervisionConfig.WithDefaults().TaskProgressInterval > 0
	return writes, windowSkipped, conflictsDropped, errors, enabled
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

// localSubagentBatchLifecycleProjector is the live host bridge for both
// synchronous and durable background spawn_subagents terminal states. The
// agent package stays independent from supervision; this adapter turns the
// host-neutral record into a durable lifecycle notification and then offers
// the wake consumer a runnable transition point.
func localSubagentBatchLifecycleProjector(host *localChatRuntimeHost) agent.BatchLifecycleProjector {
	return localSubagentBatchLifecycleProjectorWithWakeDrain(host, true)
}

// localSubagentBatchRecoveryLifecycleProjector persists and schedules durable
// lifecycle wakes during startup recovery. Interactive resume defers delivery
// until the next normal parent runnable transition so startup cannot suppress
// the first composer; headless recovery keeps its immediate-delivery behavior.
func localSubagentBatchRecoveryLifecycleProjector(host *localChatRuntimeHost) agent.BatchLifecycleProjector {
	drainWake := host == nil || !chatHostSessionInteractive(host.BaseSession)
	return localSubagentBatchLifecycleProjectorWithWakeDrain(host, drainWake)
}

// localSubagentBatchStartupReplay 是启动期唯一的终态重放入口（实时装配路径与后台
// 恢复路径都走它）。
//
// 它刻意使用「无 worker + interactive 感知」的 coordinator：ReplayTerminalDeliveries
// 会对每个遗留终态 background batch 重新投影一次 lifecycle，而实时 coordinator 的
// 投影器（localSubagentBatchLifecycleProjector）drainWake 恒为 true ——
// ProjectLifecycle 刚 schedule 出来的 critical wake 会在同一次重放里被立刻 drain 成
// 一个真实父 turn。
//
// 2026-09-23 现场回归：交互式 `aicli resume` 启动期先落一行
// supervision_wake_delivered，紧接着 session_start 带着 supervision.AutoWakePrompt
// 直接开了一个隐藏 turn。用户看到的是没有 `>` 输入区的 "Analyzing"（actor Busy 抑制
// composer）和在 5 万 token 恢复上下文上白烧的一个 LLM turn（"恢复非常慢"）。
//
// 换成恢复态投影器后：lifecycle 投影与 mailbox 幂等补投逐字节不变，只有 wake drain
// 被推迟到下一次自然 turn 的 preflight digest 或显式 `/supervision wake --deliver`；
// headless（NoInteractive）场景仍然立即投递，行为不变。
func localSubagentBatchStartupReplay(ctx context.Context, host *localChatRuntimeHost, store subagentbatch.BatchStore, parentSessionID string, limit int) {
	if host == nil || store == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	replayer := agent.NewSubagentBatchCoordinator(agent.SubagentBatchCoordinatorConfig{
		Store:              store,
		Emitter:            localSubagentBatchEmitter(host),
		TerminalSink:       localSubagentBatchTerminalSink(host),
		LifecycleProjector: localSubagentBatchRecoveryLifecycleProjector(host),
	})
	_, _ = replayer.SetTerminalSinkAndReplay(ctx, localSubagentBatchTerminalSink(host), parentSessionID, limit)
}

func localSubagentBatchLifecycleProjectorWithWakeDrain(host *localChatRuntimeHost, drainWake bool) agent.BatchLifecycleProjector {
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
		convergeHint := ""
		reason := fmt.Sprintf("subagent batch %s finished with status %s (%d/%d completed)", terminal.BatchID, terminal.Status, terminal.CompletedCount, terminal.TaskCount)
		switch terminal.Status {
		case subagentbatch.BatchFailed:
			// 2026-09-26 真机回归：批次已终态（reason 就是 "finished with status
			// failed"），supervision_state 落 terminated；blocked 的语义是"等外部
			// 裁决/审批"，会让 digest 与矩阵把它读成"待决策卡住"，与 Reason 自相
			// 矛盾（见 §7.9 的 subagent_status 复现）。severity/resolution 保持
			// critical + unresolved：父代理仍必须看到失败并汇报，只是不再谎称
			// "有可裁决的行"。
			severity = supervision.SeverityCritical
			supervisionState = supervision.SupervisionTerminated
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
		default:
			// P1-C：成功完成的 batch 行推荐"收敛"——关闭已结束的子会话。
			// 该行本身是 resolution=closed，evaluator 对已关闭行只允许
			// inspect（plan §4.1.1），所以推荐动作必须配合 reason 里的
			// close_agent 工具提示使用，而不是让模型对终态行发 control 动作。
			if terminal.FailedCount == 0 && terminal.TimedOutCount == 0 {
				recommended = string(supervision.ActionClose)
				convergeHint = localBatchConvergeHint(ctx, host, terminal.BatchID)
			}
		}
		if strings.TrimSpace(terminal.Error) != "" {
			reason += ": " + strings.TrimSpace(terminal.Error)
		}
		if convergeHint != "" {
			reason += "; " + convergeHint
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
		// P1-C 方案 C：batch 终态投影之后按策略收敛已完成的子会话（默认 off
		// 时该调用不产生任何写入）。
		localConvergeTerminalBatchChildren(ctx, host, terminal)
		if !drainWake {
			return nil
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

// localAutoCloseCompletedPolicy 解析 agents.autoCloseCompleted（P1-C 方案 C）。
// 未装配 runtime config 或字段未设置时回落 "off"，即与现状完全一致；未知取值
// 一律按 off 处理（配置校验已拒绝它们，这里再兜一层，避免把拼写错误解释成
// 「自动关闭所有子会话」）。
func localAutoCloseCompletedPolicy(host *localChatRuntimeHost) string {
	if host == nil || host.RuntimeConfig == nil {
		return runtimecfg.AutoClosePolicyOff
	}
	switch policy := strings.ToLower(strings.TrimSpace(
		runtimecfg.NormalizeAgentsConfig(host.RuntimeConfig.Agents).AutoCloseCompleted)); policy {
	case runtimecfg.AutoClosePolicyCompleted, runtimecfg.AutoClosePolicyBatchTerminal:
		return policy
	default:
		return runtimecfg.AutoClosePolicyOff
	}
}

// localConvergeTerminalBatchChildren 是 P1-C 方案 C 的收敛钩子：batch 终态后，
// 对「任务成功」的终态子会话先投影一条 unresolved 的收敛行（recommended_action=
// close），再通过 LocalControlService.Control 真正关闭子会话——关闭动作因此有
// durable action audit，回执由 ActionService 的 resolution 投影产出。
//
// 契约：
//   - opt-in：agents.autoCloseCompleted 默认 off，本函数在 off 时不产生任何投影/
//     动作，宿主行为与实施前逐字节一致；
//   - 只收敛成功的子会话：failed/timed_out/canceled 的终态子会话是父 agent 需要
//     判断的现场，批次级 critical/警告行已经覆盖它们；
//   - 幂等：重放（startup recovery + 实时投影）时收敛行已 decided/resolved，
//     ActionRequired() 为 false，不重复关闭、不重复写 audit；
//   - best-effort：控制面未装配执行器时连收敛行都不投影（悬空建议比没有建议更
//     糟）；单点失败不影响 batch 终态投影结果，也不阻塞其它子会话。
func localConvergeTerminalBatchChildren(ctx context.Context, host *localChatRuntimeHost, terminal agent.BatchTerminalLifecycle) {
	policy := localAutoCloseCompletedPolicy(host)
	if policy == runtimecfg.AutoClosePolicyOff {
		return
	}
	if host == nil || host.Supervision == nil || host.Supervision.Store == nil || host.SubagentBatches == nil {
		return
	}
	// 「completed」只收敛干净完成的批次；失败/超时/孤儿的现场交给批次级行。
	if policy == runtimecfg.AutoClosePolicyCompleted && terminal.Status != subagentbatch.BatchCompleted {
		return
	}
	if host.Supervision.Actions == nil || !host.Supervision.Actions.ExecutorReady() {
		return
	}
	rootScopeID := strings.TrimSpace(terminal.RootScopeID)
	parentSessionID := strings.TrimSpace(terminal.ParentSessionID)
	batchID := strings.TrimSpace(terminal.BatchID)
	if rootScopeID == "" || batchID == "" {
		return
	}
	requestedBy := parentSessionID
	if requestedBy == "" {
		requestedBy = rootScopeID
	}
	tasks, err := host.SubagentBatches.ListTasks(ctx, batchID)
	if err != nil {
		return
	}
	control := supervision.NewLocalControlService(host.Supervision.Store, host.Supervision.Actions)
	auditReason := fmt.Sprintf(
		"auto-close child session: subagent batch %s finished with status %s (agents.autoCloseCompleted=%s)",
		batchID, terminal.Status, policy)
	for _, task := range tasks {
		childSessionID := strings.TrimSpace(task.ChildSessionID)
		if childSessionID == "" || task.Status != subagentbatch.TaskSucceeded {
			continue
		}
		notification, err := supervision.ProjectLifecycle(ctx, host.Supervision.Store, nil, supervision.LifecycleProjection{
			RootScopeID:           rootScopeID,
			TargetParentSessionID: parentSessionID,
			SubjectKind:           supervision.SubjectAgentSession,
			SubjectID:             childSessionID,
			// 用 batch 终态版本做 subject version：同一终态的重复投影命中同一行。
			SubjectVersion: terminal.SubjectVersion,
			EventType:      localBatchConvergenceEventType,
			// Severity must be warning/critical: Notification.ActionRequired()
			// (and therefore the control plane) refuses to act on an
			// informational row, which would leave the recommendation dangling.
			// The row stops nagging as soon as the close below lands its
			// resolution receipt.
			Severity:          supervision.SeverityWarning,
			SupervisionState:  supervision.SupervisionTerminated,
			Reason:            fmt.Sprintf("child session %s finished with batch %s; close it to release the spawn thread quota", childSessionID, batchID),
			RecommendedAction: string(supervision.ActionClose),
			ResolutionState:   supervision.ResolutionUnresolved,
		})
		if err != nil {
			continue
		}
		if !notification.ActionRequired() || strings.TrimSpace(notification.NotificationID) == "" {
			// 已收敛（或已由父 agent 处理）：不重复关闭，也不重复写 audit。
			continue
		}
		if _, err := control.Control(ctx, supervision.ControlRequest{
			NotificationID: notification.NotificationID,
			Scopes:         []string{rootScopeID, parentSessionID},
			RequestedByID:  requestedBy,
			Action:         supervision.ActionClose,
			Reason:         auditReason,
		}); err != nil {
			// 拒绝/执行失败都已落 durable action 记录；收敛行保持可见，父 agent
			// 下一次 preflight 仍能按 next_action 处理。
			continue
		}
	}
}

// localBatchConvergenceEventType 是 P1-C 收敛行的稳定事件类型：与
// ProjectAgentCompletion 的 agent_completed 区分，这样「已完成」的存档行不会把
// 「待收敛」的动作需求顶掉。
const localBatchConvergenceEventType = "agent_close_recommended"

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

// EventSupervisionWakeDeliveryFailed is published when an auto-wake turn could
// not be handed to the parent actor. The wake itself is re-scheduled instead of
// being dropped and the durable notification stays in the inbox (plan §6-F).
const EventSupervisionWakeDeliveryFailed = "supervision.wake_delivery_failed"

// requeueSupervisionWake records a failed auto-wake delivery and puts a fresh
// durable wake back into the scheduler (plan §6-F).
//
// The WakeConsumer resolves the claimed wake rows as soon as the asynchronous
// Deliver callback returns, so a submission failure that only got logged would
// silently consume the parent's only auto-wake: the notification survives in
// the inbox, but an idle parent would never be woken for it. Re-scheduling
// keeps the retry path alive — the next runnable transition claims the fresh
// row, and the existing class budget still bounds how often one broken
// delivery path may retry.
func (h *localChatRuntimeHost) requeueSupervisionWake(parentSessionID, rootScopeID string, wakeIDs []string, cause error) {
	if h == nil || h.Supervision == nil || h.Supervision.Wakes == nil {
		return
	}
	parentSessionID = strings.TrimSpace(parentSessionID)
	rootScopeID = strings.TrimSpace(rootScopeID)
	if parentSessionID == "" || rootScopeID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// "critical_lifecycle" lands in the bounded `other` class: a repeatedly
	// failing delivery cannot spin, because an exhausted class budget defers
	// the wake instead of dropping it (wake_budget.go WakeBudgetClassOf).
	_, requeueErr := h.Supervision.Wakes.ScheduleWake(ctx, supervision.WakeRequest{
		RootScopeID:           rootScopeID,
		TargetParentSessionID: parentSessionID,
		WakeReason:            "critical_lifecycle",
	})
	if h.EventBus == nil {
		return
	}
	detail := ""
	if cause != nil {
		detail = cause.Error()
	}
	requeueDetail := ""
	if requeueErr != nil {
		requeueDetail = requeueErr.Error()
	}
	h.EventBus.Publish(runtimeevents.Event{
		Type:      EventSupervisionWakeDeliveryFailed,
		SessionID: parentSessionID,
		Payload: map[string]interface{}{
			"parent_session_id": parentSessionID,
			"root_scope_id":     rootScopeID,
			"wake_ids":          strings.Join(wakeIDs, ","),
			"delivery_error":    detail,
			"requeue_error":     requeueDetail,
		},
	})
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
		// A6：resume = 起 turn，先过同一套门控（MaxConcurrent / MaxDepth）。
		// 瞬时容量（agents.maxThreads 槽位占满）⇒ 排队 + 位次，边界见
		// supervision.ResumeQueuePolicy；静态策略（深度越界）不可恢复，禁止
		// 排队，改为放行 + digest 的 resume_gate 行显式告知本回合不得再派发。
		ResumeCapacity: supervision.CombineResumeProbes(
			supervision.NewSubagentCapacityProbe(h.subagentCapacityView),
			supervision.NewResumePolicyGate(h.resumePolicyView),
		),
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
			// C4-3 / AC-P3-3c：挂起 turn（`awaiting_obligations`）算 busy——
			// 它不接受**新** turn——但 resume/steer 是同一 turn 的新 episode，
			// 必须放行；否则重启后挂起 turn 的 wake 永远投不出去（假空闲的反面
			// 是死锁）。AcceptsResume 就是这条口径。
			return state.Summary().AcceptsResume()
		},
		Deliver: func(ctx context.Context, parentSessionID, rootScopeID string, digest *supervision.Digest, wakeIDs []string) error {
			return h.deliverSupervisionWake(ctx, parentSessionID, rootScopeID, wakeIDs, supervision.AutoWakePrompt)
		},
		// C2-1（改动 #4）/ G2：wake 升级为 resume——投递的 prompt 携带同一 turn 的
		// resume 上下文（turn_id + rollup + I1 终局判据 + 可用动作清单，见
		// ResumePrompt），父会话不必回溯历史。resume 为 nil（账本/进度投影未接线）
		// 时 AutoWakePromptFor 逐字节回落 AutoWakePrompt，接线不改变旧行为。
		DeliverResume: func(ctx context.Context, parentSessionID, rootScopeID string, digest *supervision.Digest, wakeIDs []string, resume *supervision.ResumeContext) error {
			return h.deliverSupervisionWake(ctx, parentSessionID, rootScopeID, wakeIDs, supervision.AutoWakePromptFor(resume))
		},
		// G3：挂起 → 恢复的对外闭环。投递成功后把 §6.8 的 turn.resumed 发到宿主
		// 事件总线（A+D 契约：落盘 + 回合末尾巴帧），UI/审计据此结束"托管中"表达；
		// 投递失败不发（wake 仍在 durable 队列里，播报会与事实相反）。
		Announce: func(_ context.Context, announcement supervision.ResumeAnnouncement) {
			h.publishLocalTurnResumed(announcement)
		},
	}
	h.bindSupervisionWakeConsumer()
	// Recovery runs concurrently during host initialization and may have
	// projected a critical terminal batch before the actor registry/wake
	// consumer was ready. Drain any such durable wake once the consumer is
	// wired; the scheduler keeps it pending if the parent is still busy.
	//
	// Interactive sessions must NOT drain during host init: the drain would
	// asynchronously start a real auto-wake turn before the first prompt is
	// rendered, which flips the SessionActor into Busy and suppresses the
	// composer (`aicli resume` showed "Analyzing" with no prompt input area).
	// The wake stays durable and surfaces through the next natural turn's
	// preflight digest, or through an explicit `/supervision wake`
	// (2026-09-22: turn-end auto drain is opt-in via
	// supervision.turn_end_check).
	if !chatHostSessionInteractive(h.BaseSession) && h.BaseSession != nil && h.BaseSession.RuntimeSession != nil {
		rootSessionID := strings.TrimSpace(h.BaseSession.RuntimeSession.ID)
		if rootSessionID != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = h.wakeSupervisedParent(ctx, rootSessionID, rootSessionID)
		}
	}
}

// deliverSupervisionWake 是本地宿主的 wake/resume 投递通道：异步起一轮父 turn
// （调用方是投影/事件回调，不得阻塞在一整轮父 turn 上），prompt 由调用方按是否
// 携带 resume 上下文选择（AutoWakePrompt / ResumePrompt）。失败路径必须把已认领
// 的 wake 行重新排队（plan §6-F：只记日志会静默吃掉父会话唯一一次自动唤醒）。
func (h *localChatRuntimeHost) deliverSupervisionWake(ctx context.Context, parentSessionID, rootScopeID string, wakeIDs []string, prompt string) error {
	if h == nil || h.ActorRegistry == nil {
		return fmt.Errorf("actor registry is not ready")
	}
	if strings.TrimSpace(prompt) == "" {
		prompt = supervision.AutoWakePrompt
	}
	// Deliver asynchronously: the caller is a projection / event handler and
	// must not block on a full parent turn.
	go func() {
		// The wake turn must never wedge the parent UI into a
		// non-interruptible state: bind its ctx to the host lifecycle so
		// close/exit cancels it immediately instead of waiting out the 30m
		// cap, and mark it as an internal bypass run so tool approvals / user
		// questions cannot park the actor in SessionWaitingApproval /
		// SessionWaitingInput with no UI responder attached to this
		// background turn.
		baseCtx := context.Background()
		if h.lifecycleCtx != nil {
			baseCtx = h.lifecycleCtx
		}
		runCtx, cancel := context.WithTimeout(baseCtx, 30*time.Minute)
		defer cancel()
		releaseTurn, err := h.acquireActorTurnGate(runCtx, parentSessionID)
		if err != nil {
			// Plan §6-F: the claimed wake rows are resolved as soon as this
			// callback returns, so a failure that is only logged would
			// silently consume the parent's only auto-wake.
			h.requeueSupervisionWake(parentSessionID, rootScopeID, wakeIDs, err)
			return
		}
		defer releaseTurn()
		if err := h.submitParentWakeTurn(runCtx, parentSessionID, prompt); err != nil {
			h.requeueSupervisionWake(parentSessionID, rootScopeID, wakeIDs, err)
		}
	}()
	return nil
}

// chatHostSessionInteractive reports whether the local host base session is
// attached to an interactive foreground prompt (the resume/chat TTY path).
// Headless and JSON runs keep the original startup wake-drain semantics.
func chatHostSessionInteractive(session *ChatSession) bool {
	return session != nil && !session.NoInteractive && !session.JSONOutput
}

// beginWakeTurnRun engages the UI run-epoch protocol for one wake turn and
// returns the matching endRun, which must be invoked (defer) when the turn
// finishes. Without BeginRun the wake turn's runtime events are captured
// with run epoch 0 and every one of them is rejected by the actor-side
// fence (chatRuntimeEventBridge.isRunEpochCurrent) as targeting a closed
// run epoch — observed as a fully blank parent UI for the entire wake turn
// while the log fills with "render suppressed reason=... closed run epoch".
func (h *localChatRuntimeHost) beginWakeTurnRun(prompt string) func() {
	if h == nil {
		return func() {}
	}
	if bridge := ensureChatRuntimeEventBridge(h.BaseSession); bridge != nil {
		if strings.TrimSpace(prompt) == "" {
			prompt = supervision.AutoWakePrompt
		}
		bridge.PrepareRunPrompt(prompt)
		// 内部轮次：直接经 ActorRegistry 提交，不经过 sendMessage 的
		// StartWaiting/CompleteWaiting 协议。必须按 internal 归属启动，
		// 否则会继承前台 turn 冻结的 "Worked for" 完成摘要，状态行在整个
		// wake turn 期间显示上一轮完成时间而 transcript 仍在继续输出。
		bridge.BeginRunKind(chatRunKindInternal)
		// 阶段 C：监督唤醒回合同样由 actor 驱动，必须像前台回合一样可被 TUI ESC 中断。
		stopEscapeConsumer := startChatEscapeInterruptWatcher(h.BaseSession)
		return func() {
			stopEscapeConsumer()
			bridge.EndRun()
		}
	}
	return func() {}
}

// submitParentWakeTurn delivers one supervision wake turn on the parent
// actor, wrapped in the UI run-epoch protocol (BeginRun/EndRun).
func (h *localChatRuntimeHost) submitParentWakeTurn(ctx context.Context, parentSessionID, prompt string) error {
	if h == nil || h.ActorRegistry == nil {
		return fmt.Errorf("actor registry is not ready")
	}
	if strings.TrimSpace(prompt) == "" {
		prompt = supervision.AutoWakePrompt
	}
	endRun := h.beginWakeTurnRun(prompt)
	defer endRun()
	_, err := h.ActorRegistry.SubmitPrompt(ctx, parentSessionID, prompt, &team.RunMeta{PermissionMode: "bypass_permissions"})
	return err
}

// bindRuntimeEventPersistence 安装本地 A 通道桥（总线 → 会话事件库）。
//
// runtimeEventPersistSettings 读取宿主的批量落盘配置（默认关闭、逐条同步路径）。
func (h *localChatRuntimeHost) runtimeEventPersistSettings() runtimechat.EventPersistSettings {
	if h == nil || h.RuntimeConfig == nil {
		return runtimechat.EventPersistSettings{}
	}
	cfg := h.RuntimeConfig.SessionRuntime.EventPersist
	return runtimechat.EventPersistSettings{
		Enabled:         cfg.BatchingEnabled,
		BatchSize:       cfg.BatchSize,
		FlushInterval:   cfg.FlushInterval,
		QueueLimit:      cfg.QueueLimit,
		QueueBytesLimit: cfg.QueueBytesLimit,
		ShutdownTimeout: cfg.ShutdownTimeout,
		FailMode:        cfg.FailMode,
		AsyncDispatch:   cfg.AsyncDispatch,
		// D3：关键事件（approval / session 终态 / 工具完成 / checkpoint）立即 flush。
		CriticalTypes: runtimeevents.IsPersistCriticalEventType,
	}
}

// bindRuntimeEventPersistence 安装本地 A 通道桥（总线 → 会话事件库）。
//
// 方案 §0.2 / G2：runtime-server 的 A 通道桥在 api/skills
// handler.attachRuntimeEventBridge 内，而 aicli 本地 chat 使用
// localChatRuntimeHost，历史上只有若干特例镜像（子代理完成、team 生命周期、
// 输入事件、agent 回收），agent loop 发布的 tool.requested/tool.completed 等
// 事件因此从未写入本地 session_runtime.sqlite。
//
// 落盘判定复用 internal/events 的声明式注册表（单一真源），别名映射复用
// events.SessionStoreTypeAlias；不得在此另建白名单或第二份 switch。
// 生产者已直接落库的事件（payload 携带 seq，如 SessionActor.publish、
// 子代理完成镜像、team 生命周期）在这里跳过，避免重复 append。
func (h *localChatRuntimeHost) bindRuntimeEventPersistence() {
	if h == nil || h.EventBus == nil || h.EventStore == nil {
		return
	}
	h.runtimeEventBridgeOnce.Do(func() {
		var lastPersistWarnAt atomic.Int64
		settings := h.runtimeEventPersistSettings()
		if settings.AsyncDispatch && !settings.Enabled {
			logpkg.Warnf("runtime event persist async dispatch requires batching; ignoring (P2.11 前置条件未满足)")
		}
		if settings.Enabled {
			if batchStore, ok := h.EventStore.(runtimechat.EventPersistBatchStore); ok {
				buffer := runtimechat.NewEventPersistBuffer(batchStore, settings.BufferConfig())
				h.runtimeEventBuffer = buffer
				// cleanupFns 逆序执行：先退订（停止新事件入队），再关闭缓冲（flush 尾部）。
				h.cleanupFns = append(h.cleanupFns, func() {
					ctx, cancel := context.WithTimeout(context.Background(), settings.ShutdownTimeoutOrDefault())
					defer cancel()
					if err := buffer.Close(ctx); err != nil {
						logpkg.Warnf("runtime event persist buffer close: %v", err)
					}
				})
			} else {
				logpkg.Warnf("runtime event persist batching enabled but store does not support AppendEvents; keeping synchronous path")
			}
		}
		handler := func(event runtimeevents.Event) {
			if strings.TrimSpace(event.SessionID) == "" {
				return
			}
			// 生产者已自行落盘的事件（AppendEvent 后回填 payload["seq"]）不再重复
			// append；判定收敛在 internal/events.ProducerPersistedEvent，与 runtime
			// server 的 A 通道桥（api/skills/handler.go）共用同一实现。
			if runtimeevents.ProducerPersistedEvent(event) {
				return
			}
			if !runtimeevents.IsPersistedEventType(event.Type) {
				return
			}
			mapped := event
			mapped.Type = runtimeevents.SessionStoreTypeAlias(event.Type)
			if h.runtimeEventBuffer != nil {
				// P1.5：只入队（非阻塞），落盘、重试、关闭 flush 由缓冲 worker 负责。
				h.runtimeEventBuffer.Enqueue(mapped)
				return
			}
			if _, err := h.EventStore.AppendEvent(context.Background(), mapped); err != nil {
				// 历史上这里吞掉了持久化错误，池饱和时整条发布链会静默卡住。
				// 现在 store 侧有操作超时，超时会以错误返回；限频告警让故障可见
				// 又不会在事件洪峰时刷爆日志。
				now := time.Now().UnixNano()
				if last := lastPersistWarnAt.Load(); now-last > int64(5*time.Second) && lastPersistWarnAt.CompareAndSwap(last, now) {
					logpkg.Warnf("runtime event persistence failed (type=%s session=%s): %v", mapped.Type, mapped.SessionID, err)
				}
			}
		}
		h.runtimeEventBridgeUnsub = h.EventBus.SubscribeCancelable("", handler)
		if h.runtimeEventBridgeUnsub != nil {
			h.cleanupFns = append(h.cleanupFns, h.runtimeEventBridgeUnsub)
		}
	})
}

// bindSupervisionWakeConsumer subscribes the parent root session turn end so
// wakes accumulated while the parent was busy are drained as soon as the
// parent becomes idle again (doc 6.5 rule 2 closure).
//
// 2026-09-22 调整（docs/plan/supervision-manual-audit-plan-20260922.md）：该
// 自动核查默认关闭（supervision.turn_end_check，nil/false 等价）。关闭时本
// 函数不订阅 EventSessionEnd：turn 结束路径零回调，积压 wake 由下一次自然
// turn 的 preflight digest 被动注入，或由用户显式执行 `/supervision audit`
// （只读）/ `/supervision wake`（投递）。显式 true 才恢复历史自动闭合语义。
func (h *localChatRuntimeHost) bindSupervisionWakeConsumer() {
	if h == nil || h.supervisionWake == nil || h.EventBus == nil || h.BaseSession == nil || h.BaseSession.RuntimeSession == nil {
		return
	}
	if !h.supervisionConfig.TurnEndCheckEnabled() {
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
		if err := h.wakeSupervisedParent(ctx, rootSessionID, rootSessionID); errors.Is(err, supervision.ErrWakeRateLimited) {
			// P1-6 方案 4: the class budget deferred the wake; give the parent
			// one bounded digest-only turn instead of leaving it idle with an
			// undelivered digest (opt-in via
			// supervision.wake_self_check_per_window).
			_ = h.selfCheckSupervisedParent(ctx, rootSessionID, rootSessionID)
		}
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

// selfCheckSupervisedParent is the CLI turn-end self-check (plan P1-6 方案 4).
// It runs only after the auto-wake path was deferred by an exhausted class
// budget and shares the scheduler-owned per-window allowance, so it can start
// at most one extra digest-only parent turn per scope and window. Disabled by
// default (allowance 0).
func (h *localChatRuntimeHost) selfCheckSupervisedParent(ctx context.Context, parentSessionID, rootScopeID string) error {
	if h == nil || h.supervisionWake == nil {
		return nil
	}
	_, err := h.supervisionWake.MaybeSelfCheckParent(ctx, parentSessionID, "", rootScopeID)
	return err
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
		Config:       runtimeConfig,
		SkillDirs:    resolveChatSkillDirs(cfg, session, nil),
		DiscoverOnly: true,
		// SK-6：profile 选择与 skills_runtime.disabled_skills 取交集，禁用优先。
		SkillFilter: runtimeprofileinput.WithDisabledSkills(
			runtimeprofileinput.BuildSkillFilter(session.ProfileSkillSelection),
			disabledSkillNames(cfg),
		),
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
	// P0-2 改动 3：原始配置低于 30s 下限时，构造路径就把钳制事实记进日志
	//（host.supervisionConfig 存的是钳制后的值，启动巡查时不再重复告警）。
	warnProgressCheckIntervalClamped(cfg.Supervision.ProgressCheckInterval, supervisionConfig.ProgressCheckInterval)
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
		ledgerDriver:       strings.TrimSpace(cfg.Database.Driver),
		ledgerDSN:          strings.TrimSpace(cfg.Database.DSN),
		ledgerEnabled:      cfg.SkillsRuntime != nil && cfg.SkillsRuntime.UsageLedgerEnabled,
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
					LifecycleProjector: localSubagentBatchRecoveryLifecycleProjector(host),
				})
				_, _ = coordinator.RecoverStaleBatches(recoveryCtx, localSubagentBatchRestartGrace, "", 512)
				// 终态重放统一走 localSubagentBatchStartupReplay：两条启动重放路径
				// 必须共用同一道 interactive 闸门，否则其中一条会重新变成
				// "启动期 drain wake → 隐藏 auto-wake turn" 的入口。
				localSubagentBatchStartupReplay(recoveryCtx, host, batchStore, "", 512)
				// C4-3：run 账本的"恢复 or orphaned"决策与 batch 恢复同批执行。
				// 宽限期内仍有心跳的 run 保留（可恢复：归属者还在，或等 resume）；
				// 宽限期外无心跳的 run 判 orphaned + 提升 fencing token + 投影
				// critical 告警，晚到写入被 CAS 拒绝（AC-P3-3b）。
				if reconciler := host.newLocalExecutionSupervisor(); reconciler != nil {
					_, _ = reconciler.ReconcileRestart(recoveryCtx, supervision.RestartReconcilePolicy{
						StaleAfter: localSubagentBatchRestartGrace,
					})
				}
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
	// G2：控制面就绪后立刻把 batch 账本/进度投影接到 wake scheduler，保证第一次
	// resume 投递就带上 pending_count 与 rollup（不依赖 opt-in 的周期巡查）。
	host.wireLocalSupervisionSources()
	// P2-9：宿主启动时即开启低频一致性对账（默认 observe，10 分钟），
	// 让上一次进程崩溃/TTL 清理留下的 active 漂移在首个 pass 就被发现。
	host.startLocalRegistryReconcile()
	// P2-D：opt-in 周期巡查（supervision.progress_check_interval，默认 0
	// 关闭时该调用是空操作，不注册 ticker、不新增 goroutine）。
	host.startLocalSupervisionProgressCheck()
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

	// A 通道桥：先把总线 → 会话事件库的落盘管道接上（方案 §0.2），
	// 再接采集器；两者都只按订阅生效，顺序不影响各自语义。
	host.bindRuntimeEventPersistence()
	// 缓存分析 collector：本地 runtime host 初始化完成即挂载（对齐
	// runtime-server 路由注册即挂载，§3.2）。EventBus 订阅不具备回溯能力：
	// 若延迟到首次 /web/api/cache/* 查询才构建 service，会丢失此前的
	// llm.request.* 事件，表现为"打开缓存页只有 live 记录、没有历史请求"。
	// EventBus 缺失时内部静默降级为纯内存懒构建（v1 行为）。
	ensureLocalCacheService(host)
	// 统一用量分析：同一 EventBus 实时写入本地 usage_analytics.sqlite，
	// /web/api/usage/* 与 /web/api/cache/* 读同一数据库（DB 单一数据源）。
	ensureLocalUsageService(host)

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
	// A1/A2-a entry invariant: an actor must never be built for a base session
	// whose durable row does not exist yet. Flush deferred-storage shells and
	// verify the row is loadable (with one host-side restore attempt) so
	// durable-dependent tools such as enter_plan_mode cannot erupt mid-turn.
	if isBaseSession && session != nil {
		if err := ensureSessionDurableBeforeActor(session); err != nil {
			return nil, err
		}
		if err := ensureSessionRowLoadable(context.Background(), sessionStore, sessionID, session); err != nil {
			return nil, err
		}
	}
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
		toolkitBasePath := ""
		switch {
		case runtimeConfig != nil:
			toolkitBasePath = runtimeConfig.Workspace.Root
		case h != nil && h.RuntimeConfig != nil:
			toolkitBasePath = h.RuntimeConfig.Workspace.Root
		}
		applyLocalChildAgentdefToolPolicy(apiAgent, childAgentType, session, workspaceRoot, toolkitBasePath)
	}
	applyLocalChildReadOnlyPolicy(apiAgent, childReadOnly)
	applyLocalChildDepthPolicy(apiAgent, childDepth, localAgentDepthCeiling(runtimeConfig, h))
	leaseHandle, leaseErr := acquireLocalChatSessionLease(context.Background(), h.RuntimeStore, sessionID)
	if leaseErr != nil {
		return nil, leaseErr
	}
	loopConfig := buildLocalChatLoopConfig(runtimeConfig, session, requestedReasoningEffort)
	// §6.1 宿主接线：主 Agent 路由只接主会话。子会话走 aicli.subagents.routing
	// （scheduler 侧），主 Agent 的开关不得改变子 Agent 行为（§6.3 配置隔离）。
	applyLocalChatMainAgentRouting(loopConfig, session, isBaseSession)
	applyLocalChatCompletionRequirement(loopConfig, session, childAgentType, childCompletionRequirement, workspaceRoot)
	actor, err := runtimechat.NewSessionActor(sessionID, runtimechat.SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   h.Bootstrap.LLMRuntime(),
		SessionStore: sessionStore,
		StateStore:   h.RuntimeStore,
		EventStore:   h.EventStore,
		EventBus:     h.EventBus,
		LoopConfig:   loopConfig,
		// 灰度开关（默认开）：run 终态后到达的审批决议零恢复；显式
		// supervision.approval_terminal_guard=false 可回退旧行为。
		ApprovalTerminalGuard: h.supervisionConfig.ApprovalTerminalGuard,
		// P0-3a/M5 + P0-3b/M6：v2 指令语义显式开启且未关闭 trigger_turn_auto
		// 时，run 结束后消费子会话 mailbox 的 trigger_turn 指令并自动起一次
		// 新 turn。RunMeta 复用 followup_task 的 child-session 重建口径。
		TriggerTurnDrain: h.supervisionConfig.TriggerTurnDrainEnabled(),
		TriggerTurnRunMeta: func(_ context.Context, session *runtimechat.Session) *team.RunMeta {
			return toolbroker.SpawnAgentRunMetaFromContext(session)
		},
		PrepareRun:  localChatPrepareRunHook(apiAgent, session, workspaceRoot, isBaseSession, h.ToolSurface),
		PersistHook: localGoalPersistHook(sessionStore),
		// Phase 2 自愈钩子：仅基础会话启用，子会话不得以子 ID 复活基础会话行。
		EnsureSession: localChatActorEnsureSession(session, isBaseSession),
		RecoverStale:  true,
		// 长 turn 中途增量落库：ReAct 循环每次提交 durable 历史后按该间隔把
		// 已提交内容写回权威会话存储，避免长 turn 期间会话行长时间停在起始
		// 状态（默认 15s，见 chat.DefaultSessionCheckpointInterval；可用
		// AICLI_SESSION_CHECKPOINT_INTERVAL 覆盖或关闭）。
		CheckpointInterval: localChatSessionCheckpointIntervalFromEnv(),
		// run 无进展 watchdog 默认关闭（0）：长任务/自动化自然执行到结束，
		// 上游挂死由请求级超时 + 重试负责（失败以类型化错误返回，不会把会话
		// 卡在 busy）。需要 run 级兜底时用 AICLI_RUN_STALL_TIMEOUT 显式启用；
		// 启用后触发会以 context.Canceled 中止 run 并释放 lease 让会话可恢复。
		RunStallTimeout: localChatRunStallTimeoutFromEnv(),
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

// localChatActorEnsureSession wires the actor self-heal hook for the base
// session only. Child-session actors share the CLI's ChatSession pointer but
// must not resurrect the base session row under a child ID.
func localChatActorEnsureSession(session *ChatSession, isBaseSession bool) func(context.Context, string) error {
	if !isBaseSession || session == nil {
		return nil
	}
	return func(ctx context.Context, sessionID string) error {
		return restoreChatRuntimeSessionRow(ctx, session, sessionID)
	}
}

// applyLocalChildAgentdefToolPolicy overlays agentdef allow/deny/read-only onto
// a child actor when spawn_agent agent_type resolves to a portable definition.
// toolkitBasePath is the base path the builtin tools were registered with
// (SetBasePath(config.Workspace.Root)); it becomes the child policy's fallback
// anchor for relative path checks - never the child's own binding workspace,
// which the executor only honors through toolctx.WorkspaceRoot.
func applyLocalChildAgentdefToolPolicy(apiAgent *agent.Agent, agentType string, session *ChatSession, workspaceRoot string, toolkitBasePath string) {
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
	// A freshly created child policy inherits nothing, so seat the toolkit base
	// path on it before a sandbox is materialized; otherwise its relative path
	// checks would fall back to the server process directory while the executor
	// resolves against the registered base path.
	if strings.TrimSpace(toolPolicy.PathAnchorRoot) == "" {
		toolPolicy.SetPathAnchorRoot(toolkitBasePath)
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

// localAgentDepthCeiling resolves the effective child-depth ceiling for a child
// actor. 与 API 宿主同口径（runtimecfg.NormalizeAgentsConfig）：0 是「未设置」而不是
// 「无上限」，否则只设了某个无关 agents 字段的配置块会静默抬掉默认深度上限。
func localAgentDepthCeiling(runtimeConfig *runtimecfg.RuntimeConfig, h *localChatRuntimeHost) int {
	switch {
	case runtimeConfig != nil:
		return runtimecfg.NormalizeAgentsConfig(runtimeConfig.Agents).MaxDepth
	case h != nil && h.RuntimeConfig != nil:
		return runtimecfg.NormalizeAgentsConfig(h.RuntimeConfig.Agents).MaxDepth
	default:
		return runtimecfg.NormalizeAgentsConfig(runtimecfg.AgentsConfig{}).MaxDepth
	}
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

// subagentGlobalLimiter returns the ONE process-wide subagent concurrency
// limiter shared by every scheduler this host builds (P1-4/H12).
//
// Precedence (documented, and mirrored by the API host):
//   - agents.maxThreads > 0 → the limiter is sized maxThreads, so the total
//     number of children running at once across all batches never exceeds it;
//     agents.maxConcurrent (per batch) still applies on top, so a single batch
//     cannot consume more than its own share of that budget.
//   - agents.maxThreads ≤ 0, including the documented -1 "explicitly
//     unlimited" → nil, i.e. no process-wide ceiling and only the per-batch
//     agents.maxConcurrent remains in force (today's behavior for those
//     configurations).
//
// The limiter is created on first use and kept for the life of the host: a
// later config reload must not strand goroutines waiting on a retired limiter
// or transiently double the budget with two live limiters.
func (h *localChatRuntimeHost) subagentGlobalLimiter(maxThreads int) *agent.SubagentConcurrencyLimiter {
	if maxThreads <= 0 {
		return nil
	}
	if h == nil {
		return agent.NewSubagentConcurrencyLimiter(maxThreads)
	}
	h.subagentLimiterMu.Lock()
	defer h.subagentLimiterMu.Unlock()
	if h.subagentLimiter == nil {
		h.subagentLimiter = agent.NewSubagentConcurrencyLimiter(maxThreads)
	}
	return h.subagentLimiter
}

// subagentCapacityView 返回 A6 resume 门控需要的进程级子代理准入瞬时视图。
// 只读缓存字段：探测不得创建 limiter（未配置 agents.maxThreads 时 limiter 为
// nil ⇒ 视图 Limit=0 ⇒ 放行），也不得触发配置解析——它跑在父会话 runnable
// 转换路径上，必须是纯内存读取。
func (h *localChatRuntimeHost) subagentCapacityView() supervision.SubagentCapacityView {
	if h == nil {
		return supervision.SubagentCapacityView{}
	}
	h.subagentLimiterMu.Lock()
	limiter := h.subagentLimiter
	h.subagentLimiterMu.Unlock()
	if limiter == nil {
		return supervision.SubagentCapacityView{}
	}
	return supervision.SubagentCapacityView{
		Limit:    limiter.Limit(),
		InFlight: limiter.InFlight(),
	}
}

// resumePolicyView 返回 A6 的**静态**派发门控视图（深度）。口径与
// applyLocalChildDepthPolicy 完全一致：depth >= ceiling 的子会话在重建时会被
// 禁掉 spawn_*，因此这类会话 resume 出来的回合同样不能再派发。
//
// 命中时**不拒绝 resume**（纯汇报型回合不需要 spawn，拒绝会让它永久排队并不断
// 升级）：放行并由 digest 的 resume_gate 行显式告知本回合不得再派发。查询失败
// 一律 fail-open——门控不可读不得卡住 supervision。
func (h *localChatRuntimeHost) resumePolicyView(ctx context.Context, req supervision.ResumeCapacityRequest) supervision.ResumePolicyView {
	if h == nil || h.SessionStore == nil {
		return supervision.ResumePolicyView{}
	}
	parentSessionID := strings.TrimSpace(req.TargetParentSessionID)
	if parentSessionID == "" {
		return supervision.ResumePolicyView{}
	}
	ceiling := localAgentDepthCeiling(h.RuntimeConfig, h)
	if ceiling <= 0 {
		return supervision.ResumePolicyView{}
	}
	session, err := h.SessionStore.Load(ctx, parentSessionID)
	if err != nil || session == nil {
		return supervision.ResumePolicyView{}
	}
	if depth := localAgentSessionDepth(session); depth >= ceiling {
		return supervision.ResumePolicyView{
			Restricted: true,
			Reason:     supervision.ResumeGateDepth,
			Detail:     fmt.Sprintf("depth=%d max=%d", depth, ceiling),
		}
	}
	return supervision.ResumePolicyView{}
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
		SystemPrompt: composeLocalChatSystemPrompt(session, nil, workspaceRoot),
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
		agentConfig.MaxRepeatedPollCalls = runtimeConfig.Agent.MaxRepeatedPollCalls
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
	// P1-4/H12：并发上限透传（过去只透传 Routing，导致每个 background batch
	// 各自拿到默认 4 路窗口 → 实际并发 4×N）。每批上限 = agents.maxConcurrent
	//（默认 4，与调度器内建默认一致）；进程级上限 = agents.maxThreads（>0 时
	// 生效，全部 batch 共用同一 limiter）；背压默认关闭。
	agentsConfig := runtimecfg.DefaultAgentsConfig()
	if runtimeConfig != nil {
		agentsConfig = runtimecfg.NormalizeAgentsConfig(runtimeConfig.Agents)
	}
	scheduler := agent.NewSubagentScheduler(apiAgent, agent.SubagentSchedulerConfig{
		Routing:       localChatSubagentRoutingConfig(session),
		MaxConcurrent: agentsConfig.MaxConcurrent,
		GlobalLimiter: host.subagentGlobalLimiter(agentsConfig.MaxThreads),
		MaxQueueDepth: agentsConfig.MaxConcurrentQueueDepth,
		QueueTimeout:  time.Duration(agentsConfig.MaxConcurrentQueueTimeoutMs) * time.Millisecond,
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
			// P0-1a/M1: task-level progress write-back is an explicit opt-in
			// (supervision.task_progress_interval, default 0 = no writes).
			TaskProgressInterval: host.supervisionConfig.WithDefaults().TaskProgressInterval,
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
				// 2026-09-23：这里过去直接用实时 coordinator 重放，而它的投影器
				// drainWake 恒为 true —— 重放时重新投影出来的 critical wake 被当场
				// drain 成真实父 turn，交互式 resume 因此在第一个 composer 渲染之前
				// 就变成 Busy（没有 `>` 输入区）并白烧一个 turn。实时 coordinator 的
				// TerminalSink 已在构造时装配（下方 SetTerminalSink 再兜一次），
				// 所以这里只把「重放」交给统一的 interactive 感知入口。
				localSubagentBatchStartupReplay(replayCtx, host, host.SubagentBatches, parentSessionID, 512)
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
		broker.ExecutionSupervisor = host.getLocalExecutionSupervisor()
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
			ExecutionSupervisor: host.getLocalExecutionSupervisor(),
		})
	} else if broker := apiAgent.GetToolBroker(); broker != nil && broker.AgentSessions == nil && host.ActorRegistry != nil {
		broker.AgentSessions = host.ActorRegistry
		broker.ExecutionSupervisor = host.getLocalExecutionSupervisor()
	}
	if broker := apiAgent.GetToolBroker(); broker != nil && broker.SessionContextStore == nil {
		broker.SessionContextStore = toolbrokersessionctx.New(host.SessionStore)
	}
	// wait_team resolves its window inside the broker, so the broker must read
	// the same live policy the local wait_agent/read_agent_events paths use
	// (P2-11 目标：任何路径的等待时长都落在 [minWaitTimeoutMs, maxWaitTimeoutMs]).
	if broker := apiAgent.GetToolBroker(); broker != nil && host.ActorRegistry != nil {
		broker.WaitTimeoutPolicy = host.ActorRegistry.localWaitTimeoutPolicy
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
	// P2-12 方案 3: expose the durable supervision control plane to the model
	// when the local host has one. A host without a store leaves the field nil
	// and the three tools stay out of the tool list entirely.
	if broker := apiAgent.GetToolBroker(); broker != nil && broker.Supervision == nil {
		if controller := newLocalSupervisionToolController(host, session); controller != nil {
			broker.Supervision = controller
		}
	}
	if toolPolicy := buildLocalChatToolPolicy(session, host.ToolSurface, apiAgent.GetToolBroker()); toolPolicy != nil {
		// Mirror the toolkit base path (SetBasePath(config.Workspace.Root)) for the
		// static policy: when the run context carries no workspace root, relative
		// path arguments still have to be validated against the file the executor
		// will touch instead of the server process working directory.
		runtimeWorkspaceRoot := ""
		if runtimeConfig != nil {
			runtimeWorkspaceRoot = runtimeConfig.Workspace.Root
		}
		toolPolicy.SetPathAnchorRoot(runtimeWorkspaceRoot)
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

func localChatPrepareRunHook(apiAgent *agent.Agent, session *ChatSession, workspaceRoot string, isBaseSession bool, surface runtimeskill.MCPManager) func(context.Context, *runtimechat.Session, bool) error {
	if apiAgent == nil || session == nil || !isBaseSession {
		return nil
	}
	return func(ctx context.Context, runtimeSession *runtimechat.Session, resume bool) error {
		// turn 边界：合成 allowlist 是 agent 构建期的工具面快照，MCP 服务器
		// （客户端下发 / 本地配置链）总在会话引导之后才握手完成，迟到的工具
		// 会被 AllowToolInfo 拒绝，从而既进不了模型工具面、也无法触发冻结面
		// 重建。这里在每次 run 开始前把新工具补进同一份策略（§4.7 R1 / §4.10）。
		if added := syncLocalChatToolPolicyAllowlist(session, surface, apiAgent.GetToolBroker(), apiAgent.GetToolExecutionPolicy()); len(added) > 0 {
			logpkg.Infof("AICLI tool policy allowlist synced with live tool surface: +%d (%s)",
				len(added), strings.Join(added, ", "))
		}
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
			cfg.SystemPrompt = composeLocalChatSystemPrompt(session, runtimeSession, workspaceRoot)
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

// composeLocalChatSystemPrompt returns the outbound instruction head for the
// session.
//
// Provider prompt caching requires messages[0] to stay byte-identical for the
// whole session, so the composed head is anchored on first compose and reused
// afterwards. The anchor lives here rather than at the call sites because the
// head has more than one caller (agent construction and the per-run prepare
// hook); freezing only the prepare hook left agent construction free to emit a
// differently-composed head, which is exactly how a later workspace-root
// resolution rewrote the cached prefix mid-session.
//
// runtimeSession, when non-nil, is the durable session handed to the prepare
// hook; it is preferred over session.RuntimeSession for anchoring so the
// prepared head and the anchored head never diverge.
func composeLocalChatSystemPrompt(session *ChatSession, runtimeSession *runtimechat.Session, workspaceRoot string) string {
	if frozen := loadFrozenChatSystemPrompt(runtimeSession, session); frozen != "" {
		return frozen
	}
	composed := buildLocalChatSystemPrompt(session, workspaceRoot)
	storeFrozenChatSystemPrompt(runtimeSession, session, composed)
	return composed
}

// buildLocalChatSystemPrompt performs one unfrozen composition pass. Callers
// must go through composeLocalChatSystemPrompt so the session keeps a single
// stable instruction head.
func buildLocalChatSystemPrompt(session *ChatSession, workspaceRoot string) string {
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
		config.MaxRepeatedPollCalls = runtimeConfig.Agent.MaxRepeatedPollCalls
		config.EnableParallelTools = runtimeConfig.Agent.EnableParallelTools
		if runtimeConfig.Agent.MaxParallelToolCalls > 0 {
			config.MaxParallelToolCalls = runtimeConfig.Agent.MaxParallelToolCalls
		}
	}
	// PR-4 §6.4（主 chat 接线）：--budget-tokens → ChatSession.TurnBudgetTokens →
	// LoopReActConfig.TurnBudgetTokens；调用方显式的 loopRunOptions.BudgetTokens
	// 仍优先（子代理/团队按任务预算），此值只作为缺省。
	if session != nil && session.TurnBudgetTokens > 0 {
		config.TurnBudgetTokens = session.TurnBudgetTokens
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
	if session == nil || session.Config == nil || session.Config.AICLI == nil {
		return nil
	}
	override := chatSessionRoutingOverride(session)
	// 无配置节且无会话覆盖时保持历史快路径（不读工作区偏好）。
	if session.Config.AICLI.Subagents == nil && !(override != nil && override.HasSubAgentFields()) {
		return nil
	}
	// §4.2：读取链固定为 session > workspace > config（经统一解析器）。
	// 零覆盖时解析器返回配置层同一指针，行为与历史完全一致。
	res := config.ResolveSubagentRouting(session.Config, override, chatRoutingWorkspacePreferences(session), nil)
	return res.EffectiveSub
}

// localChatWorkspaceRoutingPreferences 读取当前工作区偏好文件的 routing 子树
// （方案 §3.3）。会话层覆盖（§3.4）在 P1/P2 接入 TUI 写入路径后进入解析器；
// 在此之前工作区层与配置层已按 §4.1 阶梯生效。
func localChatWorkspaceRoutingPreferences() *config.AICLIWorkspaceRoutingPreferences {
	prefs, err := config.LoadWorkspaceRoutingPreferences()
	if err != nil || prefs == nil || !prefs.HasRoutingFields() {
		return nil
	}
	return prefs
}

// resolveLocalChatMainAgentRouting 走统一解析器（§4.1/§4.2）：配置 → 工作区
// → 会话 → 请求逐字段合并；零覆盖时返回配置层同一指针（M8/REG 前提）。
func resolveLocalChatMainAgentRouting(session *ChatSession) *config.AICLIMainAgentRoutingConfig {
	if session == nil || session.Config == nil {
		return nil
	}
	res := config.ResolveMainAgentRouting(session.Config, chatSessionRoutingOverride(session), chatRoutingWorkspacePreferences(session), nil)
	if res.Effective == nil || !res.Effective.Enabled {
		return nil
	}
	return res.Effective
}

// localChatMainAgentRoutingConfig 返回主 Agent 动态路由配置
// （aicli.main_agent.routing）。与子 Agent 路由是两个独立配置节：任一方的开关都
// 不改变另一方（§6.3 配置隔离）。enabled=false 视为未配置，宿主不接线。
func localChatMainAgentRoutingConfig(session *ChatSession) *config.AICLIMainAgentRoutingConfig {
	if session == nil || session.Config == nil {
		return nil
	}
	routing := config.EffectiveMainAgentRoutingConfig(session.Config)
	if routing == nil || !routing.Enabled {
		return nil
	}
	return routing
}

// applyLocalChatMainAgentRouting 把主 Agent 路由接到**主会话**的 loop 配置上
// （§6.1 宿主接线）。子会话不接：子 Agent 走 aicli.subagents.routing（scheduler
// 侧），主 Agent 的开关不得改变子 Agent 行为（§6.3 配置隔离）。
func applyLocalChatMainAgentRouting(loopConfig *agent.LoopReActConfig, session *ChatSession, isBaseSession bool) {
	if loopConfig == nil || !isBaseSession {
		return
	}
	loopConfig.MainAgentRouting = resolveLocalChatMainAgentRouting(session)
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
			MaxTransportRetries:   runtimellm.ProviderMaxTransportRetriesFromAgentConfig(session.Config),
			RetryTuning:           retryTuning,
			RetryRules:            retryRules,
			DefaultModel:          session.Provider.DefaultModel,
			SupportedModels:       append([]string(nil), session.Provider.SupportedModels...),
			ModelMappings:         cloneStringMap(session.Provider.ModelMappings),
			ModelCapabilities:     cloneProviderModelCapabilities(session.Provider.ModelCapabilities),
			EnableImageGeneration: session.Provider.EnableImageGeneration,
			Headers:               effectiveChatProviderHeaders(session),
			HeaderMappings:        cloneStringMap(session.Provider.HeaderMappings),
			ResponseMarkerRules:   cloneResponseMarkerRules(session.Provider.ResponseMarkerRules),
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
		var readPool runtimecfg.ReadPoolConfig
		if runtimeConfig != nil {
			readPool = runtimeConfig.SessionRuntime.ReadPool
		}
		store, err := runtimechat.NewSQLiteRuntimeStore(&runtimechat.RuntimeStoreConfig{
			Path:                 storePath,
			DisableReadPool:      readPool.Disable,
			ReadPoolSize:         readPool.Size,
			ReadPoolBusyTimeout:  readPool.BusyTimeout,
			ReadOperationTimeout: readPool.OperationTimeout,
		})
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

// localSupervisionHostCapabilities declares which notification action channels
// this CLI host can actually execute, so preflight and the model-facing
// supervision_snapshot only announce reachable actions (P2-12 方案 1).
//
// acknowledge/defer/resolve are durable store writes inside this process
// (/debug supervision ack|defer|resolve and the model ack_lifecycle tool), so
// they are reachable whenever the store is. cancel/close additionally need a
// wired runtime executor: without one the action row can only be recorded and
// execution fails with "runtime executor not configured" — exactly the step the
// announcement must not invite.
func localSupervisionHostCapabilities(host *localChatRuntimeHost) *supervision.HostCapabilities {
	if host == nil || host.Supervision == nil || host.Supervision.Store == nil {
		return nil
	}
	caps := supervision.FullHostCapabilities()
	caps.ControlActions = host.Supervision.Actions != nil && host.Supervision.Actions.ExecutorReady()
	return &caps
}

// localSupervisionSubjectPresence mirrors the API host checks: notifications
// whose execution-run rows or team records disappeared are stale, so they stop
// counting as critical (P2-12). Subjects the local host cannot verify (for
// example agent sessions without a durable existence probe) stay critical.
func localSupervisionSubjectPresence(host *localChatRuntimeHost) supervision.SubjectPresenceFunc {
	if host == nil {
		return nil
	}
	var runStore supervision.ExecutionRunStore
	if host.Supervision != nil && host.Supervision.Store != nil {
		runStore, _ = host.Supervision.Store.(supervision.ExecutionRunStore)
	}
	teamStore := host.TeamStore
	if runStore == nil && teamStore == nil {
		return nil
	}
	return func(ctx context.Context, n supervision.Notification) (bool, bool) {
		subjectID := strings.TrimSpace(n.SubjectID)
		if subjectID == "" {
			return false, false
		}
		switch n.SubjectKind {
		case supervision.SubjectAgentRun:
			if runStore == nil {
				return false, false
			}
			if _, err := runStore.GetExecutionRun(ctx, subjectID); err != nil {
				if errors.Is(err, supervision.ErrRunNotFound) {
					return false, true
				}
				return false, false
			}
			return true, true
		case supervision.SubjectTeam:
			if teamStore == nil {
				return false, false
			}
			record, err := teamStore.GetTeam(ctx, subjectID)
			if err != nil {
				return false, false
			}
			return record != nil, true
		default:
			return false, false
		}
	}
}

// localWakeBudgetStates projects the auto-wake ledger for the CLI preflight
// line: one row per scope × class, in the class order the API host and the
// `/debug supervision list` view already use. Returning nil (no scheduler, or
// no scope requested) keeps the preflight text byte-identical to the behavior
// before the budget line existed. Repeated scopes collapse, matching the API
// projection.
func localWakeBudgetStates(ctx context.Context, host *localChatRuntimeHost, scopes ...string) []supervision.WakeBudgetState {
	if host == nil || host.Supervision == nil || host.Supervision.Wakes == nil {
		return nil
	}
	classes := []supervision.WakeBudgetClass{
		supervision.WakeBudgetClassApproval,
		supervision.WakeBudgetClassFailure,
		supervision.WakeBudgetClassOther,
		supervision.WakeBudgetClassProgress,
	}
	seen := make(map[string]bool, len(scopes))
	states := make([]supervision.WakeBudgetState, 0, len(scopes)*len(classes))
	for _, raw := range scopes {
		scope := strings.TrimSpace(raw)
		if scope == "" || seen[scope] {
			continue
		}
		seen[scope] = true
		for _, class := range classes {
			states = append(states, host.Supervision.Wakes.BudgetState(ctx, scope, class))
		}
	}
	if len(states) == 0 {
		return nil
	}
	return states
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
		SubjectPresence:       localSupervisionSubjectPresence(host),
		HostCapabilities:      localSupervisionHostCapabilities(host),
		// P0-B：把宿主已有的 durable batch 控制面投影成 progress 区块。只读，
		// 不落新表也不产生唤醒；P0-1c 起同一构造器带上 per-host 的 live-only
		// 镜像富化（last_message 与 API 宿主一致）；host.SubagentBatches 未装配
		// 时返回 nil，注入文本与改动前完全一致。
		Progress: host.newLocalBatchProgressSource(),
	})
	if err != nil {
		return "", fmt.Errorf("build supervision preflight digest: %w", err)
	}
	// 有 lifecycle 行或只有 progress 区块都要注入：正常进度（2/3 完成）过去正是
	// "看起来什么都没发生"的那一半，S3 缺口的修复点就在这里。
	if digest == nil || (len(digest.Items) == 0 && len(digest.Progress) == 0) {
		return prompt, nil
	}
	now := time.Now().UTC()
	for _, item := range digest.Items {
		// Throttled (plan §4.3): rows the parent already saw are not re-marked,
		// because each write only churned version/updated_at.
		if err := supervision.MarkDigestItemDelivered(ctx, host.Supervision.Store, item, now); err != nil {
			return "", err
		}
	}
	text := strings.TrimSpace(digest.Text)
	// P1-6 follow-up: carry the same model-visible ledger as the API preflight,
	// so a deferred (rate-limited) wake is observable inside local CLI turns
	// too. These two preflight paths are separate implementations, so the line
	// is asserted in both packages against the same formatter.
	if budgetLine := supervision.FormatWakeBudgetLine(localWakeBudgetStates(ctx, host, sessionID, targetTeamID)); budgetLine != "" {
		text = strings.TrimSpace(text + "\n" + budgetLine)
	}
	return text + "\n\n" + prompt, nil
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

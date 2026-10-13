package runtimehost

import (
	"context"
	"fmt"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// BuildSessionActor 组装一个会话 actor（E3.1b-2b-iii：从 runtimeapi.buildSessionActor 移入）。
//
// 纯编排：profile / agent / loop / 钩子细节全部由 Deps 回调提供，本函数**不感知**
// profile 内部结构，因此可脱离 runtimeapi 编译与单测。行为与原
// Handler.buildSessionActor 逐条一致（含租约作用域与释放时机）。
func BuildSessionActor(sessionID string, deps Deps) (*chat.SessionActor, error) {
	if deps.SessionStorage == nil {
		return nil, fmt.Errorf("session storage not configured")
	}
	if deps.PrepareAgent == nil || deps.BuildLoopConfig == nil {
		return nil, fmt.Errorf("runtimehost: agent/loop callbacks not configured")
	}
	if deps.AcquireSessionLeaseNoWait == nil || deps.AcquireSessionLease == nil {
		return nil, fmt.Errorf("runtimehost: session lease callbacks not configured")
	}

	sessionStore := deps.SessionStorage
	sessionCtx := deps.SessionContext
	apiAgent, selectedConfig, profileHandle := deps.PrepareAgent(context.Background(), sessionID, sessionCtx)

	stateStore := deps.RuntimeStore
	eventStore := deps.EventStore
	// NoWait: this runs inside the session hub factory while the hub lock is
	// held; queueing behind a same-process holder here would stall every
	// session in the hub. Construction-time conflicts surface immediately and
	// the caller (GetOrCreate) maps them to 409.
	leaseHandle, leaseErr := deps.AcquireSessionLeaseNoWait(context.Background(), sessionID)
	if leaseErr != nil {
		return nil, leaseErr
	}
	loopConfig := deps.BuildLoopConfig(sessionID, sessionCtx, selectedConfig, profileHandle)

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
		next, err := deps.AcquireSessionLease(ctx, sessionID)
		if err != nil {
			return err
		}
		leaseHandle = next
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
	// PrepareRun 默认取会话租约；宿主可注入自定义实现（aicli 走本地 hook）。
	prepareRun := deps.PrepareRun
	if prepareRun == nil {
		prepareRun = func(ctx context.Context, _ *chat.Session, _ bool) error {
			return ensureLease(ctx)
		}
	}
	// E3.4-0：租约持有期由 LeaseMode 决定（零值 = run-scoped，行为不变）。
	onRunFinished := func() {}
	if deps.LeaseMode.ReleasesOnRunFinished() {
		onRunFinished = releaseLease
	}
	actor, err := chat.NewSessionActor(sessionID, chat.SessionActorConfig{
		Agent:                 apiAgent,
		LLMRuntime:            deps.LLMRuntime,
		SessionStore:          sessionStore,
		StateStore:            stateStore,
		EventStore:            eventStore,
		EventBus:              deps.EventBus,
		LoopConfig:            loopConfig,
		OnProgress:            deps.OnProgress,
		OnBudgetProgress:      deps.OnBudgetProgress,
		ApprovalTerminalGuard: deps.ApprovalTerminalGuard,
		TriggerTurnDrain:      deps.TriggerTurnDrain,
		TriggerTurnRunMeta:    deps.TriggerTurnRunMeta,
		SuspendedTurnAbandon:  deps.SuspendedTurnAbandon,
		PersistHook:           deps.PersistHook,
		EnsureSession:         deps.EnsureSession,
		CheckpointInterval:    deps.CheckpointInterval,
		RunStallTimeout:       deps.RunStallTimeout,
		RecoverStale:          true,
		PrepareRun:            prepareRun,
		OnRunStalled: func(turnID string) {
			releaseLease()
		},
		OnRunFinished: onRunFinished,
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
	// E3.4-0：actor-scoped 模式**不**在此释放（aicli 在 actor 生命周期内持锁）。
	if deps.LeaseMode.ReleasesAfterConstruction() {
		releaseLease()
	}
	return actor, nil
}

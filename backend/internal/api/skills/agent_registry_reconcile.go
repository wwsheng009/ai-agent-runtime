package skills

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// P2-9 生命周期回收与一致性对账（API 宿主）。
//
// 与 CLI 宿主（cmd/aicli/commands/chat_actor_reconcile.go）共用
// internal/agentcontrol 的 Reconciler：先尽力刷新投影，再审计 durable
// registry 与真实会话的漂移，最后按 mode（observe/enforce）决定是否收敛。
// 本文件只负责 API 侧的宿主接线与可见性，不改变审计语义。
const (
	apiRegistryReconcileIntervalEnv = "AICLI_REGISTRY_RECONCILE_INTERVAL"
	apiRegistryReconcileModeEnv     = "AICLI_REGISTRY_RECONCILE_MODE"
)

// ensureAgentRegistryReconciler lazily builds the P2-9 periodic reconcile loop
// for the API host and binds it to the currently configured durable agent
// store. Config reloads swap that store, and a pass bound to a closed store
// would only produce audit errors, so the loop is rebuilt whenever the store
// changes and stopped once the store goes away (config cleared). It returns
// nil when no durable store is configured, in which case there is nothing to
// audit against and the guest keeps working without periodic convergence.
func (h *Handler) ensureAgentRegistryReconciler() *agentcontrol.Reconciler {
	if h == nil {
		return nil
	}
	store := h.getAgentControlAgentStore()

	h.agentControlReconcileMu.Lock()
	defer h.agentControlReconcileMu.Unlock()
	if h.agentControlReconciler != nil && h.agentControlReconcilerStore == store {
		return h.agentControlReconciler
	}
	if h.agentControlReconcilerStop != nil {
		h.agentControlReconcilerStop()
		h.agentControlReconcilerStop = nil
	}
	h.agentControlReconciler = nil
	h.agentControlReconcilerStore = nil
	if store == nil {
		return nil
	}

	mode, interval := h.agentRegistryReconcileTuning()
	reconciler := &agentcontrol.Reconciler{
		Store: store,
		// 与 CLI 宿主一致：先刷新投影再审计，避免用过期快照做判定。
		List: func(ctx context.Context) ([]agentcontrol.AgentRecord, error) {
			if err := h.materializeAgentControlAgentProjections(ctx, store, agentcontrol.AgentFilter{IncludeClosed: true}); err != nil {
				return nil, err
			}
			return store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{IncludeClosed: true})
		},
		Lookup: h.agentRegistrySessionBindingLookup(),
		// P2-9 方案 3：审计之后跑同一套 P2-8 驱逐判定，让终态/空闲子会话在
		// 周期里自动释放配额（enforce 才真正关闭，observe 只报候选）。
		Reclaim:  h.reclaimAgentControlAgentQuota,
		Mode:     mode,
		Interval: interval,
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.agentControlReconciler = reconciler
	h.agentControlReconcilerStore = store
	h.agentControlReconcilerStop = cancel
	go reconciler.RunLoop(ctx)
	return reconciler
}

// reclaimAgentControlAgentQuota is the API-side half of the periodic reclaim
// sweep (plan §P2-9 方案 3), mirroring the CLI host
// (cmd/aicli/commands/chat_agent_reconcile_reclaim.go): the audited roster is
// projected through the same observation helper the spawn gate uses, then the
// shared policy either closes the selected subtrees (enforce) or only counts
// them (observe). A fresh controller is enough because both halves need only
// the handler — session hub, session manager and the runtime event bus — and
// never a specific parent session.
func (h *Handler) reclaimAgentControlAgentQuota(ctx context.Context, records []agentcontrol.AgentRecord, enforce bool, now time.Time) (agentcontrol.ReclaimOutcome, error) {
	outcome := agentcontrol.ReclaimOutcome{}
	if h == nil {
		return outcome, fmt.Errorf("handler is not initialized")
	}
	controller := &sessionAgentController{handler: h}
	var reclaimStore agentcontrol.AgentReclaimStore
	if enforce {
		store := h.getAgentControlAgentStore()
		if store == nil {
			return outcome, fmt.Errorf("agent registry store is not initialized")
		}
		typed, ok := store.(agentcontrol.AgentReclaimStore)
		if !ok || typed == nil {
			return outcome, fmt.Errorf("agent registry store does not support reclaim")
		}
		reclaimStore = typed
	}
	policy := agentcontrol.ReclaimPolicy{
		// 与 spawn 闸门同源（agents.reclaimIdleMs）：空闲驱逐是显式开关，
		// 终态/容器消失的判定不依赖它。
		IdleTimeout: time.Duration(controller.agentsConfig().ReclaimIdleMs) * time.Millisecond,
		Now:         now,
	}
	return agentcontrol.SweepAgentQuotaReclaim(
		ctx,
		reclaimStore,
		records,
		controller.observeQuotaChildren,
		policy,
		enforce,
		func(ctx context.Context, rootSessionID string, pass agentcontrol.ReclaimOutcome) {
			controller.publishAgentReclaimEvent(rootSessionID, rootSessionID, agentcontrol.ReclaimSourceReconcile, pass)
		},
	)
}

// agentRegistryReconcileSummary renders the cached pass outcome for the
// supervision HTTP API. Hosts without a durable store (or where the loop has
// not completed a first pass yet) report reconcile=not_run instead of an empty
// string so clients can distinguish "nothing configured" from "no report".
func (h *Handler) agentRegistryReconcileSummary() string {
	if h == nil {
		return "reconcile=not_run"
	}
	h.agentControlReconcileMu.Lock()
	reconciler := h.agentControlReconciler
	h.agentControlReconcileMu.Unlock()
	if reconciler == nil {
		return "reconcile=not_run"
	}
	summary := strings.TrimSpace(reconciler.ReconcileSummary())
	if summary == "" {
		return "reconcile=not_run"
	}
	return summary
}

// agentRegistryReconcileTuning resolves the effective mode/interval with the
// same precedence the CLI host uses (process env > runtime config > defaults),
// so a single deployment cannot end up with two different cadences. Interval
// and mode both fall back to the safe defaults: 10min and observe-only.
func (h *Handler) agentRegistryReconcileTuning() (agentcontrol.ReconcileMode, time.Duration) {
	mode := agentcontrol.ReconcileModeObserve
	interval := agentcontrol.DefaultReconcileInterval
	if h != nil && h.runtimeConfig != nil {
		interval = agentcontrol.NormalizeReconcileInterval(h.runtimeConfig.Agents.RegistryReconcileInterval)
		mode = agentcontrol.ParseReconcileMode(h.runtimeConfig.Agents.RegistryReconcileMode)
	}
	if raw := strings.TrimSpace(os.Getenv(apiRegistryReconcileIntervalEnv)); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			interval = parsed
		}
	}
	if raw := strings.TrimSpace(os.Getenv(apiRegistryReconcileModeEnv)); raw != "" {
		mode = agentcontrol.ParseReconcileMode(raw)
	}
	return mode, agentcontrol.NormalizeReconcileInterval(interval)
}

// agentRegistrySessionBindingLookup resolves one durable registry row's session
// through the same sources the agent APIs use (session storage plus the runtime
// state store), mirroring the CLI host so the read-only audit exposed over HTTP
// and the periodic pass cannot disagree about where the drift is.
func (h *Handler) agentRegistrySessionBindingLookup() agentcontrol.SessionBindingLookup {
	return func(ctx context.Context, sessionID string) (agentcontrol.SessionBindingSnapshot, error) {
		result := agentcontrol.SessionBindingSnapshot{SessionID: strings.TrimSpace(sessionID)}
		if h == nil || h.sessionManager == nil || h.sessionManager.GetStorage() == nil {
			return result, nil
		}
		session, loadErr := h.sessionManager.GetStorage().Load(ctx, result.SessionID)
		if errors.Is(loadErr, chat.ErrSessionNotFound) || (loadErr == nil && session == nil) {
			return result, nil
		}
		if loadErr != nil {
			return result, loadErr
		}
		result.Exists = true
		result.Closed = session.State == chat.StateClosed || session.State == chat.StateArchived
		if result.Closed {
			result.Status = string(chat.SessionStopped)
		}
		store := h.getSessionRuntimeStore()
		if store == nil {
			return result, nil
		}
		state, stateErr := store.LoadState(ctx, result.SessionID)
		if stateErr != nil || state == nil {
			return result, stateErr
		}
		result.Status = string(state.Status)
		if stale, leaseErr := apiAgentRegistryHasExpiredLease(ctx, store, result.SessionID); leaseErr != nil {
			return result, leaseErr
		} else if stale && (state.Status == chat.SessionRunning || state.Status == chat.SessionRewinding || state.Status == chat.SessionStopped) {
			result.Stale = true
		}
		return result, nil
	}
}

// apiAgentRegistryHasExpiredLease reports whether the session's execution lease
// has already expired while the runtime state still claims progress, which is
// the crash signature the audit reports as STALE.
func apiAgentRegistryHasExpiredLease(ctx context.Context, store chat.RuntimeStateStore, sessionID string) (bool, error) {
	leaseStore, ok := store.(chat.SessionLeaseStore)
	if !ok || leaseStore == nil {
		return false, nil
	}
	lease, err := leaseStore.GetLease(ctx, strings.TrimSpace(sessionID))
	if err != nil || lease == nil {
		return false, err
	}
	return !lease.ExpiresAt.After(time.Now().UTC()), nil
}

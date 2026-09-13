package commands

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
)

// localRegistryReconcileIntervalEnv / localRegistryReconcileModeEnv override the
// plan §P2-9 consistency sweep for the CLI host. The environment wins over
// `agents.registryReconcile*` because an operator debugging one session should
// not have to edit runtime config; both default to the safe settings (10m,
// observe).
const (
	localRegistryReconcileIntervalEnv = "AICLI_REGISTRY_RECONCILE_INTERVAL"
	localRegistryReconcileModeEnv     = "AICLI_REGISTRY_RECONCILE_MODE"
)

// startLocalRegistryReconcile builds the periodic audit + convergence loop
// (plan §P2-9 方案 1/3) and binds it to the host lifecycle: the loop starts at
// host construction, runs one immediate pass so drift left by an unclean
// shutdown converges without waiting a full interval, then ticks at the
// configured cadence until Close() cancels lifecycleCtx.
//
// It returns nil when the durable registry store is not configured; local
// spawns then keep working without reconciliation, exactly like the P0-4
// watchdog.
func (h *localChatRuntimeHost) startLocalRegistryReconcile() *agentcontrol.Reconciler {
	if h == nil || h.ActorRegistry == nil {
		return nil
	}
	h.registryReconcilerOnce.Do(func() {
		reconciler := h.buildLocalRegistryReconciler()
		if reconciler == nil {
			return
		}
		h.registryReconciler = reconciler
		if h.lifecycleCtx == nil || h.lifecycleCtx.Err() != nil {
			// The host is closing or was never fully initialized: keep the
			// reconciler usable for on-demand passes, but never start a loop
			// that Close() would no longer wait for.
			return
		}
		ctx, cancel := context.WithCancel(h.lifecycleCtx)
		h.registryReconcilerStop = cancel
		h.asyncWG.Add(1)
		go func() {
			defer h.asyncWG.Done()
			reconciler.RunLoop(ctx)
		}()
	})
	return h.registryReconciler
}

func (h *localChatRuntimeHost) buildLocalRegistryReconciler() *agentcontrol.Reconciler {
	if h == nil || h.ActorRegistry == nil {
		return nil
	}
	registry := h.ActorRegistry
	store := registry.localAgentRegistryStore()
	if store == nil {
		return nil
	}
	mode, interval := h.localRegistryReconcileTuning()
	return &agentcontrol.Reconciler{
		Store: store,
		List: func(ctx context.Context) ([]agentcontrol.AgentRecord, error) {
			store := registry.localAgentRegistryStore()
			if store == nil {
				return nil, nil
			}
			// Refresh the projection before auditing: the same sweep recycles
			// terminal child rows (plan §P2-9 方案 3), so a time-based call
			// covers spawns whose parent never materialized them again. A pass
			// must never audit a snapshot older than the last spawn.
			// Best effort: a projection refresh failure must not stop drift
			// detection, because the durable rows listed below are the audited
			// snapshot either way.
			_ = registry.materializeLocalAgentRegistry(ctx)
			return store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{IncludeClosed: true})
		},
		Lookup: registry.localAgentSessionBindingLookup(),
		// P2-9 方案 3：审计之后跑同一套 P2-8 驱逐判定，让终态/空闲子会话在
		// 周期里自动释放配额，而不是等到下一次 spawn 被拒（enforce 模式才
		// 真正关闭；observe 只报 reclaim_candidates）。
		Reclaim:  registry.reclaimLocalAgentRegistryQuota,
		Mode:     mode,
		Interval: interval,
	}
}

// localRegistryReconcileTuning resolves the sweep settings with the precedence
// environment > runtime config > built-in default.
func (h *localChatRuntimeHost) localRegistryReconcileTuning() (agentcontrol.ReconcileMode, time.Duration) {
	rawMode := strings.TrimSpace(os.Getenv(localRegistryReconcileModeEnv))
	rawInterval := strings.TrimSpace(os.Getenv(localRegistryReconcileIntervalEnv))
	mode := agentcontrol.ReconcileModeObserve
	interval := time.Duration(0)
	if h != nil && h.ActorRegistry != nil {
		cfg := h.ActorRegistry.localAgentsConfig()
		mode = agentcontrol.ParseReconcileMode(cfg.RegistryReconcileMode)
		interval = cfg.RegistryReconcileInterval
	}
	if rawMode != "" {
		mode = agentcontrol.ParseReconcileMode(rawMode)
	}
	if parsed, err := time.ParseDuration(rawInterval); rawInterval != "" && err == nil {
		interval = parsed
	}
	return mode, agentcontrol.NormalizeReconcileInterval(interval)
}

// localRegistryReconcile returns the reconciler without starting the loop. Used
// by the debug surfaces, which must stay read-only.
func (h *localChatRuntimeHost) localRegistryReconcile() *agentcontrol.Reconciler {
	if h == nil {
		return nil
	}
	return h.registryReconciler
}

// localRegistryReconcileSummary renders the cached P2-9 report for `/debug`
// and the agent panel. An unstarted loop reports `reconcile=not_run` instead of
// an empty line so operators can tell "no drift" from "never ran".
func (h *localChatRuntimeHost) localRegistryReconcileSummary() string {
	if reconciler := h.localRegistryReconcile(); reconciler != nil {
		return reconciler.ReconcileSummary()
	}
	return "reconcile=not_run"
}

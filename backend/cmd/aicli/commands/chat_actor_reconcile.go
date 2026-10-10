package commands

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/isolation/worktree"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// localRegistryReconcileIntervalEnv / localRegistryReconcileModeEnv override the
// plan §P2-9 consistency sweep for the CLI host. The environment wins over
// `agents.registryReconcile*` because an operator debugging one session should
// not have to edit runtime config; both default to the safe settings (10m,
// observe).
const (
	localRegistryReconcileIntervalEnv = "AICLI_REGISTRY_RECONCILE_INTERVAL"
	localRegistryReconcileModeEnv     = "AICLI_REGISTRY_RECONCILE_MODE"
	localRegistryRetentionEnv         = "AICLI_REGISTRY_RETENTION"
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
		Reclaim: registry.reclaimLocalAgentRegistryQuota,
		// P2-9 retention: prune terminal rows + their wake events once they fall
		// outside the configured window (0 → shared default, negative → keep
		// forever). Only rows that were already terminal can match, so the pass
		// never touches a child that still holds quota.
		//
		// H3 wake governance runs in the same hook: the closed/stale half
		// converges on a shorter window than the identity rows (default 7 days),
		// and the active half drains the per-agent backlog that predates the
		// append-time cap. Observe (the default) only reports the candidates, so
		// wiring this cannot delete anything before an operator opts into enforce.
		Purge: func(ctx context.Context, now time.Time) (agentcontrol.TerminalPurgeOutcome, error) {
			store := registry.localAgentRegistryStore()
			if store == nil {
				return agentcontrol.TerminalPurgeOutcome{}, nil
			}
			outcome, err := agentcontrol.PurgeTerminalAgentRecords(ctx, store, agentcontrol.TerminalPurgePolicy{
				Now:       now,
				Retention: h.localRegistryTerminalRetention(),
			})
			if err != nil {
				return outcome, err
			}
			wakeOutcome, wakeErr := agentcontrol.PruneAgentWakeEvents(ctx, store, agentcontrol.AgentWakePrunePolicy{
				Now:  now,
				Mode: mode,
			})
			outcome.WakePruneMode = wakeOutcome.Mode
			outcome.WakePruneCandidates = wakeOutcome.ClosedCandidates + wakeOutcome.OverflowCandidates
			outcome.WakeEvents += wakeOutcome.ClosedDeleted + wakeOutcome.OverflowDeleted
			if wakeErr != nil {
				return outcome, wakeErr
			}
			if outcome.FirstError == "" {
				outcome.FirstError = wakeOutcome.FirstError
			}
			return outcome, nil
		},
		// P1-3 (H11/H15): the worktree base dir drifts independently of the
		// registry, so the same pass reconciles the directories against the git
		// registration table and the paths live children still own. Observe
		// mode only reports; enforce reclaims the provable leaks.
		Worktrees: registry.reconcileLocalWorktrees,
		// 第三层自愈：把义务已全终态的孤儿挂起记录（终态 wake 与下一个 run
		// 双缺）在对账节拍里清掉，见 sweepSettledParkedTurns。
		Sweep:    h.sweepSettledParkedTurns,
		Mode:     mode,
		Interval: interval,
	}
}

// reconcileLocalWorktrees runs the worktree half of the reconcile pass
// (isolation/worktree.ReconcileWorktrees, plan P1-3 / findings H11+H15). A
// directory that is neither git-registered nor owned by a live session is the
// crashed-child leak; a git registration whose directory vanished is provable
// drift. Everything else is reported, never reclaimed.
func (r *localActorRegistry) reconcileLocalWorktrees(ctx context.Context, records []agentcontrol.AgentRecord, enforce bool, now time.Time) (agentcontrol.WorktreeReconcileOutcome, error) {
	if r == nil || r.Host == nil {
		return agentcontrol.WorktreeReconcileOutcome{}, nil
	}
	repoRoot := resolveLocalWorkspacePath(r.Host.RuntimeConfig, r.Host.BaseSession)
	if strings.TrimSpace(repoRoot) == "" {
		return agentcontrol.WorktreeReconcileOutcome{}, nil
	}
	mode := worktree.ReconcileModeObserve
	if enforce {
		mode = worktree.ReconcileModeEnforce
	}
	report, err := worktree.ReconcileWorktrees(ctx, worktree.ReconcileOptions{
		RepoRoot:        repoRoot,
		RegisteredPaths: r.liveWorktreePaths(ctx, records),
		Mode:            mode,
		Now:             now,
	})
	if err != nil {
		return agentcontrol.WorktreeReconcileOutcome{}, err
	}
	return agentcontrol.WorktreeReconcileOutcome{
		Supported:     true,
		Dirs:          report.DirsChecked,
		Orphans:       report.OrphanDirs,
		StaleGit:      report.StaleGit,
		StaleRegistry: report.StaleRegistry,
		Unmanaged:     report.Unmanaged,
		ReclaimedDirs: report.ReclaimedDirs,
		ReclaimedGit:  report.ReclaimedGit,
		Failed:        report.Failed,
	}, nil
}

// liveWorktreePaths resolves the worktree paths the live registry still owns.
// The durable agent rows carry identity, not workspace context, so the session
// store is what maps a row back to the path a child is still working in.
func (r *localActorRegistry) liveWorktreePaths(ctx context.Context, records []agentcontrol.AgentRecord) []string {
	if r == nil || r.Host == nil || r.Host.SessionStore == nil {
		return nil
	}
	seen := make(map[string]bool, len(records))
	paths := make([]string, 0, len(records))
	for _, record := range records {
		sessionID := strings.TrimSpace(record.SessionID)
		if sessionID == "" || seen[sessionID] {
			continue
		}
		seen[sessionID] = true
		session, err := r.Host.SessionStore.Load(ctx, sessionID)
		if err != nil || session == nil {
			continue
		}
		if path := strings.TrimSpace(agentcontrol.ContextString(session, toolbroker.AgentSessionContextWorktreePath)); path != "" {
			paths = append(paths, path)
		}
	}
	return paths
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

// localRegistryTerminalRetention resolves registry retention with the same
// precedence as the sweep settings (environment > runtime config > built-in
// default). The normalized value is what the purge policy consumes: 0 disables
// the purge, so an operator can opt out with AICLI_REGISTRY_RETENTION=off.
func (h *localChatRuntimeHost) localRegistryTerminalRetention() time.Duration {
	window := time.Duration(0)
	if h != nil && h.ActorRegistry != nil {
		window = h.ActorRegistry.localAgentsConfig().RegistryTerminalRetention
	}
	if parsed, ok := agentcontrol.ParseTerminalRetention(os.Getenv(localRegistryRetentionEnv)); ok {
		window = parsed
	}
	return agentcontrol.NormalizeTerminalRetention(window)
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

// sweepSettledParkedTurns 是 §6.12 挂起回合的第三层自愈（真机 2026-10-10）：
// 跟随 P2-9 对账节拍（默认 10m）扫描 durable 账本，把义务已全终态的挂起记录
// 清掉——包括「终态 wake 与下一个 run 双双缺失」这类事件驱动层永远看不到的
// 孤儿记录（成功批次完成后没有任何后续事件，会话会永久渲染
// "Waiting for subagents" 且 turn_id 被无限复用）。
//
// 只清「可证明已结清」的记录（结清谓词沿用 run 边界同一条，仍欠运行中义务的
// 记录保持不动）。清理前先排一笔结算 wake 并尝试投递：父会话可运行时会起一轮
// 自然 turn 消费未读的批次汇报；即使投递失败也会继续清账并补发 turn.resumed，
// 保证 UI 不再谎报等待态。
func (h *localChatRuntimeHost) sweepSettledParkedTurns(ctx context.Context, _ time.Time) (agentcontrol.TurnSweepOutcome, error) {
	outcome := agentcontrol.TurnSweepOutcome{}
	// 只探测显式装配的控制面：非 durable store 不可能持有挂起记录（Park 要求
	// durable），所以这里不需要再按 IsDurable 过滤（测试夹具用的共享内存库同样
	// 装载 durable 语义的账本）。
	if h == nil || h.SubagentBatches == nil {
		return outcome, nil
	}
	outcome.Supported = true
	if ctx == nil {
		ctx = context.Background()
	}
	sweepCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	records, err := h.SubagentBatches.ListAllTurnSuspensions(sweepCtx)
	if err != nil {
		return outcome, err
	}
	var wakeScheduler *supervision.WakeScheduler
	if h.Supervision != nil {
		wakeScheduler = h.Supervision.Wakes
	}
	for _, record := range records {
		if record == nil {
			continue
		}
		outcome.Scanned++
		settled, err := subagentbatch.TurnObligationsSettledWithTeams(
			sweepCtx,
			h.SubagentBatches,
			record,
			h.agentSessionObligationResolver(),
			h.teamObligationResolver(),
		)
		if err != nil || !settled {
			continue
		}
		rootScopeID := strings.TrimSpace(record.RootScopeID)
		if rootScopeID == "" {
			rootScopeID = strings.TrimSpace(record.SessionID)
		}
		_, _ = supervision.ScheduleSettledTurnWakeWithTeams(
			sweepCtx,
			h.SubagentBatches,
			h.agentSessionObligationResolver(),
			h.teamObligationResolver(),
			wakeScheduler,
			record.SessionID,
			rootScopeID,
		)
		_ = h.wakeSupervisedParent(sweepCtx, record.SessionID, rootScopeID)
		if err := h.SubagentBatches.ClearTurnSuspension(sweepCtx, record.SessionID, record.TurnID); err != nil {
			outcome.Failed++
			continue
		}
		outcome.Cleared++
		h.publishParkedTurnSettledResume(sweepCtx, record)
	}
	return outcome, nil
}

// publishParkedTurnSettledResume 复刻 run 边界的非 wake 闭合事件：只依赖
// turn.resumed 清理挂起横幅的消费者（Web SSE）在清扫路径上同样收敛。载荷字段
// 与 settleParkedTurnOnRunEnd 保持一致，另加 sweep=true 便于审计区分来源。
//
// 落盘：runtime-events.jsonl 的写入判定（events.ProducerPersistedEvent）要求
// payload 带 seq，因此这里与 agent completion 镜像同口径——先 EventStore
// AppendEvent 取 seq 再发总线；AppendEvent 失败时保持实时发布（fail-open，
// 实时消费者/横幅不受影响，只是审计缺一条），与其它宿主镜像的降级语义一致。
func (h *localChatRuntimeHost) publishParkedTurnSettledResume(ctx context.Context, record *subagentbatch.TurnSuspension) {
	if h == nil || record == nil {
		return
	}
	event := runtimeevents.Event{
		Type:      runtimeevents.EventTurnResumed,
		AgentName: "reconcile",
		SessionID: record.SessionID,
		Timestamp: time.Now().UTC(),
		Payload: map[string]interface{}{
			"turn_id":          record.TurnID,
			"session_id":       record.SessionID,
			"trigger":          runtimeevents.TurnResumedTriggerSettled,
			"settled":          true,
			"obligation_count": len(record.ObligationIDs),
			"sweep":            true,
		},
	}
	if h.EventStore != nil {
		if seq, err := h.EventStore.AppendEvent(ctx, event); err == nil {
			event.Payload["seq"] = seq
		}
	}
	if h.EventBus != nil {
		h.EventBus.Publish(event)
	}
}

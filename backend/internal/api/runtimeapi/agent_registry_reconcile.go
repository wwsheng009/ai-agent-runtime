package runtimeapi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/isolation/worktree"
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
	apiRegistryRetentionEnv         = "AICLI_REGISTRY_RETENTION"
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
		Reclaim: h.reclaimAgentControlAgentQuota,
		// P2-9 retention, same policy as the CLI host: prune terminal rows plus
		// their wake events once they fall outside the configured window
		// (0 → shared default, negative → keep forever). Only already-terminal
		// rows can match, so a purge never races a quota-holding child.
		//
		// H3 wake governance rides the same hook, exactly like the CLI host: the
		// closed/stale half uses the shorter wake window and the active half
		// drains the per-agent backlog. Observe (the default) reports candidates
		// only, so this stays inert until an operator opts into enforce.
		Purge: func(ctx context.Context, now time.Time) (agentcontrol.TerminalPurgeOutcome, error) {
			outcome, err := agentcontrol.PurgeTerminalAgentRecords(ctx, store, agentcontrol.TerminalPurgePolicy{
				Now:       now,
				Retention: h.agentRegistryTerminalRetention(),
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
		// P1-3 (H11/H15): worktree drift is host-owned, so the API host folds
		// the same pass in as the CLI host. Registered paths stay empty here on
		// purpose: the reconciler never reclaims a git-registered worktree, so a
		// live child cannot be deleted, and the API host has no cheap mapping
		// from a durable row back to its session context. A live worktree is
		// therefore reported as an unmanaged registration rather than skipped.
		Worktrees: h.reconcileAgentWorktrees,
		Mode:      mode,
		Interval:  interval,
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

// reconcileAgentWorktrees runs the worktree half of the reconcile pass
// (isolation/worktree.ReconcileWorktrees, plan P1-3 / findings H11+H15) for the
// API host. The workspace root comes from the same runtime config the spawn
// isolation uses, so both surfaces agree on where worktrees live.
func (h *Handler) reconcileAgentWorktrees(ctx context.Context, _ []agentcontrol.AgentRecord, enforce bool, now time.Time) (agentcontrol.WorktreeReconcileOutcome, error) {
	if h == nil {
		return agentcontrol.WorktreeReconcileOutcome{}, nil
	}
	repoRoot := ""
	if runtimeConfig := h.resolveRuntimeConfig(UsageScope{}); runtimeConfig != nil {
		repoRoot = strings.TrimSpace(runtimeConfig.Workspace.Root)
	}
	if repoRoot == "" {
		return agentcontrol.WorktreeReconcileOutcome{}, nil
	}
	mode := worktree.ReconcileModeObserve
	if enforce {
		mode = worktree.ReconcileModeEnforce
	}
	report, err := worktree.ReconcileWorktrees(ctx, worktree.ReconcileOptions{
		RepoRoot: repoRoot,
		Mode:     mode,
		Now:      now,
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

// agentRegistryTerminalRetention resolves registry retention for the API host
// with the same precedence the sweep settings use (environment > runtime
// config > built-in default), so one deployment cannot end up with two
// retention windows. A normalized 0 disables the purge, which is the operator
// opt-out (AICLI_REGISTRY_RETENTION=off / agents.registryTerminalRetention < 0).
func (h *Handler) agentRegistryTerminalRetention() time.Duration {
	window := time.Duration(0)
	if h != nil && h.runtimeConfig != nil {
		window = h.runtimeConfig.Agents.RegistryTerminalRetention
	}
	if parsed, ok := agentcontrol.ParseTerminalRetention(os.Getenv(apiRegistryRetentionEnv)); ok {
		window = parsed
	}
	return agentcontrol.NormalizeTerminalRetention(window)
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

// agentRegistryCanResolveSessionBindings reports whether the host can turn a
// registry row's session id into a verdict at all. It mirrors the CLI host's
// guard (localAgentRegistryTerminalState): without the session storage the
// binding lookup can only answer "not found" for every row, and a sweep built
// on that answer would mark a whole healthy registry stale.
func (h *Handler) agentRegistryCanResolveSessionBindings() bool {
	return h != nil && h.sessionManager != nil && h.sessionManager.GetStorage() != nil
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

// apiAgentRegistrySessionClaimsProgress mirrors the CLI host's progress test:
// only a state that still claims a live owner turns an expired lease into the
// crash signature, so an idle session (which legitimately holds no lease) is
// never marked stale.
func apiAgentRegistrySessionClaimsProgress(status chat.SessionStatus) bool {
	switch status {
	case chat.SessionRunning, chat.SessionRewinding, chat.SessionStopped:
		return true
	default:
		return false
	}
}

// sweepStaleAgentControlAgentRegistry is the API-host twin of the CLI's
// sweepStaleLocalAgentRegistry (cmd/aicli/commands/chat_actor_registry.go):
// every projection refresh converges abandoned bindings before the roster is
// upserted, so a deployment whose periodic pass stays in observe mode still
// releases the quota of children whose container is provably gone. Absent and
// lease-expired containers are marked stale — the diagnostics-preserving
// terminal state the spawn-reservation release also uses — while provably
// terminal ones go through the shared P2-8 eviction path, so the parent stream
// sees the same agent.reclaimed event as the spawn gate and the manual cleanup
// entry. Without this, only the CLI host converged during a refresh and the two
// hosts disagreed about the same registry file (G3).
func (h *Handler) sweepStaleAgentControlAgentRegistry(ctx context.Context, store agentcontrol.AgentRegistryStore) error {
	if h == nil || store == nil {
		return nil
	}
	// Same rule as the CLI host's localAgentRegistryTerminalState: a host that
	// cannot consult a session source has no evidence that a row's container is
	// gone, so "unverifiable" must not turn into "stale". The read-only audit
	// still reports the drift for operators; only the convergence stays out of
	// it.
	if !h.agentRegistryCanResolveSessionBindings() {
		return nil
	}
	existing, err := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{IncludeClosed: true})
	if err != nil {
		return err
	}
	if len(existing) == 0 {
		return nil
	}
	lookup := h.agentRegistrySessionBindingLookup()
	marked := make(map[string]bool, len(existing))
	terminalChildren := make([]agentcontrol.AgentRecord, 0, len(existing))
	for _, record := range existing {
		record = record.Normalize()
		if record.Closed() || record.RootSessionID == "" || record.AgentPath == "" || record.SessionID == "" {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(record.RootSessionID) + "|" + strings.TrimSpace(record.AgentPath))
		if marked[key] {
			continue
		}
		snapshot, lookupErr := lookup(ctx, record.SessionID)
		if lookupErr != nil {
			return lookupErr
		}
		switch {
		case !snapshot.Exists:
			if err := h.markAgentControlSubtreeStale(ctx, store, record); err != nil {
				return err
			}
			marked[key] = true
		case snapshot.Closed:
			terminalChildren = append(terminalChildren, record)
			marked[key] = true
		case snapshot.Stale:
			if err := h.markAgentControlSubtreeStale(ctx, store, record); err != nil {
				return err
			}
			marked[key] = true
		case strings.EqualFold(strings.TrimSpace(snapshot.Status), string(chat.SessionStopped)):
			terminalChildren = append(terminalChildren, record)
			marked[key] = true
		}
	}
	if len(terminalChildren) == 0 {
		return nil
	}
	reclaimStore, ok := store.(agentcontrol.AgentReclaimStore)
	if !ok || reclaimStore == nil {
		// Store cannot distinguish reclaim reasons: keep the legacy behaviour
		// and converge the terminal rows with a plain close.
		now := time.Now().UTC()
		for _, record := range terminalChildren {
			if _, closeErr := store.CloseAgentControlAgentSubtree(ctx, record.RootSessionID, record.AgentPath, now); closeErr != nil {
				return closeErr
			}
		}
		return nil
	}
	controller := &sessionAgentController{handler: h}
	outcome, err := agentcontrol.SweepAgentQuotaReclaim(
		ctx,
		reclaimStore,
		terminalChildren,
		func(_ context.Context, children []agentcontrol.AgentRecord) []agentcontrol.ReclaimObservation {
			// These rows were just proven terminal by the binding lookup, so the
			// shared policy can judge them without another container round-trip.
			observations := make([]agentcontrol.ReclaimObservation, 0, len(children))
			for _, child := range children {
				observations = append(observations, agentcontrol.ReclaimObservation{
					AgentID:           child.AgentID,
					AgentPath:         child.AgentPath,
					SessionID:         child.SessionID,
					Status:            child.Status,
					RegistryUpdatedAt: child.UpdatedAt,
					SessionTerminal:   true,
				})
			}
			return observations
		},
		agentcontrol.ReclaimPolicy{Now: time.Now().UTC()},
		true,
		func(ctx context.Context, rootSessionID string, pass agentcontrol.ReclaimOutcome) {
			controller.publishAgentReclaimEvent(rootSessionID, rootSessionID, agentcontrol.ReclaimSourceReconcile, pass)
		},
	)
	if err != nil {
		return err
	}
	if first := strings.TrimSpace(outcome.FirstError); first != "" {
		return fmt.Errorf("sweep terminal agent rows: %s", first)
	}
	return nil
}

// markAgentControlSubtreeStale records an abandoned binding without pretending
// it was an orderly close, falling back to a plain close on stores that cannot
// keep the distinction (same contract as the CLI host helper).
func (h *Handler) markAgentControlSubtreeStale(ctx context.Context, store agentcontrol.AgentRegistryStore, record agentcontrol.AgentRecord) error {
	staleAt := time.Now().UTC()
	if marker, ok := store.(agentcontrol.AgentStaleMarker); ok && marker != nil {
		_, err := marker.MarkAgentControlAgentSubtreeStale(ctx, record.RootSessionID, record.AgentPath, staleAt)
		return err
	}
	_, err := store.CloseAgentControlAgentSubtree(ctx, record.RootSessionID, record.AgentPath, staleAt)
	return err
}

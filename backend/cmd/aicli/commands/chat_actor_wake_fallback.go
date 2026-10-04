package commands

import (
	"context"
	"errors"
	"strings"
	"time"

	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// localWakeFallbackInterval resolves the effective wake-fallback cadence:
//
//   - unset (0) => supervision.DefaultWakeFallbackInterval (default-on: the
//     edge-triggered drain has no other guarantee when the parent is idle);
//   - negative => disabled, restoring the historical "next natural turn
//     preflight / explicit /supervision wake" semantics;
//   - positive => clamped to supervision.MinWakeFallbackInterval.
//
// The interpretation lives here (not in WithDefaults) so existing config
// goldens stay byte-identical while the default stays on for every host.
func localWakeFallbackInterval(cfg supervision.Config) time.Duration {
	switch {
	case cfg.WakeFallbackInterval < 0:
		return 0
	case cfg.WakeFallbackInterval == 0:
		return supervision.DefaultWakeFallbackInterval
	case cfg.WakeFallbackInterval < supervision.MinWakeFallbackInterval:
		return supervision.MinWakeFallbackInterval
	default:
		return cfg.WakeFallbackInterval
	}
}

// requestSupervisedWakeDrain attempts one asynchronous parent drain. It is the
// ExecutionSupervisor.WakeReady hook body: scans may produce wakes while the
// parent sits idle, so the scheduling edge itself must try a delivery
// (Runnable / budget gates stay inside MaybeWakeParent and keep the wake
// durable when they deny).
func (h *localChatRuntimeHost) requestSupervisedWakeDrain(rootScopeID, parentSessionID string) {
	if h == nil {
		return
	}
	parentSessionID = strings.TrimSpace(parentSessionID)
	if parentSessionID == "" {
		return
	}
	rootScopeID = strings.TrimSpace(rootScopeID)
	if rootScopeID == "" {
		rootScopeID = parentSessionID
	}
	baseCtx := context.Background()
	if h.lifecycleCtx != nil {
		baseCtx = h.lifecycleCtx
	}
	ctx, cancel := context.WithTimeout(baseCtx, 30*time.Second)
	go func() {
		defer cancel()
		_ = h.wakeSupervisedParent(ctx, parentSessionID, rootScopeID)
	}()
}

// startLocalSupervisionWakeFallback starts the default-on bounded sweep that
// delivers durable wakes to an idle parent. The sweep is deliberately cheap:
// it probes for unclaimed wakes targeting this host's parent session and only
// then attempts a drain; with nothing pending it performs a single indexed
// read per tick. Like the other host loops it is bound to lifecycleCtx and
// tracked by asyncWG, so Close() waits for it instead of leaving a stray
// goroutine behind.
func (h *localChatRuntimeHost) startLocalSupervisionWakeFallback() {
	if h == nil || h.Supervision == nil || h.Supervision.Store == nil || h.Supervision.Wakes == nil || h.supervisionWake == nil {
		return
	}
	interval := localWakeFallbackInterval(h.supervisionConfig)
	if interval <= 0 {
		return
	}
	if h.lifecycleCtx == nil || h.lifecycleCtx.Err() != nil {
		// Host is closing or was never fully initialized: keep the once
		// helper callable for tests, but never start a loop Close() would
		// no longer wait for.
		return
	}
	h.wakeFallbackOnce.Do(func() {
		ctx, cancel := context.WithCancel(h.lifecycleCtx)
		h.wakeFallbackStop = cancel
		h.asyncWG.Add(1)
		go func() {
			defer h.asyncWG.Done()
			h.runLocalSupervisionWakeFallbackLoop(ctx, interval)
		}()
	})
}

// stopLocalSupervisionWakeFallback lets tests/debug paths stop the sweep
// without closing the whole host (Close() goes through lifecycleCtx).
func (h *localChatRuntimeHost) stopLocalSupervisionWakeFallback() {
	if h == nil || h.wakeFallbackStop == nil {
		return
	}
	h.wakeFallbackStop()
}

func (h *localChatRuntimeHost) runLocalSupervisionWakeFallbackLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Best-effort: one failed read skips this tick, never the loop.
			if _, err := h.runLocalSupervisionWakeFallbackOnce(ctx); err != nil {
				logpkg.Warnf("supervision: wake fallback sweep failed: %v", err)
			}
		}
	}
}

// runLocalSupervisionWakeFallbackOnce probes for unclaimed wakes that target
// this host's parent session and, when found, attempts one drain per owned
// scope (session + active team). It returns whether a drain attempt was made.
//
// Busy parents and exhausted budgets keep the wake durable (the same gates as
// every other delivery path); a parent that is genuinely idle and inside
// budget gets woken without waiting for the next natural turn.
func (h *localChatRuntimeHost) runLocalSupervisionWakeFallbackOnce(ctx context.Context) (bool, error) {
	if h == nil || h.Supervision == nil || h.Supervision.Store == nil || h.Supervision.Wakes == nil || h.supervisionWake == nil {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	parentSessionID := h.localSupervisionProgressCheckSessionID()
	if parentSessionID == "" {
		return false, nil
	}
	// Bounded probe: any unclaimed wake aimed at this parent (session scope or
	// team-lead scope) is enough to justify a drain attempt.
	pending, err := h.Supervision.Store.ListWakePending(ctx, supervision.WakeFilter{
		TargetParentSessionID: parentSessionID,
		UnclaimedOnly:         true,
		Limit:                 1,
	})
	if err != nil {
		return false, err
	}
	if len(pending) == 0 {
		return false, nil
	}
	attempted := false
	for _, scope := range chatDebugSupervisionScopes(h.BaseSession, "") {
		if strings.TrimSpace(scope) == "" {
			continue
		}
		err := h.wakeSupervisedParent(ctx, parentSessionID, scope)
		switch {
		case err == nil:
			attempted = true
		case errors.Is(err, supervision.ErrWakeParentBusy), errors.Is(err, supervision.ErrWakeRateLimited):
			// Durable by design: the wake stays pending for the next sweep.
		default:
			logpkg.Warnf("supervision: wake fallback drain failed (parent=%s scope=%s): %v", parentSessionID, scope, err)
		}
	}
	return attempted, nil
}

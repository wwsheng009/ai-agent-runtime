package supervision

import (
	"context"
	"strings"
)

// AutoWakePrompt is the fixed parent turn prompt used for auto-scheduled
// supervision wakes (doc 6.5 rule 5: the wake prompt references only the
// lifecycle digest; the digest itself is injected by the preflight step on
// the parent turn start path).
const AutoWakePrompt = "[supervision] 存在待处理的子 Agent / Team 关键生命周期事件，请检查生命周期摘要（lifecycle digest）并继续。"

// WakeConsumer wires the durable wake scheduler into a host-provided parent
// turn delivery path. It is the concrete P2 closure for doc 6.5: there is no
// resident polling goroutine; the consumer is invoked at runnable state
// transition points (child completion projection, parent turn end) and, when
// the parent is runnable and the auto-turn budget allows it, delivers the
// aggregated lifecycle digest as one parent turn.
type WakeConsumer struct {
	// Wakes is the durable wake scheduler; required.
	Wakes *WakeScheduler
	// Runnable decides whether the parent may start another turn right now
	// (doc 6.5 rule 2: no concurrent second turn while running / waiting
	// approval / waiting input / rewinding). When nil the parent is always
	// considered runnable; hosts should provide a state-based check so a
	// busy parent keeps the wake pending instead of queueing a second turn.
	Runnable ParentRunnable
	// Deliver submits the aggregated digest as one parent turn. It receives
	// the claimed wake ids so hosts can correlate the delivery. A nil
	// Deliver still drains and resolves wakes (notification stays durable in
	// the inbox and preflight injects it on the next natural turn).
	Deliver func(ctx context.Context, parentSessionID string, digest *Digest, wakeIDs []string) error
}

// MaybeWakeParent is called at every parent runnable state-transition point:
// after a child completion projection and after the parent's own turn ends.
// It is safe to call when no wake is pending and when the parent is busy;
// both are no-ops that keep the durable wake row for a later runnable point.
func (c *WakeConsumer) MaybeWakeParent(ctx context.Context, parentSessionID, parentTeamID, rootScopeID string) error {
	if c == nil || c.Wakes == nil {
		return nil
	}
	parentSessionID = strings.TrimSpace(parentSessionID)
	rootScopeID = strings.TrimSpace(rootScopeID)
	if parentSessionID == "" || rootScopeID == "" {
		return nil
	}
	claimed, digest, err := c.Wakes.DrainRunnable(ctx, parentSessionID, parentTeamID, rootScopeID, c.Runnable)
	if err != nil {
		// ErrWakeParentBusy / ErrWakeRateLimited: keep the wake durable;
		// the next runnable point (or the next natural turn preflight)
		// delivers the digest.
		return err
	}
	if len(claimed) == 0 || digest == nil {
		return nil
	}
	wakeIDs := make([]string, 0, len(claimed))
	for _, w := range claimed {
		wakeIDs = append(wakeIDs, w.WakeID)
	}
	if c.Deliver != nil {
		if err := c.Deliver(ctx, parentSessionID, digest, wakeIDs); err != nil {
			// Delivery failed: release the claims anyway. The notification
			// stays durable in the inbox and the next natural parent turn
			// injects it via preflight; keeping the row claimed would block
			// coalescing of later events for the same dedup key.
			c.release(ctx, wakeIDs)
			return err
		}
	}
	// The turn delivery consumed the wakes: a later event for the same
	// root + parent + reason must coalesce into a fresh row again.
	c.release(ctx, wakeIDs)
	return nil
}

func (c *WakeConsumer) release(ctx context.Context, wakeIDs []string) {
	for _, id := range wakeIDs {
		_ = c.Wakes.ResolveWake(ctx, id)
	}
}

package supervision

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Turn-end self-check (plan P1-6 方案 4).
//
// The auto-wake budget deliberately bounds how many parent turns a burst of
// child exceptions may start. When a class budget is exhausted, DrainRunnable
// keeps the wake durable and returns ErrWakeRateLimited, so the parent keeps
// the notification in its inbox — but if the parent is going idle right now
// (its turn just ended) nothing delivers that digest until the next *natural*
// parent turn, which may be hours away or may never happen.
//
// The self-check closes that last gap without weakening the budget: when a
// parent turn ends while unclaimed durable wakes are still pending, the host
// may start exactly one extra digest-only parent turn, bounded by its own
// per-window allowance. Self-check turns never charge the class budgets, so a
// failure storm still cannot spawn unbounded turns: the allowance is consumed
// inside the same rolling window the scheduler already uses, and a self-check
// turn that ends again finds the allowance exhausted (or an empty digest) and
// stops. The default is 0 (disabled), keeping historical behavior unchanged.

// selfCheckLimit returns the per-window self-check allowance; 0 disables the
// turn-end self-check.
func (s *WakeScheduler) selfCheckLimit() int {
	if s == nil {
		return 0
	}
	return s.maxSelfCheck
}

// AllowSelfCheck consumes one turn-end self-check allowance for the root scope.
// It reports false when the feature is disabled or the window allowance is
// already spent. The allowance shares the scheduler's rate window and clock, so
// it rolls at the same cadence as the class budgets.
func (s *WakeScheduler) AllowSelfCheck(rootScopeID string) bool {
	if s == nil {
		return false
	}
	rootScopeID = strings.TrimSpace(rootScopeID)
	if rootScopeID == "" || s.selfCheckLimit() <= 0 {
		return false
	}
	now := s.now()
	cutoff := now.Add(-s.rateWindow)

	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	if s.selfChecks == nil {
		s.selfChecks = map[string][]time.Time{}
	}
	kept := s.selfChecks[rootScopeID][:0]
	for _, at := range s.selfChecks[rootScopeID] {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	if len(kept) >= s.selfCheckLimit() {
		s.selfChecks[rootScopeID] = kept
		return false
	}
	kept = append(kept, now)
	s.selfChecks[rootScopeID] = kept
	return true
}

// SelfCheckPending reports whether the root scope still has unclaimed durable
// wakes, i.e. a wake that the class budget pushed to a later window.
func (s *WakeScheduler) SelfCheckPending(ctx context.Context, rootScopeID string) (bool, error) {
	if s == nil || s.store == nil {
		return false, nil
	}
	rootScopeID = strings.TrimSpace(rootScopeID)
	if rootScopeID == "" {
		return false, nil
	}
	pending, err := s.store.ListWakePending(ctx, WakeFilter{
		RootScopeID:   rootScopeID,
		UnclaimedOnly: true,
		Limit:         1,
	})
	if err != nil {
		return false, fmt.Errorf("list wake pending: %w", err)
	}
	return len(pending) > 0, nil
}

// RootDigest builds the same aggregated lifecycle digest the parent turn
// preflight injects. It is read-only: the delivery path marks the entries as
// delivered/seen, exactly like a natural turn would.
func (s *WakeScheduler) RootDigest(ctx context.Context, rootScopeID, parentSessionID, parentTeamID string) (*Digest, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	return BuildDigest(ctx, s.store, DigestRequest{
		RootScopeID:           strings.TrimSpace(rootScopeID),
		TargetParentSessionID: strings.TrimSpace(parentSessionID),
		TargetParentTeamID:    strings.TrimSpace(parentTeamID),
	})
}

// ResolveUnclaimedWakes drops durable wakes whose notification no longer has
// anything to inject (already resolved / acknowledged). It is the self-check
// counterpart of WakeConsumer.release: without it a stale row would keep
// requesting self-check turns that would all end in an empty digest.
func (s *WakeScheduler) ResolveUnclaimedWakes(ctx context.Context, rootScopeID string) (int, error) {
	if s == nil || s.store == nil {
		return 0, nil
	}
	rootScopeID = strings.TrimSpace(rootScopeID)
	if rootScopeID == "" {
		return 0, nil
	}
	pending, err := s.store.ListWakePending(ctx, WakeFilter{
		RootScopeID:   rootScopeID,
		UnclaimedOnly: true,
	})
	if err != nil {
		return 0, fmt.Errorf("list wake pending: %w", err)
	}
	resolved := 0
	for _, row := range pending {
		if strings.TrimSpace(row.WakeID) == "" {
			continue
		}
		if err := s.ResolveWake(ctx, row.WakeID); err != nil {
			return resolved, err
		}
		resolved++
	}
	return resolved, nil
}

// MaybeSelfCheckParent is the turn-end hook for a parent whose wake was
// deferred by the class budget (plan P1-6 方案 4). Callers invoke it after
// MaybeWakeParent reported ErrWakeRateLimited; it starts at most one extra
// digest-only parent turn per root scope and rate window through the same
// Deliver path the auto-wake uses.
//
// It returns true when a self-check turn was delivered. A false result with a
// nil error means the feature is disabled, the parent is busy, no wake is
// pending, the allowance is spent, or there was nothing new to inject.
func (c *WakeConsumer) MaybeSelfCheckParent(ctx context.Context, parentSessionID, parentTeamID, rootScopeID string) (bool, error) {
	if c == nil || c.Wakes == nil || c.Deliver == nil {
		return false, nil
	}
	parentSessionID = strings.TrimSpace(parentSessionID)
	parentTeamID = strings.TrimSpace(parentTeamID)
	rootScopeID = strings.TrimSpace(rootScopeID)
	if parentSessionID == "" || rootScopeID == "" {
		return false, nil
	}
	if c.Wakes.selfCheckLimit() <= 0 {
		return false, nil
	}
	if c.Runnable != nil && !c.Runnable(ctx, rootScopeID, parentSessionID, parentTeamID) {
		return false, nil
	}
	pending, err := c.Wakes.SelfCheckPending(ctx, rootScopeID)
	if err != nil || !pending {
		return false, err
	}
	digest, err := c.Wakes.RootDigest(ctx, rootScopeID, parentSessionID, parentTeamID)
	if err != nil {
		return false, err
	}
	if digest == nil || len(digest.Items) == 0 {
		// The notification was resolved (or already acknowledged) while the
		// wake stayed durable: drop the stale rows instead of spending an
		// allowance on a content-free turn.
		if _, err := c.Wakes.ResolveUnclaimedWakes(ctx, rootScopeID); err != nil {
			return false, err
		}
		return false, nil
	}
	if !c.Wakes.AllowSelfCheck(rootScopeID) {
		return false, nil
	}
	if err := c.Deliver(ctx, parentSessionID, digest, nil); err != nil {
		return false, err
	}
	return true, nil
}

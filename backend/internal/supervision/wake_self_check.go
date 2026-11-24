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
	})
	if err != nil {
		return false, fmt.Errorf("list wake pending: %w", err)
	}
	for _, row := range pending {
		if strings.TrimSpace(row.DeliveryState) == "" {
			return true, nil
		}
	}
	return false, nil
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
		HostCapabilities:      s.hostCapabilitySnapshot(),
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
	owner := uniqueSupervisionID("wake-owner-")
	now := s.now().UTC()
	resolved := 0
	for _, row := range pending {
		if strings.TrimSpace(row.WakeID) == "" {
			continue
		}
		if strings.TrimSpace(row.DeliveryState) != "" {
			continue
		}
		claimed, err := s.store.ClaimWakePending(ctx, row.WakeID, owner, now)
		if err != nil {
			return resolved, err
		}
		if !claimed {
			continue
		}
		claimedAt := now
		row.ClaimedAt = &claimedAt
		row.ClaimedBy = owner
		if err := s.ResolveWakeOwned(ctx, row); err != nil {
			return resolved, err
		}
		resolved++
	}
	return resolved, nil
}

func (c *WakeConsumer) maybeSelfCheckLegacy(ctx context.Context, parentSessionID, parentTeamID, rootScopeID string) (bool, error) {
	now := c.clock()
	staleBefore := now.Add(-c.Wakes.claimStaleAfter)
	if _, err := c.Wakes.store.ReleaseStaleWakePending(ctx, staleBefore); err != nil {
		return false, err
	}
	if _, err := c.Wakes.store.ReleaseStaleAcceptedWakePending(ctx, staleBefore); err != nil {
		return false, err
	}
	if _, err := c.Wakes.store.ReleaseStaleSubmittingWakePending(ctx, staleBefore); err != nil {
		return false, err
	}
	pending, err := c.Wakes.store.ListWakePending(ctx, WakeFilter{
		RootScopeID:           rootScopeID,
		TargetParentSessionID: parentSessionID,
		TargetParentTeamID:    parentTeamID,
		UnclaimedOnly:         true,
	})
	if err != nil || len(pending) == 0 {
		return false, err
	}

	claimOwner := uniqueSupervisionID("wake-owner-")
	var ordinary, accepted []WakePending
	for _, row := range pending {
		state := strings.TrimSpace(row.DeliveryState)
		if state != "" && state != WakeDeliveryStateAccepted {
			continue
		}
		owned, claimErr := c.Wakes.store.ClaimWakePending(ctx, row.WakeID, claimOwner, now)
		if claimErr != nil {
			for _, claimed := range append(ordinary, accepted...) {
				_ = c.Wakes.ReleaseWake(ctx, claimed.WakeID, claimOwner)
			}
			return false, claimErr
		}
		if !owned {
			continue
		}
		claimedAt := now
		row.ClaimedAt = &claimedAt
		row.ClaimedBy = claimOwner
		if state == WakeDeliveryStateAccepted {
			accepted = append(accepted, row)
		} else {
			ordinary = append(ordinary, row)
		}
	}

	if len(accepted) > 0 {
		deliveryCtx, stopRenewal := c.startWakeClaimRenewal(ctx, accepted)
		err := c.deliverResultsConsumptionRetry(deliveryCtx, parentSessionID, rootScopeID, accepted)
		stopRenewal()
		if err != nil {
			_ = c.releaseForRetry(ctx, accepted)
			for _, wake := range ordinary {
				_ = c.Wakes.ReleaseWake(ctx, wake.WakeID, wake.ClaimedBy)
			}
			return false, err
		}
	}
	if len(ordinary) == 0 {
		return false, nil
	}

	deliveryCtx, stopRenewal := c.startWakeClaimRenewal(ctx, ordinary)
	defer stopRenewal()
	digest, err := c.Wakes.RootDigest(deliveryCtx, rootScopeID, parentSessionID, parentTeamID)
	if err != nil {
		for _, wake := range ordinary {
			_ = c.Wakes.ReleaseWake(ctx, wake.WakeID, wake.ClaimedBy)
		}
		return false, err
	}
	if digest == nil || (len(digest.Items) == 0 && len(digest.Progress) == 0) {
		stopRenewal()
		for _, wake := range ordinary {
			_ = c.Wakes.ResolveWakeOwned(ctx, wake)
		}
		return false, nil
	}
	if !c.Wakes.AllowSelfCheck(rootScopeID) {
		for _, wake := range ordinary {
			_ = c.Wakes.ReleaseWake(ctx, wake.WakeID, wake.ClaimedBy)
		}
		return false, nil
	}
	wakeIDs := wakeIDsOf(ordinary)
	wakeOwner := wakeClaimOwner(ordinary)
	// Preserve the legacy digest-only host contract: these wakes are fenced
	// internally, but a self-check is not a direct wake-id resumption and does
	// not consume the queued wake. Only an explicit acceptance error carrying
	// a receipt crosses the durable acceptance boundary.
	if deliveryErr := c.Deliver(deliveryCtx, parentSessionID, rootScopeID, digest, nil); deliveryErr != nil {
		if deliveryCtx.Err() != nil {
			return false, fmt.Errorf("wake delivery lease lost: %w", ErrWakeClaimFenceLost)
		}
		_, accepted := AcceptedDelivery(deliveryErr)
		if accepted {
			return true, c.handleDeliveryError(ctx, deliveryCtx, parentSessionID, rootScopeID, digest, ordinary, nil, wakeIDs, nil, func() {}, stopRenewal, deliveryErr)
		}
		stopRenewal()
		if ctx.Err() != nil || wakeDeliveryLeaseLost(deliveryCtx) {
			return false, fmt.Errorf("wake delivery lease lost: %w", ErrWakeClaimFenceLost)
		}
		releaseErr := c.releaseForRetry(ctx, ordinary)
		return false, joinDeliveryErrors(deliveryErr, releaseErr)
	}
	if deliveryCtx.Err() != nil {
		return false, fmt.Errorf("wake delivery lease lost: %w", ErrWakeClaimFenceLost)
	}
	stopRenewal()
	if ctx.Err() != nil || wakeDeliveryLeaseLost(deliveryCtx) {
		return false, fmt.Errorf("wake delivery lease lost: %w", ErrWakeClaimFenceLost)
	}
	var releaseErr error
	for _, wake := range ordinary {
		if err := c.Wakes.ReleaseWake(ctx, wake.WakeID, wakeOwner); err != nil {
			releaseErr = joinDeliveryErrors(releaseErr, err)
		}
	}
	return true, releaseErr
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
	if c.Admission == nil {
		return c.maybeSelfCheckLegacy(ctx, parentSessionID, parentTeamID, rootScopeID)
	}

	// The self-check bypasses the class budget, not finalization admission or
	// exactly-once. Consume late/notification-only settlements, claim one
	// settlement per episode, and resolve the delivered wakes so a later window
	// cannot replay the same continuation.
	_, _ = c.Admission.RecoverStaleClaims(ctx)
	if _, err := c.Wakes.store.ReleaseStaleWakePending(ctx, c.clock().Add(-c.Wakes.claimStaleAfter)); err != nil {
		return false, err
	}
	if _, err := c.Wakes.store.ReleaseStaleAcceptedWakePending(ctx, c.clock().Add(-c.Wakes.claimStaleAfter)); err != nil {
		return false, err
	}
	pending, err := c.Wakes.store.ListWakePending(ctx, WakeFilter{
		RootScopeID:           rootScopeID,
		TargetParentSessionID: parentSessionID,
		TargetParentTeamID:    parentTeamID,
		UnclaimedOnly:         true,
	})
	if err != nil {
		return false, err
	}
	// Self-check is still a parent-turn delivery path. Do not let it process
	// an accepted/submitting row as a model wake, and do not evaluate an
	// unowned row that a concurrent normal drain could reclaim underneath us.
	claimOwner := uniqueSupervisionID("wake-owner-")
	claimedRows := make([]WakePending, 0, len(pending))
	now := c.clock()
	for _, row := range pending {
		if strings.TrimSpace(row.DeliveryState) != "" {
			continue
		}
		owned, claimErr := c.Wakes.store.ClaimWakePending(ctx, row.WakeID, claimOwner, now)
		if claimErr != nil {
			for _, held := range claimedRows {
				_ = c.Wakes.ReleaseWake(ctx, held.WakeID, claimOwner)
			}
			return false, claimErr
		}
		if !owned {
			continue
		}
		claimedAt := now
		row.ClaimedAt = &claimedAt
		row.ClaimedBy = claimOwner
		claimedRows = append(claimedRows, row)
	}
	if len(claimedRows) == 0 {
		return false, nil
	}
	claims := map[string]ResumeClaim{}
	stopClaimRenewal := func() {}
	deliveryCtx, stopWakeClaimRenewal := c.startWakeClaimRenewal(ctx, claimedRows)
	defer stopWakeClaimRenewal()
	admitted := make([]WakePending, 0, len(claimedRows))
	releaseWakeClaims := func() {
		for _, row := range claimedRows {
			_ = c.Wakes.ReleaseWake(ctx, row.WakeID, row.ClaimedBy)
		}
	}
	releaseClaims := func() {
		stopClaimRenewal()
		stopWakeClaimRenewal()
		for _, held := range claims {
			_ = c.Admission.Release(ctx, held)
		}
		releaseWakeClaims()
	}
	for _, row := range claimedRows {
		result, admitErr := c.Admission.Evaluate(deliveryCtx, ResumeAdmissionInputFromWake(row))
		if admitErr != nil {
			releaseClaims()
			return false, nil
		}
		switch result.Decision {
		case NotifyOnly, RejectStale:
			consumption := "notification_only"
			reason := string(result.Decision)
			if result.Decision == RejectStale {
				consumption = "stale"
			}
			if consumeErr := c.Wakes.markConsumed(deliveryCtx, row, consumption, reason); consumeErr != nil {
				releaseClaims()
				return false, consumeErr
			}
		case RetryAdmission:
			releaseClaims()
			return false, nil
		default:
			if result.Decision == ResumeSameParkedTurn {
				claimKey := strings.TrimSpace(result.ClaimKey)
				if claimKey == "" {
					claimKey = DeriveResumeClaimKey(result.Input.ParentSessionID, result.Input.ParentTurnID, result.Input.ParentEpisodeID, result.Input.SettlementCohortID, result.Input.SettlementID)
				}
				if _, exists := claims[claimKey]; !exists {
					claim, won, claimErr := c.Admission.Claim(deliveryCtx, result)
					if claimErr != nil {
						releaseClaims()
						return false, claimErr
					}
					if !won {
						if consumeErr := c.Wakes.markConsumed(deliveryCtx, row, "notification_only", "settlement_already_claimed"); consumeErr != nil {
							releaseClaims()
							return false, consumeErr
						}
						continue
					}
					claims[claimKey] = claim
				}
			}
			admitted = append(admitted, row)
		}
	}
	if len(admitted) == 0 {
		releaseClaims()
		return false, nil
	}
	stopClaimRenewal = c.startClaimRenewal(claims)
	defer stopClaimRenewal()
	digest, err := c.Wakes.RootDigest(deliveryCtx, rootScopeID, parentSessionID, parentTeamID)
	if err != nil {
		releaseClaims()
		return false, err
	}
	if digest == nil || (len(digest.Items) == 0 && len(digest.Progress) == 0) {
		stopClaimRenewal()
		stopWakeClaimRenewal()
		for _, held := range claims {
			_ = c.Admission.Release(ctx, held)
		}
		for _, row := range admitted {
			_ = c.Wakes.ResolveWakeOwned(ctx, row)
		}
		return false, nil
	}
	if !c.Wakes.AllowSelfCheck(rootScopeID) {
		releaseClaims()
		return false, nil
	}
	wakeIDs := make([]string, 0, len(admitted))
	for _, row := range admitted {
		wakeIDs = append(wakeIDs, row.WakeID)
	}
	wakeOwner := wakeClaimOwner(admitted)
	if err := c.Wakes.store.MarkWakeDeliverySubmittingOwned(deliveryCtx, wakeIDs, wakeOwner, false, c.Wakes.wakeClaimValidAfter()); err != nil {
		releaseClaims()
		return false, err
	}
	if deliveryErr := c.Deliver(deliveryCtx, parentSessionID, rootScopeID, digest, wakeIDs); deliveryErr != nil {
		if deliveryCtx.Err() != nil {
			return false, fmt.Errorf("wake delivery lease lost: %w", ErrWakeClaimFenceLost)
		}
		_, accepted := AcceptedDelivery(deliveryErr)
		handledErr := c.handleDeliveryError(ctx, deliveryCtx, parentSessionID, rootScopeID, digest, admitted, nil, wakeIDs, claims, stopClaimRenewal, stopWakeClaimRenewal, deliveryErr)
		return accepted, handledErr
	}
	if deliveryCtx.Err() != nil {
		return false, fmt.Errorf("wake delivery lease lost: %w", ErrWakeClaimFenceLost)
	}
	// Self-check resumes the same parked turn through the legacy Deliver hook,
	// so it must emit the same wake-start milestone as MaybeWakeParent; otherwise
	// the durable audit sees only the later settled closure.
	stopClaimRenewal()
	stopWakeClaimRenewal()
	if ctx.Err() != nil || wakeDeliveryLeaseLost(deliveryCtx) {
		return false, fmt.Errorf("wake delivery lease lost: %w", ErrWakeClaimFenceLost)
	}
	acceptedErr := c.Wakes.store.MarkWakeDeliveryAcceptedOwned(ctx, wakeIDs, wakeOwner, false, c.Wakes.wakeClaimValidAfter())
	if acceptedErr != nil {
		return true, acceptedErr
	}
	c.announceResume(ctx, parentSessionID, rootScopeID, admitted, nil)
	submittedErr := c.markClaimsSubmitted(ctx, claims, wakeIDs)
	var consumptionErr error
	if submittedErr == nil && acceptedErr == nil {
		consumptionErr = c.Wakes.store.CommitWakeConsumptionOwned(
			ctx,
			admitted,
			wakeOwner,
			c.Wakes.wakeClaimValidAfter(),
			c.clock(),
			"model_turn",
			"",
		)
		if consumptionErr != nil {
			consumptionErr = joinDeliveryErrors(consumptionErr, c.releaseForRetry(ctx, admitted))
		}
	} else {
		releaseErr := c.releaseForRetry(ctx, admitted)
		consumptionErr = releaseErr
	}
	return true, joinDeliveryErrors(submittedErr, joinDeliveryErrors(acceptedErr, consumptionErr))
}

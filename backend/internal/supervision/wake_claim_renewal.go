package supervision

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// startClaimRenewal keeps a live exactly-once claim warm while its delivery is
// in flight. A model turn can easily outlive ClaimStaleAfter; without the
// heartbeat another host's recovery would converge the row to submitted while
// the first delivery is still running. Renewal is best-effort: store failures
// leave the original claim in place, and recovery's conservative
// submitted/recovered_unknown fallback still prevents a duplicate model turn.
//
// The returned stop function is idempotent and waits for the heartbeat
// goroutine to exit. A no-op stop is returned when admission is unwired.
func (c *WakeConsumer) startClaimRenewal(claims map[string]ResumeClaim) func() {
	noop := func() {}
	if c == nil || c.Admission == nil || len(claims) == 0 {
		return noop
	}
	interval := c.Admission.ClaimRenewInterval()
	if interval <= 0 {
		return noop
	}
	snapshot := make([]ResumeClaim, 0, len(claims))
	for _, claim := range claims {
		if claim.ClaimKey == "" {
			continue
		}
		snapshot = append(snapshot, claim)
	}
	if len(snapshot) == 0 {
		return noop
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				for index, claim := range snapshot {
					renewed, ok, err := c.Admission.RenewClaim(ctx, claim)
					if err == nil && ok {
						// Keep the private heartbeat snapshot fenced with the
						// newest version. The caller refreshes its map after
						// stopping this goroutine, before any final CAS.
						snapshot[index] = renewed
					}
				}
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
			// Publish only tokens this worker actually obtained through a
			// successful renewal. Reading the row by key here would let a
			// worker that lost its lease adopt a successor's fencing token.
			for _, claim := range snapshot {
				if claim.ClaimKey != "" {
					claims[claim.ClaimKey] = claim
				}
			}
		})
	}
}

// startWakeClaimRenewal keeps the ordinary wake lease valid while digest
// assembly and host delivery are in flight. The scheduler gives one unique
// owner to every drain; a stale worker therefore cannot renew or mutate a
// successor's claim.
func (c *WakeConsumer) startWakeClaimRenewal(parent context.Context, wakes []WakePending) (context.Context, func()) {
	noop := func() {}
	if c == nil || c.Wakes == nil || c.Wakes.store == nil || len(wakes) == 0 {
		return parent, noop
	}
	owner := wakeClaimOwner(wakes)
	if owner == "" {
		return parent, noop
	}
	ids := wakeIDsOf(wakes)
	if len(ids) == 0 {
		return parent, noop
	}
	interval := c.Wakes.wakeClaimRenewInterval()
	if interval <= 0 {
		return parent, noop
	}

	deliveryCtx, cancelDelivery := context.WithCancelCause(parent)
	renewalCtx, cancelRenewal := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-renewalCtx.Done():
				return
			case <-ticker.C:
				now := c.clock()
				renewed, err := c.Wakes.store.RenewWakePending(renewalCtx, ids, owner, now, now.Add(-c.Wakes.claimStaleAfter))
				if err != nil || renewed != int64(len(ids)) {
					cancelDelivery(ErrWakeClaimFenceLost)
					return
				}
			}
		}
	}()

	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancelRenewal()
			<-done
			cancelDelivery(context.Canceled)
		})
	}
	return deliveryCtx, stop
}

func wakeDeliveryLeaseLost(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	return errors.Is(context.Cause(ctx), ErrWakeClaimFenceLost)
}

func wakeClaimOwner(wakes []WakePending) string {
	for _, wake := range wakes {
		if owner := strings.TrimSpace(wake.ClaimedBy); owner != "" {
			return owner
		}
	}
	return ""
}

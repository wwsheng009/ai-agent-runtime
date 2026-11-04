package supervision

import (
	"context"
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
				for _, claim := range snapshot {
					_, _ = c.Admission.RenewClaim(ctx, claim)
				}
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
}

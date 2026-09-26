package supervision

import (
	"context"
	"fmt"
	"strings"
)

// ResolvePendingWakesForSession drops the pending wake obligations targeted at a
// session that is closing, claimed or not. A wake only means "start the next
// turn of this session for this evidence"; once the session is gone there is no
// turn left to deliver it, while the durable notification rows (and the job
// store behind a background job) keep the evidence for the next natural turn or
// resume. Leaving them behind would replay a stale digest as an extra turn on
// top of the preflight digest after a resume.
//
// Best-effort by design: callers on shutdown paths ignore the error, because a
// supervision outage must never block the session from closing.
func ResolvePendingWakesForSession(ctx context.Context, store Store, rootScopeID, sessionID string) (int, error) {
	if store == nil {
		return 0, nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return 0, nil
	}
	wakes, err := store.ListWakePending(ctx, WakeFilter{
		RootScopeID:           strings.TrimSpace(rootScopeID),
		TargetParentSessionID: sessionID,
	})
	if err != nil {
		return 0, fmt.Errorf("supervision: list session wakes: %w", err)
	}
	resolved := 0
	for _, wake := range wakes {
		if strings.TrimSpace(wake.WakeID) == "" {
			continue
		}
		if err := store.ResolveWakePending(ctx, wake.WakeID); err != nil {
			return resolved, fmt.Errorf("supervision: resolve session wake: %w", err)
		}
		resolved++
	}
	return resolved, nil
}

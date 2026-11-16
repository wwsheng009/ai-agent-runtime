package runtimehost

import (
	"context"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

// TeamOwnerAllowed reports whether a supervisor may start a team loop without
// first racing another durable owner. It is advisory for scheduling only: the
// orchestrator still acquires and renews the authoritative lease in RunWithWake,
// so a stale lease always fails closed there.
//
// A missing store means the host is running single-process/in-memory; allow the
// loop. An unreadable store is treated as "not allowed" to avoid starting a
// second owner on an unverified lease.
func TeamOwnerAllowed(ctx context.Context, store team.Store, teamID, ownerID string, now time.Time) bool {
	if store == nil {
		return true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	lease, err := store.GetOrchestratorLease(ctx, strings.TrimSpace(teamID))
	if err != nil {
		return false
	}
	if lease == nil || lease.LeaseUntil.IsZero() || !now.Before(lease.LeaseUntil) {
		return true
	}
	current := strings.TrimSpace(ownerID)
	if current == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(lease.OwnerID), current)
}

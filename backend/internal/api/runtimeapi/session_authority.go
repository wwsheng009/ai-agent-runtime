package runtimeapi

import (
	"context"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionruntime"
)

// sessionAuthoritySnapshot describes execution authority and the concrete
// stores backing the observation. A lease holder is the execution authority;
// a matching store path is only an identifier, never proof of authority.
func (h *Handler) sessionAuthoritySnapshot(ctx context.Context, sessionID string) sessionruntime.AuthorityInfo {
	var runtimeStore any
	var eventStore any
	var leaseStore any
	var lease *chat.SessionLease
	if h != nil {
		runtimeStore = h.getSessionRuntimeStore()
		eventStore = h.getSessionEventStore()
		if store := h.getSessionLeaseStore(); store != nil {
			leaseStore = store
			if ctx == nil {
				ctx = context.Background()
			}
			lease, _ = store.GetLease(ctx, sessionID)
		}
	}
	input := sessionruntime.AuthorityInput{
		LocalAuthority: sessionruntime.AuthorityRuntimeServer,
		RuntimeStore:   runtimeStore,
		EventStore:     eventStore,
		LeaseStore:     leaseStore,
		Now:            time.Now().UTC(),
	}
	if lease != nil {
		input.OwnerID = lease.OwnerID
		input.OwnerKind = lease.OwnerKind
		input.PID = lease.PID
		input.Hostname = lease.Hostname
		input.ExpiresAt = lease.ExpiresAt
	}
	return sessionruntime.BuildAuthorityInfo(input)
}

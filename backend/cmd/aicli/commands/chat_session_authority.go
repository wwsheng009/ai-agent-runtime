package commands

import (
	"context"
	"strings"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionruntime"
)

func chatSessionAuthorityInfo(session *ChatSession) *sessionruntime.AuthorityInfo {
	if session == nil {
		return nil
	}
	sessionID := currentRuntimeSessionID(session)
	if host := session.LocalRuntimeHost; host != nil {
		var lease *runtimechat.SessionLease
		leaseStore := host.LeaseStore
		if leaseStore == nil {
			leaseStore, _ = host.RuntimeStore.(runtimechat.SessionLeaseStore)
		}
		if leaseStore != nil {
			lease, _ = leaseStore.GetLease(context.Background(), sessionID)
		}
		input := sessionruntime.AuthorityInput{
			LocalAuthority: sessionruntime.AuthorityAICLILocal,
			RuntimeStore:   host.RuntimeStore,
			EventStore:     host.EventStore,
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
		info := sessionruntime.BuildAuthorityInfo(input)
		return &info
	}
	if executor, ok := session.ChatExecutor.(*aicliRuntimeServerChatExecutor); ok {
		return executor.cachedAuthority(strings.TrimSpace(sessionID))
	}
	return nil
}

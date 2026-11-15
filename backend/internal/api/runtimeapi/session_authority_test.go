package runtimeapi

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionruntime"
)

func TestSessionAuthoritySnapshotUsesLiveLeaseNotStorePath(t *testing.T) {
	handler := NewHandler(nil, nil, nil)
	store := chat.NewInMemoryRuntimeStore(16)
	handler.sessionRuntimeStore = store
	handler.sessionEventStore = store

	now := time.Now().UTC()
	_, err := store.AcquireLease(context.Background(), chat.LeaseRequest{
		SessionID: "session-foreign",
		OwnerID:   "aicli-actor:42",
		OwnerKind: "aicli-actor",
		PID:       42,
		Hostname:  "cli-host",
		TTL:       time.Minute,
		Now:       now,
	})
	require.NoError(t, err)

	foreign := handler.sessionAuthoritySnapshot(context.Background(), "session-foreign")
	require.Equal(t, sessionruntime.AuthorityAICLILocal, foreign.ExecutionAuthority)
	require.False(t, foreign.Authoritative)
	require.Equal(t, "lease", foreign.AuthoritySource)
	require.Equal(t, "aicli-actor", foreign.LeaseOwnerKind)
	require.Equal(t, 42, foreign.LeaseOwnerPID)
	require.Equal(t, "cli-host", foreign.LeaseOwnerHostname)
	require.NotEmpty(t, foreign.RuntimeStoreID)
	require.Equal(t, foreign.RuntimeStoreID, foreign.LeaseStoreID)
	require.Equal(t, foreign.LeaseStoreID, foreign.AuthorityDomain)

	unowned := handler.sessionAuthoritySnapshot(context.Background(), "session-unowned")
	require.Equal(t, sessionruntime.AuthorityUnowned, unowned.ExecutionAuthority)
	require.True(t, unowned.Authoritative)
	require.Equal(t, "unowned", unowned.AuthoritySource)
}

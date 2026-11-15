package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionruntime"
)

func TestChatSessionAuthorityInfoLocalAndRemoteCache(t *testing.T) {
	store := runtimechat.NewInMemoryRuntimeStore(8)
	runtimeSession := runtimechat.NewSession("authority-user")
	now := time.Now().UTC()
	_, err := store.AcquireLease(context.Background(), runtimechat.LeaseRequest{
		SessionID: runtimeSession.ID,
		OwnerID:   "aicli-actor:" + runtimeSession.ID,
		OwnerKind: "aicli-actor",
		PID:       99,
		Hostname:  "cli-host",
		TTL:       time.Minute,
		Now:       now,
	})
	require.NoError(t, err)

	local := &ChatSession{
		RuntimeSession: runtimeSession,
		LocalRuntimeHost: &localChatRuntimeHost{
			RuntimeStore: store,
			EventStore:   store,
		},
	}
	info := chatSessionAuthorityInfo(local)
	require.NotNil(t, info)
	require.Equal(t, sessionruntime.AuthorityAICLILocal, info.ExecutionAuthority)
	require.True(t, info.Authoritative)
	require.Equal(t, "aicli-actor", info.LeaseOwnerKind)
	require.Equal(t, 99, info.LeaseOwnerPID)
	require.NotEmpty(t, info.RuntimeStoreID)
	require.Contains(t, buildChatStatusAuthorityValue(local), "aicli-local · authoritative · lease")

	executor := &aicliRuntimeServerChatExecutor{}
	remote := &ChatSession{
		RuntimeSession: runtimeSession,
		ChatExecutor:   executor,
	}
	require.Nil(t, chatSessionAuthorityInfo(remote))
	executor.cacheAuthority(runtimeSession.ID, sessionruntime.AuthorityInfo{
		ExecutionAuthority: sessionruntime.AuthorityRuntimeServer,
		Authoritative:      true,
		AuthoritySource:    "lease",
		RuntimeStoreID:     "sqlite:/server/runtime.sqlite",
	})
	cached := chatSessionAuthorityInfo(remote)
	require.NotNil(t, cached)
	require.Equal(t, sessionruntime.AuthorityRuntimeServer, cached.ExecutionAuthority)
	require.Equal(t, "sqlite:/server/runtime.sqlite", cached.RuntimeStoreID)
}

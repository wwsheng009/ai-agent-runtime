package commands

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// metadataCountingSessionStore 记录审计路径实际调用的是元数据读取还是完整 Load。
type metadataCountingSessionStore struct {
	runtimechat.SessionStorage
	loadCalls     int
	metadataCalls int
}

func (s *metadataCountingSessionStore) Load(ctx context.Context, sessionID string) (*runtimechat.Session, error) {
	s.loadCalls++
	return s.SessionStorage.Load(ctx, sessionID)
}

func (s *metadataCountingSessionStore) LoadMetadata(ctx context.Context, sessionID string) (*runtimechat.Session, error) {
	s.metadataCalls++
	return s.SessionStorage.Load(ctx, sessionID)
}

// 审计/对账只需要「存在性 + 状态」，必须优先走元数据读取：完整 Load 会在单连接
// 会话库上与启动期历史分页、/web/api/status 快照相互饿死。
func TestLocalAgentSessionBindingLookupPrefersMetadataReader(t *testing.T) {
	ctx := context.Background()
	store := &metadataCountingSessionStore{SessionStorage: runtimechat.NewInMemoryStorage()}
	session := runtimechat.NewSession("audit-metadata-user")
	require.NoError(t, store.Save(ctx, session))

	registry := &localActorRegistry{Host: &localChatRuntimeHost{SessionStore: store}}
	snapshot, err := registry.localAgentSessionBindingLookup()(ctx, session.ID)
	require.NoError(t, err)
	require.True(t, snapshot.Exists)
	require.False(t, snapshot.Closed)
	require.Equal(t, 1, store.metadataCalls)
	require.Zero(t, store.loadCalls, "审计不得为一次存在性判定反序列化整段历史")
}

// 不支持元数据读取的存储必须回退到完整 Load，判定语义不变。
func TestLocalAgentSessionBindingLookupFallsBackToLoad(t *testing.T) {
	ctx := context.Background()
	store := runtimechat.NewInMemoryStorage()
	session := runtimechat.NewSession("audit-fallback-user")
	require.NoError(t, store.Save(ctx, session))

	registry := &localActorRegistry{Host: &localChatRuntimeHost{SessionStore: store}}
	lookup := registry.localAgentSessionBindingLookup()

	snapshot, err := lookup(ctx, session.ID)
	require.NoError(t, err)
	require.True(t, snapshot.Exists)

	missing, err := lookup(ctx, "missing-session")
	require.NoError(t, err)
	require.False(t, missing.Exists, "缺失会话必须报告 Exists=false 而不是错误")
}

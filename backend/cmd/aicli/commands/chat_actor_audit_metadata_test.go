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

// 对账扫描（sweepStaleLocalAgentRegistry → localAgentRegistryTerminalState）
// 同样只需要「存在性 + state」，必须与 binding lookup 一样优先走元数据读取。
//
// 为什么单独钉住：这条路径每个对账轮次要为注册表里**每条**记录调用一次。
// 生产实例（运行 14h）的 pprof 把 129 GB ScanColumn + 78 GB json.makeString
// 记在 `Load → loadPromptMessages` 上，占该进程累计分配的 42.7%；
// 一旦有人把这里的 LoadMetadata 改回 Load，内存会立刻回到同一量级。
func TestLocalAgentRegistryTerminalStatePrefersMetadataReader(t *testing.T) {
	ctx := context.Background()
	store := &metadataCountingSessionStore{SessionStorage: runtimechat.NewInMemoryStorage()}
	session := runtimechat.NewSession("registry-terminal-user")
	require.NoError(t, store.Save(ctx, session))

	registry := &localActorRegistry{Host: &localChatRuntimeHost{SessionStore: store}}
	terminal, stale, err := registry.localAgentRegistryTerminalState(ctx, session.ID)
	require.NoError(t, err)

	// 全新 active 会话：不是终态，也不 stale。
	require.False(t, terminal)
	require.False(t, stale)
	require.Equal(t, 1, store.metadataCalls,
		"对账必须走 LoadMetadata")
	require.Zero(t, store.loadCalls,
		"对账不得为一次终态判定反序列化整段 prompt 历史")
}

// 未实现 SessionStorageMetadataReader 的存储必须安全回落到 Load：
// 行为与改动前一致，不能因为缺能力而判定错误。
func TestLocalAgentRegistryTerminalStateFallsBackWithoutMetadataReader(t *testing.T) {
	ctx := context.Background()
	inner := runtimechat.NewInMemoryStorage()
	session := runtimechat.NewSession("registry-terminal-fallback-user")
	require.NoError(t, inner.Save(ctx, session))

	// 只暴露 SessionStorage（不实现 LoadMetadata）。
	registry := &localActorRegistry{Host: &localChatRuntimeHost{SessionStore: inner}}
	terminal, stale, err := registry.localAgentRegistryTerminalState(ctx, session.ID)
	require.NoError(t, err)
	require.False(t, terminal)
	require.False(t, stale)
}

// 会话不存在时必须报告 (terminal=true, stale=true)，语义与改动前一致。
func TestLocalAgentRegistryTerminalStateMissingSession(t *testing.T) {
	ctx := context.Background()
	store := &metadataCountingSessionStore{SessionStorage: runtimechat.NewInMemoryStorage()}
	registry := &localActorRegistry{Host: &localChatRuntimeHost{SessionStore: store}}

	terminal, stale, err := registry.localAgentRegistryTerminalState(ctx, "session_does_not_exist")
	require.NoError(t, err)
	require.True(t, terminal, "缺失会话视为终态")
	require.True(t, stale, "缺失会话视为 stale")
	require.Equal(t, 1, store.metadataCalls)
	require.Zero(t, store.loadCalls)
}


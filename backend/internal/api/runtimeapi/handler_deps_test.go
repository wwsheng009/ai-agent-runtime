package runtimeapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimehost"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

// TestSessionDepsPopulatesInfraAndContext 验证 E3.1b-1 的依赖装配：
// 存储/事件 getter 被填好，会话元数据被派生成 SessionContext。
func TestSessionDepsPopulatesInfraAndContext(t *testing.T) {
	ctx := context.Background()
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(sessionManager.Stop)

	handler := NewHandler(nil, nil, nil)
	handler.SetSessionManager(sessionManager)

	session, err := sessionManager.Create(ctx, "user-deps")
	require.NoError(t, err)
	session.Metadata.Context = map[string]interface{}{sessionmeta.WorkspacePath: "/workspace/deps"}
	require.NoError(t, sessionManager.Update(ctx, session))

	deps := handler.SessionDeps(session.ID)
	require.NotNil(t, deps.SessionStorage)
	require.NotNil(t, deps.RuntimeStore)
	require.NotNil(t, deps.EventStore)
	require.NotNil(t, deps.EventBus)
	require.NotNil(t, deps.AcquireSessionLeaseNoWait)
	require.NotNil(t, deps.AcquireSessionLease)
	require.NotNil(t, deps.PrepareAgent)
	require.NotNil(t, deps.BuildLoopConfig)
	require.Equal(t, "/workspace/deps", deps.SessionContext.WorkspacePath)
}

// TestSessionDepsUnknownSessionIsZeroContext：未知会话不派生上下文（不报错）。
func TestSessionDepsUnknownSessionIsZeroContext(t *testing.T) {
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(sessionManager.Stop)
	handler := NewHandler(nil, nil, nil)
	handler.SetSessionManager(sessionManager)

	deps := handler.SessionDeps("no-such-session")
	require.NotNil(t, deps.SessionStorage)
	require.Equal(t, runtimehost.SessionContext{}, deps.SessionContext)
}

// TestSessionDepsNilHandlerIsZero：nil handler fail-closed 返回零值。
func TestSessionDepsNilHandlerIsZero(t *testing.T) {
	var handler *Handler
	require.Equal(t, runtimehost.Deps{}, handler.SessionDeps("s"))
}

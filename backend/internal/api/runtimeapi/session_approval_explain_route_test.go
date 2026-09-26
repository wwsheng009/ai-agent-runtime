package runtimeapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

// §4.13 解释调用跟随会话路由：会话解析出 provider+model 时优先使用；
// 只有半边信息时不做猜测，退回运行时默认（由 summarizeApproval 兜底）。

func TestSessionRouteForApprovalExplain(t *testing.T) {
	handler := NewHandler(nil, nil, nil)
	storage := chat.NewInMemoryStorage()
	manager := chat.NewSessionManager(storage, chat.DefaultSessionManagerConfig())
	t.Cleanup(manager.Stop)
	handler.SetSessionManager(manager)

	ctx := context.Background()
	session := chat.NewSession("session-1")
	session.ID = "session-route"
	session.Metadata.Context = map[string]interface{}{
		sessionmeta.EffectiveProvider: "openai",
		sessionmeta.EffectiveModel:    "gpt-5",
	}
	require.NoError(t, storage.Save(ctx, session))

	provider, model := handler.sessionRouteForApprovalExplain(ctx, "session-route")
	require.Equal(t, "openai", provider)
	require.Equal(t, "gpt-5", model)

	// effective 缺失时回退 requested/provider_name 形态（与 /runtime 路由透传同源）。
	session.Metadata.Context = map[string]interface{}{
		sessionmeta.ProviderName: "anthropic",
		sessionmeta.Model:        "claude-sonnet",
	}
	require.NoError(t, storage.Save(ctx, session))
	provider, model = handler.sessionRouteForApprovalExplain(ctx, "session-route")
	require.Equal(t, "anthropic", provider)
	require.Equal(t, "claude-sonnet", model)

	// 只有模型、没有 provider：不回退猜测，交给运行时默认解析链。
	session.Metadata.Context = map[string]interface{}{sessionmeta.Model: "gpt-5"}
	require.NoError(t, storage.Save(ctx, session))
	provider, model = handler.sessionRouteForApprovalExplain(ctx, "session-route")
	require.Empty(t, provider)
	require.Empty(t, model)

	// 会话不存在 / 无 sessionManager：静默空值（解释退回运行时默认）。
	provider, model = handler.sessionRouteForApprovalExplain(ctx, "session-missing")
	require.Empty(t, provider)
	require.Empty(t, model)
	bare := NewHandler(nil, nil, nil)
	provider, model = bare.sessionRouteForApprovalExplain(ctx, "session-route")
	require.Empty(t, provider)
	require.Empty(t, model)
}

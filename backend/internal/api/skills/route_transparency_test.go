package skills

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

func TestAgentChatRouteTransparencyResolvesAliasAndPersistsCanonicalContext(t *testing.T) {
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultProvider: "canonical-provider", DefaultModel: "canonical-model"})
	require.NoError(t, runtime.RegisterProvider("canonical-provider", &testLLMProvider{name: "canonical-provider"}))
	require.NoError(t, runtime.RegisterProviderAlias("configured-alias", "canonical-provider"))

	session := chat.NewSession("tester")
	sessionmeta.Set(session.Metadata.Context, sessionmeta.PermissionMode, "plan")
	route := resolveAgentChatRouteTransparency(
		runtime,
		session,
		"configured-alias",
		"friendly-model",
		"xhigh",
		"configured-alias",
		"canonical-model",
		"high",
	)
	require.Equal(t, "configured-alias", route.RequestedProvider)
	require.Equal(t, "canonical-provider", route.EffectiveProvider)
	require.Equal(t, "friendly-model", route.RequestedModel)
	require.Equal(t, "canonical-model", route.EffectiveModel)
	require.Equal(t, "xhigh", route.RequestedReasoningEffort)
	require.Equal(t, "high", route.EffectiveReasoningEffort)
	require.Equal(t, "plan", route.RequestedPermissionMode)
	require.Equal(t, "plan", route.EffectivePermissionMode)

	persistExecutionRouteTransparency(session, route)
	context := session.Metadata.Context
	assert.Equal(t, "configured-alias", sessionmeta.String(context, sessionmeta.RequestedProvider))
	assert.Equal(t, "canonical-provider", sessionmeta.String(context, sessionmeta.EffectiveProvider))
	assert.Equal(t, "canonical-provider", sessionmeta.String(context, sessionmeta.ProviderName))
	assert.Equal(t, "friendly-model", sessionmeta.String(context, sessionmeta.RequestedModel))
	assert.Equal(t, "canonical-model", sessionmeta.String(context, sessionmeta.EffectiveModel))
	assert.Equal(t, "canonical-model", sessionmeta.String(context, sessionmeta.Model))
	assert.Equal(t, "xhigh", sessionmeta.String(context, sessionmeta.RequestedReasoningEffort))
	assert.Equal(t, "high", sessionmeta.String(context, sessionmeta.EffectiveReasoningEffort))
	assert.Equal(t, "high", sessionmeta.String(context, sessionmeta.ReasoningEffort))
}

func TestAttachSessionExecutionRouteAddsRouteToRuntimeAPIEnvelope(t *testing.T) {
	manager := chat.NewSessionManager(chat.NewInMemoryStorage(), &chat.SessionManagerConfig{
		TTL: time.Hour, MaxHistory: 20, CleanupInterval: time.Hour, IdleTimeout: time.Hour,
	})
	defer manager.Stop()
	session, err := manager.CreateSession(context.Background(), "tester")
	require.NoError(t, err)
	persistExecutionRouteTransparency(session, executionRouteTransparency{
		RequestedProvider: "configured-alias",
		EffectiveProvider: "canonical-provider",
		RequestedModel:    "friendly-model",
		EffectiveModel:    "canonical-model",
	})
	require.NoError(t, manager.Update(context.Background(), session))

	handler := &Handler{}
	handler.SetSessionManager(manager)
	payload := handler.attachSessionExecutionRoute(context.Background(), session.ID, map[string]interface{}{"result": "ok"})
	assert.Equal(t, "ok", payload["result"])
	assert.Equal(t, "configured-alias", payload["requested_provider"])
	assert.Equal(t, "canonical-provider", payload["effective_provider"])
	assert.Equal(t, "friendly-model", payload["requested_model"])
	assert.Equal(t, "canonical-model", payload["effective_model"])
}

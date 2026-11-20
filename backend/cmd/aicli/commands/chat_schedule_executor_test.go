package commands

import (
	"testing"

	"github.com/stretchr/testify/require"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimehost"
)

func TestScheduleRouteOverrideTrimsAndOmitsEmpty(t *testing.T) {
	require.Nil(t, scheduleRouteOverride(runtimehost.TurnRequest{}))
	override := scheduleRouteOverride(runtimehost.TurnRequest{
		Provider:        " provider-a ",
		Model:           " model-a ",
		ReasoningEffort: " high ",
	})
	require.NotNil(t, override)
	require.Equal(t, "provider-a", override.Provider)
	require.Equal(t, "model-a", override.Model)
	require.Equal(t, "high", override.ReasoningEffort)
}

func TestNewAICLIScheduleExecutorRequiresSessionManagerAndHost(t *testing.T) {
	_, err := newAICLIScheduleExecutor(runtimecfg.DefaultRuntimeConfig(), &ChatSession{}, nil)
	require.ErrorContains(t, err, "session manager")

	manager := runtimechat.NewSessionManager(runtimechat.NewInMemoryStorage(), nil)
	t.Cleanup(manager.Stop)
	_, err = newAICLIScheduleExecutor(runtimecfg.DefaultRuntimeConfig(), &ChatSession{
		SessionManager: manager,
	}, nil)
	require.ErrorContains(t, err, "local runtime host")
}

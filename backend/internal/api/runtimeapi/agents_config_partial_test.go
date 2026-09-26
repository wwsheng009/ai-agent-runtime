package runtimeapi

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// TestSessionAgentController_PartialAgentsConfigKeepsDepthCeiling 是 CLI 同名用例的 API
// 侧对照（方案 附录 B 未决项 4）：`agents: {maxThreads: 4}` 这种部分写入的配置块里
// maxDepth 是未设置（0），必须回落到内置默认上限，而不是被 enforceSpawnLimits 读成
// 「无上限」。
func TestSessionAgentController_PartialAgentsConfigKeepsDepthCeiling(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	defer handler.getSessionHub().StopAll()
	handler.SetSessionManager(sessionManager)

	cfg := runtimecfg.DefaultRuntimeConfig()
	// 部分写入：除 maxThreads 外全部留空。
	cfg.Agents = runtimecfg.AgentsConfig{MaxThreads: 4}
	handler.SetRuntimeConfig(cfg, "")

	rootSession, err := sessionManager.Create(ctx, "user-partial-agents-config")
	require.NoError(t, err)

	controller := handler.getAgentSessionController()
	require.NotNil(t, controller)

	_, err = controller.Spawn(ctx, rootSession.ID, toolbroker.SpawnAgentArgs{ID: "partial-parent"})
	require.NoError(t, err)

	defaultCeiling := runtimecfg.NormalizeAgentsConfig(runtimecfg.AgentsConfig{}).MaxDepth
	_, err = controller.Spawn(ctx, "partial-parent", toolbroker.SpawnAgentArgs{ID: "partial-child"})
	require.Error(t, err, "the built-in depth ceiling (%d) must apply to a partially configured agents block", defaultCeiling)
	require.Contains(t, err.Error(), "depth limit")
	require.Contains(t, err.Error(), "requested_depth=2")

	// 显式写入更大的上限后，同一深度必须放行——证明上面的拒绝来自上限而不是别的门。
	cfg.Agents.MaxDepth = defaultCeiling + 1
	handler.SetRuntimeConfig(cfg, "")
	_, err = controller.Spawn(ctx, "partial-parent", toolbroker.SpawnAgentArgs{ID: "partial-child"})
	require.NoError(t, err)
}

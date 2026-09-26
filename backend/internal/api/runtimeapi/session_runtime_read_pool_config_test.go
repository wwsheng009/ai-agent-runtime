package runtimeapi

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// P1.7/Q4：runtime-server 的 sessionRuntime.readPool 配置必须落到 store 构造，
// 且配置变更触发 store 重建（读池参数只在构造期生效）。
func TestSessionRuntimeStoreReadPoolConfigWiring(t *testing.T) {
	ctx := context.Background()
	handler := &Handler{}
	handler.getRuntimeEventBus()
	storePath := filepath.Join(t.TempDir(), "session_runtime.sqlite")
	// 注册在 TempDir 之后：LIFO 下先关 store，TempDir 清理才能删除库文件。
	t.Cleanup(func() {
		closeRuntimeStore(handler.sessionRuntimeStore, handler.sessionEventStore)
	})
	config := &runtimecfg.RuntimeConfig{SessionRuntime: runtimecfg.SessionRuntimeConfig{StorePath: storePath}}

	poolStats := func() chat.RuntimeStorePoolStats {
		statter, ok := handler.sessionEventStore.(interface {
			PoolStats() chat.RuntimeStorePoolStats
		})
		require.True(t, ok, "装配的 store 必须支持 PoolStats")
		return statter.PoolStats()
	}

	changed, err := handler.refreshSessionRuntimeStore(config, "")
	require.NoError(t, err)
	require.True(t, changed)
	_, err = handler.sessionEventStore.AppendEvent(ctx, runtimeevents.Event{
		Type: "readpool.config", SessionID: "readpool-config", Payload: map[string]interface{}{"x": 1},
	})
	require.NoError(t, err)
	_, err = handler.sessionEventStore.ListEvents(ctx, "readpool-config", 0, 10)
	require.NoError(t, err)
	stats := poolStats()
	require.True(t, stats.ReadPoolOpen, "默认配置必须启用读池")
	require.Equal(t, 4, stats.ReadMaxOpenConnections)
	require.False(t, stats.ReadPoolDegraded, "reason=%s", stats.ReadPoolDegradedReason)

	// 同配置重复刷新不得重建（避免每次 reload 都换 store）。
	changed, err = handler.refreshSessionRuntimeStore(config, "")
	require.NoError(t, err)
	require.False(t, changed, "配置未变时不得重建 store")

	// 回滚开关：disable=true 必须重建 store，且读池不打开。
	config.SessionRuntime.ReadPool.Disable = true
	changed, err = handler.refreshSessionRuntimeStore(config, "")
	require.NoError(t, err)
	require.True(t, changed, "读池配置变更必须重建 store")
	_, err = handler.sessionEventStore.AppendEvent(ctx, runtimeevents.Event{
		Type: "readpool.config", SessionID: "readpool-config", Payload: map[string]interface{}{"x": 2},
	})
	require.NoError(t, err)
	_, err = handler.sessionEventStore.ListEvents(ctx, "readpool-config", 0, 10)
	require.NoError(t, err)
	stats = poolStats()
	require.False(t, stats.ReadPoolOpen, "disable=true 时读必须回落写池")
	require.Zero(t, stats.ReadMaxOpenConnections)
}

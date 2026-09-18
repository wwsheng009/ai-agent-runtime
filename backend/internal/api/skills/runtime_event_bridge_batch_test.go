package skills

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// P1.5 M3 接线回归（分册 §7.9–10 的 runtime-server 侧）。

func newBatchBridgeTestHandler(t *testing.T, enabled bool) (*Handler, *chat.SQLiteRuntimeStore) {
	t.Helper()
	store, err := chat.NewSQLiteRuntimeStore(&chat.RuntimeStoreConfig{
		Path: filepath.Join(t.TempDir(), "session_runtime.sqlite"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	handler := &Handler{
		runtimeConfig: &runtimecfg.RuntimeConfig{SessionRuntime: runtimecfg.SessionRuntimeConfig{
			EventPersist: runtimecfg.EventPersistConfig{
				BatchingEnabled: enabled,
				BatchSize:       2,
				FlushInterval:   20 * time.Millisecond,
				ShutdownTimeout: time.Second,
			},
		}},
	}
	handler.sessionRuntimeStore = store
	handler.sessionEventStore = store
	handler.getRuntimeEventBus()
	t.Cleanup(handler.CloseRuntimeEventPersistence)
	return handler, store
}

func (h *Handler) testEventPersistBuffer() *chat.EventPersistBuffer {
	h.runtimeEventPersistMu.Lock()
	defer h.runtimeEventPersistMu.Unlock()
	return h.runtimeEventPersistBuffer
}

func TestRuntimeServerEventBridgeBatchesAndClosesCleanly(t *testing.T) {
	handler, store := newBatchBridgeTestHandler(t, true)
	buffer := handler.testEventPersistBuffer()
	require.NotNil(t, buffer)

	for index := 0; index < 3; index++ {
		handler.runtimeEventBus.Publish(runtimeevents.Event{
			Type:      "tool.completed",
			SessionID: "api-batch",
			ToolName:  "demo",
			Payload:   map[string]interface{}{"index": index},
		})
	}
	require.Eventually(t, func() bool {
		events, err := store.ListEvents(context.Background(), "api-batch", 0, 0)
		return err == nil && len(events) == 3
	}, 3*time.Second, 10*time.Millisecond)
	require.Equal(t, int64(3), buffer.Stats().FlushedEvents)

	handler.CloseRuntimeEventPersistence()
	require.Nil(t, handler.testEventPersistBuffer())
	events, err := store.ListEvents(context.Background(), "api-batch", 0, 0)
	require.NoError(t, err)
	require.Len(t, events, 3)
}

func TestRuntimeServerEventBridgeSyncWhenDisabled(t *testing.T) {
	handler, store := newBatchBridgeTestHandler(t, false)
	require.Nil(t, handler.testEventPersistBuffer())

	handler.runtimeEventBus.Publish(runtimeevents.Event{Type: "tool.completed", SessionID: "api-sync"})
	events, err := store.ListEvents(context.Background(), "api-sync", 0, 0)
	require.NoError(t, err)
	require.Len(t, events, 1, "关闭批量时必须保持同步逐条落盘（P0.5 可见性不回退）")
}

// P1.5/M4：健康快照（/api/runtime/health）在启用批量时附带 persist 段，
// 关闭时保持既有响应形状（不出现 persist）。
func TestRuntimeHealthIncludesPersistStatsWhenBatchingEnabled(t *testing.T) {
	handler, _ := newBatchBridgeTestHandler(t, true)
	snapshot := handler.executionDiagnosticsSnapshot(context.Background())
	persist, ok := snapshot["persist"]
	require.True(t, ok, "启用批量时 health 快照必须包含 persist 段")
	stats, ok := persist.(chat.EventPersistBufferStats)
	require.True(t, ok)
	require.Equal(t, 2, stats.BatchSize, "快照应反映宿主配置的批大小")

	handler.CloseRuntimeEventPersistence()
	snapshot = handler.executionDiagnosticsSnapshot(context.Background())
	_, ok = snapshot["persist"]
	require.False(t, ok, "关闭后 persist 段必须消失")
}

func TestRuntimeHealthOmitsPersistStatsWhenBatchingDisabled(t *testing.T) {
	handler, _ := newBatchBridgeTestHandler(t, false)
	snapshot := handler.executionDiagnosticsSnapshot(context.Background())
	_, ok := snapshot["persist"]
	require.False(t, ok, "未启用批量时不得改变 health 响应形状")
}

// P1.7/G6：runtime store 双池统计出现在健康快照；无 store 时不出现该段。
func TestRuntimeHealthIncludesStorePoolStats(t *testing.T) {
	handler, store := newBatchBridgeTestHandler(t, false)
	ctx := context.Background()
	_, err := store.AppendEvent(ctx, runtimeevents.Event{Type: "pool.health", SessionID: "pool-health", Payload: map[string]interface{}{"x": 1}})
	require.NoError(t, err)
	_, err = store.ListEvents(ctx, "pool-health", 0, 10)
	require.NoError(t, err)

	snapshot := handler.executionDiagnosticsSnapshot(ctx)
	raw, ok := snapshot["store_pools"]
	require.True(t, ok, "装配 runtime store 时 health 必须包含 store_pools 段")
	stats, ok := raw.(chat.RuntimeStorePoolStats)
	require.True(t, ok)
	require.True(t, stats.ReadPoolOpen, "fileBacked store 的读池应已打开")
	require.False(t, stats.ReadPoolDegraded, "reason=%s", stats.ReadPoolDegradedReason)

	empty := &Handler{}
	snapshot = empty.executionDiagnosticsSnapshot(ctx)
	_, ok = snapshot["store_pools"]
	require.False(t, ok, "无 runtime store 时不得出现 store_pools 段")
}

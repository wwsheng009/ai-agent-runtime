package commands

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// P1.5 M3 接线回归（分册 §7.9–10 的 aicli 侧）。

func newPersistBridgeTestHost(t *testing.T, enabled bool) (*localChatRuntimeHost, *runtimechat.SQLiteRuntimeStore) {
	t.Helper()
	store, err := runtimechat.NewSQLiteRuntimeStore(&runtimechat.RuntimeStoreConfig{
		Path: filepath.Join(t.TempDir(), "session_runtime.sqlite"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	host := &localChatRuntimeHost{
		EventBus:   runtimeevents.NewBus(),
		EventStore: store,
		RuntimeConfig: &runtimecfg.RuntimeConfig{SessionRuntime: runtimecfg.SessionRuntimeConfig{
			EventPersist: runtimecfg.EventPersistConfig{
				BatchingEnabled: enabled,
				BatchSize:       2,
				FlushInterval:   20 * time.Millisecond,
				ShutdownTimeout: time.Second,
			},
		}},
	}
	host.bindRuntimeEventPersistence()
	t.Cleanup(host.Close)
	return host, store
}

func TestLocalRuntimeEventBridgeBatchesAndFlushesOnClose(t *testing.T) {
	host, store := newPersistBridgeTestHost(t, true)
	require.NotNil(t, host.runtimeEventBuffer)

	for index := 0; index < 3; index++ {
		host.EventBus.Publish(runtimeevents.Event{
			Type:      "tool.completed",
			SessionID: "bridge-batch",
			ToolName:  "demo",
			Payload:   map[string]interface{}{"index": index},
		})
	}
	require.Eventually(t, func() bool {
		events, err := store.ListEvents(context.Background(), "bridge-batch", 0, 0)
		return err == nil && len(events) == 3
	}, 3*time.Second, 10*time.Millisecond)

	stats := host.runtimeEventBuffer.Stats()
	require.Equal(t, int64(3), stats.FlushedEvents)
	require.Zero(t, stats.Failed)
	require.Zero(t, stats.ShutdownRemaining)

	host.Close()
	events, err := store.ListEvents(context.Background(), "bridge-batch", 0, 0)
	require.NoError(t, err)
	require.Len(t, events, 3, "Close 后尾部事件必须全部落盘")
}

func TestLocalRuntimeEventBridgeSyncPathWhenDisabled(t *testing.T) {
	host, store := newPersistBridgeTestHost(t, false)
	require.Nil(t, host.runtimeEventBuffer)

	host.EventBus.Publish(runtimeevents.Event{Type: "tool.completed", SessionID: "bridge-sync"})
	events, err := store.ListEvents(context.Background(), "bridge-sync", 0, 0)
	require.NoError(t, err)
	require.Len(t, events, 1, "关闭批量时必须保持同步逐条落盘")
}

func TestLocalRuntimeEventBridgeKeepsPersistedSeqSkipRule(t *testing.T) {
	host, store := newPersistBridgeTestHost(t, true)
	require.NotNil(t, host.runtimeEventBuffer)

	host.EventBus.Publish(runtimeevents.Event{
		Type:      "tool.completed",
		SessionID: "bridge-skip",
		Payload:   map[string]interface{}{"seq": int64(7)},
	})
	host.Close()
	events, err := store.ListEvents(context.Background(), "bridge-skip", 0, 0)
	require.NoError(t, err)
	require.Empty(t, events, "payload 携带 seq 的事件不得重复落盘")
	// 跳过发生在桥接层（入队之前），因此 buffer 既未入队也未落盘。
	require.Zero(t, host.runtimeEventBuffer.Stats().Enqueued)
}

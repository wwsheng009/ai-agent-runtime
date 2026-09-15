package chat

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// runtimeWindowTestStore 是本文件用到的最小契约：内存存储与 SQLite 存储都实现了它，
// 同一组窗口读取断言因此可以在两种实现上复用。
type runtimeWindowTestStore interface {
	AppendEvent(ctx context.Context, event runtimeevents.Event) (int64, error)
	ListEventsBefore(ctx context.Context, sessionID string, beforeSeq int64, limit int) ([]runtimeevents.Event, error)
}

// seedRuntimeWindowEvents 造 count 条事件，返回按升序排列的 seq 列表，便于按 seq 断言分页结果。
func seedRuntimeWindowEvents(t *testing.T, store runtimeWindowTestStore, sessionID string, count int) []int64 {
	t.Helper()
	seqs := make([]int64, 0, count)
	for index := 1; index <= count; index++ {
		seq, err := store.AppendEvent(context.Background(), runtimeevents.Event{
			Type:      EventAssistantMessage,
			SessionID: sessionID,
			Payload:   map[string]interface{}{"content": fmt.Sprintf("event-%d", index)},
		})
		require.NoErrorf(t, err, "造第 %d 条事件失败", index)
		seqs = append(seqs, seq)
	}
	return seqs
}

// runtimeWindowEventSeq 取生产代码注入的 payload.seq：窗口读取必须与 ListEvents
// 一致地把序号带回给调用方（前端据此拼分页游标）。
func runtimeWindowEventSeq(t *testing.T, event runtimeevents.Event, index int) int64 {
	t.Helper()
	require.NotNilf(t, event.Payload, "第 %d 条事件缺少 payload", index)
	switch value := event.Payload["seq"].(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	default:
		t.Fatalf("第 %d 条事件 payload.seq 期望数字，实际 %#v", index, event.Payload["seq"])
		return 0
	}
}

// runtimeWindowEventSeqs 按返回顺序提取 seq，用于断言升序与「取的是最近 limit 条」。
func runtimeWindowEventSeqs(t *testing.T, events []runtimeevents.Event) []int64 {
	t.Helper()
	seqs := make([]int64, 0, len(events))
	for index, event := range events {
		seqs = append(seqs, runtimeWindowEventSeq(t, event, index))
	}
	return seqs
}

// TestInMemoryRuntimeStoreListEventsBeforeUsesExclusiveUpperBound 覆盖排他上界：
// seq == beforeSeq 的事件必须被排除，返回值严格升序。
func TestInMemoryRuntimeStoreListEventsBeforeUsesExclusiveUpperBound(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryRuntimeStore(64)
	seqs := seedRuntimeWindowEvents(t, store, "session-window-bound", 5)
	require.Equal(t, []int64{1, 2, 3, 4, 5}, seqs, "内存存储应按写入顺序分配 1..5 的 seq")

	events, err := store.ListEventsBefore(ctx, "session-window-bound", 5, 0)
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 2, 3, 4}, runtimeWindowEventSeqs(t, events),
		"before_seq=5 应排除 seq=5（排他上界）并按升序返回更早事件")

	events, err = store.ListEventsBefore(ctx, "session-window-bound", 6, 0)
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 2, 3, 4, 5}, runtimeWindowEventSeqs(t, events),
		"before_seq=6 时全部 5 条都满足 seq<6，应按升序全部返回")

	events, err = store.ListEventsBefore(ctx, "session-window-bound", 1, 0)
	require.NoError(t, err)
	assert.Empty(t, events, "before_seq=1 时没有更早事件，应返回空")

	events, err = store.ListEventsBefore(ctx, "session-window-bound", 0, 0)
	require.NoError(t, err)
	assert.Empty(t, events, "before_seq=0 时没有更早事件，应返回空")
}

// TestInMemoryRuntimeStoreListEventsBeforeLimitKeepsMostRecent 覆盖 limit 截断语义：
// 截断取的是「最近 limit 条」而不是最早 limit 条（尾部优先窗口的核心）。
func TestInMemoryRuntimeStoreListEventsBeforeLimitKeepsMostRecent(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryRuntimeStore(64)
	seedRuntimeWindowEvents(t, store, "session-window-limit", 5)

	events, err := store.ListEventsBefore(ctx, "session-window-limit", 6, 2)
	require.NoError(t, err)
	assert.Equal(t, []int64{4, 5}, runtimeWindowEventSeqs(t, events),
		"limit=2 应取最近的 seq=4,5，而不是最早的 seq=1,2")

	events, err = store.ListEventsBefore(ctx, "session-window-limit", 6, 1)
	require.NoError(t, err)
	assert.Equal(t, []int64{5}, runtimeWindowEventSeqs(t, events),
		"limit=1 应取最新的 seq=5")

	events, err = store.ListEventsBefore(ctx, "session-window-limit", 6, 3)
	require.NoError(t, err)
	assert.Equal(t, []int64{3, 4, 5}, runtimeWindowEventSeqs(t, events),
		"limit=3 应取最近的 seq=3,4,5")

	events, err = store.ListEventsBefore(ctx, "session-window-limit", 4, 10)
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 2, 3}, runtimeWindowEventSeqs(t, events),
		"limit 大于可用条数时应返回全部更早事件，不补齐也不报错")

	events, err = store.ListEventsBefore(ctx, "session-window-limit", 3, 2)
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 2}, runtimeWindowEventSeqs(t, events),
		"截断后再按 before_seq 向前翻页仍应取最近的 2 条（seq=1,2 恰好是最老一页）")
}

// TestInMemoryRuntimeStoreListEventsBeforeUnlimitedAndEmpty 覆盖 limit<=0 表示不限量、
// 空会话返回空，以及不同会话之间的 seq 空间互不串号。
func TestInMemoryRuntimeStoreListEventsBeforeUnlimitedAndEmpty(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryRuntimeStore(64)
	seedRuntimeWindowEvents(t, store, "session-window-unlimited", 5)

	events, err := store.ListEventsBefore(ctx, "session-window-unlimited", 6, 0)
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 2, 3, 4, 5}, runtimeWindowEventSeqs(t, events),
		"limit=0 表示不限量，应返回全部更早事件")

	events, err = store.ListEventsBefore(ctx, "session-window-unlimited", 6, -1)
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 2, 3, 4, 5}, runtimeWindowEventSeqs(t, events),
		"limit<0 同样表示不限量")

	events, err = store.ListEventsBefore(ctx, "session-window-empty", 100, 5)
	require.NoError(t, err)
	assert.Empty(t, events, "没有事件的会话应返回空，而不是报错或 nil 之外的占位")

	seedRuntimeWindowEvents(t, store, "session-window-other", 2)
	events, err = store.ListEventsBefore(ctx, "session-window-other", 100, 0)
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 2}, runtimeWindowEventSeqs(t, events),
		"每个会话有独立 seq 空间，窗口读取不应混入其它会话的事件")
}

// TestSQLiteRuntimeStoreListEventsBeforeWindowSemantics 用同语义覆盖 SQLite 实现：
// 排他上界、升序、limit 取最近、limit<=0 不限、空会话返回空。
func TestSQLiteRuntimeStoreListEventsBeforeWindowSemantics(t *testing.T) {
	ctx := context.Background()
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
		DSN: "file:runtime-store-list-events-before-test?mode=memory&cache=shared",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	seqs := seedRuntimeWindowEvents(t, store, "session-window-sqlite", 5)
	require.Equal(t, []int64{1, 2, 3, 4, 5}, seqs, "SQLite 存储应按写入顺序分配 1..5 的 seq")

	events, err := store.ListEventsBefore(ctx, "session-window-sqlite", 5, 0)
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 2, 3, 4}, runtimeWindowEventSeqs(t, events),
		"SQLite：before_seq=5 应排除 seq=5（排他上界）并升序返回")

	events, err = store.ListEventsBefore(ctx, "session-window-sqlite", 6, 2)
	require.NoError(t, err)
	assert.Equal(t, []int64{4, 5}, runtimeWindowEventSeqs(t, events),
		"SQLite：limit=2 应取最近 2 条而不是最早 2 条")
	require.Len(t, events, 2)
	assert.Equal(t, "event-4", events[0].Payload["content"], "SQLite 往返后 payload.content 应保持不变")
	assert.Equal(t, "event-5", events[1].Payload["content"], "SQLite 往返后 payload.content 应保持不变")

	events, err = store.ListEventsBefore(ctx, "session-window-sqlite", 6, -1)
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 2, 3, 4, 5}, runtimeWindowEventSeqs(t, events),
		"SQLite：limit<=0 表示不限量")

	events, err = store.ListEventsBefore(ctx, "session-window-sqlite", 3, 5)
	require.NoError(t, err)
	assert.Equal(t, []int64{1, 2}, runtimeWindowEventSeqs(t, events),
		"SQLite：更早事件不足 limit 时返回全部")

	events, err = store.ListEventsBefore(ctx, "session-window-sqlite-missing", 100, 5)
	require.NoError(t, err)
	assert.Empty(t, events, "SQLite：没有事件的会话应返回空")
}

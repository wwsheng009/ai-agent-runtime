package chat

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// P1.5-B 缓冲回归（分册 §7.4–8）。

type fakePersistStore struct {
	mu          sync.Mutex
	events      []runtimeevents.Event
	singleCalls int
	batchCalls  int
	batchErr    func(call int) error
	block       chan struct{}
	blocked     chan struct{}
	blockOnce   sync.Once
}

func (f *fakePersistStore) AppendEvent(_ context.Context, event runtimeevents.Event) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.singleCalls++
	f.events = append(f.events, event)
	return int64(len(f.events)), nil
}

func (f *fakePersistStore) AppendEvents(ctx context.Context, events []runtimeevents.Event) ([]int64, error) {
	f.mu.Lock()
	call := f.batchCalls
	f.batchCalls++
	f.mu.Unlock()

	if f.block != nil {
		if f.blocked != nil {
			f.blockOnce.Do(func() { close(f.blocked) })
		}
		select {
		case <-f.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.batchErr != nil {
		if err := f.batchErr(call); err != nil {
			return nil, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	seqs := make([]int64, len(events))
	for index, event := range events {
		f.events = append(f.events, event)
		seqs[index] = int64(len(f.events))
	}
	return seqs, nil
}

func (f *fakePersistStore) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

func (f *fakePersistStore) eventTypes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	types := make([]string, len(f.events))
	for index, event := range f.events {
		types[index] = event.Type
	}
	return types
}

func (f *fakePersistStore) snapshot() []runtimeevents.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]runtimeevents.Event(nil), f.events...)
}

func bufferTestEvent(eventType string) runtimeevents.Event {
	return runtimeevents.Event{Type: eventType, SessionID: "buffer-session", Payload: map[string]interface{}{"type": eventType}}
}

func TestEventPersistBufferFlushTriggers(t *testing.T) {
	t.Run("size", func(t *testing.T) {
		fake := &fakePersistStore{}
		buffer := NewEventPersistBuffer(fake, EventPersistBufferConfig{BatchSize: 3, FlushInterval: time.Hour})
		t.Cleanup(func() { _ = buffer.Close(context.Background()) })
		for index := 0; index < 3; index++ {
			buffer.Enqueue(bufferTestEvent("size"))
		}
		require.Eventually(t, func() bool { return fake.count() == 3 }, 2*time.Second, 5*time.Millisecond)
		require.Equal(t, int64(1), buffer.Stats().Batches)
	})

	t.Run("interval", func(t *testing.T) {
		fake := &fakePersistStore{}
		buffer := NewEventPersistBuffer(fake, EventPersistBufferConfig{BatchSize: 100, FlushInterval: 20 * time.Millisecond})
		t.Cleanup(func() { _ = buffer.Close(context.Background()) })
		buffer.Enqueue(bufferTestEvent("interval"))
		require.Eventually(t, func() bool { return fake.count() == 1 }, 2*time.Second, 5*time.Millisecond)
	})

	t.Run("critical", func(t *testing.T) {
		fake := &fakePersistStore{}
		buffer := NewEventPersistBuffer(fake, EventPersistBufferConfig{
			BatchSize:     100,
			FlushInterval: time.Hour,
			CriticalTypes: func(eventType string) bool { return eventType == "critical" },
		})
		t.Cleanup(func() { _ = buffer.Close(context.Background()) })
		buffer.Enqueue(bufferTestEvent("normal"))
		time.Sleep(80 * time.Millisecond)
		require.Zero(t, fake.count(), "非关键事件不应在 interval 前落盘")
		buffer.Enqueue(bufferTestEvent("critical"))
		require.Eventually(t, func() bool { return fake.count() == 2 }, 2*time.Second, 5*time.Millisecond)
	})
}

func TestEventPersistBufferOverflowBlocksInsteadOfDropping(t *testing.T) {
	ctx := context.Background()
	fake := &fakePersistStore{}
	buffer := NewEventPersistBuffer(fake, EventPersistBufferConfig{
		BatchSize:     10,
		FlushInterval: time.Hour,
		QueueLimit:    1,
	})
	t.Cleanup(func() { _ = buffer.Close(ctx) })

	buffer.Enqueue(bufferTestEvent("queued"))
	buffer.Enqueue(bufferTestEvent("overflow")) // 队列深度 1 >= 上限 → 同步落盘

	stats := buffer.Stats()
	require.Equal(t, int64(1), stats.SyncFallback)
	require.Zero(t, stats.Dropped)
	require.Equal(t, 1, fake.singleCalls)

	require.NoError(t, buffer.Close(ctx))
	require.Equal(t, 2, fake.count(), "block 模式下任何事件都不得丢失")
	require.Equal(t, int64(0), buffer.Stats().ShutdownRemaining)
}

func TestEventPersistBufferOverflowDropsOnlyWhenConfigured(t *testing.T) {
	ctx := context.Background()
	fake := &fakePersistStore{}
	buffer := NewEventPersistBuffer(fake, EventPersistBufferConfig{
		BatchSize:     10,
		FlushInterval: time.Hour,
		QueueLimit:    1,
		FailMode:      EventPersistFailDrop,
	})
	t.Cleanup(func() { _ = buffer.Close(ctx) })

	buffer.Enqueue(bufferTestEvent("queued"))
	buffer.Enqueue(bufferTestEvent("overflow"))
	require.Equal(t, int64(1), buffer.Stats().Dropped)
	require.Zero(t, fake.singleCalls)
}

// P0-1：可重建的 delta 类事件溢出按 drop 处理（不产生同步落盘背压），
// 非 delta 事件仍走 FailMode 默认 block（同步落盘、不丢）。
func TestEventPersistBufferOverflowDropsStreamDeltaByType(t *testing.T) {
	ctx := context.Background()
	fake := &fakePersistStore{}
	buffer := NewEventPersistBuffer(fake, EventPersistBufferConfig{
		BatchSize:     10,
		FlushInterval: time.Hour,
		QueueLimit:    1,
		OverflowDropTypes: func(eventType string) bool {
			return eventType == "assistant_delta" || eventType == "assistant.reasoning"
		},
	})
	t.Cleanup(func() { _ = buffer.Close(ctx) })

	buffer.Enqueue(bufferTestEvent("queued"))
	buffer.Enqueue(bufferTestEvent("assistant_delta"))
	buffer.Enqueue(bufferTestEvent("assistant.reasoning"))
	stats := buffer.Stats()
	require.Equal(t, int64(2), stats.Dropped, "delta 类溢出必须丢弃计数")
	require.Zero(t, fake.singleCalls, "delta 类溢出不得触发同步落盘")

	buffer.Enqueue(bufferTestEvent("overflow"))
	require.Equal(t, 1, fake.singleCalls, "非 delta 事件溢出仍走同步落盘兜底")
	require.NoError(t, buffer.Close(ctx))
	require.Equal(t, 2, fake.count(), "block 模式下非 delta 事件不得丢失")
}

func TestEventPersistBufferRetriesOnBusyAndKeepsOrder(t *testing.T) {
	ctx := context.Background()
	fake := &fakePersistStore{batchErr: func(call int) error {
		if call == 0 {
			return fmt.Errorf("database is locked (5) (SQLITE_BUSY)")
		}
		return nil
	}}
	buffer := NewEventPersistBuffer(fake, EventPersistBufferConfig{BatchSize: 3, FlushInterval: 10 * time.Millisecond})
	t.Cleanup(func() { _ = buffer.Close(ctx) })

	buffer.Enqueue(bufferTestEvent("first"))
	buffer.Enqueue(bufferTestEvent("second"))
	buffer.Enqueue(bufferTestEvent("third"))

	require.Eventually(t, func() bool { return fake.count() == 3 }, 3*time.Second, 5*time.Millisecond)
	require.Equal(t, []string{"first", "second", "third"}, fake.eventTypes(), "重试后顺序必须保持")
	stats := buffer.Stats()
	require.GreaterOrEqual(t, stats.Retried, int64(1))
	require.Equal(t, int64(3), stats.FlushedEvents)
	require.Zero(t, stats.ShutdownRemaining)
}

func TestEventPersistBufferCloseFlushesTail(t *testing.T) {
	ctx := context.Background()
	fake := &fakePersistStore{}
	buffer := NewEventPersistBuffer(fake, EventPersistBufferConfig{BatchSize: 100, FlushInterval: time.Hour})

	for index := 0; index < 3; index++ {
		buffer.Enqueue(bufferTestEvent("tail"))
	}
	require.Zero(t, fake.count(), "interval 前不应有落盘")

	require.NoError(t, buffer.Close(ctx))
	require.Equal(t, 3, fake.count(), "Close 必须 flush 尾部事件")
	require.Equal(t, int64(0), buffer.Stats().ShutdownRemaining)

	// 关闭后入队必须被拒绝且计数，不得静默丢失。
	buffer.Enqueue(bufferTestEvent("after-close"))
	require.Equal(t, int64(1), buffer.Stats().Dropped)
}

func TestEventPersistBufferCloseTimeoutCountsRemaining(t *testing.T) {
	block := make(chan struct{})
	fake := &fakePersistStore{block: block, blocked: make(chan struct{})}
	buffer := NewEventPersistBuffer(fake, EventPersistBufferConfig{
		BatchSize:       1,
		FlushInterval:   time.Hour,
		FlushTimeout:    100 * time.Millisecond,
		ShutdownTimeout: 200 * time.Millisecond,
	})
	buffer.Enqueue(bufferTestEvent("stuck"))
	<-fake.blocked

	start := time.Now()
	err := buffer.Close(context.Background())
	require.Error(t, err, "存储卡住时 Close 必须在 ShutdownTimeout 内返回错误")
	require.Less(t, time.Since(start), 2*time.Second)

	require.Eventually(t, func() bool { return buffer.Stats().ShutdownRemaining > 0 }, 3*time.Second, 20*time.Millisecond)
	close(block)
}

func TestEventPersistBufferSkipsAlreadyPersistedSeq(t *testing.T) {
	ctx := context.Background()
	fake := &fakePersistStore{}
	buffer := NewEventPersistBuffer(fake, EventPersistBufferConfig{BatchSize: 10, FlushInterval: time.Hour})
	t.Cleanup(func() { _ = buffer.Close(ctx) })

	event := bufferTestEvent("already-persisted")
	event.Payload = map[string]interface{}{"seq": int64(42)}
	buffer.Enqueue(event)

	stats := buffer.Stats()
	require.Equal(t, int64(1), stats.SkippedPersisted)
	require.Zero(t, stats.Enqueued)
	require.Zero(t, fake.count())
}

// 回归：Enqueue 必须冻结 payload 快照。Bus.Publish 在发布方 goroutine 内同步回调
// 入队，Enqueue 返回后发布方仍可能继续写入自己持有的 map；若落盘侧继续别名同一张
// map，worker 的 json.Marshal 会与发布方写入竞态，触发 internal/runtime/maps.fatal
// （concurrent map read and map write，栈顶为 makeMapArshaler/MapIndex）。
func TestEventPersistBufferSnapshotsPayloadAtEnqueue(t *testing.T) {
	ctx := context.Background()
	fake := &fakePersistStore{}
	buffer := NewEventPersistBuffer(fake, EventPersistBufferConfig{BatchSize: 1, FlushInterval: time.Hour})
	t.Cleanup(func() { _ = buffer.Close(ctx) })

	nested := map[string]interface{}{"value": "before"}
	payload := map[string]interface{}{"value": "before", "nested": nested}
	buffer.Enqueue(runtimeevents.Event{Type: "tool.completed", SessionID: "snapshot-session", Payload: payload})

	// 入队返回后，发布方继续改写自己持有的 map（真实竞态场景）。
	payload["value"] = "after"
	nested["value"] = "after"

	require.Eventually(t, func() bool { return fake.count() == 1 }, 2*time.Second, 5*time.Millisecond)
	stored := fake.snapshot()[0]
	require.Equal(t, "before", stored.Payload["value"], "buffer 必须在入队时冻结 payload 顶层快照")
	require.Equal(t, "before", stored.Payload["nested"].(map[string]interface{})["value"], "嵌套 map 同样必须被深拷贝")
}

// P0-1 验收基准（docs/plan/aicli-stream-pipeline-performance-audit-and-optimization-plan-20261011.md §6）：
// 默认配置语义下（delta 溢出 drop、critical block），生产者 Enqueue 热路径
// （含恒定溢出的 drop 分支）必须亚 10µs 且不触碰 store——LLM 回调链零同步磁盘 I/O。
func BenchmarkEventPersistBufferEnqueueStreamDeltaOverflowDrop(b *testing.B) {
	fake := &fakePersistStore{}
	buffer := NewEventPersistBuffer(fake, EventPersistBufferConfig{
		BatchSize:         64,
		FlushInterval:     time.Hour,
		QueueLimit:        1,
		OverflowDropTypes: runtimeevents.IsPersistStreamDeltaEventType,
		CriticalTypes:     runtimeevents.IsPersistCriticalEventType,
	})
	defer func() { _ = buffer.Close(context.Background()) }()
	buffer.Enqueue(bufferTestEvent("assistant_delta")) // 占满队列：此后恒定溢出
	event := bufferTestEvent("assistant_delta")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buffer.Enqueue(event)
	}
	b.StopTimer()
	if fake.singleCalls != 0 {
		b.Fatalf("drop 路径不得触发同步落盘, singleCalls=%d", fake.singleCalls)
	}
}

// 非溢出快路径：队列写入 + 触发信号，无任何磁盘 I/O。
func BenchmarkEventPersistBufferEnqueueFastPath(b *testing.B) {
	fake := &fakePersistStore{}
	buffer := NewEventPersistBuffer(fake, EventPersistBufferConfig{
		BatchSize:     1 << 20,
		FlushInterval: time.Hour,
	})
	defer func() { _ = buffer.Close(context.Background()) }()
	event := bufferTestEvent("assistant_delta")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buffer.Enqueue(event)
	}
}

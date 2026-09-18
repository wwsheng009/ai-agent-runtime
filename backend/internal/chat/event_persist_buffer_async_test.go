package chat

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// P2.11 回归：发布链异步派发（分册 §3.3 / §7）。

func TestEventPersistBufferAsyncDispatchWaitsForSpaceInsteadOfSyncWrite(t *testing.T) {
	fake := &fakePersistStore{}
	buffer := NewEventPersistBuffer(fake, EventPersistBufferConfig{
		BatchSize:           8,
		FlushInterval:       30 * time.Millisecond,
		QueueLimit:          1,
		AsyncDispatch:       true,
		DispatchWaitTimeout: 2 * time.Second,
	})
	t.Cleanup(func() { _ = buffer.Close(context.Background()) })

	buffer.Enqueue(bufferTestEvent("first"))
	buffer.Enqueue(bufferTestEvent("second"))

	require.Eventually(t, func() bool { return fake.count() == 2 }, 2*time.Second, 5*time.Millisecond)
	stats := buffer.Stats()
	require.Equal(t, int64(1), stats.DispatchWaits, "第二次入队应在等位后成功，而不是同步兜底")
	require.Zero(t, stats.SyncFallback)
	require.Zero(t, stats.Dropped)
	require.Greater(t, stats.DispatchP95Ns, int64(0))
}

func TestEventPersistBufferSyncModeStillWritesInlineWhenFull(t *testing.T) {
	fake := &fakePersistStore{}
	buffer := NewEventPersistBuffer(fake, EventPersistBufferConfig{
		BatchSize:     8,
		FlushInterval: 30 * time.Millisecond,
		QueueLimit:    1,
		// AsyncDispatch 关闭 = P1.5 语义：溢出即同步兜底（计数可见）。
	})
	t.Cleanup(func() { _ = buffer.Close(context.Background()) })

	buffer.Enqueue(bufferTestEvent("first"))
	buffer.Enqueue(bufferTestEvent("second"))

	require.Eventually(t, func() bool { return fake.count() == 2 }, 2*time.Second, 5*time.Millisecond)
	stats := buffer.Stats()
	require.Equal(t, int64(1), stats.SyncFallback)
	require.Zero(t, stats.DispatchWaits)
}

func TestEventPersistBufferAsyncWaitTimeoutFallsBackAndCounts(t *testing.T) {
	fake := &fakePersistStore{}
	buffer := NewEventPersistBuffer(fake, EventPersistBufferConfig{
		BatchSize:           8,
		FlushInterval:       time.Hour, // 不靠 interval 触发，确保等待期内无空间
		QueueLimit:          1,
		AsyncDispatch:       true,
		DispatchWaitTimeout: 20 * time.Millisecond,
	})
	t.Cleanup(func() { _ = buffer.Close(context.Background()) })

	buffer.Enqueue(bufferTestEvent("queued"))
	buffer.Enqueue(bufferTestEvent("overflow"))

	require.Eventually(t, func() bool { return fake.count() == 1 }, time.Second, 5*time.Millisecond,
		"超时后第二事件应立即经同步兜底落盘")
	stats := buffer.Stats()
	require.Equal(t, int64(1), stats.DispatchWaitTimeouts, "等待超时必须计数")
	require.Equal(t, int64(1), stats.SyncFallback, "超时后同步兜底，不丢事件")
	// 队首事件等待关闭 flush（interval 1h 不触发）。
	require.NoError(t, buffer.Close(context.Background()))
	require.Equal(t, 2, fake.count())
}

// §3.3 验收：store 被故障注入拖慢时，发布链延迟不上升（与磁盘解耦）。
func TestEventPersistBufferAsyncDispatchDecouplesPublishFromSlowStore(t *testing.T) {
	fake := &fakePersistStore{
		block:   make(chan struct{}),
		blocked: make(chan struct{}),
	}
	buffer := NewEventPersistBuffer(fake, EventPersistBufferConfig{
		BatchSize:           8,
		FlushInterval:       5 * time.Millisecond,
		QueueLimit:          64,
		AsyncDispatch:       true,
		DispatchWaitTimeout: 50 * time.Millisecond,
	})
	t.Cleanup(func() { _ = buffer.Close(context.Background()) })

	for index := 0; index < 8; index++ {
		buffer.Enqueue(bufferTestEvent("slow"))
	}
	select {
	case <-fake.blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("worker 未进入慢速 AppendEvents")
	}

	var maxEnqueue time.Duration
	for index := 0; index < 56; index++ {
		begin := time.Now()
		buffer.Enqueue(bufferTestEvent("burst"))
		if elapsed := time.Since(begin); elapsed > maxEnqueue {
			maxEnqueue = elapsed
		}
	}
	// 首批仍被阻塞：此刻写盘延迟已经 >150ms（下面释放后再断言）。
	require.Less(t, maxEnqueue, 10*time.Millisecond, "发布链不得等待磁盘")

	time.Sleep(150 * time.Millisecond)
	close(fake.block)

	require.Eventually(t, func() bool { return fake.count() == 64 }, 3*time.Second, 5*time.Millisecond)
	stats := buffer.Stats()
	require.GreaterOrEqual(t, stats.FlushLatencyMax, int64(150*time.Millisecond), "写盘确实被拖慢")
	require.Less(t, stats.DispatchP95Ns, int64(10*time.Millisecond), "发布延迟与磁盘延迟解耦")
	require.Zero(t, stats.Failed)
	require.Zero(t, stats.Dropped)
}

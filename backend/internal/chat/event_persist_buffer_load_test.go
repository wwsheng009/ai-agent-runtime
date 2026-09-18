package chat

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// P1.5/M5 压测入口：1k events/s 持续 60s（默认跳过，CI 不做长压测）。
//
// 复现命令：
//
//	$env:AICLI_RUNTIME_LOAD_TEST=1; go test ./internal/chat -run TestEventPersistBufferLoad1k -v -timeout 300s
//
// 时长可用 AICLI_RUNTIME_LOAD_TEST_SECONDS 覆盖（例如 20 秒的快速验证）。
func TestEventPersistBufferLoad1k(t *testing.T) {
	if os.Getenv("AICLI_RUNTIME_LOAD_TEST") != "1" {
		t.Skip("set AICLI_RUNTIME_LOAD_TEST=1 to run the 1k events/s load test")
	}
	duration := 60 * time.Second
	if raw := os.Getenv("AICLI_RUNTIME_LOAD_TEST_SECONDS"); raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 {
			duration = time.Duration(seconds) * time.Second
		}
	}

	ctx := context.Background()
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
		Path: filepath.Join(t.TempDir(), "persist-load.sqlite"),
		// 压测关注「缓冲不丢事件」，retention 裁剪是有意策略：放大保留量避免干扰计数。
		EventRetention: 1_000_000,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.ensure())

	// 批参数可用环境变量覆盖，用于验证「写事务压缩 ≥32×」所需的 flushInterval。
	batchSize := 64
	if raw := os.Getenv("AICLI_RUNTIME_LOAD_TEST_BATCH"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			batchSize = parsed
		}
	}
	flushInterval := 25 * time.Millisecond
	if raw := os.Getenv("AICLI_RUNTIME_LOAD_TEST_FLUSH_MS"); raw != "" {
		if ms, err := strconv.Atoi(raw); err == nil && ms > 0 {
			flushInterval = time.Duration(ms) * time.Millisecond
		}
	}
	buffer := NewEventPersistBuffer(store, EventPersistBufferConfig{
		BatchSize:       batchSize,
		FlushInterval:   flushInterval,
		QueueLimit:      8192,
		QueueBytesLimit: 32 << 20,
		ShutdownTimeout: 15 * time.Second,
		// P2.11：异步派发开启（队列容量远大于负载，正常路径零等待）。
		AsyncDispatch: true,
	})
	t.Cleanup(func() { _ = buffer.Close(context.Background()) })

	const sessionID = "persist-load"
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()

	deadline := time.Now().Add(duration)
	start := time.Now()
	enqueued := 0
	var maxEnqueue time.Duration
	for time.Now().Before(deadline) {
		<-ticker.C
		expected := int(time.Since(start).Seconds() * 1000)
		for enqueued < expected {
			begin := time.Now()
			buffer.Enqueue(runtimeevents.Event{
				Type:      "assistant_delta",
				SessionID: sessionID,
				Payload:   map[string]interface{}{"index": enqueued},
			})
			if elapsed := time.Since(begin); elapsed > maxEnqueue {
				maxEnqueue = elapsed
			}
			enqueued++
		}
	}
	elapsed := time.Since(start)
	require.NoError(t, buffer.Close(ctx))

	events, err := store.ListEvents(ctx, sessionID, 0, 0)
	require.NoError(t, err)
	stats := buffer.Stats()
	appendStats := store.AppendTimingStats()
	avgBatch := float64(stats.FlushedEvents) / math.Max(1, float64(stats.Batches))
	rate := float64(enqueued) / elapsed.Seconds()
	t.Logf("load: duration=%s enqueued=%d rate=%.0f/s durable=%d max_enqueue=%s",
		elapsed.Round(time.Millisecond), enqueued, rate, len(events), maxEnqueue.Round(time.Microsecond))
	t.Logf("config: batch_size=%d flush_interval=%s", batchSize, flushInterval)
	t.Logf("persist: batches=%d avg_batch=%.1f flush_p95=%s flush_max=%s failed=%d dropped=%d sync_fallback=%d retry=%d",
		stats.Batches, avgBatch, time.Duration(stats.FlushP95Ns).Round(time.Microsecond),
		time.Duration(stats.FlushLatencyMax).Round(time.Microsecond), stats.Failed, stats.Dropped, stats.SyncFallback, stats.Retried)
	t.Logf("dispatch: p95=%s max=%s waits=%d wait_timeouts=%d",
		time.Duration(stats.DispatchP95Ns).Round(time.Microsecond),
		time.Duration(stats.DispatchMaxNs).Round(time.Microsecond), stats.DispatchWaits, stats.DispatchWaitTimeouts)
	t.Logf("store: batch_lock_hold_total=%s per_event=%s",
		time.Duration(appendStats.BatchLockHoldNs).Round(time.Microsecond),
		time.Duration(appendStats.BatchLockHoldNs/maxInt64(appendStats.BatchedEvents, 1)).Round(time.Microsecond))

	require.Len(t, events, enqueued, "durable 事件数必须与入队数一致（不丢）")
	require.Zero(t, stats.Failed)
	require.Zero(t, stats.Dropped)
	require.Zero(t, stats.ShutdownRemaining)
	require.Greater(t, stats.Batches, int64(0))
	require.GreaterOrEqual(t, avgBatch, 8.0, "1k/s × 25ms 间隔下批均值应接近 25（目标 ≥32 见分册 §1.3）")
	require.Less(t, float64(stats.FlushP95Ns), float64(50*time.Millisecond), "flush P95 ≤50ms")
	require.Less(t, maxEnqueue, 5*time.Millisecond, "Enqueue 必须零磁盘等待（发布链不被落盘阻塞）")
	require.Less(t, stats.DispatchP95Ns, int64(time.Millisecond), "Publish→return P95 ≤1ms（P2.11 目标）")
	require.Zero(t, stats.DispatchWaitTimeouts)
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

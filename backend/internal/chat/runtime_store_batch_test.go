package chat

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
)

// P1.5-A store 批量写入回归（分册 §7.1–3、§7.11、§7.15）。

func batchTestEvent(sessionID, eventType string, index int) runtimeevents.Event {
	return runtimeevents.Event{
		Type:      eventType,
		SessionID: sessionID,
		Payload:   map[string]interface{}{"index": index},
	}
}

func TestAppendEventsAssignsContiguousSeqsPerSession(t *testing.T) {
	ctx := context.Background()
	store := newHotpathTestStore(t, nil)

	sessions := []string{"batch-a", "batch-b"}
	events := make([]runtimeevents.Event, 0, 6)
	for index := 0; index < 6; index++ {
		events = append(events, batchTestEvent(sessions[index%2], "batch", index))
	}
	seqs, err := store.AppendEvents(ctx, events)
	require.NoError(t, err)
	require.Equal(t, []int64{1, 1, 2, 2, 3, 3}, seqs, "返回顺序必须与输入顺序一致，且各 session 内连续")

	for _, sessionID := range sessions {
		stored, err := store.ListEvents(ctx, sessionID, 0, 0)
		require.NoError(t, err)
		require.Len(t, stored, 3)
		for index, event := range stored {
			require.Equal(t, int64(index+1), event.Payload["seq"])
		}
	}

	// 第二批衔接已有 seq。
	moreSeqs, err := store.AppendEvents(ctx, []runtimeevents.Event{
		batchTestEvent("batch-a", "batch", 100),
		batchTestEvent("batch-a", "batch", 101),
	})
	require.NoError(t, err)
	require.Equal(t, []int64{4, 5}, moreSeqs)

	stats := store.AppendTimingStats()
	require.Equal(t, int64(2), stats.Batches)
	require.Equal(t, int64(8), stats.BatchedEvents)
}

func TestAppendEventsIsAtomicOnFailure(t *testing.T) {
	ctx := context.Background()
	store := newHotpathTestStore(t, nil)
	runtimeAppendBatchHook = func(index int) error {
		if index == 2 {
			return fmt.Errorf("injected batch failure at %d", index)
		}
		return nil
	}
	t.Cleanup(func() { runtimeAppendBatchHook = nil })

	events := []runtimeevents.Event{
		batchTestEvent("atomic-session", "atomic", 1),
		batchTestEvent("atomic-session", "atomic", 2),
		batchTestEvent("atomic-session", "atomic", 3),
	}
	_, err := store.AppendEvents(ctx, events)
	require.Error(t, err)

	stored, err := store.ListEvents(ctx, "atomic-session", 0, 0)
	require.NoError(t, err)
	require.Empty(t, stored, "批内任一写入失败必须整批回滚")

	// 清除注入后同一批必须成功且 seq 从 1 开始（无空洞）。
	runtimeAppendBatchHook = nil
	seqs, err := store.AppendEvents(ctx, events)
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2, 3}, seqs)
}

func TestAppendEventsRespectsMaxBatch(t *testing.T) {
	ctx := context.Background()
	store := newHotpathTestStore(t, nil)

	oversized := make([]runtimeevents.Event, maxAppendEventsBatch+1)
	for index := range oversized {
		oversized[index] = batchTestEvent("max-batch", "max", index)
	}
	_, err := store.AppendEvents(ctx, oversized)
	require.Error(t, err)

	stored, err := store.ListEvents(ctx, "max-batch", 0, 0)
	require.NoError(t, err)
	require.Empty(t, stored, "超限批次不得产生部分写入")

	seqs, err := store.AppendEvents(ctx, []runtimeevents.Event{
		batchTestEvent("max-batch", "max", 0),
		batchTestEvent("max-batch", "max", 1),
	})
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2}, seqs)
}

func TestAppendEventsConcurrentInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-batch-concurrent.sqlite")
	first := newHotpathTestStore(t, &RuntimeStoreConfig{Path: path})
	second := newHotpathTestStore(t, &RuntimeStoreConfig{Path: path})
	require.NoError(t, first.ensure())

	const batchesPerStore = 4
	const batchSize = 7
	var wait sync.WaitGroup
	errs := make(chan error, batchesPerStore*2)
	for _, store := range []*SQLiteRuntimeStore{first, second} {
		wait.Add(1)
		go func(store *SQLiteRuntimeStore) {
			defer wait.Done()
			for batch := 0; batch < batchesPerStore; batch++ {
				events := make([]runtimeevents.Event, batchSize)
				for index := range events {
					events[index] = batchTestEvent("batch-concurrent", "batch", batch*batchSize+index)
				}
				if _, err := store.AppendEvents(context.Background(), events); err != nil {
					errs <- err
					return
				}
			}
		}(store)
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	stored, err := first.ListEvents(context.Background(), "batch-concurrent", 0, 0)
	require.NoError(t, err)
	require.Len(t, stored, batchesPerStore*2*batchSize)
	seen := map[int64]bool{}
	for _, event := range stored {
		seq := event.Payload["seq"].(int64)
		require.False(t, seen[seq], "duplicate seq %d across batch instances", seq)
		seen[seq] = true
	}
	require.Len(t, seen, batchesPerStore*2*batchSize)
}

func TestAppendEventsLockHoldBudget(t *testing.T) {
	ctx := context.Background()
	store := newHotpathTestStore(t, nil)
	events := make([]runtimeevents.Event, 64)
	for index := range events {
		events[index] = batchTestEvent("lock-budget", "batch", index)
	}
	seqs, err := store.AppendEvents(ctx, events)
	require.NoError(t, err)
	require.Len(t, seqs, 64)

	stats := store.AppendTimingStats()
	require.Equal(t, int64(1), stats.Batches)
	require.Equal(t, int64(64), stats.BatchedEvents)
	require.Greater(t, stats.BatchLockHoldNs, int64(0))
	perEvent := time.Duration(stats.BatchLockHoldNs / stats.BatchedEvents)
	t.Logf("batch lock hold: total=%s per-event=%s (release budget P95<=5ms)", time.Duration(stats.BatchLockHoldNs), perEvent)
	require.Less(t, time.Duration(stats.BatchLockHoldNs), 500*time.Millisecond, "写锁持有必须有界")
}

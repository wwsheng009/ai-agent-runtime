package chat

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// P0.5 回归（审查 R1）：两个 store 实例（各自一条连接，等价于两个进程共享
// 同一 session_runtime.sqlite）并发对同一 session 追加事件。
//
// 修复前：deferred 事务的「先读后写」在另一实例提交后提升写锁会失败
// （SQLITE_BUSY_SNAPSHOT，ext 517），且重试同一事务不可能成功。
// 修复后：BEGIN IMMEDIATE + BUSY 家族新事务重试，必须全量成功、seq 连续、
// 517 计数为 0。
func TestAppendEventConcurrentInstancesNoBusySnapshot(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runtime-busy.sqlite")

	first, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Close() })

	// 先落一条并完成迁移/建表，再打开第二个实例（与生产多进程启动顺序一致）。
	_, err = first.AppendEvent(ctx, runtimeevents.Event{SessionID: "warmup", Type: "session.progress"})
	require.NoError(t, err)

	second, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })

	const (
		stores   = 2
		perStore = 30
		session  = "shared-session"
		total    = stores * perStore
	)

	var wg sync.WaitGroup
	errCh := make(chan error, stores)
	run := func(store *SQLiteRuntimeStore, tag string) {
		defer wg.Done()
		for i := 0; i < perStore; i++ {
			event := runtimeevents.Event{
				SessionID: session,
				Type:      "session.progress",
				Payload:   map[string]interface{}{"store": tag, "i": i},
			}
			if _, err := store.AppendEvent(ctx, event); err != nil {
				errCh <- fmt.Errorf("%s append %d: %w", tag, i, err)
				return
			}
		}
	}
	wg.Add(stores)
	go run(first, "first")
	go run(second, "second")
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}

	events, err := first.ListEvents(ctx, session, 0, 0)
	require.NoError(t, err)
	require.Len(t, events, total)

	lastSeq, err := first.LastEventSeq(ctx, session)
	require.NoError(t, err)
	require.Equal(t, int64(total), lastSeq, "seq must be contiguous across instances")

	for name, store := range map[string]*SQLiteRuntimeStore{"first": first, "second": second} {
		stats := store.ContentionStats()
		require.Zero(t, stats.BusySnapshotErrors, "%s: BUSY_SNAPSHOT(517) must not occur", name)
		require.Zero(t, stats.BusyExhausted, "%s: no busy error may escape AppendEvent", name)
	}
}

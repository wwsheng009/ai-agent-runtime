package background

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// P0.5b：background_job_events 的 seq 分配是「读后写」（SELECT MAX(seq)+1 →
// INSERT）。background.sqlite 由 aicli 与 runtime-server 共享，deferred 开启的
// 事务在并发写者提交后会提升失败（SQLITE_BUSY_SNAPSHOT/517，重试同一事务永不
// 成功）→ 作业事件丢失。
//
// 说明：517 的复现窗口只有微秒级（读与写之间的间隙），本用例是**双实例并发
// 烟雾 + seq 连续性回归**，不保证在 deferred 实现下必然变红；deferred 写法的硬
// 门禁在 internal/sqliteutil 的 TestWriteTransactionsUseImmediateOptions，
// 517 语义的确定性复现在 internal/sqliteutil/tx_test.go。
func TestAppendEventConcurrentInstancesNoBusySnapshot(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "background-busy.sqlite")

	first, err := NewSQLiteStore(&StoreConfig{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Close() })

	const jobID = "job_busy_snapshot"
	require.NoError(t, first.SaveJob(ctx, Job{
		ID:        jobID,
		SessionID: "session-busy",
		Kind:      "shell",
		Status:    StatusPending,
		Command:   "echo busy",
		CreatedAt: time.Now().UTC(),
	}))
	// 先落一条，完成建表/迁移后再打开第二个实例（与双进程启动顺序一致）。
	require.NoError(t, first.AppendEvent(ctx, jobID, "queued", map[string]interface{}{"i": 0}))

	second, err := NewSQLiteStore(&StoreConfig{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })

	const (
		stores   = 2
		perStore = 60
		total    = stores*perStore + 1 // 含预热事件
	)

	var wg sync.WaitGroup
	errCh := make(chan error, stores)
	run := func(store *SQLiteStore, tag string) {
		defer wg.Done()
		for i := 0; i < perStore; i++ {
			if err := store.AppendEvent(ctx, jobID, "running", map[string]interface{}{"store": tag, "i": i}); err != nil {
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

	events, err := first.ListEvents(ctx, jobID, 0, 0)
	require.NoError(t, err)
	require.Len(t, events, total)
	for index, event := range events {
		require.Equal(t, int64(index+1), event.Seq, "seq 必须跨实例连续（读后写不可丢号/重复）")
	}
}

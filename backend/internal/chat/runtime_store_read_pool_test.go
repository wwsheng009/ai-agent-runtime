package chat

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// P1.7 读写双池回归（分册 §5 DoD 1–10、12）。

// readPoolTestStore 建一个 fileBacked store 并触发一次读，让读池惰性打开。
func readPoolTestStore(t *testing.T, cfg *RuntimeStoreConfig) (*SQLiteRuntimeStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session_runtime.sqlite")
	if cfg == nil {
		cfg = &RuntimeStoreConfig{}
	}
	cfg.Path = path
	store, err := NewSQLiteRuntimeStore(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.ensure())
	_, err = store.LoadState(context.Background(), "no-such-session")
	require.NoError(t, err)
	return store, path
}

func TestReadPoolDSNPragmasApply(t *testing.T) {
	store, _ := readPoolTestStore(t, nil)
	stats := store.PoolStats()
	require.True(t, stats.ReadPoolOpen, "fileBacked store 的读池应已打开")
	require.False(t, stats.ReadPoolDegraded, "reason=%s", stats.ReadPoolDegradedReason)
	require.Equal(t, defaultRuntimeReadPoolSize, stats.ReadMaxOpenConnections)

	// 逐连接：同时持有两个读连接，分别读回 PRAGMA（DSN `_pragma` 必须对每个新连接生效）。
	readDB := store.readDB
	require.NotNil(t, readDB)
	ctx := context.Background()
	conn1, err := readDB.Conn(ctx)
	require.NoError(t, err)
	defer conn1.Close()
	conn2, err := readDB.Conn(ctx)
	require.NoError(t, err)
	defer conn2.Close()

	for index, conn := range []*sql.Conn{conn1, conn2} {
		var queryOnly, busyTimeout, cacheSize, mmapSize int64
		require.NoError(t, conn.QueryRowContext(ctx, "PRAGMA query_only").Scan(&queryOnly))
		require.Equal(t, int64(1), queryOnly, "conn %d query_only", index)
		require.NoError(t, conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout))
		require.Equal(t, defaultRuntimeBusyTimeout.Milliseconds(), busyTimeout, "conn %d busy_timeout", index)
		require.NoError(t, conn.QueryRowContext(ctx, "PRAGMA cache_size").Scan(&cacheSize))
		require.Equal(t, int64(-runtimeReadPoolCacheKiB), cacheSize, "conn %d cache_size", index)
		require.NoError(t, conn.QueryRowContext(ctx, "PRAGMA mmap_size").Scan(&mmapSize))
		require.Equal(t, int64(0), mmapSize, "conn %d mmap_size", index)
	}
}

func TestReadPoolRejectsWrites(t *testing.T) {
	store, _ := readPoolTestStore(t, nil)
	readDB := store.readDB
	require.NotNil(t, readDB)
	ctx := context.Background()
	for _, statement := range []string{
		`INSERT INTO session_runtime_state (session_id, status, updated_at) VALUES ('x', 'running', 'now')`,
		`UPDATE session_runtime_state SET status = 'stopped'`,
		`CREATE TABLE read_pool_should_not_write (id INTEGER)`,
	} {
		_, err := readDB.ExecContext(ctx, statement)
		require.Error(t, err, "读池必须拒绝写: %s", statement)
		require.Contains(t, strings.ToLower(err.Error()), "readonly", "statement=%s err=%v", statement, err)
	}
}

func TestReadPoolDoesNotCreateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent", "session_runtime.sqlite")
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{Path: path})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	state, err := store.LoadState(context.Background(), "missing")
	require.NoError(t, err)
	require.Nil(t, state)
	events, err := store.ListEvents(context.Background(), "missing", 0, 10)
	require.NoError(t, err)
	require.Empty(t, events)
	seq, err := store.LastEventSeq(context.Background(), "missing")
	require.NoError(t, err)
	require.Zero(t, seq)

	_, statErr := os.Stat(path)
	require.True(t, os.IsNotExist(statErr), "读路径不得创建库文件：%v", statErr)
	stats := store.PoolStats()
	require.False(t, stats.ReadPoolOpen, "文件不存在时不得打开读池")
	require.Zero(t, stats.ReadWaitCount)
}

func TestReadPoolDegradesOnOpenFailure(t *testing.T) {
	store, _ := readPoolTestStore(t, nil)
	// 注入不可打开的读 DSN：sql.Open 惰性成功、Ping/PRAGMA 校验失败 → 降级。
	store.closeReadPool()
	store.readPoolMu.Lock()
	store.readPoolDegraded = false
	store.readPoolDegradedReason = ""
	store.readDB = nil
	store.readDSN = "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "missing-dir", "runtime.sqlite")) + "?mode=rw"
	store.readPoolMu.Unlock()

	state, err := store.LoadState(context.Background(), "no-such-session")
	require.NoError(t, err, "降级后读必须继续可用（走写池）")
	require.Nil(t, state)
	stats := store.PoolStats()
	require.True(t, stats.ReadPoolDegraded)
	require.NotEmpty(t, stats.ReadPoolDegradedReason)
	require.Equal(t, int64(1), stats.ReadPoolOpenErrors)
	require.False(t, stats.ReadPoolOpen)
}

func TestMemoryDSNNotSplit(t *testing.T) {
	store, err := NewSQLiteRuntimeStore(&RuntimeStoreConfig{
		DSN: fmt.Sprintf("file:read-pool-memory-%d?mode=memory&cache=shared", time.Now().UnixNano()),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	require.NoError(t, store.SaveState(ctx, &RuntimeState{SessionID: "s1", Status: SessionRunning}))
	loaded, err := store.LoadState(ctx, "s1")
	require.NoError(t, err)
	require.NotNil(t, loaded)
	stats := store.PoolStats()
	require.False(t, stats.ReadPoolOpen, "内存 DSN 不拆分")
	require.Zero(t, stats.ReadMaxOpenConnections)
}

func TestReadPoolLimitClamp(t *testing.T) {
	store, _ := readPoolTestStore(t, nil)
	ctx := context.Background()
	for index := 0; index < 3; index++ {
		_, err := store.AppendEvent(ctx, runtimeevents.Event{
			Type:      "clamp.test",
			SessionID: "clamp-session",
			Payload:   map[string]interface{}{"index": index},
		})
		require.NoError(t, err)
	}
	events, err := store.ListEvents(ctx, "clamp-session", 0, 5000)
	require.NoError(t, err)
	require.Len(t, events, 3)
	stats := store.PoolStats()
	require.Equal(t, int64(1), stats.ReadLimitClamps)
	require.Positive(t, stats.ReadQueries, "读 API 必须经过读池访问器")
}

func TestCloseClosesReadPoolBeforeCheckpoint(t *testing.T) {
	store, path := readPoolTestStore(t, nil)
	ctx := context.Background()
	_, err := store.AppendEvent(ctx, runtimeevents.Event{Type: "close.test", SessionID: "close-session", Payload: map[string]interface{}{"x": 1}})
	require.NoError(t, err)
	_, err = store.ListEvents(ctx, "close-session", 0, 10)
	require.NoError(t, err)
	require.True(t, store.PoolStats().ReadPoolOpen)

	require.NoError(t, store.Close())
	require.Nil(t, store.readDB, "Close 必须关闭读池")

	// 读池先关闭 → TRUNCATE checkpoint 成功 → WAL 被截断。
	info, statErr := os.Stat(path + "-wal")
	if statErr == nil {
		require.Zero(t, info.Size(), "WAL 应在 Close 的 TRUNCATE checkpoint 后归零")
	}
}

func TestReadPoolIdleMemoryBudget(t *testing.T) {
	store, _ := readPoolTestStore(t, nil)
	readDB := store.readDB
	require.NotNil(t, readDB)
	ctx := context.Background()

	var wg sync.WaitGroup
	for index := 0; index < defaultRuntimeReadPoolSize; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = store.ListEvents(ctx, "idle-budget", 0, 10)
		}()
	}
	wg.Wait()

	require.Eventually(t, func() bool {
		return readDB.Stats().OpenConnections <= runtimeReadPoolMaxIdleConns
	}, 2*time.Second, 10*time.Millisecond, "空闲连接应回落到 MaxIdleConns=%d", runtimeReadPoolMaxIdleConns)
}

// TestReadPoolInvalidatedOnWritePoolReopen 覆盖 DoD 7：写池重开（生产路径即
// ensureCtx 在写句柄被丢弃后重建连接）必须作废读池，下一次读惰性重建。
func TestReadPoolInvalidatedOnWritePoolReopen(t *testing.T) {
	store, _ := readPoolTestStore(t, nil)
	ctx := context.Background()
	_, err := store.AppendEvent(ctx, runtimeevents.Event{Type: "reopen.test", SessionID: "reopen-session", Payload: map[string]interface{}{"x": 1}})
	require.NoError(t, err)
	_, err = store.ListEvents(ctx, "reopen-session", 0, 10)
	require.NoError(t, err)

	staleReadDB := store.readDB
	require.NotNil(t, staleReadDB)

	// 模拟写池重建：丢弃旧写句柄（生产上由锁重试/重建路径完成），再 ensure。
	store.openMu.Lock()
	staleWriteDB := store.db
	store.db = nil
	store.openMu.Unlock()
	require.NotNil(t, staleWriteDB)
	require.NoError(t, staleWriteDB.Close())
	require.NoError(t, store.ensure())

	require.Nil(t, store.readDB, "写池重开必须作废读池")
	require.False(t, store.PoolStats().ReadPoolOpen)
	err = staleReadDB.PingContext(ctx)
	require.Error(t, err, "旧读池句柄必须已关闭")
	require.Contains(t, err.Error(), "closed")

	// 下一次读重建读池，且是新的句柄。
	_, err = store.ListEvents(ctx, "reopen-session", 0, 10)
	require.NoError(t, err)
	freshReadDB := store.readDB
	require.NotNil(t, freshReadDB)
	require.NotSame(t, staleReadDB, freshReadDB)
	require.True(t, store.PoolStats().ReadPoolOpen)
}

// TestReadPoolWALBudgetUnderLongRead 覆盖 DoD 11：长读（快照读事务钉住 WAL）
// 与持续写并发时写不失败、WAL 增长受"写入量 × 放大系数"约束；长读结束后
// checkpoint 能回收 WAL；批量写（P1.5）显著降低每事件 WAL 放大。
// 默认窗口 5s；AICLI_RUNTIME_WAL_BUDGET_FULL=1 放大到 60s。
func TestReadPoolWALBudgetUnderLongRead(t *testing.T) {
	window := 5 * time.Second
	if os.Getenv("AICLI_RUNTIME_WAL_BUDGET_FULL") == "1" {
		window = 60 * time.Second
	}
	store, _ := readPoolTestStore(t, nil)
	ctx := context.Background()
	require.NotNil(t, store.readDB)

	const sessionID = "wal-budget"
	for index := 0; index < 64; index++ {
		_, err := store.AppendEvent(ctx, runtimeevents.Event{
			Type: "wal.warmup", SessionID: sessionID,
			Payload: map[string]interface{}{"index": index, "text": strings.Repeat("w", 256)},
		})
		require.NoError(t, err)
	}

	pinSnapshot := func() (func(), error) {
		conn, err := store.readDB.Conn(ctx)
		if err != nil {
			return nil, err
		}
		tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM session_events").Scan(&count); err != nil {
			_ = tx.Rollback()
			_ = conn.Close()
			return nil, err
		}
		var once sync.Once
		return func() {
			once.Do(func() {
				_ = tx.Rollback()
				_ = conn.Close()
			})
		}, nil
	}
	checkpoint := func() {
		store.mu.Lock()
		writeDB := store.db
		store.mu.Unlock()
		require.NotNil(t, writeDB)
		_, err := writeDB.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
		require.NoError(t, err)
	}

	// 阶段 1：钉住快照 + 逐条写（未启用批量）。
	// 限速 200/s：DoD 11 关注"增长有界 + 可回收"，限速让 60s 模式 WAL 规模可控。
	const targetRatePerSec = 200
	unpin, err := pinSnapshot()
	require.NoError(t, err)
	deadline := time.Now().Add(window)
	start := time.Now()
	var writes, writeErrors int64
	var peakWAL int64
	for time.Now().Before(deadline) {
		if _, err := store.AppendEvent(ctx, runtimeevents.Event{
			Type: "wal.load", SessionID: sessionID,
			Payload: map[string]interface{}{"text": strings.Repeat("x", 256)},
		}); err != nil {
			writeErrors++
		} else {
			writes++
		}
		// 快照读（读池第二连接）在长读期间也必须可用。
		if _, err := store.ListEvents(ctx, sessionID, 0, 10); err != nil {
			unpin()
			t.Fatalf("长读期间读失败: %v", err)
		}
		if size := store.WALSizeBytes(); size > peakWAL {
			peakWAL = size
		}
		if target := time.Duration(writes) * time.Second / targetRatePerSec; time.Since(start) < target {
			time.Sleep(target - time.Since(start))
		}
	}
	unpin()
	require.Zero(t, writeErrors, "长读不得导致写失败（writes=%d）", writes)
	require.Positive(t, writes)
	// 快照被钉住时 WAL 只能按写入量线性增长（无法 checkpoint 回收）；
	// 断言"增长与写入量成比例、放大有界"，抓住真正的失控（而非线性累积本值）。
	perWrite := peakWAL / writes
	t.Logf("unbatched: writes=%d peak=%d per_write=%d", writes, peakWAL, perWrite)
	require.Less(t, perWrite, int64(64<<10), "单事件 WAL 放大必须小于 64KiB：writes=%d peak=%d", writes, peakWAL)
	require.Less(t, peakWAL, int64(writes)*(64<<10), "WAL 增长必须与写入量成比例：writes=%d peak=%d", writes, peakWAL)

	checkpoint()
	require.Less(t, store.WALSizeBytes(), int64(1<<20), "长读结束后 checkpoint 必须能回收 WAL")

	// 阶段 2：同样钉住快照，但改用 64/批 的批量写（P1.5）：每事件放大应显著更低。
	unpinBatch, err := pinSnapshot()
	require.NoError(t, err)
	const batchSize = 64
	batchStart := time.Now()
	var batchedEvents int64
	var batchPeakWAL int64
	for time.Since(batchStart) < time.Second {
		events := make([]runtimeevents.Event, batchSize)
		for index := range events {
			events[index] = runtimeevents.Event{
				Type: "wal.batch", SessionID: sessionID,
				Payload: map[string]interface{}{"text": strings.Repeat("y", 256)},
			}
		}
		if _, err := store.AppendEvents(ctx, events); err != nil {
			unpinBatch()
			t.Fatalf("批量写失败: %v", err)
		}
		batchedEvents += batchSize
		if size := store.WALSizeBytes(); size > batchPeakWAL {
			batchPeakWAL = size
		}
	}
	unpinBatch()
	require.Positive(t, batchedEvents)
	batchedPerEvent := batchPeakWAL / batchedEvents
	t.Logf("batched: events=%d peak=%d per_event=%d", batchedEvents, batchPeakWAL, batchedPerEvent)
	require.Less(t, batchedPerEvent, perWrite, "批量写的每事件 WAL 放大必须低于逐条写")

	checkpoint()
	require.Less(t, store.WALSizeBytes(), int64(1<<20), "批量阶段结束后 checkpoint 必须能回收 WAL")
}

// TestReadAPIInventoryRouting 是路由清单门禁（DoD 9）：13 个读 API 必须走
// readQueryer()，且写 API 不得使用读池访问器。
func TestReadAPIInventoryRouting(t *testing.T) {
	allowed := map[string]bool{
		"GetLease": true, "LoadState": true, "GetToolReceipt": true, "ListToolReceipts": true,
		"ListEvents": true, "ListEventsBefore": true, "ListMailbox": true,
		"ListAgentControlMailbox": true, "ListAgentControlMailboxRecords": true,
		"LastEventSeq": true, "LastMailboxSeq": true,
		"LastAgentControlMailboxSeq": true, "LastAgentControlMailboxRecordSeq": true,
	}
	require.Len(t, allowed, 13)

	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	source := ""
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		require.NoError(t, err)
		source += string(data) + "\n"
	}
	functions := map[string]string{}
	for _, block := range strings.Split(source, "\nfunc (s *SQLiteRuntimeStore) ")[1:] {
		index := strings.Index(block, "(")
		if index <= 0 {
			continue
		}
		body := block
		if end := strings.Index(block, "\nfunc "); end >= 0 {
			body = block[:end]
		}
		functions[block[:index]] = body
	}
	require.NotEmpty(t, functions)

	var routed, enforced []string
	for name, body := range functions {
		if name == "readQueryer" {
			continue
		}
		if strings.Contains(body, "readQueryer()") {
			routed = append(routed, name)
		}
		if strings.Contains(body, "ensureForReadCtx(") && name != "ensureForRead" && name != "ensureForReadCtx" {
			enforced = append(enforced, name)
		}
	}
	for _, name := range routed {
		require.True(t, allowed[name], "非白名单函数使用了读池：%s", name)
	}
	for _, name := range enforced {
		require.True(t, allowed[name], "非白名单函数调用 ensureForReadCtx：%s", name)
	}
	for name := range allowed {
		body, ok := functions[name]
		require.True(t, ok, "白名单读 API 不存在：%s", name)
		require.Contains(t, body, "readQueryer()", "读 API 未路由到读池：%s", name)
		require.Contains(t, body, "readOperationContext(", "读 API 缺少读侧超时：%s", name)
	}
}

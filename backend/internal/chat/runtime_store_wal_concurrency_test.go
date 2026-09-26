package chat

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
)

// 回归护栏：Windows 上并发 WAL 写必须保持主库完整。
//
// 背景（上游 ncruces/go-sqlite3 issue #404 / PR #405、#413）：
// v0.35.3 之前的 Windows VFS 用“按锁边界拷贝”的伪共享内存实现 WAL-index，
// 多连接并发写 + checkpoint 会让 checkpointer 回填陈旧页并截断未回填的帧，
// 直接把库写成 malformed / not a database（本仓库 2026-09-26 的线上事故）。
// 上游修复（v0.35.3+，Windows 10 1803+ 直映射 -shm）之后该形态必须干净。
//
// 这个测试在旧驱动上会稳定失败，因此它同时是“禁止把驱动降级回受影响版本”的
// 回归证明。短模式（go test -short）跳过，避免在低配机器上拖慢 CI。
func TestConcurrentWALWritersKeepDatabaseIntegrity(t *testing.T) {
	if testing.Short() {
		t.Skip("concurrent WAL stress skipped in -short mode")
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "wal-concurrency.sqlite")

	db, err := sql.Open("sqlite3", path)
	require.NoError(t, err)
	// 与线上一致：多连接 + WAL + 普通 busy_timeout，不做任何特殊串行化。
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	t.Cleanup(func() { _ = db.Close() })

	for _, pragma := range []string{
		"PRAGMA busy_timeout=10000",
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
	} {
		_, err := db.ExecContext(ctx, pragma)
		require.NoError(t, err)
	}
	_, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS stress (id INTEGER PRIMARY KEY AUTOINCREMENT, worker INTEGER NOT NULL, payload TEXT NOT NULL)`)
	require.NoError(t, err)

	const (
		workers    = 8
		iterations = 150
	)
	var wait sync.WaitGroup
	errs := make(chan error, workers)
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			for index := 0; index < iterations; index++ {
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					errs <- fmt.Errorf("worker %d begin: %w", worker, err)
					return
				}
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO stress (worker, payload) VALUES (?, ?)`,
					worker, fmt.Sprintf("%d-%d", worker, index)); err != nil {
					_ = tx.Rollback()
					errs <- fmt.Errorf("worker %d insert: %w", worker, err)
					return
				}
				if err := tx.Commit(); err != nil {
					errs <- fmt.Errorf("worker %d commit: %w", worker, err)
					return
				}
				// 周期性触发 checkpoint（与线上关闭路径同类的写-检查点竞争形态）。
				if index%25 == 0 {
					if _, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)"); err != nil {
						errs <- fmt.Errorf("worker %d checkpoint: %w", worker, err)
						return
					}
				}
			}
		}(worker)
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.NoError(t, db.Close())

	// cold reopen：模拟下一次进程启动时的完整性检查。
	verify, err := sql.Open("sqlite3", path)
	require.NoError(t, err, "reopen after concurrent WAL writes must succeed (corruption symptom: file is not a database)")
	verify.SetMaxOpenConns(1)
	defer func() { _ = verify.Close() }()

	var result string
	require.NoError(t, verify.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result))
	require.Equal(t, "ok", result, "concurrent WAL writers corrupted the database")

	var rows int64
	require.NoError(t, verify.QueryRowContext(ctx, `SELECT COUNT(*) FROM stress`).Scan(&rows))
	require.Equal(t, int64(workers*iterations), rows)
}

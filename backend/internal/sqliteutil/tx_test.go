package sqliteutil

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

// 本文件固化 R1 缺陷的复现与修复证据：
//
//   - deferred 事务「先读后写」在另一连接写入 WAL 后，提升写锁失败
//     （SQLITE_BUSY_SNAPSHOT，扩展码 517）；
//   - BEGIN IMMEDIATE（WriteTxOptions）在事务开始时即持有写锁，使
//     「读快照 → 写」不可能被并发写者作废；
//   - RetryWriteTx 仅对 BUSY 家族重试，且每次重试都开新事务。

func openTxProbeDB(t *testing.T, path string, busyTimeoutMS int) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", filepath.ToSlash(path))
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatalf("enable WAL: %v", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=" + intToStr(busyTimeoutMS)); err != nil {
		t.Fatalf("set busy_timeout: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS tx_probe (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatalf("create probe table: %v", err)
	}
	return db
}

// TestDeferredReadThenWriteFailsWithBusySnapshot 复现 R1：deferred 事务读到快照后，
// 另一连接提交写事务（WAL 前进），此时原事务写库会失败——这正是修复前的
// AppendEvent/AcquireLease 等路径在多进程场景下的行为。
func TestDeferredReadThenWriteFailsWithBusySnapshot(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "deferred-hazard.sqlite")
	dbA := openTxProbeDB(t, path, 200)
	dbB := openTxProbeDB(t, path, 200)

	tx, err := dbA.BeginTx(ctx, nil) // 修复前的写法：deferred
	if err != nil {
		t.Fatalf("begin deferred tx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tx_probe`).Scan(&n); err != nil {
		t.Fatalf("read in deferred tx: %v", err)
	}

	// 另一连接写入并提交，使 A 的读快照过期。
	if _, err := dbB.ExecContext(ctx, `INSERT INTO tx_probe(v) VALUES('b')`); err != nil {
		t.Fatalf("concurrent writer B: %v", err)
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO tx_probe(v) VALUES('a')`); err == nil {
		t.Fatal("expected deferred read->write promotion to fail after concurrent WAL write")
	} else if !IsBusyError(err) {
		t.Fatalf("expected SQLITE_BUSY family, got %v", err)
	} else if !IsBusySnapshotError(err) {
		t.Logf("note: promotion failed with %v (expected BUSY_SNAPSHOT on most platforms)", err)
	}
}

// TestImmediateTxReadThenWriteSucceeds 证明修复语义：IMMEDIATE 事务在 BEGIN 时就
// 持有写锁，并发的 B 写者被阻塞到事务结束，A 的读快照不可能被作废。
func TestImmediateTxReadThenWriteSucceeds(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "immediate-fix.sqlite")
	dbA := openTxProbeDB(t, path, 200)
	dbB := openTxProbeDB(t, path, 200)

	tx, err := dbA.BeginTx(ctx, WriteTxOptions)
	if err != nil {
		t.Fatalf("begin immediate tx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tx_probe`).Scan(&n); err != nil {
		t.Fatalf("read in immediate tx: %v", err)
	}

	// B 在 A 持有写锁期间尝试写入：必须被锁住（busy_timeout 后失败），
	// 而不是悄悄提交并作废 A 的快照。
	if _, err := dbB.ExecContext(ctx, `INSERT INTO tx_probe(v) VALUES('b')`); err == nil {
		t.Fatal("concurrent writer B must not commit while IMMEDIATE tx holds the write lock")
	} else if !IsBusyError(err) {
		t.Fatalf("expected busy error for blocked writer B, got %v", err)
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO tx_probe(v) VALUES('a')`); err != nil {
		t.Fatalf("write inside immediate tx must succeed: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit immediate tx: %v", err)
	}
}

// TestIsBusyErrorClassifiesLockTimeout 验证分类器识别普通 BUSY（busy_timeout
// 耗尽）而非 517。
func TestIsBusyErrorClassifiesLockTimeout(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "busy-timeout.sqlite")
	dbA := openTxProbeDB(t, path, 50)
	dbB := openTxProbeDB(t, path, 50)

	lockTx, err := dbA.BeginTx(ctx, WriteTxOptions)
	if err != nil {
		t.Fatalf("hold write lock: %v", err)
	}
	defer func() { _ = lockTx.Rollback() }()

	_, err = dbB.ExecContext(ctx, `INSERT INTO tx_probe(v) VALUES('b')`)
	if err == nil {
		t.Fatal("expected busy timeout error")
	}
	if !IsBusyError(err) {
		t.Fatalf("expected IsBusyError, got %v", err)
	}
	if IsBusySnapshotError(err) {
		t.Fatalf("lock timeout must not be classified as BUSY_SNAPSHOT: %v", err)
	}
}

// TestRetryWriteTxRetriesBusyWithFreshTransaction 验证重试语义：第一次因写锁被
// 占用失败，onRetry 释放锁后，第二次用新事务成功提交。
func TestRetryWriteTxRetriesBusyWithFreshTransaction(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "retry.sqlite")
	dbA := openTxProbeDB(t, path, 50)
	dbB := openTxProbeDB(t, path, 50)

	lockTx, err := dbA.BeginTx(ctx, WriteTxOptions)
	if err != nil {
		t.Fatalf("hold write lock: %v", err)
	}
	defer func() { _ = lockTx.Rollback() }()

	var (
		retries  int
		release  sync.Once
		attempts int
	)
	err = RetryWriteTx(ctx, func(err error, retry int) {
		retries++
		release.Do(func() { _ = lockTx.Rollback() })
	}, func(attemptCtx context.Context) error {
		attempts++
		tx, err := dbB.BeginTx(attemptCtx, WriteTxOptions)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.ExecContext(attemptCtx, `INSERT INTO tx_probe(v) VALUES('retry')`); err != nil {
			return err
		}
		return tx.Commit()
	})
	if err != nil {
		t.Fatalf("expected success after retry, got %v", err)
	}
	if retries == 0 {
		t.Fatal("expected at least one busy retry")
	}
	if attempts != retries+1 {
		t.Fatalf("expected fresh transaction per attempt, attempts=%d retries=%d", attempts, retries)
	}
}

// TestRetryWriteTxExhaustsAttempts 验证重试耗尽后返回最后一个 BUSY 错误。
func TestRetryWriteTxExhaustsAttempts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "retry-exhaust.sqlite")
	dbA := openTxProbeDB(t, path, 30)
	dbB := openTxProbeDB(t, path, 30)

	lockTx, err := dbA.BeginTx(ctx, WriteTxOptions)
	if err != nil {
		t.Fatalf("hold write lock: %v", err)
	}
	defer func() { _ = lockTx.Rollback() }()

	attempts := 0
	err = RetryWriteTx(ctx, nil, func(attemptCtx context.Context) error {
		attempts++
		tx, err := dbB.BeginTx(attemptCtx, WriteTxOptions)
		if err != nil {
			return err
		}
		return tx.Rollback()
	})
	if err == nil {
		t.Fatal("expected busy error after exhausting retries")
	}
	if !IsBusyError(err) {
		t.Fatalf("expected busy error, got %v", err)
	}
	if attempts != WriteRetries {
		t.Fatalf("expected %d attempts, got %d", WriteRetries, attempts)
	}
}

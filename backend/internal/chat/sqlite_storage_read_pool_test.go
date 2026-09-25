package chat

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 读池存在的理由：写事务持锁期间读仍能完成（WAL 多读者 + 单写者）。
// 单连接实现下这个用例会超时——它就是这个优化要防的回归。
func TestSQLiteSessionStorageReadPoolServesReadsDuringWriteLock(t *testing.T) {
	store := newTestSQLiteSessionStorage(t, nil)
	ctx := context.Background()

	session := NewSession("read-pool-user")
	session.Metadata.Title = "读池会话"
	if err := store.Save(ctx, session); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 用写池显式开启一个写事务并保持打开：它持有 SQLite 的写锁。
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE sessions SET title = ? WHERE id = ?`, "写事务里的标题", session.ID); err != nil {
		t.Fatalf("update in tx: %v", err)
	}

	type result struct {
		session *Session
		err     error
	}
	done := make(chan result, 1)
	go func() {
		loaded, err := store.LoadMetadata(ctx, session.ID)
		done <- result{session: loaded, err: err}
	}()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("LoadMetadata during write lock: %v", got.err)
		}
		// 读到的必须是已提交值（写事务未提交，WAL 快照隔离）。
		if got.session.Metadata.Title != "读池会话" {
			t.Fatalf("title = %q, want committed value %q", got.session.Metadata.Title, "读池会话")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("LoadMetadata blocked behind an open write transaction (read pool not in use)")
	}

	if !store.readPoolOpen {
		t.Fatal("read pool must be open after a read")
	}
}

func TestSQLiteSessionStorageReadPoolPragmasMatchConfig(t *testing.T) {
	store := newTestSQLiteSessionStorage(t, nil)
	ctx := context.Background()
	if _, err := store.LoadMetadata(ctx, "missing-session"); err == nil {
		t.Fatal("expected ErrSessionNotFound")
	}
	if !store.readPoolOpen || store.readDB == nil {
		t.Fatal("read pool must be open")
	}
	if err := verifySQLiteSessionReadPoolPragmas(ctx, store.readDB, store.cfg.BusyTimeout.Milliseconds(), store.cfg.SQLiteCacheKiB); err != nil {
		t.Fatalf("read pool pragmas: %v", err)
	}
	if got := store.readDB.Stats().MaxOpenConnections; got != store.cfg.ReadPoolSize {
		t.Fatalf("read pool MaxOpenConnections = %d, want %d", got, store.cfg.ReadPoolSize)
	}
}

// ReadPoolSize=1 显式退回历史单连接行为：读仍然正确，只是不再并发。
func TestSQLiteSessionStorageReadPoolDisabledFallsBackToWritePool(t *testing.T) {
	store := newTestSQLiteSessionStorage(t, func(cfg *PersistentSessionStorageConfig) {
		cfg.ReadPoolSize = 1
	})
	ctx := context.Background()

	session := NewSession("single-conn-user")
	if err := store.Save(ctx, session); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := store.LoadMetadata(ctx, session.ID)
	if err != nil {
		t.Fatalf("LoadMetadata: %v", err)
	}
	if loaded.ID != session.ID {
		t.Fatalf("loaded id = %q, want %q", loaded.ID, session.ID)
	}
	if store.readPoolOpen || store.readDB != nil {
		t.Fatal("read pool must stay closed when ReadPoolSize=1")
	}
	if !store.readPoolDegraded {
		t.Fatal("read pool must be marked degraded when explicitly disabled")
	}
}

// 读池打开失败（DSN 不可用）必须 sticky 降级回写池，读语义不受影响。
func TestSQLiteSessionStorageReadPoolDegradesOnBrokenDSN(t *testing.T) {
	store := newTestSQLiteSessionStorage(t, nil)
	ctx := context.Background()

	session := NewSession("degraded-user")
	session.Metadata.Title = "降级会话"
	if err := store.Save(ctx, session); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// 注入非法 DSN（生产恒为空）：读池打不开 → 降级。
	store.readDSN = "file:" + strings.Repeat("x", 3) + "?_pragma=definitely_not_a_pragma(1)"

	loaded, err := store.LoadMetadata(ctx, session.ID)
	if err != nil {
		t.Fatalf("LoadMetadata after degradation: %v", err)
	}
	if loaded.Metadata.Title != "降级会话" {
		t.Fatalf("title = %q, want %q", loaded.Metadata.Title, "降级会话")
	}
	if store.readPoolOpen || store.readDB != nil {
		t.Fatal("broken DSN must not leave a read pool open")
	}
	if !store.readPoolDegraded {
		t.Fatal("broken DSN must mark the read pool degraded (sticky)")
	}
}

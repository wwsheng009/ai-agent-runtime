package usageanalytics

import (
	"path/filepath"
	"testing"
	"time"
)

// 可写 Store 的查询必须走独立只读连接池：写连接被采集事务占用时，
// database/sql 的单连接等待没有超时，HTTP 查询会一直挂到前端
// AbortSignal 超时（signal timed out）。WAL 允许读写并发，因此读必须
// 不被写事务挡住。
func TestReadQueriesUseIndependentPoolWhileWriterBusy(t *testing.T) {
	store, err := Open(Config{Path: filepath.Join(t.TempDir(), "usage.sqlite")})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()
	if store.reader == nil {
		t.Fatal("expected an independent read pool on a writable store")
	}

	tx, err := store.db.Begin()
	if err != nil {
		t.Fatalf("begin writer tx: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO usage_sessions (session_id, title) VALUES ('busy-writer', 'busy')`); err != nil {
		t.Fatalf("insert in writer tx: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		rows, ok, err := store.query(`SELECT COUNT(*) FROM usage_sessions`)
		if err == nil && ok && rows != nil {
			_ = rows.Close()
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("read query failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read query waited for the busy writer connection; independent read pool is not active")
	}
}

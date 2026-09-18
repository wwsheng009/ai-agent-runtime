package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// P2.13 会话快照加固的 store 侧回归（对分册 §4 测试清单 2–8、11 的覆盖）。

func TestSnapshotSessionPreservesPreExistingDestination(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	session := NewSession("snapshot-ownership-user")
	require.NoError(t, store.Save(ctx, session))
	require.NoError(t, store.AddMessage(ctx, session.ID, *types.NewUserMessage("hello")))

	destination := filepath.Join(t.TempDir(), "existing.sqlite")
	original := []byte("pre-existing content that must survive")
	require.NoError(t, os.WriteFile(destination, original, 0o600))

	err := store.SnapshotSession(ctx, session.ID, destination)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already exists")

	content, readErr := os.ReadFile(destination)
	require.NoError(t, readErr)
	require.Equal(t, original, content, "非本次创建的文件不得被覆盖或删除")
}

func TestSnapshotSessionCancelCleansUpOwnDestination(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	session := NewSession("snapshot-cancel-user")
	require.NoError(t, store.Save(ctx, session))
	require.NoError(t, store.AddMessage(ctx, session.ID, *types.NewUserMessage("hello")))

	destination := filepath.Join(t.TempDir(), "cancel.sqlite")
	canceled, cancel := context.WithCancel(ctx)
	cancel()

	err := store.SnapshotSession(canceled, session.ID, destination)
	require.Error(t, err)
	_, statErr := os.Stat(destination)
	require.True(t, os.IsNotExist(statErr), "取消后必须清理本次自建的 destination")
}

func TestSnapshotSessionTimeoutIsBoundedAndCounted(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, func(cfg *PersistentSessionStorageConfig) {
		cfg.SessionSnapshotTimeout = time.Nanosecond
	})
	session := NewSession("snapshot-timeout-user")
	require.NoError(t, store.Save(ctx, session))
	require.NoError(t, store.AddMessage(ctx, session.ID, *types.NewUserMessage("hello")))

	destination := filepath.Join(t.TempDir(), "timeout.sqlite")
	err := store.SnapshotSession(ctx, session.ID, destination)
	require.Error(t, err)
	require.True(t,
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrSessionSnapshotTimeout),
		"expected a deadline-classified error, got %v", err)
	_, statErr := os.Stat(destination)
	require.True(t, os.IsNotExist(statErr), "超时后必须清理 destination")

	stats := store.SnapshotStats()
	require.Equal(t, int64(1), stats.Total)
	require.Equal(t, int64(1), stats.Timeouts)
	require.Zero(t, stats.Succeeded)
}

func TestSnapshotSessionPreflightTooLarge(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, func(cfg *PersistentSessionStorageConfig) {
		cfg.SessionSnapshotMaxBytes = 64
	})
	session := NewSession("snapshot-too-large-user")
	require.NoError(t, store.Save(ctx, session))
	require.NoError(t, store.AddMessage(ctx, session.ID, *types.NewUserMessage("this message is definitely larger than sixty-four bytes")))

	destination := filepath.Join(t.TempDir(), "too-large.sqlite")
	err := store.SnapshotSession(ctx, session.ID, destination)
	require.ErrorIs(t, err, ErrSessionSnapshotTooLarge)
	_, statErr := os.Stat(destination)
	require.True(t, os.IsNotExist(statErr), "预检拒绝时不得留下 destination")

	stats := store.SnapshotStats()
	require.Equal(t, int64(1), stats.TooLarge)
	require.Equal(t, int64(1), stats.Failed)
}

func TestSnapshotSessionCopiesUserVersion(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	session := NewSession("snapshot-version-user")
	require.NoError(t, store.Save(ctx, session))
	require.NoError(t, store.AddMessage(ctx, session.ID, *types.NewUserMessage("versioned")))

	const wantVersion = 7
	_, err := store.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", wantVersion))
	require.NoError(t, err)

	destination := filepath.Join(t.TempDir(), "version.sqlite")
	require.NoError(t, store.SnapshotSession(ctx, session.ID, destination))

	snapshotDB, err := sql.Open("sqlite3", destination)
	require.NoError(t, err)
	t.Cleanup(func() { _ = snapshotDB.Close() })
	var version int
	require.NoError(t, snapshotDB.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version))
	require.Equal(t, wantVersion, version)
}

func TestSnapshotSessionDegradesWhenPoolUnavailable(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	session := NewSession("snapshot-degrade-user")
	require.NoError(t, store.Save(ctx, session))
	require.NoError(t, store.AddMessage(ctx, session.ID, *types.NewUserMessage("degraded")))

	// 注入不可打开的 DSN（父目录不存在）：专用池必须失败并降级主池。
	store.snapshotDSNOverride = filepath.Join(t.TempDir(), "missing-parent", "snapshot.sqlite")

	destination := filepath.Join(t.TempDir(), "degraded.sqlite")
	require.NoError(t, store.SnapshotSession(ctx, session.ID, destination))

	stats := store.SnapshotStats()
	require.Equal(t, int64(1), stats.Succeeded)
	require.Equal(t, int64(1), stats.DegradedToMainPool)

	snapshotDB, err := sql.Open("sqlite3", destination)
	require.NoError(t, err)
	t.Cleanup(func() { _ = snapshotDB.Close() })
	var messages int
	require.NoError(t, snapshotDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM session_messages WHERE session_id = ?`, session.ID).Scan(&messages))
	require.Equal(t, 1, messages)
}

func TestSnapshotSessionDetectsRowLoss(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	session := NewSession("snapshot-drift-user")
	require.NoError(t, store.Save(ctx, session))
	require.NoError(t, store.AddMessage(ctx, session.ID, *types.NewUserMessage("will be dropped")))

	store.snapshotAfterCopyHook = func(ctx context.Context, connection *sql.Conn, sessionID string) {
		_, _ = connection.ExecContext(ctx, `DELETE FROM snapshot.session_messages WHERE session_id = ?`, sessionID)
	}

	destination := filepath.Join(t.TempDir(), "drift.sqlite")
	err := store.SnapshotSession(ctx, session.ID, destination)
	require.ErrorIs(t, err, ErrSessionSnapshotDrift)
	_, statErr := os.Stat(destination)
	require.True(t, os.IsNotExist(statErr), "一致性校验失败必须回滚并清理 destination")

	stats := store.SnapshotStats()
	require.Equal(t, int64(1), stats.SchemaDrift)
	require.Equal(t, int64(1), stats.Failed)
}

func TestSnapshotSessionSchemaParity(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	session := NewSession("snapshot-schema-user")
	require.NoError(t, store.Save(ctx, session))
	require.NoError(t, store.AddMessage(ctx, session.ID, *types.NewUserMessage("schema")))

	destination := filepath.Join(t.TempDir(), "schema.sqlite")
	require.NoError(t, store.SnapshotSession(ctx, session.ID, destination))

	snapshotDB, err := sql.Open("sqlite3", destination)
	require.NoError(t, err)
	t.Cleanup(func() { _ = snapshotDB.Close() })

	for _, table := range []string{"sessions", "session_messages", "session_prompt_messages"} {
		// 门禁：迁移改表而不同步快照 schema 时，这里必须失败（CI 红）。
		require.Equal(t, sqliteTableColumns(t, store.db, table), sqliteTableColumns(t, snapshotDB, table),
			"snapshot schema drift detected for table %s", table)
	}
}

func sqliteTableColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	require.NoError(t, err)
	defer rows.Close()
	columns := make([]string, 0, 16)
	for rows.Next() {
		var (
			cid, notNull, primaryKey int
			name, dataType           string
			defaultValue             interface{}
		)
		require.NoError(t, rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey))
		columns = append(columns, fmt.Sprintf("%s|%s|%d|%d", name, dataType, notNull, primaryKey))
	}
	require.NoError(t, rows.Err())
	return columns
}

func TestSnapshotSessionDoesNotBlockSessionWrites(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	session := NewSession("snapshot-nonblocking-user")
	require.NoError(t, store.Save(ctx, session))
	require.NoError(t, store.AddMessage(ctx, session.ID, *types.NewUserMessage("before")))

	started := make(chan struct{})
	release := make(chan struct{})
	store.snapshotAfterCopyHook = func(context.Context, *sql.Conn, string) {
		close(started)
		<-release
	}

	destination := filepath.Join(t.TempDir(), "nonblocking.sqlite")
	snapshotDone := make(chan error, 1)
	go func() { snapshotDone <- store.SnapshotSession(ctx, session.ID, destination) }()

	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("snapshot did not reach the copy stage")
	}

	// 快照事务仍在进行：用户写入必须能立即完成（专用池隔离，F1/G1）。
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- store.AddMessage(ctx, session.ID, *types.NewUserMessage("during snapshot"))
	}()
	select {
	case err := <-writeDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("session write blocked behind an in-flight snapshot")
	}

	close(release)
	require.NoError(t, <-snapshotDone)
}

func TestSnapshotSessionDoesNotBlockCheckpoint(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)
	session := NewSession("snapshot-checkpoint-user")
	require.NoError(t, store.Save(ctx, session))
	require.NoError(t, store.AddMessage(ctx, session.ID, *types.NewUserMessage("wal frame")))

	// 手动在专用池连接上持有读事务（模拟进行中的跨进程快照）。
	pool, err := store.snapshotPool()
	require.NoError(t, err)
	connection, err := pool.Conn(ctx)
	require.NoError(t, err)
	defer func() {
		_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		_ = connection.Close()
	}()
	_, err = connection.ExecContext(ctx, "BEGIN")
	require.NoError(t, err)
	var count int
	require.NoError(t, connection.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&count))

	done := make(chan error, 1)
	go func() { done <- store.CloseStorage() }()
	select {
	case closeErr := <-done:
		require.NoError(t, closeErr, "CloseStorage 不应因快照读者而失败")
	case <-time.After(10 * time.Second):
		t.Fatal("CloseStorage blocked behind a snapshot read transaction")
	}

	require.GreaterOrEqual(t, store.SnapshotStats().CheckpointBlocked, int64(1),
		"TRUNCATE 被快照读者阻塞时应降级 PASSIVE 并计数")
}

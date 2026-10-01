package chat

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// insertLegacySessionRow 直接落一条旧格式脏行（绕过写入边界的规范化），
// 模拟生产库里的 /root/p26s3 这类“写得进、读不出”的历史数据。
func insertLegacySessionRow(t *testing.T, store *SQLiteSessionStorage, id, updatedAt string) {
	t.Helper()
	_, err := store.db.ExecContext(context.Background(), `
		INSERT INTO sessions (
			id, user_id, state, title, title_source, summary, message_count, head_offset,
			tags_json, metadata_json, created_at, updated_at, expires_at
		) VALUES (?, 'cleanup-user', 'idle', '', 'derived', '', 0, 0, '[]', '{}', ?, ?, NULL)
	`, id, updatedAt, updatedAt)
	require.NoError(t, err)
}

// 生产事故回归：旧实现对候选 ID 再跑 sanitizeSessionID 后删除，"/root/p26s3"
// 被改写成 "p26s3" 命中 0 行，ErrSessionNotFound 又被吞掉，清理循环永不退出
// （~1 核 + 数百 MB/s 重读直到进程重启）。修复后必须按落库原样 ID 删除并在
// 有限时间内返回。
func TestSQLiteSessionStorageCleanupDeletesUnaddressableIDs(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)

	old := time.Now().UTC().Add(-30 * 24 * time.Hour).Format(time.RFC3339Nano)
	for _, id := range []string{"/root/p26s3", "/root/p26s3b"} {
		insertLegacySessionRow(t, store, id, old)
	}

	// 正常会话：验证 TTL 分支仍然工作。
	normal := NewSession("cleanup-user")
	require.NoError(t, store.Save(ctx, normal))
	_, err := store.db.ExecContext(ctx, `UPDATE sessions SET updated_at = ? WHERE id = ?`, old, normal.ID)
	require.NoError(t, err)

	// 显式过期分支（expires_at 非空）也必须被清理。
	explicit := NewSession("cleanup-user")
	require.NoError(t, store.Save(ctx, explicit))
	_, err = store.db.ExecContext(ctx, `UPDATE sessions SET expires_at = ? WHERE id = ?`,
		time.Now().UTC().Add(-2*time.Hour).Format(time.RFC3339Nano), explicit.ID)
	require.NoError(t, err)

	type outcome struct {
		removed int
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		removed, err := store.Cleanup(ctx, time.Now().UTC().Add(-time.Hour))
		done <- outcome{removed: removed, err: err}
	}()

	select {
	case got := <-done:
		require.NoError(t, got.err)
		require.GreaterOrEqual(t, got.removed, 4)
	case <-time.After(15 * time.Second):
		t.Fatal("cleanup did not return: unaddressable rows must not stall the loop")
	}

	var remaining int
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sessions WHERE id IN ('/root/p26s3', '/root/p26s3b')`).Scan(&remaining))
	require.Zero(t, remaining, "legacy unaddressable rows must be removed by exact id")

	var explicitRemaining int
	require.NoError(t, store.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sessions WHERE id = ?`, explicit.ID).Scan(&explicitRemaining))
	require.Zero(t, explicitRemaining, "explicitly expired rows must be removed")
}

// 无进展守卫：即使候选行因为任何原因删不掉（这里用 BEFORE DELETE 触发器模拟），
// 清理也必须带着 ErrCleanupStalled 快速退出，而不是原地重扫。
func TestSQLiteSessionStorageCleanupStopsWithoutProgress(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)

	old := time.Now().UTC().Add(-30 * 24 * time.Hour).Format(time.RFC3339Nano)
	insertLegacySessionRow(t, store, "blocked-session", old)
	_, err := store.db.ExecContext(ctx,
		`CREATE TRIGGER cleanup_block_delete BEFORE DELETE ON sessions BEGIN SELECT RAISE(IGNORE); END`)
	require.NoError(t, err)

	started := time.Now()
	removed, err := store.Cleanup(ctx, time.Now().UTC().Add(-time.Hour))
	require.ErrorIs(t, err, ErrCleanupStalled)
	require.Zero(t, removed)
	require.Less(t, time.Since(started), 5*time.Second, "stall guard must exit promptly")
}

// v3 索引与查询计划：清理查询不得退化为 SCAN sessions + TEMP B-TREE 排序。
func TestSQLiteSessionStorageCleanupUsesIndexedQueries(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)

	// 造足够多的行，让规划器有理由选择索引（极小的表可能直接全扫）。
	old := time.Now().UTC().Add(-30 * 24 * time.Hour).Format(time.RFC3339Nano)
	tx, err := store.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO sessions (
			id, user_id, state, title, title_source, summary, message_count, head_offset,
			tags_json, metadata_json, created_at, updated_at, expires_at
		) VALUES (?, 'plan-user', 'idle', '', 'derived', '', 0, 0, '[]', '{}', ?, ?, NULL)
	`)
	require.NoError(t, err)
	for i := 0; i < 512; i++ {
		_, err := stmt.ExecContext(ctx, fmt.Sprintf("plan-session-%04d", i), old, old)
		require.NoError(t, err)
	}
	require.NoError(t, stmt.Close())
	require.NoError(t, tx.Commit())

	indexes := map[string]bool{}
	rows, err := store.db.QueryContext(ctx, `PRAGMA index_list(sessions)`)
	require.NoError(t, err)
	for rows.Next() {
		var seq, unique, partial int
		var name, origin string
		require.NoError(t, rows.Scan(&seq, &name, &unique, &origin, &partial))
		indexes[name] = true
	}
	require.NoError(t, rows.Close())
	require.True(t, indexes["idx_sessions_expires"], "missing cleanup index idx_sessions_expires")

	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, tc := range []struct {
		statement string
		arg       string
	}{
		{
			`SELECT id FROM sessions WHERE expires_at IS NOT NULL AND expires_at < ? ORDER BY expires_at ASC, updated_at ASC, id ASC LIMIT 128`,
			now,
		},
		{
			`SELECT id FROM sessions WHERE expires_at IS NULL AND updated_at < ? ORDER BY updated_at ASC, id ASC LIMIT 128`,
			now,
		},
	} {
		planRows, err := store.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+tc.statement, tc.arg)
		require.NoError(t, err)
		var details []string
		for planRows.Next() {
			var id, parent, notUsed int
			var detail string
			require.NoError(t, planRows.Scan(&id, &parent, &notUsed, &detail))
			details = append(details, detail)
		}
		require.NoError(t, planRows.Close())
		plan := strings.Join(details, " | ")
		require.Contains(t, plan, "idx_sessions_expires", "cleanup query must use idx_sessions_expires, plan=%s", plan)
		require.NotContains(t, plan, "TEMP B-TREE", "cleanup query must not sort, plan=%s", plan)
		require.NotContains(t, plan, "SCAN sessions", "cleanup query must not full-scan, plan=%s", plan)
	}
}

// 写入边界：不可寻址 ID 必须被规范化或拒绝，不能再产生新的脏行。
func TestSQLiteSessionStorageWriteNormalizesSessionIDs(t *testing.T) {
	ctx := context.Background()
	store := newTestSQLiteSessionStorage(t, nil)

	session := NewSession("write-guard-user")
	session.ID = "/root/p26s3"
	require.NoError(t, store.Save(ctx, session))
	require.Equal(t, "p26s3", session.ID)

	loaded, err := store.Load(ctx, "p26s3")
	require.NoError(t, err)
	require.Equal(t, "p26s3", loaded.ID)

	bad := NewSession("write-guard-user")
	bad.ID = "<nil>"
	require.ErrorIs(t, store.Save(ctx, bad), ErrUnaddressableSessionID)
}

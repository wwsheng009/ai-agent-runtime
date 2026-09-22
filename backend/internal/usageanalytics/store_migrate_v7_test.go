package usageanalytics

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// downgradeToV6 把当前库还原成 v6 形态（删 usage_subagents 的 v7 增量列），
// 用于模拟"既有 v6 旧库"迁移前状态。
func downgradeToV6(t *testing.T, store *Store) {
	t.Helper()
	for _, column := range []string{"task_type", "task_subject"} {
		if _, err := store.db.Exec("ALTER TABLE usage_subagents DROP COLUMN " + column); err != nil {
			t.Fatalf("drop column %s: %v", column, err)
		}
	}
}

// seedV6SubagentRow 写入一行 v6 形态的子代理记录（只填 v6 及更早的列）。
func seedV6SubagentRow(t *testing.T, store *Store) {
	t.Helper()
	if err := store.execWithLockRetry(`
INSERT INTO usage_subagents (subagent_id, parent_session_id, child_session_id, role, success, completion_reason, completed_at_unix_nano)
VALUES ('legacy-sa-1', 'session-legacy', 'child-legacy', 'researcher', 1, 'completed', ?)`,
		time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC).UnixNano()); err != nil {
		t.Fatalf("seed v6 subagent row: %v", err)
	}
}

// TestStoreMigratesUsageSubagentsV7Columns 锁定 P4 收尾改动点：旧库（v6）缺
// task_type / task_subject 时 Open 幂等补列（TEXT NOT NULL DEFAULT ”），
// 既有行读到空串，重复 Open 不破坏读写。
func TestStoreMigratesUsageSubagentsV7Columns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage_analytics.sqlite")
	store, err := Open(Config{Path: path})
	require.NoError(t, err)
	seedV6SubagentRow(t, store)
	downgradeToV6(t, store)
	if has, err := store.hasColumn("usage_subagents", "task_type"); err != nil || has {
		t.Fatalf("模拟旧库失败：task_type 列仍存在 (has=%v err=%v)", has, err)
	}
	require.NoError(t, store.Close())

	reopened, err := Open(Config{Path: path})
	require.NoError(t, err)
	for _, column := range []string{"task_type", "task_subject"} {
		has, err := reopened.hasColumn("usage_subagents", column)
		require.NoError(t, err)
		if !has {
			t.Fatalf("v7 迁移应补列 usage_subagents.%s", column)
		}
	}
	row := queryRow(t, reopened, `SELECT task_type, task_subject FROM usage_subagents WHERE subagent_id='legacy-sa-1'`)
	if row[0] != "" || row[1] != "" {
		t.Fatalf("旧行补列后应为空串: %v", row)
	}
	// 补列后读路径照常工作；缺省行读到空串（前端按「未记录」处理）。
	stats, err := reopened.SubagentStats(SubagentStatsQuery{})
	require.NoError(t, err)
	if len(stats.Subagents) != 1 || stats.Subagents[0].TaskType != "" || stats.Subagents[0].TaskSubject != "" {
		t.Fatalf("旧行统计应读到空 task_type / task_subject: %#v", stats.Subagents)
	}
	require.NoError(t, reopened.Close())

	// 幂等：重复 Open 不改写列集合与行数。
	again, err := Open(Config{Path: path})
	require.NoError(t, err)
	defer func() { _ = again.Close() }()
	assertRowCount(t, again, `SELECT COUNT(*) FROM usage_subagents`, 1)
}

// TestReadOnlySubagentStatsDegradesMissingV7Columns 锁定只读降级：只读打开旧库
// 不得改写 schema，SubagentStats 读路径按列存在性退化为空串，
// 而不是整条查询报错（与 usage_routes v6 列的退化策略一致）。
func TestReadOnlySubagentStatsDegradesMissingV7Columns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage_analytics.sqlite")
	store, err := Open(Config{Path: path})
	require.NoError(t, err)
	seedV6SubagentRow(t, store)
	downgradeToV6(t, store)
	require.NoError(t, store.Close())

	readOnly := openReadOnly(path, 0)
	defer func() { _ = readOnly.Close() }()
	if readOnly.Empty() {
		t.Fatalf("既有库只读打开不应标记 empty")
	}
	if has, err := readOnly.hasColumn("usage_subagents", "task_type"); err != nil || has {
		t.Fatalf("只读打开不得补列 (has=%v err=%v)", has, err)
	}
	stats, err := readOnly.SubagentStats(SubagentStatsQuery{})
	require.NoError(t, err)
	if len(stats.Subagents) != 1 || stats.Subagents[0].TaskType != "" || stats.Subagents[0].TaskSubject != "" {
		t.Fatalf("只读旧库应退化为空 task_type / task_subject: %#v", stats.Subagents)
	}
	if stats.Summary.Total != 1 {
		t.Fatalf("只读旧库汇总不应受缺列影响: %#v", stats.Summary)
	}
}

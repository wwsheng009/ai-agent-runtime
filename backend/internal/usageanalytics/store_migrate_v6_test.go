package usageanalytics

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// downgradeToV5 把当前库还原成 v5 形态（删 usage_routes 的 v6 增量列），
// 用于模拟"既有 v5 旧库"迁移前状态。
func downgradeToV5(t *testing.T, store *Store) {
	t.Helper()
	for _, column := range []string{"task_type", "task_subject"} {
		if _, err := store.db.Exec("ALTER TABLE usage_routes DROP COLUMN " + column); err != nil {
			t.Fatalf("drop column %s: %v", column, err)
		}
	}
}

// seedV5RouteRow 写入一行 v5 形态的路由观测（只填 v5 及更早的列）。
func seedV5RouteRow(t *testing.T, store *Store) {
	t.Helper()
	if err := store.execWithLockRetry(`
INSERT INTO usage_routes (route_event_id, session_id, scope, kind, reason, recorded_at_unix_nano)
VALUES ('legacy-v5-1', 'session-legacy', 'subagent', 'applied', 'route_resolved', ?)`,
		time.Date(2026, 9, 22, 9, 0, 0, 0, time.UTC).UnixNano()); err != nil {
		t.Fatalf("seed v5 route row: %v", err)
	}
}

// TestStoreMigratesUsageRoutesV6Columns 锁定 plan §9 / doc8 改动点 28：
// 旧库（v5）缺 task_type / task_subject 时 Open 幂等补列（TEXT NOT NULL DEFAULT ”），
// 既有行读到空串，重复 Open 不破坏读写。
func TestStoreMigratesUsageRoutesV6Columns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage_analytics.sqlite")
	store, err := Open(Config{Path: path})
	require.NoError(t, err)
	seedV5RouteRow(t, store)
	downgradeToV5(t, store)
	if has, err := store.hasColumn("usage_routes", "task_type"); err != nil || has {
		t.Fatalf("模拟旧库失败：task_type 列仍存在 (has=%v err=%v)", has, err)
	}
	require.NoError(t, store.Close())

	reopened, err := Open(Config{Path: path})
	require.NoError(t, err)
	for _, column := range []string{"task_type", "task_subject"} {
		has, err := reopened.hasColumn("usage_routes", column)
		require.NoError(t, err)
		if !has {
			t.Fatalf("v6 迁移应补列 usage_routes.%s", column)
		}
	}
	row := queryRow(t, reopened, `SELECT task_type, task_subject FROM usage_routes WHERE route_event_id='legacy-v5-1'`)
	if row[0] != "" || row[1] != "" {
		t.Fatalf("旧行补列后应为空串: %v", row)
	}
	// 补列后读路径照常工作；缺省行不进 by_task_type 桶（不把「未记录」伪装成取值）。
	events, err := reopened.RouteEvents(RouteQuery{})
	require.NoError(t, err)
	if len(events.Events) != 1 || events.Events[0].TaskType != "" || events.Events[0].TaskSubject != "" {
		t.Fatalf("旧行明细应读到空 task_type / task_subject: %#v", events.Events)
	}
	stats, err := reopened.RouteStats(RouteQuery{})
	require.NoError(t, err)
	if len(stats.ByTaskType) != 0 {
		t.Fatalf("缺省行不得进 by_task_type 桶: %#v", stats.ByTaskType)
	}
	require.NoError(t, reopened.Close())

	// 幂等：重复 Open 不改写列集合与行数。
	again, err := Open(Config{Path: path})
	require.NoError(t, err)
	defer func() { _ = again.Close() }()
	assertRowCount(t, again, `SELECT COUNT(*) FROM usage_routes`, 1)
}

// TestReadOnlyRouteEventsDegradesMissingV6Columns 锁定只读降级：只读打开旧库
// 不得改写 schema，stats / events 读路径按列存在性退化为空串与空桶，
// 而不是整条查询报错（与 v5 goal 列的退化策略一致）。
func TestReadOnlyRouteEventsDegradesMissingV6Columns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage_analytics.sqlite")
	store, err := Open(Config{Path: path})
	require.NoError(t, err)
	seedV5RouteRow(t, store)
	downgradeToV5(t, store)
	require.NoError(t, store.Close())

	readOnly := openReadOnly(path, 0)
	defer func() { _ = readOnly.Close() }()
	if readOnly.Empty() {
		t.Fatalf("既有库只读打开不应标记 empty")
	}
	if has, err := readOnly.hasColumn("usage_routes", "task_type"); err != nil || has {
		t.Fatalf("只读打开不得补列 (has=%v err=%v)", has, err)
	}
	events, err := readOnly.RouteEvents(RouteQuery{})
	require.NoError(t, err)
	if len(events.Events) != 1 || events.Events[0].TaskType != "" || events.Events[0].TaskSubject != "" {
		t.Fatalf("只读旧库明细应退化为空 task_type / task_subject: %#v", events.Events)
	}
	stats, err := readOnly.RouteStats(RouteQuery{})
	require.NoError(t, err)
	if len(stats.ByTaskType) != 0 {
		t.Fatalf("只读旧库 by_task_type 应为空桶: %#v", stats.ByTaskType)
	}
	if stats.Totals.Total != 1 {
		t.Fatalf("只读旧库 totals 不应受缺列影响: %#v", stats.Totals)
	}
}

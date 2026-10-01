package knowledge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

// Phase 5 交付 4（GC）测试：物理清理软删除行、保留期、触发口径（软上限 +
// 冷却窗口）、FTS 一致性与只读降级。

// backdateSoftDeleted 把已软删除文件（与其符号）的 deleted_at 回拨到 before：
// "超过保留期"不必真的等 30 天。
func backdateSoftDeleted(t *testing.T, dbPath string, before time.Time, relPaths ...string) {
	t.Helper()
	ctx := context.Background()
	db, err := sqliteutil.OpenFileCtx(ctx, dbPath, true)
	require.NoError(t, err)
	defer db.Close()
	for _, rel := range relPaths {
		_, err := db.ExecContext(ctx,
			`UPDATE files SET deleted_at = ? WHERE path = ?`, unixMillis(before), rel)
		require.NoError(t, err)
		_, err = db.ExecContext(ctx, `
			UPDATE symbols SET deleted_at = ?
			WHERE file_id IN (SELECT id FROM files WHERE path = ?)`, unixMillis(before), rel)
		require.NoError(t, err)
	}
}

// seedSoftDeletedFile 建库 → 索引 → 磁盘删除 → 软删除，返回 workspace id。
func seedSoftDeletedFile(t *testing.T, layer *Layer, root string) string {
	t.Helper()
	ctx := context.Background()
	writeTree(t, root, "demo/a.go", demoGoSource) // 含 helper(...) 调用 → 有 refs
	writeTree(t, root, "demo/keep.go", "package demo\n\nfunc Keep() {}\n")
	if _, err := RunIndex(ctx, layer.Store(), layerConfigForTest(root)); err != nil {
		t.Fatalf("RunIndex(seed): %v", err)
	}
	wsID, err := layer.planWorkspaceID(ctx)
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(root, "demo", "a.go")))
	if _, err := RunIndex(ctx, layer.Store(), layerConfigForTest(root)); err != nil {
		t.Fatalf("RunIndex(soft delete): %v", err)
	}
	rec, ok, err := layer.Store().FileByPath(ctx, wsID, "demo/a.go")
	require.NoError(t, err)
	require.True(t, ok, "软删除必须保留行")
	require.NotZero(t, rec.DeletedAt)
	return wsID
}

func TestLayerRunGCRemovesOldSoftDeletedFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	layer := activateChangeLayerForTest(t, root)
	wsID := seedSoftDeletedFile(t, layer, root)

	backdateSoftDeleted(t, layer.DBPath(), time.Now().Add(-31*24*time.Hour), "demo/a.go")

	report, err := layer.RunGC(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, report.Files, "超过保留期的软删除文件必须被物理清理")
	require.Greater(t, report.Symbols, 0, "其符号必须一并清理")
	require.Greater(t, report.Refs, 0, "其引用必须一并清理")
	require.Equal(t, wsID, report.WorkspaceID)

	// 状态面摘要：一次成功运行、最近报告可见。
	status := layer.GCStats()
	require.Equal(t, 1, status.Runs)
	require.NotNil(t, status.Last)
	require.Equal(t, 1, status.Last.Files)
	require.Empty(t, status.LastError)

	// 行真的没了（不是再标记一次）；存活文件与它的符号不受影响。
	_, ok, err := layer.Store().FileByPath(ctx, wsID, "demo/a.go")
	require.NoError(t, err)
	require.False(t, ok, "GC 后 FileByPath 必须找不到被清理的文件")
	symbols, err := layer.Store().FindSymbols(ctx, SymbolQuery{Name: "OpenFile", Exact: true})
	require.NoError(t, err)
	require.Empty(t, symbols, "被清理文件的符号必须不可见")
	keep, err := layer.Store().FindSymbols(ctx, SymbolQuery{Name: "Keep", Exact: true})
	require.NoError(t, err)
	require.Len(t, keep, 1, "存活文件的符号必须保留")

	// 幂等：没有可清理的行时是纯 no-op。
	second, err := layer.RunGC(ctx)
	require.NoError(t, err)
	require.Zero(t, second.Files)
	require.Zero(t, second.Symbols)
	require.Zero(t, second.Refs)

	// 摘要的"Last"语义：最近一次（这里是 no-op）覆盖上一次。
	status = layer.GCStats()
	require.Equal(t, 2, status.Runs)
	require.NotNil(t, status.Last)
	require.Zero(t, status.Last.Files)

	// FTS 一致性：外键级联不触发触发器，符号必须显式删——否则会留下
	// "搜得到、查不到"的幽灵符号。
	requireFTSMatchesSymbols(t, layer.DBPath())
}

func TestLayerRunGCRespectsRetention(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	layer := activateChangeLayerForTest(t, root)
	wsID := seedSoftDeletedFile(t, layer, root)

	// 刚软删除（保留期内）：不得清理。
	report, err := layer.RunGC(ctx)
	require.NoError(t, err)
	require.Zero(t, report.Files, "保留期内的软删除行必须保留（重命名/引用修复要靠它）")
	_, ok, err := layer.Store().FileByPath(ctx, wsID, "demo/a.go")
	require.NoError(t, err)
	require.True(t, ok)

	// 保留期口径：缺省 30 天，显式配置覆盖。
	require.Equal(t, DefaultGCRetentionDays*24*time.Hour, layerConfigForTest(root).GCRetention())
	one := 1
	require.Equal(t, 24*time.Hour, Config{GCRetentionDays: &one}.GCRetention())
	zero := 0
	require.Equal(t, DefaultGCRetentionDays*24*time.Hour, Config{GCRetentionDays: &zero}.GCRetention(),
		"<= 0 视为未设置，回落缺省值")
}

func TestShouldAutoGC(t *testing.T) {
	now := time.Now()
	mib := int64(1 << 20)
	cases := []struct {
		name        string
		size        int64
		maxMB       int64
		lastAttempt time.Time
		want        bool
	}{
		{"未启用（max<=0）", 10 * mib, 0, time.Time{}, false},
		{"低于软上限", 100 * mib, 512, time.Time{}, false},
		{"高于软上限且从未跑过", 600 * mib, 512, time.Time{}, true},
		{"高于软上限但冷却期内", 600 * mib, 512, now.Add(-time.Minute), false},
		{"高于软上限且冷却期已过", 600 * mib, 512, now.Add(-DefaultGCInterval), true},
		{"大小未知", 0, 512, time.Time{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldAutoGC(tc.size, tc.maxMB, tc.lastAttempt, now, DefaultGCInterval)
			if got != tc.want {
				t.Fatalf("shouldAutoGC = %v, want %v", got, tc.want)
			}
		})
	}
}

// 触发接线：判定点（ObserveVersion）在库超过软上限时必须真的清理，且在冷却
// 窗口内不重复触发。
func TestObserveVersionTriggersAutoGC(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	layer := activateChangeLayerForTest(t, root)
	wsID := seedSoftDeletedFile(t, layer, root)
	backdateSoftDeleted(t, layer.DBPath(), time.Now().Add(-31*24*time.Hour), "demo/a.go")

	// 测试注入口：把"库大小"报成远超软上限（否则要把库真的撑到 512MB）。
	layer.gcSizeFn = func() int64 { return 8 << 30 }

	obs, err := layer.ObserveVersion(ctx, 0, time.Time{})
	require.NoError(t, err)
	require.NotEmpty(t, obs.Version, "GC 不得影响版本采样")
	require.Equal(t, 1, layer.GCStats().Runs, "超过软上限时判定点必须触发一次 GC")
	_, ok, err := layer.Store().FileByPath(ctx, wsID, "demo/a.go")
	require.NoError(t, err)
	require.False(t, ok, "自动 GC 必须真的清理")

	// 再制造一条可清理的行：冷却窗口内不得再次触发。
	writeTree(t, root, "demo/gone.go", "package demo\n\nfunc Gone() {}\n")
	if _, err := RunIndex(ctx, layer.Store(), layerConfigForTest(root)); err != nil {
		t.Fatalf("RunIndex(gone): %v", err)
	}
	require.NoError(t, os.Remove(filepath.Join(root, "demo", "gone.go")))
	if _, err := RunIndex(ctx, layer.Store(), layerConfigForTest(root)); err != nil {
		t.Fatalf("RunIndex(gone soft delete): %v", err)
	}
	backdateSoftDeleted(t, layer.DBPath(), time.Now().Add(-31*24*time.Hour), "demo/gone.go")

	_, err = layer.ObserveVersion(ctx, 0, time.Time{})
	require.NoError(t, err)
	require.Equal(t, 1, layer.GCStats().Runs, "冷却窗口内不得重复触发")
	_, ok, err = layer.Store().FileByPath(ctx, wsID, "demo/gone.go")
	require.NoError(t, err)
	require.True(t, ok, "冷却期内该行仍应在库内")
}

func TestGCDeletedRefusesReadOnlyStore(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "ro.db")
	owner, err := OpenStore(ctx, path, false)
	require.NoError(t, err)
	require.NoError(t, owner.Close())

	reader, err := OpenStore(ctx, path, true)
	require.NoError(t, err)
	defer reader.Close()
	_, err = reader.GCDeleted(ctx, "w1", time.Now())
	require.True(t, errors.Is(err, ErrReadOnlyStore), "只读 store 必须硬失败: %v", err)
}

// reader 角色的 Layer：RunGC 是 no-op（零报告、无错误），自动触发也不得写库。
func TestLayerRunGCReaderIsNoop(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cfg := layerConfigForTest(root)
	owner, err := Activate(ctx, cfg, root, ActivationOptions{SkipInitialIndex: true})
	require.NoError(t, err)
	defer owner.Close()
	reader, err := Activate(ctx, cfg, root, ActivationOptions{SkipInitialIndex: true})
	require.NoError(t, err)
	defer reader.Close()
	require.Equal(t, RoleReader, reader.Role())

	report, err := reader.Layer().RunGC(ctx)
	require.NoError(t, err)
	require.Zero(t, report.Files)
	require.Zero(t, report.Symbols)

	reader.Layer().gcSizeFn = func() int64 { return 8 << 30 }
	_, err = reader.Layer().ObserveVersion(ctx, 0, time.Time{})
	require.Error(t, err, "reader 上工作区未登记，采样按未索引处理")
	require.Zero(t, reader.Layer().GCStats().Runs, "reader 不得触发 GC")
}

// requireFTSMatchesSymbols 断言 symbols_fts 与 symbols 行数一致。
//
// FTS5 不可用的构建（无该表，Search 退化为 LIKE）没有触发器，也就没有"幽灵
// 符号"风险：此时只记录不判定，保持用例在两种构建下都通过。
func requireFTSMatchesSymbols(t *testing.T, dbPath string) {
	t.Helper()
	ctx := context.Background()
	db, err := sqliteutil.OpenFileCtx(ctx, dbPath, true)
	require.NoError(t, err)
	defer db.Close()

	var exists string
	err = db.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'symbols_fts'`).Scan(&exists)
	if err != nil {
		t.Logf("FTS5 不可用（%v）：本构建走 LIKE 回退，无 FTS 一致性风险", err)
		return
	}
	var ftsRows, symbolRows int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM symbols_fts`).Scan(&ftsRows))
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM symbols`).Scan(&symbolRows))
	require.Equal(t, symbolRows, ftsRows,
		"FTS 索引必须与 symbols 行数一致（幽灵符号说明清理漏了 FTS 同步）")
}

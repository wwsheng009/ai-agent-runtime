package knowledge

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/migrate"
	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
)

// Phase 5 / 风险 R12 第三段的测试：库文件损坏时"留证 + 重建 + 降级解释"，
// 且**只**对损坏这一类错误生效（版本不匹配与只读路径绝不动文件）。

// corruptFilesIn 返回目录下所有留证文件（<db>.corrupt-*）。
func corruptFilesIn(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.corrupt-*"))
	require.NoError(t, err)
	return matches
}

func TestIsCorruptStoreError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"版本不匹配（拒绝，不是损坏）", migrate.ErrSchemaNewer, false},
		{"旧库提示", errString("knowledge: store schema v1 is older than this binary (v3); open it once as writer to migrate"), false},
		{"非数据库文件", errString("knowledge: open store: file is not a database"), true},
		{"磁盘镜像损坏", errString("database disk image is malformed"), true},
		{"其他错误", errString("knowledge: store is read-only (another process owns it)"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isCorruptStoreError(tc.err))
		})
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// 损坏库 → owner 打开必须留证 + 重建（而不是让用户卡住），且状态面解释得清。
func TestOpenRebuildsCorruptStoreAndKeepsEvidence(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cfg := layerConfigForTest(root)

	// 先用正常路径建库（迁移 + FTS 就绪），再写入垃圾字节模拟损坏。
	act, err := Activate(ctx, cfg, root, ActivationOptions{SkipInitialIndex: true})
	require.NoError(t, err)
	dbPath := act.DBPath()
	require.NoError(t, act.Close())
	const garbage = "this is not a sqlite database at all\n"
	require.NoError(t, os.WriteFile(dbPath, []byte(garbage), 0o644))

	layer, err := Open(ctx, cfg)
	require.NoError(t, err, "损坏库必须被重建，而不是把用户卡在打不开")
	defer func() { _ = layer.Close() }()

	evidence := layer.recoveredFrom
	require.NotEmpty(t, evidence, "必须留证（路径记在 Layer 上）")
	require.FileExists(t, evidence, "损坏文件必须改名留证而不是删除")
	content, err := os.ReadFile(evidence)
	require.NoError(t, err)
	require.Contains(t, string(content), "not a sqlite database", "留证文件必须是原始损坏内容")

	// 重建后的库可用：能索引、能查询。
	writeTree(t, root, "demo/a.go", demoGoSource)
	if _, err := RunIndex(ctx, layer.Store(), cfg); err != nil {
		t.Fatalf("RunIndex(after rebuild): %v", err)
	}
	wsID, err := layer.planWorkspaceID(ctx)
	require.NoError(t, err)
	stats, err := layer.Store().Stats(ctx, wsID)
	require.NoError(t, err)
	require.Greater(t, stats.Files, int64(0), "重建后必须能重新索引")

	// 状态面：留证路径 + 降级原因（空索引必须被解释，不能静默）。
	report, err := layer.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, evidence, report.StoreRecoveredFrom)
	require.Contains(t, report.DegradedReason, "rebuilt after corruption")
	require.Contains(t, report.DegradedReason, evidence)
}

// 版本不匹配（库比二进制新）不是损坏：必须继续拒绝，且**绝不**改名重建——
// 否则旧代码会把更新的数据毁掉。
func TestOpenDoesNotQuarantineNewerSchemaStore(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cfg := layerConfigForTest(root)

	path := cfg.storePath()
	store, err := OpenStore(ctx, path, false)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	migrations, err := Migrations()
	require.NoError(t, err)
	future := migrations[len(migrations)-1].Version + 1
	db, err := sqliteutil.OpenFileCtx(ctx, path, true)
	require.NoError(t, err)
	_, err = db.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, 'future', ?)`,
		future, time.Now().UTC().Format(time.RFC3339Nano))
	require.NoError(t, err)
	require.NoError(t, db.Close())

	_, err = Open(ctx, cfg)
	require.ErrorIs(t, err, migrate.ErrSchemaNewer, "新库必须被拒绝而不是重建")
	require.Empty(t, corruptFilesIn(t, filepath.Dir(path)), "版本不匹配绝不留证改名")
	require.FileExists(t, path, "原库必须原样保留")

	// 原库仍可被"更新的二进制"读取：schema_migrations 里的未来版本还在。
	raw, err := sqliteutil.OpenFileCtx(ctx, path, true)
	require.NoError(t, err)
	defer raw.Close()
	var version int
	require.NoError(t, raw.QueryRowContext(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&version))
	require.Equal(t, future, version)
}

// 只读路径（reader 语义的 store 打开）不改名：reader 没有写权限，
// 处置权必须留给 owner，而不是"顺手"动别人的文件。
func TestOpenStoreReadOnlyDoesNotQuarantineCorruptFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "knowledge.db")
	const garbage = "definitely not a database\n"
	require.NoError(t, os.WriteFile(path, []byte(garbage), 0o644))

	_, err := OpenStore(ctx, path, true)
	require.Error(t, err, "只读打开损坏库必须报错")
	require.Empty(t, corruptFilesIn(t, dir), "只读路径不得留证改名")
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, garbage, string(content), "原文件必须原样保留")
}

package sqliteutil

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestWriteTransactionsUseImmediateOptions 是全仓写事务门禁（P0.5b）：
// 除白名单外，非测试代码里的每个 BeginTx 都必须显式使用 WriteTxOptions
// （driver 映射 BEGIN IMMEDIATE），否则 deferred 的"读后写"事务在 WAL 下会被
// 并发写者顶成 SQLITE_BUSY_SNAPSHOT/517，且重试同一事务永不成功。
//
// 白名单只允许"连接池 DSN 已注入 _txlock=immediate"的存储（在该调用上等价于
// IMMEDIATE）。白名单会自校验：对应包目录必须真的出现 `_txlock` 注入，防止
// 清单腐烂（例如将来有人删掉 DSN 注入）。
//
// 纯只读事务不在本门禁的范围：请改用读写分离的读连接，或在此登记并写明理由。
func TestWriteTransactionsUseImmediateOptions(t *testing.T) {
	root := filepath.Join("..", "..")
	allowlist := map[string]string{
		// 各自构造可写 DSN 时注入 _txlock=immediate（见包内 *_dsn 助手）。
		"cmd/session-dedupe/main.go":             "sessionMessageDSN 追加 _txlock=immediate",
		"internal/subagentbatch/sqlite_store.go": "batchDSNOptions 追加 _txlock=immediate",
		"internal/supervision/sqlite_store.go":   "supervisionAppendDSN 追加 _txlock=immediate",
		"internal/usageanalytics/store_stats.go": "writableDSN 追加 _txlock=immediate",
	}

	skipDirs := map[string]bool{
		".git": true, ".tmp": true, ".scratch": true, "node_modules": true,
		"vendor": true, "testdata": true, "frontend": true, "output": true,
	}

	// 检测器自检：保证门禁不会因分类逻辑退化而变成"永不失败"。
	require.True(t, isDeferredBeginTx("tx, err := db.BeginTx(ctx, nil)"))
	require.True(t, isDeferredBeginTx("tx, err := conn.BeginTx(ctx, nil)"))
	require.False(t, isDeferredBeginTx("tx, err := db.BeginTx(ctx, sqliteutil.WriteTxOptions)"))
	require.False(t, isDeferredBeginTx("// tx, err := db.BeginTx(ctx, nil)"))

	var violations []string
	scannedFiles := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if skipDirs[entry.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		scannedFiles++
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for index, line := range strings.Split(string(data), "\n") {
			if !isDeferredBeginTx(line) {
				continue
			}
			if _, allowed := allowlist[rel]; allowed {
				continue
			}
			violations = append(violations, fmt.Sprintf("%s:%d %s", rel, index+1, strings.TrimSpace(line)))
		}
		return nil
	})
	require.NoError(t, err)
	require.Greater(t, scannedFiles, 100, "门禁必须真正扫到仓库源码（root=%s）", root)

	// 白名单自校验：对应包目录必须真的注入 _txlock。
	for file, reason := range allowlist {
		dir := filepath.Dir(filepath.Join(root, file))
		require.True(t, dirInjectsTxlock(t, dir),
			"白名单 %s（%s）的包目录已找不到 _txlock 注入，请改用 WriteTxOptions", file, reason)
	}

	require.Empty(t, violations,
		"以下写事务未使用 sqliteutil.WriteTxOptions（deferred 读后写会触发 SQLITE_BUSY_SNAPSHOT/517）：\n%s\n若为只读事务请在门禁白名单登记理由。",
		strings.Join(violations, "\n"))
}

// isDeferredBeginTx 识别未显式声明 IMMEDIATE 的事务开启行（注释行不算）。
func isDeferredBeginTx(line string) bool {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "//") {
		return false
	}
	return strings.Contains(line, "BeginTx(") && !strings.Contains(line, "WriteTxOptions")
}

func dirInjectsTxlock(t *testing.T, dir string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		if strings.Contains(string(data), "_txlock") {
			return true
		}
	}
	return false
}

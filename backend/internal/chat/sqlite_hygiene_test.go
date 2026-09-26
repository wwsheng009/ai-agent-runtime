package chat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 源码级护栏：以下操作在 2026-09-26 的 session_runtime.sqlite / artifacts.sqlite
// 损坏事故后被明确禁用（根因是 Windows 驱动层的 WAL-index 拷贝实现，见
// sqlite_journal_policy.go；这些操作是放大器/高风险形态）：
//
//   - PRAGMA auto_vacuum            —— 在线自回收会移动页并重写 ptrmap；
//   - PRAGMA incremental_vacuum(…)  —— 同上（本仓库已整体移除该维护任务）；
//   - wal_checkpoint(TRUNCATE)      —— 关库即截断 WAL/推进 wal-index，没有持久性
//     收益，却是并发缺陷的高发点。
//
// 约定：注释里可以（也应该）提到它们来解释“为什么禁用”，但非测试源码里不得出现
// 可执行语句。任何回潮都必须在这里失败，而不是在生产库里以
// "database disk image is malformed" 的形式出现。
func TestNoHighRiskSQLiteMaintenanceStatements(t *testing.T) {
	banned := []string{
		"PRAGMA auto_vacuum",
		"incremental_vacuum(",
		"wal_checkpoint(TRUNCATE",
	}
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	scanned := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(".", name))
		require.NoError(t, err)
		scanned++
		for _, raw := range strings.Split(string(data), "\n") {
			code := raw
			if idx := strings.Index(code, "//"); idx >= 0 {
				code = code[:idx]
			}
			for _, token := range banned {
				if strings.Contains(code, token) {
					t.Fatalf("%s 含被禁用的 SQLite 操作 %q：%s", name, token, strings.TrimSpace(raw))
				}
			}
		}
	}
	require.Greater(t, scanned, 30, "护栏必须真正扫到本包的源码文件")
}

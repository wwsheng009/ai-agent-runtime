package sqliteutil

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ============================================================================
// 源码级护栏（仓库级）：2026-09-26 的 session_runtime.sqlite / artifacts.sqlite
// WAL 损坏事故后，"在线数据库文件维护"被明确禁止。运行时库只允许：
//   - 连接级 PRAGMA（busy_timeout / cache_size / synchronous / journal_mode=WAL 首开等）；
//   - 关闭时的 wal_checkpoint(PASSIVE)；
//   - 打开前的陈旧 -shm 对账；
//   - 行级 DELETE（不回收文件）与只读快照（VACUUM INTO / ATTACH 只读源库）。
//
// 本测试扫描 backend 模块全部非测试 Go 源码（注释与反引号字符串不参与判定；
// SQL 语句按普通字符串字面量判定），禁止以下可执行语句回潮：
//   - incremental_vacuum(...)                    —— 在线搬页 + 重写 ptrmap；
//   - wal_checkpoint(TRUNCATE|RESTART|FULL|LOG)  —— 放大并发驱动的 wal-index 缺陷；
//   - PRAGMA auto_vacuum                         —— 改写文件头自回收模式；
//   - VACUUM（含 VACUUM INTO）                    —— 文件级重建。
//   - ATTACH DATABASE                            —— 跨库文件耦合，新增必须显式登记。
//
// 白名单必须写明"为什么源库只读 / 为什么保证离线"；规则未命中任何语句时测试
// 也会失败，防止豁免在代码迁移后变成静默掩护。
// ============================================================================

type sqliteMaintenanceBannedToken struct {
	token string
	hint  string
}

var sqliteMaintenanceBannedTokens = []sqliteMaintenanceBannedToken{
	{"incremental_vacuum(", "在线搬页并重写 ptrmap，是 2026-09 损坏形态的高风险来源"},
	{"wal_checkpoint(TRUNCATE", "关库即截断 WAL/推进 wal-index，没有持久性收益"},
	{"wal_checkpoint(RESTART", "无持久性收益，放大并发驱动缺陷"},
	{"wal_checkpoint(FULL", "关闭路径只允许 PASSIVE checkpoint"},
	{"wal_checkpoint(LOG", "关闭路径只允许 PASSIVE checkpoint"},
	{"PRAGMA auto_vacuum", "改写文件头自回收模式；只允许离线压缩工具"},
	{"VACUUM", "文件级重建；运行时在线路径禁止，只允许离线工具/只读快照"},
	{"ATTACH DATABASE", "跨库文件耦合；只允许只读快照目标与全局邮箱两个已知点"},
}

// sqliteMaintenanceAllowRule 是白名单条目：pathSuffix 按 / 归一化路径匹配；
// requireContains 非空时，行内还必须包含该子串才豁免。
type sqliteMaintenanceAllowRule struct {
	pathSuffix      string
	token           string
	requireContains string
	reason          string
	matched         int
}

func (r *sqliteMaintenanceAllowRule) allows(path, line, token string) bool {
	if !strings.HasSuffix(path, r.pathSuffix) || token != r.token {
		return false
	}
	if r.requireContains != "" && !strings.Contains(line, r.requireContains) {
		return false
	}
	r.matched++
	return true
}

var sqliteMaintenanceAllowRules = []*sqliteMaintenanceAllowRule{
	{
		pathSuffix: "cmd/aicli/commands/storage_compact.go",
		token:      "VACUUM",
		reason:     "离线压缩工具：唯一认可的空间回收入口（独占访问、quick_check 前置）",
	},
	{
		pathSuffix: "cmd/aicli/commands/storage_compact.go",
		token:      "PRAGMA auto_vacuum",
		reason:     "离线压缩读取模式并把历史库转回 auto_vacuum=NONE",
	},
	{
		pathSuffix:      "internal/chat/sqlite_storage.go",
		token:           "VACUUM",
		requireContains: "VACUUM INTO",
		reason:          "会话快照：只读源库、只写目标文件，带超时与失败降级",
	},
	{
		pathSuffix:      "internal/chat/sqlite_storage.go",
		token:           "ATTACH DATABASE",
		requireContains: "AS snapshot",
		reason:          "会话快照：事务只读 main、只写 snapshot 目标库",
	},
	{
		pathSuffix:      "internal/agentcontrol/mailbox.go",
		token:           "ATTACH DATABASE",
		requireContains: "ATTACH DATABASE ? AS ",
		reason:          "全局邮箱：运行期事务内挂载既有库，随事务 DETACH",
	},
}

func TestNoOnlineDatabaseFileMaintenanceStatements(t *testing.T) {
	root := filepath.Join("..", "..")
	files, err := collectSQLiteGuardGoFiles(root)
	if err != nil {
		t.Fatalf("扫描 backend 源码失败：%v", err)
	}
	if len(files) < 200 {
		t.Fatalf("护栏扫描面过小（%d 个非测试 Go 文件）：路径基准可能漂移", len(files))
	}

	var violations []string
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", path, err)
		}
		code := stripGoCommentsAndRawStrings(string(raw))
		slashPath := filepath.ToSlash(path)
		for index, line := range strings.Split(code, "\n") {
			for _, banned := range sqliteMaintenanceBannedTokens {
				if !strings.Contains(line, banned.token) {
					continue
				}
				allowed := false
				for _, rule := range sqliteMaintenanceAllowRules {
					if rule.allows(slashPath, line, banned.token) {
						allowed = true
						break
					}
				}
				if !allowed {
					violations = append(violations, fmt.Sprintf(
						"%s:%d 命中禁用语句 %q（%s）: %s",
						slashPath, index+1, banned.token, banned.hint, strings.TrimSpace(line)))
				}
			}
		}
	}
	if len(violations) > 0 {
		t.Fatalf("发现在线数据库文件维护语句（%d 处）：\n%s", len(violations), strings.Join(violations, "\n"))
	}
	for _, rule := range sqliteMaintenanceAllowRules {
		if rule.matched == 0 {
			t.Fatalf("白名单规则 %s/%s 未命中任何语句：代码可能已迁移，请删除或更新该豁免（%s）",
				rule.pathSuffix, rule.token, rule.reason)
		}
	}
}

// collectSQLiteGuardGoFiles 收集 backend 模块下的非测试 Go 源码；隐藏目录、
// vendor/node_modules/testdata 及纯运行时数据目录不参与扫描。
func collectSQLiteGuardGoFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path == root {
				return nil
			}
			name := entry.Name()
			if strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			switch name {
			case "vendor", "node_modules", "testdata", "tmp", "output", "logs", "data", "artifacts":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	return files, err
}

// stripGoCommentsAndRawStrings 移除行/块注释与反引号字符串内容（保留换行以维持
// 行号），保留普通字符串字面量的内容，这样可执行 SQL 仍会被扫到，而帮助文本
// （raw string）与解释性注释不会造成误报。
func stripGoCommentsAndRawStrings(src string) string {
	var out strings.Builder
	out.Grow(len(src))
	i, n := 0, len(src)
	for i < n {
		switch c := src[i]; {
		case c == '"' || c == '\'':
			quote := c
			out.WriteByte(c)
			i++
			for i < n {
				ch := src[i]
				out.WriteByte(ch)
				if ch == '\\' && i+1 < n {
					out.WriteByte(src[i+1])
					i += 2
					continue
				}
				i++
				if ch == quote {
					break
				}
			}
		case c == '`':
			out.WriteByte('`')
			i++
			for i < n && src[i] != '`' {
				if src[i] == '\n' {
					out.WriteByte('\n')
				}
				i++
			}
			if i < n {
				out.WriteByte('`')
				i++
			}
		case c == '/' && i+1 < n && src[i+1] == '/':
			for i < n && src[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && src[i+1] == '*':
			i += 2
			for i+1 < n && !(src[i] == '*' && src[i+1] == '/') {
				if src[i] == '\n' {
					out.WriteByte('\n')
				}
				i++
			}
			i += 2
		default:
			out.WriteByte(c)
			i++
		}
	}
	return out.String()
}

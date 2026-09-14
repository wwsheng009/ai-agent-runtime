package tools

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// gitWorkTreeNegativeTTL 是负结果（不在 git 工作树内）的缓存存活时间。
	//
	// 非 git 工程里的连续编辑因此只在上一次探测过期后重新向上探测父目录链，
	// 不必每写一个文件就走一遍目录链；代价是会话中途 `git init` 最迟在该
	// 窗口之后才生效（窗口内仍按“非 git”处理，继续落 `.backups/`）。
	// 取 10s：足够吸收一次编辑风暴，又短到人几乎察觉不到判定切换。
	gitWorkTreeNegativeTTL = 10 * time.Second

	// gitWorkTreeReverifyInterval 是正结果复核节流窗口：命中缓存的“在 git 内”
	// 判定在该窗口内直接复用（零系统调用），超出窗口才用一次 os.Stat 复核
	// 工作树根的 `.git` 是否仍在。仓库在窗口内被删除/迁移时会短暂沿用旧判定
	// （表现为继续跳过 `.backups/`），换取热路径上每次编辑省下一次 stat。
	gitWorkTreeReverifyInterval = 30 * time.Second
)

// gitWorkTreeNow 供测试注入时钟，验证 TTL 与复核节流而无需真实 sleep。
var gitWorkTreeNow = time.Now

// gitWorkTreeStat 供测试统计探测过程中的文件系统调用，默认 os.Stat。
var gitWorkTreeStat = os.Stat

// gitWorkTreeEntry 是缓存条目。root 为空表示负结果（仅在 expiresAt 之前有效）；
// root 非空表示正结果，verifiedAt 记录上次复核该工作树根的时刻。
type gitWorkTreeEntry struct {
	root       string
	expiresAt  time.Time
	verifiedAt time.Time
}

// gitWorkTreeCache 缓存“查询目录 → git 工作树判定”，key 为查询目录的绝对清理
// 路径。正结果长期保留但受复核节流约束；负结果只保留 gitWorkTreeNegativeTTL。
var gitWorkTreeCache sync.Map

// isInsideGitWorkTree 判断 dir 是否位于 git 工作树内：优先命中进程级缓存，
// 否则向上逐级查找 `.git` 条目（目录，或 worktree/submodule 使用的 gitfile），
// 命中即认为处于 git 工程内。
//
// 纯文件系统探测：不依赖 git 可执行文件、不启动子进程，语义与
// internal/foldertrust 及命令层 findGitRoot 的既有实现保持一致。
//
// 降频策略（按代价从低到高）：
//  1. 负结果在 gitWorkTreeNegativeTTL 内直接复用——零系统调用；
//  2. 正结果在 gitWorkTreeReverifyInterval 内直接复用——零系统调用；
//  3. 正结果超出复核窗口时用一次 os.Stat 复核工作树根的 `.git` 仍在，仍在则续期；
//  4. 复核失败或缓存缺失时向上逐级探测父目录链，并回填缓存。
//
// 用途：`edit` 在 git 工程内不再重复落 `.backups/` 本地回滚点——改动前的
// 内容由版本库承担（git diff / git checkout 可取回），避免大量未跟踪备份
// 目录污染工作区；非 git 工程仍保留原有备份行为。
func isInsideGitWorkTree(dir string) bool {
	dir = normalizeGitProbeDir(dir)
	if dir == "" {
		return false
	}
	now := gitWorkTreeNow()

	if entry, ok := loadGitWorkTreeEntry(dir); ok {
		switch {
		case entry.root != "":
			// 正结果：复核窗口内直接复用，避免每次编辑都做一次 stat。
			if now.Sub(entry.verifiedAt) < gitWorkTreeReverifyInterval {
				return true
			}
			if gitEntryAt(entry.root) {
				gitWorkTreeCache.Store(dir, gitWorkTreeEntry{root: entry.root, verifiedAt: now})
				return true
			}
			// 工作树的 .git 已消失（仓库删除/迁移），丢弃失效缓存并重新探测。
			gitWorkTreeCache.Delete(dir)
		case now.Before(entry.expiresAt):
			// 负结果仍在短 TTL 内：直接复用，避免重复向上探测父目录链。
			return false
		default:
			// 负结果已过期：重新探测，让会话中途的 `git init` 得以生效。
			gitWorkTreeCache.Delete(dir)
		}
	}

	root := findGitWorkTreeRoot(dir)
	if root == "" {
		if gitWorkTreeNegativeTTL > 0 {
			gitWorkTreeCache.Store(dir, gitWorkTreeEntry{expiresAt: now.Add(gitWorkTreeNegativeTTL)})
		}
		return false
	}
	gitWorkTreeCache.Store(dir, gitWorkTreeEntry{root: root, verifiedAt: now})
	return true
}

// findGitWorkTreeRoot 自 dir 向上探测 git 工作树根，未命中返回空串。
func findGitWorkTreeRoot(dir string) string {
	for {
		if gitEntryAt(dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// gitEntryAt 报告 dir 下是否存在 `.git` 条目（目录，或 worktree/submodule 的
// gitfile）。
func gitEntryAt(dir string) bool {
	info, err := gitWorkTreeStat(filepath.Join(dir, ".git"))
	if err != nil {
		return false
	}
	return info.IsDir() || info.Mode().IsRegular()
}

func loadGitWorkTreeEntry(dir string) (gitWorkTreeEntry, bool) {
	value, ok := gitWorkTreeCache.Load(dir)
	if !ok {
		return gitWorkTreeEntry{}, false
	}
	entry, ok := value.(gitWorkTreeEntry)
	return entry, ok
}

// normalizeGitProbeDir 把查询目录规范化为缓存 key：去空白、转绝对、清理路径。
// 绝对路径（edit 传入的目录总是绝对路径）只需一次 Clean，避免在每次判定前
// 重复做 Abs+Clean 的字符串分配。
func normalizeGitProbeDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir)
	}
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return filepath.Clean(dir)
}

// clearGitWorkTreeCache 清空进程级缓存；仅供测试隔离使用。
func clearGitWorkTreeCache() {
	gitWorkTreeCache.Range(func(key, _ interface{}) bool {
		gitWorkTreeCache.Delete(key)
		return true
	})
}

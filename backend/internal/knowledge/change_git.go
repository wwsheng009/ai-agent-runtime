package knowledge

import (
	"context"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Phase 5 交付 1（变更源 2）：git 校正源。
//
// agent edit hook（变更源 1）只覆盖"工具写盘"；shell/exec 写盘、外部进程写盘、
// git 操作（checkout / pull / reset / stash）都会绕过它，索引会静默变旧。
// 本文件用 git 自己的状态视图把这些变化找回来：
//
//   - HEAD 移动（checkout / pull / reset --hard / merge）：取 old..new 的差异文件集；
//   - 工作树状态（status --porcelain）：修改 / 未跟踪 / 删除 / 重命名与复制两侧。
//
// 契约：
//   - 非 git 工作区、git 不可用、仓库尚无 HEAD（空仓库）→ Skipped=true，**不是错误**
//     （校正源永不阻断会话，也永不把"没变化"报成变化）；
//   - 只读：所有命令带 `GIT_OPTIONAL_LOCKS=0`，`git status` 不刷新 .git/index、
//     不加锁、不改仓库状态；
//   - 路径统一为**工作区相对**、'/' 分隔、排序去重；工作区外的仓库路径（工作区是
//     仓库子目录时）一律丢弃——索引范围就是工作区。

// GitRunner 执行一条只读 git 命令并返回 stdout（stderr 归入 err）。
// 注入点仅用于测试与替换实现；默认实现见 defaultGitRunner。
type GitRunner func(ctx context.Context, dir string, args ...string) ([]byte, error)

func defaultGitRunner(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	// 只读契约：不让 status/rev-parse 抢 .git 锁或刷新 index。
	cmd.Env = append(cmd.Environ(), "GIT_OPTIONAL_LOCKS=0")
	return cmd.Output()
}

// GitChangeReport 是一次 git 校正的观测结果。
type GitChangeReport struct {
	// RepoRoot 是 `rev-parse --show-toplevel` 的结果；非仓库时为空。
	RepoRoot string
	// Head 是当前 HEAD（空仓库 / 非仓库时为空）。
	Head string
	// HeadMoved 表示本次检测到 HEAD 相对上次发生变化。
	HeadMoved bool
	// Paths 是发现的变化路径（工作区相对、'/' 分隔、已排序去重）。
	Paths []string
	// Skipped / SkipReason：非仓库或 git 不可用时跳过本次校正。
	Skipped    bool
	SkipReason string
}

// GitChangeSource 检测 git 工作区的变化；记住上次 HEAD 以识别 HEAD 移动。
//
// 非并发安全的部分只在 mu 内（lastHead）；Detect 本身可被串行调用（Layer 侧已
// 有节流与互斥），并发调用是安全的（lastHead 读写有锁）。
type GitChangeSource struct {
	workspace string
	runner    GitRunner
	mu        sync.Mutex
	lastHead  string
	// lastStatus 是上次观测到的"路径 → XY 状态码"，用于按**转移**报告工作树变化。
	lastStatus map[string]string
}

// NewGitChangeSource 创建校正源；runner 为 nil 时使用默认 git 执行器。
func NewGitChangeSource(workspace string, runner GitRunner) *GitChangeSource {
	if runner == nil {
		runner = defaultGitRunner
	}
	return &GitChangeSource{workspace: strings.TrimSpace(workspace), runner: runner}
}

// Detect 运行一次校正：HEAD 移动差异 + 工作树状态。
func (g *GitChangeSource) Detect(ctx context.Context) (GitChangeReport, error) {
	var report GitChangeReport
	if g == nil || g.workspace == "" {
		report.Skipped = true
		report.SkipReason = "workspace is empty"
		return report, nil
	}

	rootOut, err := g.runner(ctx, g.workspace, "rev-parse", "--show-toplevel")
	if err != nil {
		report.Skipped = true
		report.SkipReason = "not a git repository"
		return report, nil
	}
	root := firstGitLine(rootOut)
	if root == "" {
		report.Skipped = true
		report.SkipReason = "git repository root is empty"
		return report, nil
	}
	report.RepoRoot = root

	if headOut, err := g.runner(ctx, root, "rev-parse", "HEAD"); err == nil {
		head := firstGitLine(headOut)
		report.Head = head
		if head != "" {
			g.mu.Lock()
			previous := g.lastHead
			g.lastHead = head
			g.mu.Unlock()
			if previous != "" && previous != head {
				report.HeadMoved = true
				report.Paths = append(report.Paths, g.diffPaths(ctx, root, previous, head)...)
			}
		}
	}

	report.Paths = append(report.Paths, g.statusTransitions(ctx, root)...)
	report.Paths = g.workspaceRelative(root, report.Paths)
	return report, nil
}

// diffPaths 返回 old..new 之间内容有差异的文件（--no-renames：重命名折成
// 删除 + 新增两侧，索引侧两条路径都要处理）。
func (g *GitChangeSource) diffPaths(ctx context.Context, root, oldHead, newHead string) []string {
	out, err := g.runner(ctx, root, "diff", "--name-only", "-z", "--no-renames", oldHead, newHead)
	if err != nil {
		return nil
	}
	return splitGitPaths(out)
}

// statusTransitions 解析 `git status --porcelain -z --untracked-files=all` 并只返回
// **相对上次观测发生转移**的路径：新出现的条目、或状态码变化的条目。
//
// 为什么不按"当前是否脏"报告：一个持续 modified 的文件（相对 HEAD 一直没提交）
// 会在每次校正里重复上报，让版本判定永远带 pending 标记——索引其实早就追上了。
// 持续修改的**内容**变化由 stat 校正负责（与 store 比对，索引落地后自动清除），
// git 侧只需补齐 stat 看不见的维度：未跟踪新文件、重命名/复制的两侧。
func (g *GitChangeSource) statusTransitions(ctx context.Context, root string) []string {
	out, err := g.runner(ctx, root, "status", "--porcelain", "-z", "--untracked-files=all")
	if err != nil {
		return nil
	}
	current := parseGitStatusCodes(out)
	g.mu.Lock()
	previous := g.lastStatus
	g.lastStatus = current
	g.mu.Unlock()
	if len(current) == 0 {
		return nil
	}
	paths := make([]string, 0, len(current))
	for path, code := range current {
		if prev, ok := previous[path]; ok && prev == code {
			continue
		}
		paths = append(paths, path)
	}
	return paths
}

// parseGitStatusCodes 把 porcelain v1 的 `-z` 输出解析成"路径 → XY"。
// 重命名 / 复制的两个路径都记录（两侧都要处理）。
func parseGitStatusCodes(out []byte) map[string]string {
	fields := splitGitFields(out)
	if len(fields) == 0 {
		return nil
	}
	codes := make(map[string]string, len(fields))
	for i := 0; i < len(fields); i++ {
		entry := fields[i]
		if len(entry) < 4 {
			continue // "XY path" 最短 4 字节；空/畸形项直接跳过（永不误报）
		}
		code := entry[:2]
		codes[entry[3:]] = code
		// 重命名 / 复制在 -z 格式里多带一个原路径字段。
		if code[0] == 'R' || code[0] == 'C' || code[1] == 'R' || code[1] == 'C' {
			if i+1 < len(fields) {
				i++
				codes[fields[i]] = code
			}
		}
	}
	return codes
}

// workspaceRelative 把仓库相对路径折成工作区相对路径并排序去重；
// 工作区外的路径（工作区是仓库子目录）与空路径被丢弃。
func (g *GitChangeSource) workspaceRelative(root string, paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	workspace := g.workspace
	if abs, err := filepath.Abs(workspace); err == nil {
		workspace = abs
	}
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		abs := p
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(root, filepath.FromSlash(p))
		}
		if !pathWithinRoot(workspace, abs) {
			continue
		}
		rel, err := filepath.Rel(workspace, abs)
		if err != nil || rel == "." {
			continue
		}
		rel = filepath.ToSlash(rel)
		if seen[rel] {
			continue
		}
		seen[rel] = true
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}

// firstGitLine 取首个非空行（git 输出可能带尾随换行）。
func firstGitLine(out []byte) string {
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// splitGitPaths 拆 `-z` 输出为路径列表（NUL 分隔，忽略空项）。
func splitGitPaths(out []byte) []string {
	fields := splitGitFields(out)
	paths := make([]string, 0, len(fields))
	for _, f := range fields {
		if f != "" {
			paths = append(paths, f)
		}
	}
	return paths
}

// splitGitFields 按 NUL 拆字段，保留空项位置（status -z 的字段序号有语义）。
func splitGitFields(out []byte) []string {
	if len(out) == 0 {
		return nil
	}
	raw := strings.Split(string(out), "\x00")
	if n := len(raw); n > 0 && raw[n-1] == "" {
		raw = raw[:n-1] // 尾随 NUL 产生的空项
	}
	return raw
}

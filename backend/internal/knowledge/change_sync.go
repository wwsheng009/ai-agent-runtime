package knowledge

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Phase 5 交付 1（变更源 2）：外部变更校正的 Layer 级入口。
//
// edit hook（变更源 1）是"同步标记"：工具写盘时当场入队。它覆盖不到的变化
// （shell/exec 写盘、外部进程写盘、git checkout/pull/reset、以及
// `git checkout -- <file>` 这类"还原到 HEAD、status 干净"的变化）由本文件
// 的**校正源**找回：
//
//   - stat 校正（change_scan.go）：已索引文件的 size+mtime 与磁盘比对。
//     每次调用都跑：它廉价（O(已索引文件) 次 stat），且是"内容被改回旧值、
//     git status 干净"这类变化的唯一发现者——stale 判定的正确性靠它兜底。
//   - git 校正（change_git.go）：HEAD 移动差异 + 工作树状态（含**未跟踪新文件**，
//     这是 stat 看不见的：从未索引过的文件不在 store 清单里）。按
//     defaultExternalSyncMinInterval 节流——内容变化由 stat 兜底，git 只补
//     "新文件"这一维度，晚 ≤2s 发现不影响正确性。
//
// 触发点是"判定点"：
//   - agent turn 边界（loop 侧，turn 开始前跑一次）；
//   - 版本采样（ObserveVersion，Plan/复用判定的入口）。
//
// 校正失败、非 git 工作区、reader、off 一律退化为 no-op——校正源永不阻断会话。

const (
	// defaultExternalSyncMinInterval 是 git 校正之间的最小间隔（stat 校正不受限）。
	// 版本采样本身有 30s TTL 缓存（planner.go），但 TTL 只缓存"版本串"，
	// 校正必须每次判定都跑——否则窗口内的外部变更会被漏掉。
	defaultExternalSyncMinInterval = 2 * time.Second
	// externalVersionPendingMarker 是"校正刚发现变更、索引尚未追上"的版本标记。
	// 带标记的 token 与稳定 token 不相等 → 旧知识立即不可复用（fail-closed）。
	externalVersionPendingMarker = "#pending"
)

// ExternalChangeReport 是一次外部变更校正的观测结果（测试与状态面用）。
type ExternalChangeReport struct {
	// Ran 表示本次执行了校正（stat 校正总是执行；层/队列不可用时 false）。
	Ran bool
	// Skipped / SkipReason：未执行的原因（队列不可用 / 层不可用）。
	Skipped    bool
	SkipReason string
	// GitSkipped / GitSkipReason：git 源未执行（throttled / 非仓库 / git 不可用）。
	GitSkipped    bool
	GitSkipReason string
	// HeadMoved 表示本次 git 校正检测到 HEAD 移动。
	HeadMoved bool
	// GitPaths / StatPaths / Missing 是三个来源发现的变化路径（工作区相对）。
	GitPaths  []string
	StatPaths []string
	Missing   []string
	// Scanned 是 stat 校正比对的文件数；StatError 记录 stat 扫描失败原因（尽力而为）。
	Scanned   int
	StatError string
	// Marked 是本次交给变更队列的路径数（= GitPaths ∪ StatPaths ∪ Missing）。
	Marked int
	// Duration 是本次校正的墙钟耗时。
	Duration time.Duration
}

// SyncExternalChanges 运行一次外部变更校正并把发现的变化交给变更队列
// （异步索引；本调用不等待索引完成）。
//
// 门控：off / reader / 无 store / 队列不可用 → Skipped（零副作用）。
// 节流：仅 git 源受 defaultExternalSyncMinInterval 限制（见文件头说明）。
func (l *Layer) SyncExternalChanges(ctx context.Context) (ExternalChangeReport, error) {
	var report ExternalChangeReport
	if l == nil || l.store == nil {
		report.Skipped = true
		report.SkipReason = "layer is not available"
		return report, nil
	}
	queue := l.ChangeQueue()
	if queue == nil {
		report.Skipped = true
		report.SkipReason = "change queue is not available"
		return report, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	report.Ran = true
	started := time.Now()

	// stat 校正：以 store 的已索引文件清单为准（未索引时没有可比对的对象）。
	var fresh map[string]bool
	if wsID, err := l.planWorkspaceID(ctx); err != nil {
		report.StatError = "workspace is not indexed"
	} else {
		statReport, err := ScanIndexedFileStats(ctx, l.store, l.cfg.Workspace, wsID)
		if err != nil {
			report.StatError = err.Error()
		} else {
			report.StatPaths = statReport.Changed
			report.Missing = statReport.Missing
			report.Scanned = statReport.Scanned
			fresh = statReport.Fresh
		}
	}

	// git 校正：非仓库 / git 不可用时跳过（不是错误）；节流窗口内也跳过。
	if source := l.gitChangeSource(); source == nil {
		report.GitSkipped = true
		report.GitSkipReason = "workspace is not a git repository"
	} else if !l.gitSyncDue(started) {
		report.GitSkipped = true
		report.GitSkipReason = "throttled"
	} else {
		gitReport, _ := source.Detect(ctx)
		l.recordGitSync(started)
		report.GitSkipped = gitReport.Skipped
		report.GitSkipReason = gitReport.SkipReason
		report.HeadMoved = gitReport.HeadMoved
		report.GitPaths = dropFreshGitPaths(gitReport.Paths, fresh)
	}

	paths := make([]string, 0, len(report.GitPaths)+len(report.StatPaths)+len(report.Missing))
	paths = append(paths, report.GitPaths...)
	paths = append(paths, report.StatPaths...)
	paths = append(paths, report.Missing...)
	paths = indexableChangePaths(l.cfg, paths)
	if len(paths) > 0 {
		queue.Mark(paths...)
	}
	report.Marked = len(paths)
	report.Duration = time.Since(started)

	l.recordExternalSync(report.Marked > 0)
	return report, nil
}

// dropFreshGitPaths 丢掉"索引已经跟上"的 git 路径。
//
// git 侧看到的是工作树相对 HEAD 的状态；一个持续 modified 的文件在每次状态转移
// 时都可能被报出来，但索引可能早已收下当前内容（stat 侧判为 Fresh）。只有索引
// 真落后（不在 Fresh 里）才值得入队——否则版本判定会平白多出一轮 #pending。
// 未跟踪新文件、被删文件天然不在 Fresh 里，照常保留。
func dropFreshGitPaths(paths []string, fresh map[string]bool) []string {
	if len(paths) == 0 || len(fresh) == 0 {
		return paths
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if fresh[p] {
			continue
		}
		out = append(out, p)
	}
	return out
}

// indexableChangePaths 只保留索引器会接受的路径（工作区内 + 后缀在 codeExtensions），
// 输出工作区相对、排序去重。
//
// 校正源的输入来自 git 与磁盘，天然会带上索引永远不收的文件——最典型的是知识层
// 自己的状态目录（`.aicli/knowledge/*.db`、锁文件）：它们对 git 是未跟踪新文件，
// 交给队列只会让每次增量结果平白多出 Errors（IndexPaths 对不可索引路径显式计数）。
// 这里提前按同一口径（resolveIndexTargets）过滤，让"校正发现的变化"与
// "索引真正会处理的文件"完全一致。
func indexableChangePaths(cfg Config, paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	targets, _ := resolveIndexTargets(cfg, paths)
	if len(targets) == 0 {
		return nil
	}
	out := make([]string, 0, len(targets))
	for _, abs := range targets {
		rel, err := filepath.Rel(cfg.Workspace, abs)
		if err != nil {
			continue
		}
		out = append(out, normalizeRelPath(rel))
	}
	sort.Strings(out)
	return out
}

// gitSyncDue 判断 git 校正是否应执行（stat 校正不受此限）。
func (l *Layer) gitSyncDue(now time.Time) bool {
	l.syncMu.Lock()
	defer l.syncMu.Unlock()
	if l.lastGitSyncAt.IsZero() {
		return true
	}
	return now.Sub(l.lastGitSyncAt) >= defaultExternalSyncMinInterval
}

// recordGitSync 记录一次 git 校正的时刻。
func (l *Layer) recordGitSync(at time.Time) {
	l.syncMu.Lock()
	l.lastGitSyncAt = at
	l.syncMu.Unlock()
}

// recordExternalSync 记录一次校正的结果（是否发现变更）。
func (l *Layer) recordExternalSync(dirty bool) {
	l.syncMu.Lock()
	l.externalSyncCount++
	if dirty {
		l.externalSyncDirtyCount++
	}
	l.syncMu.Unlock()
}

// ExternalSyncStats 返回校正源的累计观测：执行次数、发现变更的次数、上次 git 校正时刻。
// 观测用；不参与任何判定。
func (l *Layer) ExternalSyncStats() (syncs int, dirtySyncs int, lastGitAt time.Time) {
	if l == nil {
		return 0, 0, time.Time{}
	}
	l.syncMu.Lock()
	defer l.syncMu.Unlock()
	return l.externalSyncCount, l.externalSyncDirtyCount, l.lastGitSyncAt
}

// gitChangeSource 惰性创建 git 校正源；工作区明显不在 git 仓库内时返回 nil
// （避免为每次校正 spawn 一个必然失败的 git 进程）。
func (l *Layer) gitChangeSource() *GitChangeSource {
	workspace := strings.TrimSpace(l.cfg.Workspace)
	if workspace == "" || !looksLikeGitWorkspace(workspace) {
		return nil
	}
	l.gitMu.Lock()
	defer l.gitMu.Unlock()
	if l.gitSource == nil {
		l.gitSource = NewGitChangeSource(workspace, nil)
	}
	return l.gitSource
}

// looksLikeGitWorkspace 沿工作区向上找 .git（目录或文件——worktree/submodule 用文件）。
// 纯 os.Stat 预筛，不 spawn 进程。
func looksLikeGitWorkspace(workspace string) bool {
	dir := workspace
	if abs, err := filepath.Abs(workspace); err == nil {
		dir = abs
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// ObserveVersion 返回工作区版本观测（W1 WorkspaceVersion 的 Layer 级采样入口）。
//
// 这是 Phase 5 的"判定点"：采样前先跑一次外部变更校正，让
// "这条探索记忆还能不能复用"的判定建立在校正后的版本上。校正发现变化时：
//
//   - 失效版本缓存——索引追上后下一次采样必须重算，不能被 TTL 缓存压住旧版本；
//   - 本次观测 token 附加 `#pendingN` 未稳定标记——索引尚未追上时，旧知识立即
//     不可复用（fail-closed），而不是返回一个"看起来没变"的旧版本。
//
// 校正失败 / 未索引一律退化为原采样语义；store 失败向上返回（调用方走 Degraded）。
// ttl<=0 时按 DefaultReuseVersionTTL。
func (l *Layer) ObserveVersion(ctx context.Context, ttl time.Duration, now time.Time) (VersionObservation, error) {
	if l == nil || l.store == nil {
		return VersionObservation{}, errWorkspaceNotIndexed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	wsID, err := l.planWorkspaceID(ctx)
	if err != nil {
		return VersionObservation{}, err
	}
	report, _ := l.SyncExternalChanges(ctx)
	if report.Marked > 0 {
		l.planVersion.invalidate()
	}
	// 交付 4：判定点顺带检查库大小（超过软上限且冷却期已过才真的清理；
	// 清理只影响软删除行，不改变工作区版本，失败也不影响本次判定）。
	l.maybeAutoGC(ctx, wsID)
	observation, err := l.planVersion.observe(ctx, l.store, wsID, ttl, now, l.ChangeQueue().Generation())
	if err != nil {
		return VersionObservation{}, err
	}
	if report.Marked > 0 {
		observation.Version += externalVersionPendingMarker + strconv.Itoa(report.Marked)
	}
	return observation, nil
}

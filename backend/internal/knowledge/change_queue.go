package knowledge

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Phase 5 交付 1/2 的执行端：变更队列。
//
// 三类变更源（agent edit hook / git diff / fsnotify）只需要把"哪些文件变了"
// 交给 Mark，队列负责去重、debounce、串行化与定向增量索引（IndexPaths）。
// 队列本身不认识变更源，也不引入任何新依赖——fsnotify 只是可选的第三类源。

const (
	// defaultChangeQueueDebounce 是"最后一次标记"到"开始处理"的静默窗口。
	// 300ms 覆盖一次工具调用内的连续多次写盘（write/edit/apply_patch），
	// 又短于人类感知阈值，编辑后的首次查询不会读到旧索引太久。
	defaultChangeQueueDebounce = 300 * time.Millisecond
	// defaultChangeQueueMaxBatch 限制单次 IndexPaths 的文件数：一次大编辑
	// （格式化、批量替换）可能标记上千文件，超出部分留到下一轮，避免单个
	// job 无限拉长并让状态面的进度失去意义。
	defaultChangeQueueMaxBatch = 256
	// defaultChangeQueueJobTimeout 是单次 IndexPaths 的墙钟上限；超时即放弃
	// 本轮（路径留在队列里可重试），不让挂起的 IO 卡死队列。
	defaultChangeQueueJobTimeout = 2 * time.Minute
)

// ChangeNotifier 是"这些文件已被写入"的接收方（04 §5 Phase 5 变更源 1：edit hook）。
//
// `*Layer` 与 `*Activation` 都实现它（`MarkChanged`）。装配方把它注入编辑类工具的
// 执行 ctx（`toolctx.WithFileChangeNotifier`）；`MarkChanged` 必须快速返回——
// 它只做去重入队，真正的索引在 debounce 后由单 worker 串行执行。
type ChangeNotifier interface {
	MarkChanged(paths ...string)
}

// ChangeQueueOptions 控制变更队列的节奏与可观测性。
type ChangeQueueOptions struct {
	// Debounce 是静默窗口（<=0 取 defaultChangeQueueDebounce）。
	Debounce time.Duration
	// MaxBatch 是单次处理的最大文件数（<=0 取 defaultChangeQueueMaxBatch）。
	MaxBatch int
	// JobTimeout 是单次 IndexPaths 的墙钟上限（<=0 取 defaultChangeQueueJobTimeout）。
	JobTimeout time.Duration
	// OnResult / OnError 是可选回调（日志/状态面），在队列 goroutine 中执行，
	// 必须快速返回；回调为 nil 时静默——结果仍可从 Pending/LastResult 观察。
	OnResult func(IndexResult)
	OnError  func(error)
}

// ChangeQueue 是串行的文件级增量队列。
//
// 不变量：
//   - 同一时刻至多一个 IndexPaths 在跑（单 worker + runMu），写事务天然串行；
//   - Mark 永不阻塞调用方（编辑路径不能被索引拖慢），也不丢标记——待办是
//     路径集合，重复标记幂等；
//   - Close 幂等：停止 worker 并**等待**当前 job 结束，避免 store 在写事务中途被关。
type ChangeQueue struct {
	cfg   Config
	store Store
	opts  ChangeQueueOptions

	mu      sync.Mutex
	pending map[string]struct{}
	closed  bool

	runMu      sync.Mutex
	lastResult IndexResult
	lastErr    error
	// generation 是"索引已落地"的代次：每次成功完成一次定向增量 +1。
	// 版本缓存（versionCache）按代次判定有效性——索引变化后旧代次的缓存
	// 必须失效，不能靠"失效时刻"与"采样时刻"的先后顺序去赌（竞态）。
	generation uint64

	wake chan struct{}
	stop chan struct{}
	done chan struct{}
}

// NewChangeQueue 创建并启动队列。store 为 nil 时仍可创建（Mark/Flush 退化为 no-op），
// 便于 off/shadow 场景的调用方不做分支。
func NewChangeQueue(cfg Config, store Store, opts ChangeQueueOptions) *ChangeQueue {
	if opts.Debounce <= 0 {
		opts.Debounce = defaultChangeQueueDebounce
	}
	if opts.MaxBatch <= 0 {
		opts.MaxBatch = defaultChangeQueueMaxBatch
	}
	if opts.JobTimeout <= 0 {
		opts.JobTimeout = defaultChangeQueueJobTimeout
	}
	q := &ChangeQueue{
		cfg:     cfg.Normalize(),
		store:   store,
		opts:    opts,
		pending: make(map[string]struct{}),
		wake:    make(chan struct{}, 1),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	go q.loop()
	return q
}

// Mark 标记文件已变更（绝对路径或工作区相对路径）。非阻塞、幂等、nil-safe。
//
// 幂等不止"去重"：**只有真正新增待办才唤醒 worker**。反复标记同一条路径
// （校正源每次判定都会重标仍然落后的文件）不得重置 debounce——否则静默窗口
// 被无限延长，worker 永远等不到可以开工的时机（饥饿）。
//
// 工作区**外**的绝对路径被静默丢弃：队列是 workspace 作用域的接收方（例如
// download 写到工作区外的文件不属于索引范围），丢弃不制造 Errors 噪声；
// 直接调用 IndexPaths 的调用方仍会看到越界路径被计入 Errors（显式不静默）。
func (q *ChangeQueue) Mark(paths ...string) {
	if q == nil || len(paths) == 0 {
		return
	}
	added := false
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	for _, raw := range paths {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		if !q.inWorkspace(p) {
			continue
		}
		if _, ok := q.pending[p]; !ok {
			q.pending[p] = struct{}{}
			added = true
		}
	}
	q.mu.Unlock()
	if added {
		select {
		case q.wake <- struct{}{}:
		default: // 已有待处理信号：worker 醒来后会看到全部待办。
		}
	}
}

// inWorkspace 判断标记路径是否可能落在索引范围内。
//
// 相对路径一律保留（按工作区相对处理，最终由 IndexPaths 归一化）；
// 绝对路径要求位于配置的工作区内。工作区未配置时保留（交由 IndexPaths 判定）。
func (q *ChangeQueue) inWorkspace(p string) bool {
	root := strings.TrimSpace(q.cfg.Workspace)
	if root == "" || !filepath.IsAbs(p) {
		return true
	}
	return pathWithinRoot(root, p)
}

// pathWithinRoot 判断绝对路径 p 是否位于 root 之内（含 root 本身）。
// 两侧都做 Clean；跨盘符 / 无法求相对路径时返回 false（fail closed）。
func pathWithinRoot(root, p string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(p))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Pending 返回当前待处理路径数（观测/测试用）。
func (q *ChangeQueue) Pending() int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pending)
}

// LastResult / LastError 返回最近一次增量运行的结果（观测/测试用）。
func (q *ChangeQueue) LastResult() IndexResult {
	if q == nil {
		return IndexResult{}
	}
	q.runMu.Lock()
	defer q.runMu.Unlock()
	return q.lastResult
}

// LastError 返回最近一次增量运行的错误（nil 表示上次成功）。
func (q *ChangeQueue) LastError() error {
	if q == nil {
		return nil
	}
	q.runMu.Lock()
	defer q.runMu.Unlock()
	return q.lastErr
}

// Flush 立即处理全部待办（忽略 debounce），同步返回聚合结果。
// 测试与进程退出路径用它；正常编辑路径不需要调用。
func (q *ChangeQueue) Flush(ctx context.Context) (IndexResult, error) {
	if q == nil {
		return IndexResult{}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var agg IndexResult
	for {
		paths := q.takePending(q.opts.MaxBatch)
		if len(paths) == 0 {
			return agg, nil
		}
		result, err := q.runOnce(ctx, paths)
		agg = mergeIndexResults(agg, result)
		if err != nil {
			return agg, err
		}
	}
}

// Close 停止 worker 并等待当前 job 结束；幂等、nil-safe。
// 关闭后 Mark 是 no-op（不再接受新标记），待办不再处理。
func (q *ChangeQueue) Close() {
	if q == nil {
		return
	}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.closed = true
	q.mu.Unlock()
	close(q.stop)
	<-q.done
}

func (q *ChangeQueue) loop() {
	defer close(q.done)
	for {
		select {
		case <-q.stop:
			return
		case <-q.wake:
		}
		if !q.debounce() {
			return
		}
		paths := q.takePending(q.opts.MaxBatch)
		if len(paths) == 0 {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), q.opts.JobTimeout)
		_, _ = q.runOnce(ctx, paths)
		cancel()
	}
}

// debounce 等待静默窗口：窗口内出现新标记就重新计时。
// 返回 false 表示队列已关闭（调用方应立即退出）。
func (q *ChangeQueue) debounce() bool {
	timer := time.NewTimer(q.opts.Debounce)
	defer timer.Stop()
	for {
		select {
		case <-q.stop:
			return false
		case <-q.wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(q.opts.Debounce)
		case <-timer.C:
			return true
		}
	}
}

// runOnce 执行一次定向增量（受 runMu 保护：队列内串行，且与 Flush 互斥）。
func (q *ChangeQueue) runOnce(ctx context.Context, paths []string) (IndexResult, error) {
	q.runMu.Lock()
	defer q.runMu.Unlock()

	if q.store == nil {
		return IndexResult{}, nil
	}
	result, err := IndexPaths(ctx, q.store, q.cfg, paths)
	q.lastResult, q.lastErr = result, err
	if err != nil {
		if q.opts.OnError != nil {
			q.opts.OnError(err)
		}
		return result, err
	}
	q.generation++ // runOnce 全程持 runMu，这里直接自增（不可重入加锁）。
	if q.opts.OnResult != nil {
		q.opts.OnResult(result)
	}
	return result, nil
}

// Generation 返回已落地的索引代次（每次成功的定向增量 +1）。
// nil / 未创建时返回 0。
func (q *ChangeQueue) Generation() uint64 {
	if q == nil {
		return 0
	}
	q.runMu.Lock()
	defer q.runMu.Unlock()
	return q.generation
}

// takePending 取走至多 limit 个待办路径（升序，便于复算）。
func (q *ChangeQueue) takePending(limit int) []string {
	if limit <= 0 {
		limit = defaultChangeQueueMaxBatch
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.pending) == 0 {
		return nil
	}
	paths := make([]string, 0, len(q.pending))
	for p := range q.pending {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	if len(paths) > limit {
		paths = paths[:limit]
	}
	for _, p := range paths {
		delete(q.pending, p)
	}
	return paths
}

// mergeIndexResults 聚合多次增量运行的结果（Flush 的返回值口径）：
// 计数相加、耗时相加、adapter 字段取最后一次。
func mergeIndexResults(agg, next IndexResult) IndexResult {
	agg.Scanned += next.Scanned
	agg.Indexed += next.Indexed
	agg.Skipped += next.Skipped
	agg.Deleted += next.Deleted
	agg.Errors += next.Errors
	agg.Symbols += next.Symbols
	agg.Refs += next.Refs
	agg.Duration += next.Duration
	agg.Truncated = agg.Truncated || next.Truncated
	agg.FullRebuild = agg.FullRebuild || next.FullRebuild
	if next.Adapter != "" {
		agg.Adapter = next.Adapter
		agg.AdapterVersion = next.AdapterVersion
	}
	if next.AdapterDegraded {
		agg.AdapterDegraded = true
		agg.AdapterDegradeReason = next.AdapterDegradeReason
	}
	return agg
}

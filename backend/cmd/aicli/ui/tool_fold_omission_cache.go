package ui

import "sync"

// 折叠 omission 判定缓存的版本号。判定只依赖 cell.Source 与 toolFoldOptions("")
// 固定的显示预算（4 行 = 3 头 + 1 尾、200 列、8KiB）。以下任一变化必须递增：
//   - cell.ToolDisplayPreviewOptions / selectHeadTail / truncateUTF8 的判定语义；
//   - toolFoldOmits 的判定式（OmittedLines > 0 || ByteTruncated）。
//
// 递增后旧结论整体失效，效果等同冷缓存。
const foldOmissionMemoVersion = 1

const (
	// foldOmissionMemoMaxEntries 与 cellRowsCacheMax 对齐：工作集是 transcript
	// 的 cell 数（一次完整扫描逐个查询），条目低于工作集会让命中率跌向 0。
	foldOmissionMemoMaxEntries = 8192
	// foldOmissionMemoMaxBytes 限制被缓存 source 的驻留量：key 持有 source 的
	// string 头（字节与 transcript cell 共享），只有 cell 被释放后才由这里兜底
	// 持有；8MiB 足够覆盖正常工具输出，又不会把已淘汰 cell 的正文长期钉住。
	foldOmissionMemoMaxBytes   = 8 * 1024 * 1024
	foldOmissionMemoFixedBytes = 96
)

// foldOmissionKey 是内容寻址键：同一份 source 的 omission 判定恒等（显示预算
// 固定在 version 中），换 source 自动 miss，不需要显式失效。
type foldOmissionKey struct {
	source  string
	version int
}

// foldOmissionCache 缓存「显示预算投影是否隐藏了正文」这一布尔判定。它补上
// sharedCellRows 缺失的另一半：渲染行早已按内容缓存，但 toolFoldTargetRows /
// toolFoldTarget 每轮布局都要重新判定每个折叠 cell 是否命中标记，此前每次判定
// 都完整执行 BuildPreview（ANSIToLines + 逐行 Render + Truncate），长会话下
// 占 UI 规划采样的近 20%（见 docs/debug/cpu-hotspots-*.md）。
type foldOmissionCache struct {
	mu  sync.Mutex
	lru *cellLayoutLRU[foldOmissionKey, bool]
}

func newFoldOmissionCache() *foldOmissionCache {
	return &foldOmissionCache{
		lru: newCellLayoutLRU[foldOmissionKey, bool](foldOmissionMemoMaxEntries, foldOmissionMemoMaxBytes),
	}
}

var sharedFoldOmissions = newFoldOmissionCache()

// omits 返回 source 的 omission 判定，命中缓存时不再触碰 BuildPreview。
func (c *foldOmissionCache) omits(source string) bool {
	if c == nil {
		return toolFoldOmitsUncached(source)
	}
	key := foldOmissionKey{source: source, version: foldOmissionMemoVersion}
	c.mu.Lock()
	if omitted, ok := c.lru.get(key); ok {
		c.mu.Unlock()
		return omitted
	}
	c.mu.Unlock()
	// 判定在锁外执行：BuildPreview 只读不可变 source，并发重复计算只会多烧
	// 一次 CPU，不会破坏缓存正确性（put 幂等）。
	omitted := toolFoldOmitsUncached(source)
	c.mu.Lock()
	c.lru.put(key, omitted, len(source)+foldOmissionMemoFixedBytes)
	c.mu.Unlock()
	return omitted
}

// warm 用调用方已经算出的投影结果预热缓存，避免同一 source 在同一轮 pass 里
// 被判定一次、渲染再一次。omission 标志与 hint 无关（hint 只改折叠标记的
// 文案，不参与行数/字节预算），因此带 hint 的投影结果可以安全回填 "" 的键。
func (c *foldOmissionCache) warm(source string, omitted bool) {
	if c == nil {
		return
	}
	key := foldOmissionKey{source: source, version: foldOmissionMemoVersion}
	c.mu.Lock()
	c.lru.put(key, omitted, len(source)+foldOmissionMemoFixedBytes)
	c.mu.Unlock()
}

// stats 返回命中/未命中/逐出计数与条目数、估算字节（诊断与测试用）。
func (c *foldOmissionCache) stats() (hits, misses, evictions uint64, entries, bytes int) {
	if c == nil {
		return 0, 0, 0, 0, 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.stats()
}

// reset 清空缓存（测试隔离；版本号切换也可用同一语义）。
func (c *foldOmissionCache) reset() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lru = newCellLayoutLRU[foldOmissionKey, bool](foldOmissionMemoMaxEntries, foldOmissionMemoMaxBytes)
}

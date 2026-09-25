package scene

import "sync"

// splitSourceLinesCache 缓存 splitSourceLines 的产物（cell → 语义行切片）。
//
// 背景：每次流式 delta 的 reduce 与渲染帧都会全量调用 LayoutTranscript，
// 对 transcript 里所有 cell 重新 splitSourceLines——12222 行的 transcript 每
// 次 delta 重切一遍，构成 profile 里的稳定热点（scene.splitSourceLines）与
// 持续的 []string 分配。COW 下已发布 cell 不可变（source 永不原地修改），
// 行切片共享 source 底层零拷贝，缓存共享完全安全。
//
// 键为 CellID，条目里带 (revision, source) 做一致性校验：
//   - Revision 由 scene 在 update/finalize 时递增，内容变化必然换 revision；
//   - source 字符串直接比较作为最终一致性保险（测试可构造同 ID/Revision
//     不同内容的 cell，hash 键会误命中）。稳态下 cell.Source 与条目里的
//     source 是同一个字符串头，Go 的 == 先比数据指针，退化为 O(1)。
//
// 为什么是「一个 cell 一个条目」而不是 (ID, Revision, source) 三元组键：
//  1. **容量**：恢复会话实测 6,719 个 cell，三元组键 + 条目上限 4096 会让
//     每轮布局稳定 miss 掉 2,600+ 个 cell，命中率退化、布局回退为全量重切
//     （基准 BenchmarkLayoutTranscriptResumeScale 实测 hot ≈ cold）。
//  2. **增长 cell 的 O(n²)**：mutable cell 每次 delta 都在增长，三元组键会让
//     每个 revision 都留一份完整 source + 行切片，既重切整份 source，又把
//     旧 revision 的整份 source 钉在内存里（上万行的 cell × 上千个 revision）。
//     单条目 + 前缀复用后，内存只随「每 cell 一份」增长。
//  3. **逐出成本**：三元组键需要在 order 上做 O(n) 线性去重（4096 条目 →
//     每次 put 扫几千个键，实测占布局耗时的大头）；单条目 + 惰性删除是 O(1)。
//
// 值只存只读 []string，调用方不得原地修改。
type splitSourceLinesCache struct {
	mu      sync.Mutex
	entries map[CellID]splitSourceEntry
	order   []splitSourceOrderEntry // FIFO 日志（惰性删除，允许含过期项）
	max     int
	lines   int    // 已缓存行总数（逐出预算）
	stamp   uint64 // 单调递增版本号，用于惰性删除判定

	// 观测计数（计划 §4.3「计数可见」）：命中/未命中/前缀复用/逐出。
	hits      int
	misses    int
	reuses    int
	evictions int
}

// splitSourceEntry 是单个 cell 最近一次 split 的产物。
//
// consumed 是 source 中「已被 lines 完整覆盖」的前缀长度：splitSourceLines 的
// 最后一个元素永远是最后一个 '\n' 之后的尾巴（可能是 ""），lines[:len-1] 全是
// 完整行。cell 只追加时（COW：source 只增长、不原地改），前缀行原样复用，
// 只需 split source[consumed:] —— 这是 append-only cell 的 O(n²) 修复点。
type splitSourceEntry struct {
	revision uint64
	source   string
	lines    []string
	consumed int
	stamp    uint64
}

type splitSourceOrderEntry struct {
	id    CellID
	stamp uint64
}

const (
	// splitSourceCacheMax 是 cell 条目上限。恢复会话实测 6,719 个 cell
	// （146,535 布局行），容量必须覆盖工作集，否则命中率退化到 0
	// （见结构体注释 1）。真实内存边界是 splitSourceCacheMaxLines。
	splitSourceCacheMax = 8192
	// splitSourceCacheMaxLines 是行数预算。恢复会话实测 146,535 行；
	// 留一倍余量给「一个 cell 上万行」的长尾（大命令输出）。
	splitSourceCacheMaxLines = 400000
)

var sharedSplitSourceCache = newSplitSourceLinesCache(splitSourceCacheMax)

func newSplitSourceLinesCache(max int) *splitSourceLinesCache {
	return &splitSourceLinesCache{
		entries: make(map[CellID]splitSourceEntry),
		max:     max,
	}
}

// linesFor 返回 cell source 的语义行；命中缓存时零分配。
func (c *splitSourceLinesCache) linesFor(id CellID, revision uint64, source string) []string {
	if source == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.entries[id]; ok && e.revision == revision && e.source == source {
		c.hits++
		return e.lines
	}
	c.misses++
	lines, consumed := c.splitIncrementalLocked(id, source)
	c.storeLocked(id, revision, source, lines, consumed)
	return lines
}

// splitIncrementalLocked 在可能时复用同一 cell 上一份条目的完整前缀行。
// 只在「新 source 以上一份 source 为前缀」时复用——COW 下这是 append-only
// cell 的正常形态；否则（replace/截断）回落全量切分。
func (c *splitSourceLinesCache) splitIncrementalLocked(id CellID, source string) ([]string, int) {
	prev, ok := c.entries[id]
	if !ok || prev.consumed > len(source) || !hasPrefix(source, prev.source) {
		return splitSourceLines(source), consumedOffset(source)
	}
	c.reuses++
	// 复用完整行前缀：prev.lines[:len-1] 是 '\n' 结尾的完整行，与 source 的
	// 对应前缀逐字节相同（source 以 prev.source 为前缀）。
	complete := prev.lines[:len(prev.lines)-1]
	suffix := source[prev.consumed:]
	var tail []string
	if suffix == "" {
		// 与全量切分保持一致：最后一行是空尾巴。
		tail = []string{""}
	} else {
		tail = splitSourceLines(suffix)
	}
	lines := make([]string, 0, len(complete)+len(tail))
	lines = append(lines, complete...)
	lines = append(lines, tail...)
	return lines, consumedOffset(source)
}

func (c *splitSourceLinesCache) storeLocked(id CellID, revision uint64, source string, lines []string, consumed int) {
	if len(lines) == 0 {
		return
	}
	c.stamp++
	prev, hadPrev := c.entries[id]
	if hadPrev {
		c.lines -= len(prev.lines)
	}
	c.entries[id] = splitSourceEntry{
		revision: revision,
		source:   source,
		lines:    lines,
		consumed: consumed,
		stamp:    c.stamp,
	}
	c.order = append(c.order, splitSourceOrderEntry{id: id, stamp: c.stamp})
	c.lines += len(lines)
	for len(c.entries) > c.max || (c.lines > splitSourceCacheMaxLines && len(c.entries) > 1) {
		if !c.evictOldestLocked() {
			break
		}
	}
}

// evictOldestLocked 从 FIFO 日志头部逐出一个仍然有效的条目（惰性删除：
// 日志里的过期项直接丢弃，不重复计入逐出）。
func (c *splitSourceLinesCache) evictOldestLocked() bool {
	for len(c.order) > 0 {
		oldest := c.order[0]
		c.order = c.order[1:]
		e, ok := c.entries[oldest.id]
		if !ok || e.stamp != oldest.stamp {
			continue // 已被更新的 revision 覆盖，日志项过期
		}
		c.lines -= len(e.lines)
		delete(c.entries, oldest.id)
		c.evictions++
		return true
	}
	return false
}

// clear 把缓存清回冷态（测试/基准使用，语义等价于新建）。
func (c *splitSourceLinesCache) clear() {
	c.mu.Lock()
	c.entries = make(map[CellID]splitSourceEntry)
	c.order = nil
	c.lines = 0
	c.hits, c.misses, c.reuses, c.evictions = 0, 0, 0, 0
	c.stamp = 0
	c.mu.Unlock()
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// consumedOffset 返回「最后一个 '\n' 之后」的偏移；无 '\n' 时为 0。
func consumedOffset(source string) int {
	for i := len(source) - 1; i >= 0; i-- {
		if source[i] == '\n' {
			return i + 1
		}
	}
	return 0
}

// layoutSplitSourceLines 是 LayoutTranscript 用的缓存化 splitSourceLines。
func layoutSplitSourceLines(c *TranscriptCell) []string {
	if c == nil || c.Source == "" {
		return nil
	}
	return sharedSplitSourceCache.linesFor(c.ID, c.Revision, c.Source)
}

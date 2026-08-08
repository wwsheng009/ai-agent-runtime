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
// 键为 (ID, Revision, source) 三元组：Revision 由 scene 在 update/finalize
// 时递增，内容变化必然换键；source 字符串直接比较作为最终一致性保险
// （测试可构造同 ID/Revision 不同内容的 cell，hash 键会误命中）。值只存
// 只读 []string，调用方不得原地修改。
type splitSourceLinesCache struct {
	mu      sync.Mutex
	entries map[splitSourceKey][]string
	order   []splitSourceKey // FIFO 逐出（尾部最近使用）
	max     int
	lines   int // 已缓存行总数（逐出预算）
}

type splitSourceKey struct {
	id       CellID
	revision uint64
	source   string
}

const (
	splitSourceCacheMax      = 4096 // cell 条目上限
	splitSourceCacheMaxLines = 200000
)

var sharedSplitSourceCache = &splitSourceLinesCache{
	entries: make(map[splitSourceKey][]string),
	max:     splitSourceCacheMax,
}

func (c *splitSourceLinesCache) get(id CellID, revision uint64, source string) ([]string, bool) {
	key := splitSourceKey{id: id, revision: revision, source: source}
	c.mu.Lock()
	defer c.mu.Unlock()
	lines, ok := c.entries[key]
	return lines, ok
}

func (c *splitSourceLinesCache) put(id CellID, revision uint64, source string, lines []string) {
	if len(lines) == 0 {
		return
	}
	key := splitSourceKey{id: id, revision: revision, source: source}
	c.mu.Lock()
	defer c.mu.Unlock()
	if prev, ok := c.entries[key]; ok {
		c.lines -= len(prev)
		for i, k := range c.order {
			if k == key {
				c.order = append(c.order[:i], c.order[i+1:]...)
				break
			}
		}
	}
	c.entries[key] = lines
	c.order = append(c.order, key)
	c.lines += len(lines)
	for len(c.order) > c.max || (c.lines > splitSourceCacheMaxLines && len(c.order) > 1) {
		oldest := c.order[0]
		c.order = c.order[1:]
		if old, ok := c.entries[oldest]; ok {
			c.lines -= len(old)
			delete(c.entries, oldest)
		}
	}
}

// layoutSplitSourceLines 是 LayoutTranscript 用的缓存化 splitSourceLines。
func layoutSplitSourceLines(c *TranscriptCell) []string {
	id, revision, source := c.ID, c.Revision, c.Source
	if source == "" {
		return nil
	}
	if lines, ok := sharedSplitSourceCache.get(id, revision, source); ok {
		return lines
	}
	lines := splitSourceLines(source)
	sharedSplitSourceCache.put(id, revision, source, lines)
	return lines
}

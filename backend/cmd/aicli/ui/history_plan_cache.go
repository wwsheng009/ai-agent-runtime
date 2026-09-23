package ui

import (
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// historyPlanCache 缓存 plain cell 的 history-plan 中间产物：每个物理行
// 的 wrap 文本、source 映射（相对 cell.Source 的绝对偏移）、fragmentID 以
// 及物化后的 render.Line（HistoryCommit.Lines 的共享引用）。
//
// 键与 cellRowsCache 相同（精确内容寻址：source + cell/presentation kind +
// width + theme 指纹），同一 cell 未变时每次 reduce 直接复用，把 planPlainCellHistoryCommits
// 的全量重新 wrap + clone 从 O(N) 降为 O(Δ)。内容变化/换宽/换主题自动失效；
// 测试隔离天然成立（不同场景即使复用 cellID 也不互相污染）。
//
// 与 cellRowsCache 共用 cellLayoutLRU：两者扫描的是同一份 cell 工作集，容量
// 小于工作集时 FIFO 逐出会让命中率退化为 0，规划因此每次都重新 wrap 并物化
// 全部历史行。
//
// 共享安全：缓存的 render.Line 为一次性物化的独立副本，HistoryCommit.Lines
// 消费方（historyRenderLineEquivalent → render.LinesEqual）只读，不原地修改。
// 动态字段（DisplayRange / LayoutGeneration / skipRows 前缀）不参与缓存，组装
// 时按当前状态填充。
type historyPlanCache struct {
	mu  sync.Mutex
	lru *cellLayoutLRU[[]planPhysicalRow]
}

// planPhysicalRow 是 plain cell 一个物理行的完整来源映射 + 物化行。
type planPhysicalRow struct {
	text     string // 物理行 wrap 文本（与 AppScreenRow.Text 一致）
	source   SourceRange
	fragment uint64
	line     render.Line // 物化 Lines（共享引用，只读）
}

const (
	// historyPlanCacheMax 与 cellRowsCacheMax 同步：工作集是 transcript 的
	// cell 数，不是并发查询数。见 cellLayoutLRU 注释。
	historyPlanCacheMax      = 8192
	historyPlanCacheMaxBytes = 64 * 1024 * 1024
)

var sharedHistoryPlan = &historyPlanCache{
	lru: newCellLayoutLRU[[]planPhysicalRow](historyPlanCacheMax, historyPlanCacheMaxBytes),
}

// planCacheKeyFor 派生 history-plan 缓存键（与布局缓存同一套内容寻址键）。
func planCacheKeyFor(cell scene.TranscriptCell, width int, themeFp string) cellLayoutKey {
	return cellLayoutKeyFor(cell, width, themeFp)
}

func (c *historyPlanCache) get(key cellLayoutKey) []planPhysicalRow {
	c.mu.Lock()
	defer c.mu.Unlock()
	rows, _ := c.lru.get(key)
	return rows
}

func (c *historyPlanCache) put(key cellLayoutKey, rows []planPhysicalRow) {
	if len(rows) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lru.put(key, rows, estimatePlanRowsBytes(key, rows))
}

// stats 返回该缓存的计数快照（诊断用）。
func (c *historyPlanCache) stats() (hits, misses, evictions uint64, entries, bytes int) {
	if c == nil {
		return 0, 0, 0, 0, 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.stats()
}

// estimatePlanRowsBytes 粗略估算物理行产物占用（逐出预算）。
func estimatePlanRowsBytes(key cellLayoutKey, rows []planPhysicalRow) int {
	bytes := len(key.source) + len(key.document) + len(key.themeFp) + 64
	for _, row := range rows {
		bytes += len(row.text) + 32
		for _, span := range row.line.Spans {
			bytes += len(span.Text) + 64
		}
	}
	return bytes
}

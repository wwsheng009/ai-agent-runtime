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
// 共享安全：缓存的 render.Line 为一次性物化的独立副本，HistoryCommit.Lines
// 消费方（historyRenderLineEquivalent → render.LinesEqual）只读，不原地修改。
// 动态字段（DisplayRange / LayoutGeneration / skipRows 前缀）不参与缓存，组装
// 时按当前状态填充。
type historyPlanCache struct {
	mu      sync.Mutex
	entries map[cellLayoutKey]cachedPlanRows
	order   []cellLayoutKey // FIFO 逐出（尾部为最近写入）
	max     int
	bytes   int
}

// planPhysicalRow 是 plain cell 一个物理行的完整来源映射 + 物化行。
type planPhysicalRow struct {
	text     string // 物理行 wrap 文本（与 AppScreenRow.Text 一致）
	source   SourceRange
	fragment uint64
	line     render.Line // 物化 Lines（共享引用，只读）
}

type cachedPlanRows struct {
	rows  []planPhysicalRow
	bytes int
}

const (
	historyPlanCacheMax      = 1024
	historyPlanCacheMaxBytes = 64 * 1024 * 1024
)

var sharedHistoryPlan = &historyPlanCache{
	entries: make(map[cellLayoutKey]cachedPlanRows),
	max:     historyPlanCacheMax,
}

// planCacheKeyFor 派生 history-plan 缓存键（与布局缓存同一套内容寻址键）。
func planCacheKeyFor(cell scene.TranscriptCell, width int, themeFp string) cellLayoutKey {
	return cellLayoutKeyFor(cell, width, themeFp)
}

func (c *historyPlanCache) get(key cellLayoutKey) []planPhysicalRow {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return nil
	}
	return entry.rows
}

func (c *historyPlanCache) put(key cellLayoutKey, rows []planPhysicalRow) {
	if len(rows) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if prev, ok := c.entries[key]; ok {
		c.bytes -= prev.bytes
		for i, k := range c.order {
			if k == key {
				c.order = append(c.order[:i], c.order[i+1:]...)
				break
			}
		}
	}
	entry := cachedPlanRows{rows: rows, bytes: estimatePlanRowsBytes(key, rows)}
	c.entries[key] = entry
	c.order = append(c.order, key)
	c.bytes += entry.bytes
	for len(c.order) > c.max || (c.bytes > historyPlanCacheMaxBytes && len(c.order) > 1) {
		oldest := c.order[0]
		c.order = c.order[1:]
		if e, ok := c.entries[oldest]; ok {
			c.bytes -= e.bytes
			delete(c.entries, oldest)
		}
	}
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

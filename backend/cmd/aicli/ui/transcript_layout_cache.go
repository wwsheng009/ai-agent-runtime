package ui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// cellRowsCache 缓存 transcript cell 的物理行布局（[]AppScreenRow）。
// 键为精确内容寻址（source + cell/presentation kind + document + width +
// theme 指纹），
// 与 renderengine.RenderCache 同模式：同一 cell 未变时每次 reduce 直接复
// 用上次布局，把全量重布局从 O(N) 降为 O(Δ)；内容变化/换宽/换主题自动
// 失效。测试隔离天然成立（不同场景即使复用 cellID 也不互相污染）。
//
// 共享安全：缓存返回的 rows 为只读共享引用；消费方（渲染
// appTranscriptRenderLine 先 clone、HistoryCommit 比较 historyRenderLineEquivalent
// 只读）都不原地修改 AppScreenRow / render.Line。
//
// 容量必须覆盖工作集：工作集是 transcript 的 cell 数（LayoutAppScreen 每帧
// 遍历全部 cell，planEligibleHistoryCommits 每次完整规划同样遍历全部 cell）。
// 容量小于工作集时逐出策略决定一切，见 cellLayoutLRU 的注释。
type cellRowsCache struct {
	mu  sync.Mutex
	lru *cellLayoutLRU[[]AppScreenRow]
}

// cellLayoutLRU 是 cellLayoutKey 索引的侵入式 LRU 表：head 为最近使用，tail
// 为最久未使用；条目内嵌 prev/next 链接，因此命中触碰与逐出都是 O(1)，
// 不需要在命中路径上线性扫描顺序表。
//
// 为什么必须是 LRU 而不是 FIFO：布局扫描按稳定的 transcript 顺序遍历全部
// cell。当工作集超过容量（N 个 cell 顺序扫描、容量 C < N）时，FIFO 会在条目
// 被再次复用之前就把它逐出，稳态命中率退化为 0 —— 每一帧都重新布局并对每个
// diff/代码单元格重跑一遍语法高亮，这是巨型会话把 UI 锁按秒级持有的直接原因。
// LRU 让「刚刚用过的 cell」活到下一轮扫描，把稳态命中率拉回工作集比例。
type cellLayoutLRU[V any] struct {
	entries  map[cellLayoutKey]*cellLayoutEntry[V]
	head     *cellLayoutEntry[V] // 最近使用（MRU）
	tail     *cellLayoutEntry[V] // 最久未使用（逐出候选）
	bytes    int
	max      int
	maxBytes int
	hits     uint64
	misses   uint64
	evict    uint64
}

type cellLayoutEntry[V any] struct {
	key   cellLayoutKey
	value V
	bytes int
	prev  *cellLayoutEntry[V]
	next  *cellLayoutEntry[V]
}

func newCellLayoutLRU[V any](max, maxBytes int) *cellLayoutLRU[V] {
	if max <= 0 {
		max = cellRowsCacheMax
	}
	if maxBytes <= 0 {
		maxBytes = cellRowsCacheMaxBytes
	}
	return &cellLayoutLRU[V]{
		entries:  make(map[cellLayoutKey]*cellLayoutEntry[V], max/4+1),
		max:      max,
		maxBytes: maxBytes,
	}
}

// get 返回缓存值，并把命中条目提到 MRU 位置（O(1) 重链）。
func (l *cellLayoutLRU[V]) get(key cellLayoutKey) (V, bool) {
	var zero V
	if l == nil {
		return zero, false
	}
	entry, ok := l.entries[key]
	if !ok {
		l.misses++
		return zero, false
	}
	l.hits++
	l.moveToFront(entry)
	return entry.value, true
}

// put 插入或覆盖条目，然后逐出到预算内。bytes 是调用方给出的确定性估算
// （与 renderengine.RenderCache 一致：用于容量控制，不是 Go heap 采样）。
func (l *cellLayoutLRU[V]) put(key cellLayoutKey, value V, bytes int) {
	if l == nil {
		return
	}
	if entry, ok := l.entries[key]; ok {
		l.bytes += bytes - entry.bytes
		entry.value = value
		entry.bytes = bytes
		l.moveToFront(entry)
	} else {
		entry := &cellLayoutEntry[V]{key: key, value: value, bytes: bytes}
		l.entries[key] = entry
		l.pushFront(entry)
		l.bytes += bytes
	}
	l.evictToBudget()
}

func (l *cellLayoutLRU[V]) moveToFront(entry *cellLayoutEntry[V]) {
	if l.head == entry {
		return
	}
	l.unlink(entry)
	l.pushFront(entry)
}

func (l *cellLayoutLRU[V]) pushFront(entry *cellLayoutEntry[V]) {
	entry.prev = nil
	entry.next = l.head
	if l.head != nil {
		l.head.prev = entry
	}
	l.head = entry
	if l.tail == nil {
		l.tail = entry
	}
}

func (l *cellLayoutLRU[V]) unlink(entry *cellLayoutEntry[V]) {
	if entry.prev != nil {
		entry.prev.next = entry.next
	} else if l.head == entry {
		l.head = entry.next
	}
	if entry.next != nil {
		entry.next.prev = entry.prev
	} else if l.tail == entry {
		l.tail = entry.prev
	}
	entry.prev, entry.next = nil, nil
}

// evictToBudget 从 LRU 端逐出，直到条目数与估算字节都回到预算内。至少保留
// 一个条目：单个超预算的巨型 cell 不能把缓存清空，否则会退回每次全量重布局。
func (l *cellLayoutLRU[V]) evictToBudget() {
	for len(l.entries) > l.max || (l.bytes > l.maxBytes && len(l.entries) > 1) {
		entry := l.tail
		if entry == nil {
			return
		}
		l.unlink(entry)
		delete(l.entries, entry.key)
		l.bytes -= entry.bytes
		l.evict++
	}
}

// stats 返回命中/未命中/逐出计数与当前条目数、估算字节。调用方负责加锁。
func (l *cellLayoutLRU[V]) stats() (hits, misses, evictions uint64, entries, bytes int) {
	if l == nil {
		return 0, 0, 0, 0, 0
	}
	return l.hits, l.misses, l.evict, len(l.entries), l.bytes
}

type cellLayoutKey struct {
	source           string
	cellKind         scene.CellKind
	presentationKind scene.PresentationKind
	document         string
	width            int
	themeFp          string
	// foldHint 区分「最近一次折叠」（标记携带 Ctrl+T 恢复提示）与更早的
	// 折叠（纯标记）。两者正文相同但投影内容不同，必须各自占用缓存条目。
	foldHint bool
}

const (
	// cellRowsCacheMax 必须覆盖 transcript 的 cell 工作集：LayoutAppScreen 与
	// planEligibleHistoryCommits 每次调用都会遍历全部 cell，容量小于工作集
	// 会让命中率退化到 0（见 cellLayoutLRU 注释）。旧值 1024 低于真实恢复会
	// 话的规模（实测 3951 个 cell / 146535 个布局行），逐出策略因此接管了
	// 整个布局成本。字节预算仍是最终内存边界，条目上限只作为防御性兜底。
	cellRowsCacheMax      = 8192
	cellRowsCacheMaxBytes = 64 * 1024 * 1024
)

var sharedCellRows = &cellRowsCache{
	lru: newCellLayoutLRU[[]AppScreenRow](cellRowsCacheMax, cellRowsCacheMaxBytes),
}

// cellLayoutKeyFor 派生 cell 的布局缓存键。
func cellLayoutKeyFor(cell scene.TranscriptCell, width int, themeFp string) cellLayoutKey {
	key := cellLayoutKey{
		source:           cell.Source,
		cellKind:         cell.Kind,
		presentationKind: cell.Presentation.Kind,
		width:            width,
		themeFp:          themeFp,
	}
	if cell.Presentation.Kind == scene.PresentationDocument {
		// Source is canonical replay text, but PresentationDocument may change
		// independently. Keep the exact structured input instead of trusting a
		// hash collision as semantic equality.
		if encoded, err := json.Marshal(cell.Presentation.Document); err == nil {
			key.document = string(encoded)
		} else {
			key.document = fmt.Sprintf("%#v", cell.Presentation.Document)
		}
	}
	return key
}

func (c *cellRowsCache) get(key cellLayoutKey) []AppScreenRow {
	c.mu.Lock()
	defer c.mu.Unlock()
	rows, _ := c.lru.get(key)
	return rows
}

func (c *cellRowsCache) put(key cellLayoutKey, rows []AppScreenRow) {
	if len(rows) == 0 {
		return
	}
	// CellID belongs to the consuming transcript occurrence, not to the cached
	// layout. Clearing it lets identical content safely share rows across cells;
	// appendCachedCellRows restores the current owner on the copied row values.
	for index := range rows {
		rows[index].CellID = 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lru.put(key, rows, estimateCellRowsBytes(key, rows))
}

// stats 返回该缓存的计数快照（诊断用）。
func (c *cellRowsCache) stats() (hits, misses, evictions uint64, entries, bytes int) {
	if c == nil {
		return 0, 0, 0, 0, 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.stats()
}

// estimateCellRowsBytes 粗略估算 rows 占用（用于逐出预算）。
func estimateCellRowsBytes(key cellLayoutKey, rows []AppScreenRow) int {
	bytes := len(key.source) + len(key.document) + len(key.themeFp) + 64
	for _, row := range rows {
		bytes += len(row.Text) + 32
		for _, span := range row.RenderLine.Spans {
			bytes += len(span.Text) + 64
		}
	}
	return bytes
}

// TranscriptLayoutCacheStats 是 transcript 布局相关缓存的计数快照。命中率长
// 期接近 0 说明缓存容量已经跟不上工作集，布局退化回「每次调用全量重算」，
// 这是 UI 锁被按秒级持有的前置信号。
type TranscriptLayoutCacheStats struct {
	CellRowsHits      uint64
	CellRowsMisses    uint64
	CellRowsEvictions uint64
	CellRowsEntries   int
	CellRowsBytes     int
	PlanHits          uint64
	PlanMisses        uint64
	PlanEvictions     uint64
	PlanEntries       int
	PlanBytes         int
}

// TranscriptLayoutCacheStatsSnapshot 采集布局缓存与 history-plan 缓存的计数
// （/debug/chat/status 的 app_state.layout_cache）。
func TranscriptLayoutCacheStatsSnapshot() TranscriptLayoutCacheStats {
	var snapshot TranscriptLayoutCacheStats
	snapshot.CellRowsHits, snapshot.CellRowsMisses, snapshot.CellRowsEvictions,
		snapshot.CellRowsEntries, snapshot.CellRowsBytes = sharedCellRows.stats()
	snapshot.PlanHits, snapshot.PlanMisses, snapshot.PlanEvictions,
		snapshot.PlanEntries, snapshot.PlanBytes = sharedHistoryPlan.stats()
	return snapshot
}

// themeFingerprint 返回主题指纹：主题切换（palette/variant/syntax/terminal
// 变化）会使结构化布局结果改变，参与缓存键。
func themeFingerprint(theme style.ThemeContext) string {
	profile := theme.Terminal.ColorProfile
	return fmt.Sprintf(
		"%q|%d|%q|%t|%d|%t|%t|%d|%s|%s|%t",
		theme.Palette.Name,
		theme.Palette.Variant,
		theme.SyntaxName,
		profile.Enabled,
		profile.Depth,
		profile.Hyperlinks,
		profile.Forced,
		theme.Terminal.Background,
		terminalRGBFingerprint(theme.Terminal.DefaultFG),
		terminalRGBFingerprint(theme.Terminal.DefaultBG),
		theme.UseHyperlink,
	)
}

func terminalRGBFingerprint(value *style.RGB) string {
	if value == nil {
		return "nil"
	}
	return strconv.Itoa(int(value.R)) + "," + strconv.Itoa(int(value.G)) + "," + strconv.Itoa(int(value.B))
}

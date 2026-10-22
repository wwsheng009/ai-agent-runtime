package ui

import (
	"sort"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// transcript_virtual_pager.go —— 虚拟化 pager 与索引/行定位扩展（任务 T5）。
//
// 设计文档：§14.9（高度来自账本、O(log n) 定位、锚点语义、ExpandAll 估算+校正）。
//
// 关键点：
//   - 高度来源：冷区 `ColdCellRef.RowsAtGen`（交付代，不可重排）/窗口 `LocalRows`
//     （当前代）；`ExpandAll` 时优先用旁路表（估算→校正），不影响交付账本；
//   - 锚点保持现有语义 `(CellID, Row, LayoutGeneration, Offset)`；
//   - 索引按（冷区长度、窗口起点/长度）自失效，避免额外挂点。

// —— TranscriptResidency 索引与全局行定位扩展 ——

type residencyIndexCache struct {
	coldLen     int
	windowStart int
	windowLen   int
	byID        map[scene.CellID]int
}

// ensureIndex 惰性维护 cellID → 全局 seq 的索引（冷区在前、窗口在后）。
func (r *TranscriptResidency) ensureIndex() *residencyIndexCache {
	if r.indexCache != nil &&
		r.indexCache.coldLen == r.Cold.Len() &&
		r.indexCache.windowStart == r.Window.StartSeq &&
		r.indexCache.windowLen == len(r.Window.Cells) {
		return r.indexCache
	}
	cache := &residencyIndexCache{
		coldLen:     r.Cold.Len(),
		windowStart: r.Window.StartSeq,
		windowLen:   len(r.Window.Cells),
		byID:        make(map[scene.CellID]int, r.Cold.Len()+len(r.Window.Cells)),
	}
	for index := range r.Cold.Cells {
		cache.byID[r.Cold.Cells[index].ID] = index
	}
	for index, cell := range r.Window.Cells {
		cache.byID[cell.ID] = r.Window.StartSeq + index
	}
	r.indexCache = cache
	return cache
}

// SeqOf 返回 cell 的全局 seq（冷区+窗口全序）。
func (r *TranscriptResidency) SeqOf(id scene.CellID) (int, bool) {
	if r == nil {
		return 0, false
	}
	seq, ok := r.ensureIndex().byID[id]
	return seq, ok
}

// CellIDAtSeq 返回 seq 对应的 cellID。
func (r *TranscriptResidency) CellIDAtSeq(seq int) (scene.CellID, bool) {
	if r == nil || seq < 0 {
		return 0, false
	}
	if coldLen := r.Cold.Len(); seq < coldLen {
		return r.Cold.Cells[seq].ID, true
	}
	offset := seq - r.Window.StartSeq
	if offset < 0 || offset >= len(r.Window.Cells) {
		return 0, false
	}
	return r.Window.Cells[offset].ID, true
}

// ColdRefOf 返回冷区引用（窗口内/未知返回 false）。
func (r *TranscriptResidency) ColdRefOf(id scene.CellID) (ColdCellRef, bool) {
	if r == nil {
		return ColdCellRef{}, false
	}
	seq, ok := r.SeqOf(id)
	if !ok || seq >= r.Cold.Len() {
		return ColdCellRef{}, false
	}
	return r.Cold.Cells[seq], true
}

// LastFoldedCellID 返回全序（冷区优先）中最后一个折叠工具链 cell。
// 这是 §14.7 的全局 foldTarget：窗口化渲染必须用它，禁止在窗口内局部重算，
// 否则跨窗口边界时 Ctrl+T 提示位会错位。
func (r *TranscriptResidency) LastFoldedCellID() scene.CellID {
	if r == nil {
		return 0
	}
	if target := r.Window.LastFoldedCellID(); target != 0 {
		return target
	}
	return r.Cold.LastFoldedCellID()
}

// windowRowStarts 返回窗口本地行起点缓存（窗口变更时重建）。
func (r *TranscriptResidency) windowRowStarts() []int64 {
	if r.windowStartsCache != nil {
		return r.windowStartsCache
	}
	r.windowStartsCache = r.Window.RowStarts()
	return r.windowStartsCache
}

// GlobalRowStart 返回 seq 的全局行起点（冷区 O(1)，窗口 O(1) 缓存）。
func (r *TranscriptResidency) GlobalRowStart(seq int) (int64, bool) {
	if r == nil || seq < 0 {
		return 0, false
	}
	coldLen := r.Cold.Len()
	if seq < coldLen {
		start, ok := r.Cold.Rows.RowStart(seq)
		return start, ok
	}
	offset := seq - r.Window.StartSeq
	if offset < 0 || offset >= len(r.Window.LocalRows) {
		return 0, false
	}
	return r.Cold.Rows.Total() + r.windowRowStarts()[offset], true
}

// SeqAtGlobalRow 返回全局行号归属的 seq（冷区二分；窗口线性回扫）。
func (r *TranscriptResidency) SeqAtGlobalRow(row int64) (int, bool) {
	if r == nil || row < 0 {
		return 0, false
	}
	coldTotal := r.Cold.Rows.Total()
	if row < coldTotal {
		index, _, ok := r.Cold.Rows.Locate(row)
		return index, ok
	}
	local := row - coldTotal
	starts := r.windowRowStarts()
	for index := len(starts) - 1; index >= 0; index-- {
		if local >= starts[index] {
			end := r.Window.TotalRows()
			if index+1 < len(starts) {
				end = starts[index+1]
			}
			if local < end {
				return r.Window.StartSeq + index, true
			}
			// 零行 span：继续向前找。
			if end == starts[index] {
				continue
			}
			return 0, false
		}
	}
	return 0, false
}

// —— 虚拟化 pager ——

// TranscriptVirtualPager 以账本高度虚拟化 transcript（只物化可见页）。
type TranscriptVirtualPager struct {
	Residency *TranscriptResidency
	ExpandAll bool

	expanded map[scene.CellID]int32
	pagerStats
}

type pagerStats struct {
	Estimated int
	Corrected int
}

// NewTranscriptVirtualPager 构造虚拟 pager。
func NewTranscriptVirtualPager(residency *TranscriptResidency) *TranscriptVirtualPager {
	return &TranscriptVirtualPager{
		Residency: residency,
		expanded:  make(map[scene.CellID]int32),
	}
}

// HeightOf 返回单个 cell 的显示高度（ExpandAll 时优先旁路表，未知先用估算）。
func (p *TranscriptVirtualPager) HeightOf(id scene.CellID) int32 {
	if p == nil || p.Residency == nil {
		return 0
	}
	ambient := p.ambientHeight(id)
	if !p.ExpandAll {
		return ambient
	}
	if rows, ok := p.expanded[id]; ok {
		return rows
	}
	p.Estimated++
	return ambient + 1 // 粗估算：展开至少比折叠多一行；加载后由 SetExpandedHeight 校正
}

func (p *TranscriptVirtualPager) ambientHeight(id scene.CellID) int32 {
	seq, ok := p.Residency.SeqOf(id)
	if !ok {
		return 0
	}
	coldLen := p.Residency.Cold.Len()
	if seq < coldLen {
		return p.Residency.Cold.Cells[seq].RowsAtGen
	}
	offset := seq - p.Residency.Window.StartSeq
	if offset < 0 || offset >= len(p.Residency.Window.LocalRows) {
		return 0
	}
	return p.Residency.Window.LocalRows[offset]
}

// SetExpandedHeight 写回展开态实际高度（估算 → 校正）。
func (p *TranscriptVirtualPager) SetExpandedHeight(id scene.CellID, rows int32) {
	if p == nil {
		return
	}
	if _, exists := p.expanded[id]; !exists {
		p.Corrected++
	}
	p.expanded[id] = rows
}

// Stats 返回估算/校正计数（观测用）。
func (p *TranscriptVirtualPager) Stats() (estimated, corrected int) {
	if p == nil {
		return 0, 0
	}
	return p.Estimated, p.Corrected
}

// TotalRows 返回当前视图总高度：非 ExpandAll 走账本 O(1)；
// ExpandAll 需逐 cell 求和（估算/校正混合），用于测试与小规模视图。
func (p *TranscriptVirtualPager) TotalRows() int64 {
	if p == nil || p.Residency == nil {
		return 0
	}
	if !p.ExpandAll {
		return p.Residency.Cold.Rows.Total() + p.Residency.Window.TotalRows()
	}
	var total int64
	coldLen := p.Residency.Cold.Len()
	for seq := 0; seq < coldLen; seq++ {
		total += int64(p.HeightOf(p.Residency.Cold.Cells[seq].ID))
	}
	for index := range p.Residency.Window.Cells {
		total += int64(p.HeightOf(p.Residency.Window.Cells[index].ID))
	}
	return total
}

// VisibleRange 依据锚点与视口行数返回覆盖的 seq 区间 [startSeq, endSeq)。
// 锚点未知时回退到 Offset（旧字段语义）。
func (p *TranscriptVirtualPager) VisibleRange(anchor TranscriptPagerAnchor, viewportRows int) (startSeq, endSeq int, ok bool) {
	if p == nil || p.Residency == nil || viewportRows <= 0 {
		return 0, 0, false
	}
	var (
		rowStart int64
		found    bool
	)
	if seq, exists := p.Residency.SeqOf(anchor.CellID); exists {
		if base, ok2 := p.Residency.GlobalRowStart(seq); ok2 {
			rowStart = base + int64(anchor.Row)
			found = true
		}
	}
	if !found {
		if anchor.Offset <= 0 {
			rowStart = 0
		} else {
			rowStart = int64(anchor.Offset)
		}
	}
	total := p.TotalRows()
	if rowStart >= total {
		rowStart = 0
	}
	start, okStart := p.Residency.SeqAtGlobalRow(rowStart)
	if !okStart {
		start = 0
	}
	lastRow := rowStart + int64(viewportRows) - 1
	if lastRow >= total {
		lastRow = total - 1
	}
	last, okLast := p.Residency.SeqAtGlobalRow(lastRow)
	if !okLast {
		last = start
	}
	return start, last + 1, true
}

// AnchorAtRow 把全局行号转成锚点（CellID + cell 内行号 + 交付/布局代）。
func (p *TranscriptVirtualPager) AnchorAtRow(row int64) (TranscriptPagerAnchor, bool) {
	if p == nil || p.Residency == nil {
		return TranscriptPagerAnchor{}, false
	}
	seq, ok := p.Residency.SeqAtGlobalRow(row)
	if !ok {
		return TranscriptPagerAnchor{}, false
	}
	id, ok := p.Residency.CellIDAtSeq(seq)
	if !ok {
		return TranscriptPagerAnchor{}, false
	}
	base, _ := p.Residency.GlobalRowStart(seq)
	generation := p.Residency.Window.Generation
	if seq < p.Residency.Cold.Len() {
		generation = p.Residency.Cold.Cells[seq].Gen
	}
	return TranscriptPagerAnchor{
		CellID:           id,
		Row:              int(row - base),
		LayoutGeneration: generation,
		Offset:           int(row),
	}, true
}

// SeqRangesForPrefetch 是预取规划的可测试纯函数：返回 [from,to) 的 seq 区间
// （lookbehind 页 + forward 页，均限制在冷区 [0, coldLen)）。
type SeqRange struct {
	From int
	To   int
}

func SeqRangesForPrefetch(visibleStart, visibleEnd, coldLen, pageCells, lookbehind, forward int) []SeqRange {
	if pageCells <= 0 || coldLen <= 0 {
		return nil
	}
	var ranges []SeqRange
	from := visibleStart - lookbehind*pageCells
	if from < 0 {
		from = 0
	}
	to := visibleStart
	if to > coldLen {
		to = coldLen
	}
	if to > from {
		ranges = append(ranges, SeqRange{From: from, To: to})
	}
	fwdFrom := visibleEnd
	if fwdFrom < 0 {
		fwdFrom = 0
	}
	fwdTo := fwdFrom + forward*pageCells
	if fwdTo > coldLen {
		fwdTo = coldLen
	}
	if fwdTo > fwdFrom {
		ranges = append(ranges, SeqRange{From: fwdFrom, To: fwdTo})
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].From < ranges[j].From })
	return ranges
}

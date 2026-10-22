package ui

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// transcript_residency_index.go —— C 档双代账本与索引（任务 T3）。
//
// 设计文档：§14.2（ColdLedger/WindowLedger/GenerationSpan）、§14.7（generation/
// PrefixFence/foldTarget/GroupKeyFP/gap 归属）。
//
// 语义边界：
//   - ColdLedger 只记录**已交付物理事实**（只读、追加、按代记账）；
//   - WindowLedger 记录当前代可重排的驻留段（本地行号由前缀和定位）；
//   - 跨代行号不混用：冷区行数属于交付代，窗口行数属于当前代；
//   - gap row 归属后继 cell（与 scene.LayoutTranscript §7.4 口径一致），
//     因此 ColdCellRef 记录 LeadingGapRows 供窗口化投影还原首格 gap。

// GenerationSpan 是某一 layout generation 的已交付行数（跨代拼接用）。
type GenerationSpan struct {
	Gen  uint64
	Rows int64
}

// GroupKeyFingerprint 折叠 tool-chain 归组键与边界归组键（只存指纹，原文按需回读）。
func GroupKeyFingerprint(chainKey, boundaryGroupKey string) uint64 {
	h := fnv.New64a()
	var length [8]byte
	binary.LittleEndian.PutUint64(length[:], uint64(len(chainKey)))
	_, _ = h.Write(length[:])
	_, _ = h.Write([]byte(chainKey))
	binary.LittleEndian.PutUint64(length[:], uint64(len(boundaryGroupKey)))
	_, _ = h.Write(length[:])
	_, _ = h.Write([]byte(boundaryGroupKey))
	return h.Sum64()
}

// 前缀指纹链复用生产路径的逐 cell 折叠原语 transcriptFenceFoldCell
// （history_effect_planner.go），保证"冷区账本链 == 生产 fence"可对拍；
// 该原语覆盖 ID/Revision/Sequence/Kind/Phase/blocked/Boundary/source len/
// ChainKey/BoundaryGroupKey（§14.7）。

// ColdCellRef 是冷区 cell 的紧凑引用（设计 §14.2）。
type ColdCellRef struct {
	ID                 scene.CellID
	Revision           uint64
	Kind               scene.CellKind
	Phase              scene.CellPhase
	SourceFP           uint64 // 与写穿编解码共用同一指纹（T1）
	SourceLen          int
	RowsAtGen          int32 // 该代显示行数（含 leading gap）
	LeadingGapRows     int8  // 归本 cell 的 gap 行数（§7.4）
	Gen                uint64
	DeliveredAt        int64 // 交付序（严格递增）
	TombstonePermanent bool  // 永久结算（唯一允许逐出的终态，H3）
	Folded             bool  // 折叠工具链 cell（foldTarget 判定）
	GroupKeyFP         uint64
	FenceBefore        uint64 // 该 cell 之前的前缀链状态（自校验：必须等于前一条的 PrefixFence）
	PrefixFence        uint64 // 该 cell 之后的前缀链状态（含自身；与生产 fence 同语义）
}

// ColdRowLedger 是冷区行账本：按交付顺序累计行数，提供 O(log n) 定位。
type ColdRowLedger struct {
	starts   []int64 // 与 ColdLedger.Cells 对齐的行起点
	total    int64
	genRows  map[uint64]int64
	genOrder []GenerationSpan
}

// Len 返回条目数。
func (l *ColdRowLedger) Len() int { return len(l.starts) }

// Total 返回已交付物理行总数（跨代累计）。
func (l *ColdRowLedger) Total() int64 { return l.total }

// Append 追加一个 cell 的行数；返回其行起点。
func (l *ColdRowLedger) Append(rows int32, gen uint64) int64 {
	start := l.total
	l.starts = append(l.starts, start)
	l.total += int64(rows)
	if l.genRows == nil {
		l.genRows = make(map[uint64]int64)
	}
	if _, seen := l.genRows[gen]; !seen {
		l.genOrder = append(l.genOrder, GenerationSpan{Gen: gen})
	}
	l.genRows[gen] += int64(rows)
	return start
}

// RowStart 返回第 index 条 cell 的行起点。
func (l *ColdRowLedger) RowStart(index int) (int64, bool) {
	if index < 0 || index >= len(l.starts) {
		return 0, false
	}
	return l.starts[index], true
}

// Locate 二分定位全局行号属于哪个 cell、以及 cell 内偏移。
func (l *ColdRowLedger) Locate(row int64) (index int, offset int32, ok bool) {
	if row < 0 || row >= l.total {
		return 0, 0, false
	}
	low, high := 0, len(l.starts)-1
	for low <= high {
		mid := (low + high) / 2
		start := l.starts[mid]
		end := l.total
		if mid+1 < len(l.starts) {
			end = l.starts[mid+1]
		}
		switch {
		case row < start:
			high = mid - 1
		case row >= end:
			low = mid + 1
		default:
			return mid, int32(row - start), true
		}
	}
	return 0, 0, false
}

// RowsAtGen 返回某代累计的已交付行数。
func (l *ColdRowLedger) RowsAtGen(gen uint64) int64 {
	if l == nil || l.genRows == nil {
		return 0
	}
	return l.genRows[gen]
}

// GenerationSpans 返回按首次出现顺序排列的代行数（只读拷贝）。
func (l *ColdRowLedger) GenerationSpans() []GenerationSpan {
	if l == nil {
		return nil
	}
	out := make([]GenerationSpan, 0, len(l.genOrder))
	for _, span := range l.genOrder {
		out = append(out, GenerationSpan{Gen: span.Gen, Rows: l.genRows[span.Gen]})
	}
	return out
}

// ColdLedger 是冷区账本（已交付、已逐出 cell 的紧凑引用，只读追加）。
type ColdLedger struct {
	Cells []ColdCellRef
	Rows  ColdRowLedger
}

// Len 返回冷区 cell 数。
func (l *ColdLedger) Len() int {
	if l == nil {
		return 0
	}
	return len(l.Cells)
}

// LastPrefixFence 返回当前前缀指纹链末端；空账本返回链初始状态
// （与生产 fence 的 FNV offset basis 相同，保证两条链可逐值对拍）。
func (l *ColdLedger) LastPrefixFence() uint64 {
	if l == nil || len(l.Cells) == 0 {
		return transcriptFenceOffsetBasis
	}
	return l.Cells[len(l.Cells)-1].PrefixFence
}

// AppendCell 计算引用并追加（DeliveredAt 必须严格递增；行数不可为负）。
func (l *ColdLedger) AppendCell(cell scene.TranscriptCell, rowsAtGen int32, leadingGap int8, gen uint64, deliveredAt int64, tombstonePermanent bool) (ColdCellRef, error) {
	if l == nil {
		return ColdCellRef{}, fmt.Errorf("cold ledger: nil receiver")
	}
	if rowsAtGen < 0 {
		return ColdCellRef{}, fmt.Errorf("cold ledger: cell %d negative rows %d", cell.ID, rowsAtGen)
	}
	if leadingGap < 0 || int32(leadingGap) > rowsAtGen {
		return ColdCellRef{}, fmt.Errorf("cold ledger: cell %d leading gap %d out of range (rows=%d)", cell.ID, leadingGap, rowsAtGen)
	}
	if n := len(l.Cells); n > 0 && deliveredAt <= l.Cells[n-1].DeliveredAt {
		return ColdCellRef{}, fmt.Errorf("cold ledger: deliveredAt must strictly increase: prev=%d got=%d",
			l.Cells[n-1].DeliveredAt, deliveredAt)
	}
	ref := ColdCellRef{
		ID:                 cell.ID,
		Revision:           cell.Revision,
		Kind:               cell.Kind,
		Phase:              cell.Phase,
		SourceFP:           CellBodySourceFingerprint(cell),
		SourceLen:          len(cell.Source),
		RowsAtGen:          rowsAtGen,
		LeadingGapRows:     leadingGap,
		Gen:                gen,
		DeliveredAt:        deliveredAt,
		TombstonePermanent: tombstonePermanent,
		Folded:             cellUsesFoldedToolPresentation(cell),
		GroupKeyFP:         GroupKeyFingerprint(cell.ChainKey, cell.BoundaryGroupKey),
		FenceBefore:        l.LastPrefixFence(),
	}
	ref.PrefixFence = transcriptFenceFoldCell(ref.FenceBefore, cell)
	l.Cells = append(l.Cells, ref)
	l.Rows.Append(rowsAtGen, gen)
	return ref, nil
}

// Validate 自校验：交付序单调、行账本与引用一致、前缀指纹链与分组指纹可重算。
// 校验失败表示账本被破坏（§7.2 要求调用方 fail-closed）。
func (l *ColdLedger) Validate() error {
	if l == nil {
		return nil
	}
	previousFence := transcriptFenceOffsetBasis
	expectedRows := int64(0)
	for index := range l.Cells {
		ref := l.Cells[index]
		if index > 0 && ref.DeliveredAt <= l.Cells[index-1].DeliveredAt {
			return fmt.Errorf("cold ledger: deliveredAt not increasing at index %d", index)
		}
		start, ok := l.Rows.RowStart(index)
		if !ok || start != expectedRows {
			return fmt.Errorf("cold ledger: row ledger out of sync at index %d (start=%d want=%d)", index, start, expectedRows)
		}
		expectedRows += int64(ref.RowsAtGen)
		if ref.FenceBefore != previousFence {
			return fmt.Errorf("cold ledger: prefix fence chain broken at index %d", index)
		}
		previousFence = ref.PrefixFence
	}
	if expectedRows != l.Rows.Total() {
		return fmt.Errorf("cold ledger: row total mismatch: ledger=%d refs=%d", l.Rows.Total(), expectedRows)
	}
	return nil
}

// LastFoldedCellID 返回冷区最后一个折叠工具链 cell（无则 0）。
func (l *ColdLedger) LastFoldedCellID() scene.CellID {
	if l == nil {
		return 0
	}
	for index := len(l.Cells) - 1; index >= 0; index-- {
		if l.Cells[index].Folded {
			return l.Cells[index].ID
		}
	}
	return 0
}

// TotalBodyBytes 返回冷区 cell 正文的字节总和（§15.3 cold.bytes 口径）。
// 用 SourceLen（逐出时固化的正文长度）而不是重新读取 store：status 端点
// 不能为了一个字节数去扫盘。
func (l *ColdLedger) TotalBodyBytes() int64 {
	if l == nil {
		return 0
	}
	var total int64
	for index := range l.Cells {
		total += int64(l.Cells[index].SourceLen)
	}
	return total
}

// RowGenerations 返回冷区行账本涉及的布局代列表（升序去重）。
func (l *ColdLedger) RowGenerations() []uint64 {
	if l == nil {
		return nil
	}
	spans := l.Rows.GenerationSpans()
	generations := make([]uint64, 0, len(spans))
	for _, span := range spans {
		generations = append(generations, span.Gen)
	}
	return generations
}

// WindowLedger 是当前代驻留窗口（可重排、可重铸）。
type WindowLedger struct {
	StartSeq  int // 在全局 cell 全序中的下标
	EndSeq    int
	Cells     []scene.TranscriptCell
	LocalRows []int32
	// LeadingGaps 是每 cell 的 leading gap 行数（与 LocalRows 对齐；nil = 全 0）。
	// 窗口化投影必须携带它，否则边界 cell 会少渲染冷区侧 gap（§7.4/§14.7）。
	LeadingGaps []int8
	Generation  uint64
}

// TotalRows 返回窗口当前代总行数。
func (l *WindowLedger) TotalRows() int64 {
	if l == nil {
		return 0
	}
	var total int64
	for _, rows := range l.LocalRows {
		total += int64(rows)
	}
	return total
}

// RowStarts 返回窗口内每 cell 的本地行起点（前缀和）。
func (l *WindowLedger) RowStarts() []int64 {
	if l == nil {
		return nil
	}
	starts := make([]int64, len(l.LocalRows))
	var running int64
	for index, rows := range l.LocalRows {
		starts[index] = running
		running += int64(rows)
	}
	return starts
}

// RowStart 二分定位本地行号属于窗口内哪个 cell。
func (l *WindowLedger) Locate(localRow int64) (index int, offset int32, ok bool) {
	starts := l.RowStarts()
	if localRow < 0 || localRow >= l.TotalRows() {
		return 0, 0, false
	}
	low, high := 0, len(starts)-1
	for low <= high {
		mid := (low + high) / 2
		start := starts[mid]
		end := int64(0)
		if mid+1 < len(starts) {
			end = starts[mid+1]
		} else {
			end = l.TotalRows()
		}
		switch {
		case localRow < start:
			high = mid - 1
		case localRow >= end:
			low = mid + 1
		default:
			return mid, int32(localRow - start), true
		}
	}
	return 0, 0, false
}

// LastFoldedCellID 返回窗口内最后一个折叠工具链 cell（无则 0）。
func (l *WindowLedger) LastFoldedCellID() scene.CellID {
	if l == nil {
		return 0
	}
	for index := len(l.Cells) - 1; index >= 0; index-- {
		if cellUsesFoldedToolPresentation(l.Cells[index]) {
			return l.Cells[index].ID
		}
	}
	return 0
}

// GlobalFoldTarget 返回全局"最近一次折叠"cell：窗口是转录尾部，因此
// 窗口内存在折叠时它必然是全局最后者；否则回退到冷区最后一个折叠（H1）。
func GlobalFoldTarget(cold *ColdLedger, window *WindowLedger) scene.CellID {
	if target := window.LastFoldedCellID(); target != 0 {
		return target
	}
	return cold.LastFoldedCellID()
}

// DetachPrefix 把窗口前 target 个 cell 移出窗口，返回被移交的 cell 段
// （T4 的滑动/逐出在写穿成功后调用；本函数只做账本变换，不写存储）。
func (l *WindowLedger) DetachPrefix(target int) ([]scene.TranscriptCell, []int32, error) {
	if l == nil {
		return nil, nil, fmt.Errorf("window ledger: nil receiver")
	}
	if target < 0 || target > len(l.Cells) {
		return nil, nil, fmt.Errorf("window ledger: detach target %d out of range (cells=%d)", target, len(l.Cells))
	}
	detached := append([]scene.TranscriptCell(nil), l.Cells[:target]...)
	rows := append([]int32(nil), l.LocalRows[:target]...)
	l.Cells = append([]scene.TranscriptCell(nil), l.Cells[target:]...)
	l.LocalRows = append([]int32(nil), l.LocalRows[target:]...)
	if l.LeadingGaps != nil {
		l.LeadingGaps = append([]int8(nil), l.LeadingGaps[target:]...)
	}
	l.StartSeq += target
	return detached, rows, nil
}

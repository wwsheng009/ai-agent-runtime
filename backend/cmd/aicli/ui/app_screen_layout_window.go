package ui

import (
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// app_screen_layout_window.go —— T8b-3 窗口化布局输入。
//
// 逐帧画面只需要视口装得下的最后若干行（layoutTranscriptTailScreenRows 的
// 既有语义），但旧实现先用 state.Transcript.LayoutRows 物化全量语义行、再用
// transcriptCellsByID 复制全量 cells map：大会话下每帧分配量与历史长度成正比
// （生产实测 3951 cells / 146535 行）。这里把「布局输入」本身也窗口化：
//
//   - 起点由 scene.TailLayoutWindowStart 给出的 cell 边界决定（与全量布局同一
//     计数口径），窗口内再按需扩张直到视口行数足够；
//   - 窗口携带一个前驱上下文 cell：gap row 归属后继 cell（§7.4/§14.7），首个
//     窗口 cell 的 leading gap 必须由前驱的 boundary 元数据决定，之后丢弃
//     上下文 cell 自身的行；
//   - foldTarget 由 transcriptGlobalFoldTarget 全局给定（§14.7），不得在窗口内
//     局部重算，否则跨窗口的「最近一次折叠」提示位会错位；
//   - cells map 只覆盖窗口，transcript 行只在窗口内物化。
//
// 与全量布局的等价性：窗口行序列是全量 LayoutTranscript 行的连续后缀（cell
// 对齐 + 保留 leading gap），每个 cell 的 wrap/结构化投影是纯函数且整 cell
// 参与（布局缓存条目与全量路径一致），因此尾部裁剪后的输出与
// layoutTranscriptTailScreenRows(全量行, maxRows) 逐行一致；窗口扩张只发生在
// 尾部行数不足时，扩张只会向前追加行，裁剪取尾部，结果不变。

// transcriptGlobalFoldTarget 返回 transcript 顺序中最后一个可折叠工具链 cell
// （无则 0）。excluded（未提交屏障及其后 cell，由 active band 渲染）不参与折叠
// 归属，与 toolFoldTargetRows 的 mutable 过滤一致。零分配扫描；稳态下 omission
// 判定命中 source 内容寻址缓存，成本 O(新增 cell)。
func transcriptGlobalFoldTarget(transcript TranscriptState, excluded map[scene.CellID]struct{}) scene.CellID {
	target := scene.CellID(0)
	for index := range transcript.Cells {
		cell := transcript.Cells[index]
		if _, skip := excluded[cell.ID]; skip {
			continue
		}
		if !cellUsesFoldedToolPresentation(cell) || !toolFoldOmits(cell.Source) {
			continue
		}
		target = cell.ID
	}
	return target
}

// layoutTranscriptWindowRows 生成 cells[start,end) 的语义布局行。
//
// start > 0 时把 start-1 作为前驱上下文 cell 一起布局，然后按「上下文 cell 自身
// 产生的行数」精确切掉前缀（gap row 归属后继 cell，所以窗口首个 cell 的 gap row
// 保留在结果里，与全量布局的后缀逐行一致）。返回行的 Index 为窗口局部值：逐帧
// 消费者不读全局行号（BoundaryKey.PolicyVersion 仍取调用方传入的布局代）。
func layoutTranscriptWindowRows(cells []scene.TranscriptCell, start, end int, policyVersion uint64) []scene.LayoutRow {
	if start >= end || len(cells) == 0 {
		return nil
	}
	contextStart := start
	if start > 0 {
		contextStart = start - 1
	}
	pointers := make([]*scene.TranscriptCell, 0, end-contextStart)
	for index := contextStart; index < end; index++ {
		pointers = append(pointers, &cells[index])
	}
	rows := scene.LayoutTranscript(pointers, policyVersion)
	if start == 0 {
		return rows
	}
	// 上下文 cell 的行是行序列的连续前缀，且只有它自己的语义行：布局输入从它
	// 开始，它没有前驱，因此不含它的 leading gap row。切掉这个前缀后，剩余行
	// 的第一行就是窗口首个 cell 的 gap row（若有）——窗口行序列因此是全量
	// LayoutTranscript 行的连续后缀。不能按 CellID 切（同一 cell 的多个 section
	// 条目共享 ID，会连带切掉窗口）。
	prefix := scene.LayoutCellRowCount(&cells[contextStart])
	if prefix >= len(rows) {
		return nil
	}
	return rows[prefix:]
}

// transcriptWindowCellsByID 只物化窗口内的 cell 快照：帧内按需 map 的语义不变
// （未引用 cell 不可观测），分配量与窗口而不是历史长度成正比。
func transcriptWindowCellsByID(cells []scene.TranscriptCell, start, end int) map[scene.CellID]scene.TranscriptCell {
	if start >= end {
		return nil
	}
	window := make(map[scene.CellID]scene.TranscriptCell, end-start)
	for index := start; index < end; index++ {
		window[cells[index].ID] = cells[index]
	}
	return window
}

// layoutTranscriptTailScreenRowsWindowed 是 layoutTranscriptTailScreenRows 的
// 窗口化实现：输入是全量 cell 切片，但只对尾部窗口调用 LayoutTranscript。
// 输出与「全量布局后取末 maxRows 行」逐行一致（见文件头等价性说明）。
func layoutTranscriptTailScreenRowsWindowed(cells []scene.TranscriptCell, excluded map[scene.CellID]struct{}, policyVersion uint64, width, maxRows int, foldTarget scene.CellID, theme style.ThemeContext) []AppScreenRow {
	if maxRows <= 0 || len(cells) == 0 {
		return nil
	}
	end := len(cells)
	start := scene.TailLayoutWindowStart(cells, excluded, maxRows)
	fold := transcriptFoldTarget{ID: foldTarget, Known: true}
	var laid []AppScreenRow
	for {
		rows := layoutTranscriptWindowRows(cells, start, end, policyVersion)
		laid = layoutTranscriptScreenRowsWithFoldTarget(rows, transcriptWindowCellsByID(cells, start, end), excluded, width, fold, theme)
		if len(laid) >= maxRows || start == 0 {
			break
		}
		// 初始窗口按「每个非 gap 语义行至少产出一行」估计，对 wrap 与结构化
		// 投影（markdown/chroma 渲染）偏小。按几何增长扩大窗口重试；重试时窗口
		// 内的 cell 全部命中布局缓存，代价只有缓存查询。
		next := start - (end - start)
		if next < 0 {
			next = 0
		}
		if next >= start {
			next = start - 1
		}
		start = next
	}
	if len(laid) > maxRows {
		laid = laid[len(laid)-maxRows:]
	}
	return laid
}

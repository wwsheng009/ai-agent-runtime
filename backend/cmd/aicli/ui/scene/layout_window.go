package scene

import "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/boundary"

// 窗口化布局（T8b-3）：逐帧画面只需要 transcript 的尾部窗口，但窗口起点必须
// 与 LayoutTranscript 的 cell 边界对齐，且窗口首个 cell 的 leading gap 依赖其
// 前驱 cell 的 boundary 元数据（gap row 归属后继 cell，§7.4/§14.7）。
//
// 本文件是「窗口选择计数口径」的唯一来源：调用方不得自己数行（否则窗口起点
// 会与 LayoutTranscript 漂移）。计数规则与 LayoutTranscript 完全一致：
// gap row 计入后继 cell、空 source 不产生行、同 ID 不产生 gap。

// LayoutCellRowCount 返回一个 cell 在 LayoutTranscript 中产生的语义行数
// （不含与前一 cell 之间的 gap row）。命中 split 缓存时零分配。
func LayoutCellRowCount(c *TranscriptCell) int {
	return len(layoutSplitSourceLines(c))
}

// LayoutGapRowCount 返回 prev 与 next 之间由 LayoutTranscript 产生的 gap 行数
// （gap row 归属后继 cell）。prev == nil 或同 ID 时为 0。
func LayoutGapRowCount(prev, next *TranscriptCell) int {
	if prev == nil || next == nil || prev.ID == next.ID {
		return 0
	}
	return int(boundary.ResolveGap(prev.BoundaryMeta(), next.BoundaryMeta()))
}

// TailLayoutWindowStart 返回尾部窗口的最早 cell 下标：从该 cell 到切片末尾
// （含该 cell 与前驱之间的 gap row）累计的语义行数不少于 minRows。
//
// excluded 中的 cell 计 0：未提交 cell 由 active band 渲染，布局阶段整块跳过
// （gap row 归属后继 cell，因此它的 gap row 也一并计 0）。计数口径与
// transcriptTailStartIndex（ui）一致；返回值指向 cell 起点，调用方自行决定
// 是否携带前驱上下文 cell 以复现 leading gap。
func TailLayoutWindowStart(cells []TranscriptCell, excluded map[CellID]struct{}, minRows int) int {
	if minRows <= 0 || len(cells) == 0 {
		return len(cells)
	}
	accumulated := 0
	for index := len(cells) - 1; index >= 0; index-- {
		if _, skip := excluded[cells[index].ID]; skip {
			continue
		}
		accumulated += LayoutCellRowCount(&cells[index])
		if index > 0 {
			accumulated += LayoutGapRowCount(&cells[index-1], &cells[index])
		}
		if accumulated >= minRows {
			return index
		}
	}
	return 0
}

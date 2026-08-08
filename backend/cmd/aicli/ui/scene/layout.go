package scene

import (
	"fmt"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/boundary"
)

// BoundaryKey 是 gap row 的稳定身份（unified plan §7.4 推荐方案）：
//
//	BoundaryKey = (PrevCellID, NextCellID, PolicyVersion)
//
// 用途：resize 时稳定重算、mutable update 不重复添加空行、handoff 时判断
// 是否已提交、测试断言 cell sequence 与 boundary sequence。
type BoundaryKey struct {
	PrevCellID    CellID
	NextCellID    CellID
	PolicyVersion uint64
}

// String 返回稳定可读 key。
func (k BoundaryKey) String() string {
	return fmt.Sprintf("b:%d->%d@%d", k.PrevCellID, k.NextCellID, k.PolicyVersion)
}

// LayoutRow 是 LayoutTranscript 的输出行：要么是 cell 的语义行，要么是
// boundary row（gap）。
type LayoutRow struct {
	CellID  CellID        // 归属 cell（gap row 归属后继 cell，§7.4）
	Text    string        // cell 的 source 文本行；gap row 为 ""
	Gap     boundary.GapRows // gap row 时为 gap 行数（1），否则 0
	Boundary *BoundaryKey // 非 nil 表示这是 boundary row
	Index   int           // 全局行序（从 0 起，便于测试断言）
}

// LayoutTranscript 派生 transcript 的 boundary row 序列（§7.4 推荐方案：
// Scene 只保存 semantic cells，Layout 遍历相邻 cell 时插入 gap row）。
//
// 规则（全部来自 boundary.ResolveGap 规则表，禁止本层特例）：
//   - 首 cell 前无 gap（transcript 不以空行开头）；
//   - 相邻 top-level cell 之间按 ResolveGap 输出 0/1 gap；
//   - mutable update/finalize 不产生新 boundary（同 ID 不参与）；
//   - 被过滤/空 cell 不推进 boundary state（调用方应先过滤，本函数
//     对空 cell 直接跳过且不推进 gap 计算）；
//   - replay 与 handoff 复用同一派生（无状态纯函数，禁止特例）。
//
// PolicyVersion 由调用方传入（LayoutGeneration 或政策版本），用于
// BoundaryKey 稳定重算与 handoff 判重。
func LayoutTranscript(cells []*TranscriptCell, policyVersion uint64) []LayoutRow {
	var rows []LayoutRow
	index := 0
	var prev *TranscriptCell
	for _, c := range cells {
		if c == nil {
			continue
		}
		// 同一 cell 的后续 section 不做边界处理；仅相邻不同 cell 参与。
		if prev != nil && prev.ID != c.ID {
			// gap 决策完全委托 boundary.ResolveGap（INV-GAP-03）：
			// 同 chain 稠密、同 ID replace、首 cell 等规则都在规则表内。
			if gap := boundary.ResolveGap(prev.BoundaryMeta(), c.BoundaryMeta()); gap == boundary.GapOne {
				rows = append(rows, LayoutRow{
					CellID:   c.ID,
					Gap:      boundary.GapOne,
					Boundary: &BoundaryKey{PrevCellID: prev.ID, NextCellID: c.ID, PolicyVersion: policyVersion},
					Index:    index,
				})
				index++
			}
		}
		prev = c
		for _, line := range layoutSplitSourceLines(c) {
			rows = append(rows, LayoutRow{CellID: c.ID, Text: line, Index: index})
			index++
		}
	}
	return rows
}

// splitSourceLines 把 cell source 拆为语义行（保留内部空行，不 TrimSpace：
// markdown/code/preformatted 内部空白属于 source，§7.2）。
// 空 source 返回 nil（不产生行，也不推进 boundary state——INV-GAP-05）。
func splitSourceLines(source string) []string {
	if source == "" {
		return nil
	}
	out := []string{}
	start := 0
	for i := 0; i < len(source); i++ {
		if source[i] == '\n' {
			out = append(out, source[start:i])
			start = i + 1
		}
	}
	out = append(out, source[start:])
	return out
}

package scene

import (
	"fmt"
	"strings"
	"testing"
)

// 本文件是计划 §4.3（L2.2 语义布局缓存/增量）的验收仪器：
// BenchmarkLayoutTranscriptResumeScale 用「恢复会话量级」的合成 transcript
// （6,719 cells / ~161k 语义行，见下）分别测量三种缓存状态下的 LayoutTranscript：
//
//	cold   每轮清空共享缓存 → 全量 splitSourceLines（首次恢复/缓存抖动时的上界）
//	hot    缓存已预热、内容不变 → 稳态帧路径（每次 delta 都会走）
//	append mutable 尾 cell 每轮追加一行（revision++）→ append-only cell 的增长路径
//
// 之所以先有基准再有优化：L2.2 的两个假设（容量不足导致抖动、增长 cell 重切整份
// source）都需要在真实规模上量化，否则无法判断收益归属。数字写入
// docs/e2e/resume-replay-census.md（§7.14）。
//
// 规模依据（计划 §4.3）：生产恢复会话实测 6,719 cells / 146,535 布局行；
// 本基准取 24 行/cell ≈ 161,256 行，落在同一量级。

const (
	layoutScaleCells        = 6719
	layoutScaleLinesPerCell = 24
)

func layoutScaleLine(cellID, line int) string {
	return fmt.Sprintf("row-%05d-%02d 这是一行典型的中文回复内容，用于撑起真实的排版宽度与行数。", cellID, line)
}

// layoutScaleCorpus 构造恢复会话量级的合成 transcript。每个 cell 都是
// committed/finalized（生产恢复路径安装的正是这些），source 以 '\n' 结尾。
func layoutScaleCorpus(cells, linesPerCell int) []*TranscriptCell {
	out := make([]*TranscriptCell, 0, cells)
	for id := 1; id <= cells; id++ {
		var builder strings.Builder
		for line := 0; line < linesPerCell; line++ {
			builder.WriteString(layoutScaleLine(id, line))
			builder.WriteByte('\n')
		}
		out = append(out, &TranscriptCell{
			ID:       CellID(id),
			Sequence: uint64(id),
			Kind:     KindAssistant,
			Source:   builder.String(),
			Revision: 1,
			Phase:    CellCommitted,
		})
	}
	return out
}

func BenchmarkLayoutTranscriptResumeScale(b *testing.B) {
	cells := layoutScaleCorpus(layoutScaleCells, layoutScaleLinesPerCell)

	b.Run("cold", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			sharedSplitSourceCache.clear()
			rows := LayoutTranscript(cells, 1)
			if len(rows) == 0 {
				b.Fatal("no rows")
			}
		}
	})

	b.Run("hot", func(b *testing.B) {
		b.ReportAllocs()
		rows := LayoutTranscript(cells, 1) // 预热
		if len(rows) == 0 {
			b.Fatal("no rows")
		}
		b.ResetTimer()
		for b.Loop() {
			LayoutTranscript(cells, 1)
		}
	})

	// append：稳态里最贵的一类 cell 是「正在流式增长的 mutable cell」。
	// 每轮给它追加一行（revision++），其余 cell 不变。
	b.Run("append", func(b *testing.B) {
		b.ReportAllocs()
		mutable := &TranscriptCell{
			ID:       CellID(layoutScaleCells + 1),
			Sequence: uint64(layoutScaleCells + 1),
			Kind:     KindAssistant,
			Source:   strings.Repeat(layoutScaleLine(layoutScaleCells+1, 0)+"\n", layoutScaleLinesPerCell),
			Revision: 1,
			Phase:    CellMutable,
		}
		corpus := append(append([]*TranscriptCell{}, cells...), mutable)
		LayoutTranscript(corpus, 1) // 预热
		var builder strings.Builder
		builder.WriteString(mutable.Source)
		line := layoutScaleLinesPerCell
		b.ResetTimer()
		for b.Loop() {
			builder.WriteString(layoutScaleLine(layoutScaleCells+1, line) + "\n")
			line++
			mutable.Source = builder.String()
			mutable.Revision++
			LayoutTranscript(corpus, 1)
		}
	})
}

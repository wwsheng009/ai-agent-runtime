package ui

import (
	"fmt"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// BenchmarkToolFoldOmitsUncached 是优化前每轮布局的真实成本：每个折叠 cell
// 完整执行一次 BuildPreview（ANSIToLines + 逐行 Render + Truncate）。
func BenchmarkToolFoldOmitsUncached(b *testing.B) {
	source := numberedToolLines("bench-uncached", 60)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !toolFoldOmitsUncached(source) {
			b.Fatal("want omitted=true")
		}
	}
}

// BenchmarkToolFoldOmitsMemoHit 是优化后的稳态成本：内容寻址 LRU 命中。
func BenchmarkToolFoldOmitsMemoHit(b *testing.B) {
	source := numberedToolLines("bench-memo", 60)
	cache := newFoldOmissionCache()
	if !cache.omits(source) {
		b.Fatal("want omitted=true")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !cache.omits(source) {
			b.Fatal("want omitted=true")
		}
	}
}

// benchmarkFoldTargetRowsLegacy 复刻优化前的 toolFoldTargetRows：无 memo、无
// 逐行去重，每个 row 都重新判定（profile 中 19.8% 采样的来源）。
func benchmarkFoldTargetRowsLegacy(rows []scene.LayoutRow, cells map[scene.CellID]scene.TranscriptCell, mutable map[scene.CellID]struct{}) scene.CellID {
	target := scene.CellID(0)
	for _, row := range rows {
		if _, excluded := mutable[row.CellID]; excluded {
			continue
		}
		candidate, found := cells[row.CellID]
		if !found || candidate.ID == target {
			continue
		}
		if !cellUsesFoldedToolPresentation(candidate) || !toolFoldOmitsUncached(candidate.Source) {
			continue
		}
		target = candidate.ID
	}
	return target
}

func benchFoldTargetRowsFixture(b *testing.B, cellCount int) ([]scene.LayoutRow, map[scene.CellID]scene.TranscriptCell, map[scene.CellID]struct{}, scene.CellID) {
	b.Helper()
	cells := make(map[scene.CellID]scene.TranscriptCell, cellCount)
	rows := make([]scene.LayoutRow, 0, cellCount*3)
	for i := 1; i <= cellCount; i++ {
		id := scene.CellID(i)
		cells[id] = committedToolCell(id, numberedToolLines(fmt.Sprintf("bench-%d", i), 60))
		rows = append(rows,
			scene.LayoutRow{CellID: id, Gap: 1, Index: len(rows)},
			scene.LayoutRow{CellID: id, Index: len(rows)},
			scene.LayoutRow{CellID: id, Index: len(rows)},
		)
	}
	return rows, cells, map[scene.CellID]struct{}{}, scene.CellID(cellCount)
}

// BenchmarkToolFoldTargetRowsResumedSessionLegacy 模拟长会话（1024 个折叠
// cell × 3 行）优化前的逐行全量判定。
func BenchmarkToolFoldTargetRowsResumedSessionLegacy(b *testing.B) {
	rows, cells, mutable, want := benchFoldTargetRowsFixture(b, 1024)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := benchmarkFoldTargetRowsLegacy(rows, cells, mutable); got != want {
			b.Fatalf("legacy target = %d, want %d", got, want)
		}
	}
}

// BenchmarkToolFoldTargetRowsResumedSession 是优化后的稳态：memo 全热 +
// 逐 cell 去重（每轮只有一次 map 查询与 source 哈希）。
func BenchmarkToolFoldTargetRowsResumedSession(b *testing.B) {
	rows, cells, mutable, want := benchFoldTargetRowsFixture(b, 1024)
	sharedFoldOmissions.reset()
	defer sharedFoldOmissions.reset()
	if got := toolFoldTargetRows(rows, cells, mutable); got != want {
		b.Fatalf("warmup target = %d, want %d", got, want)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := toolFoldTargetRows(rows, cells, mutable); got != want {
			b.Fatalf("target = %d, want %d", got, want)
		}
	}
}

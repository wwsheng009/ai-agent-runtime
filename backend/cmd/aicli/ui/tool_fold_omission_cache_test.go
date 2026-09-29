package ui

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// TestFoldOmissionCacheMemoizesPerSource 锁定缓存的核心契约：同一 source 只
// 判定一次；换 source 必须重新判定（内容寻址，无显式失效）。
func TestFoldOmissionCacheMemoizesPerSource(t *testing.T) {
	cache := newFoldOmissionCache()
	long := numberedToolLines("memo", 60)
	short := "• Completed shell in 12ms"

	if got, want := cache.omits(long), toolFoldOmitsUncached(long); got != want || !got {
		t.Fatalf("omits(long) = %v (want %v, 且应为 true)", got, want)
	}
	if hits, misses, _, entries, _ := cache.stats(); hits != 0 || misses != 1 || entries != 1 {
		t.Fatalf("首次判定后 stats hits=%d misses=%d entries=%d，want 0/1/1", hits, misses, entries)
	}

	if got := cache.omits(long); !got {
		t.Fatalf("omits(long) 第二次 = false，want true")
	}
	if hits, misses, _, _, _ := cache.stats(); hits != 1 || misses != 1 {
		t.Fatalf("重复判定后 stats hits=%d misses=%d，want 1/1", hits, misses)
	}

	if got, want := cache.omits(short), toolFoldOmitsUncached(short); got != want || got {
		t.Fatalf("omits(short) = %v (want %v, 且应为 false)", got, want)
	}
	if _, misses, _, entries, _ := cache.stats(); misses != 2 || entries != 2 {
		t.Fatalf("换 source 后 misses=%d entries=%d，want 2/2", misses, entries)
	}
}

// TestFoldOmissionCacheMatchesUncachedJudgement 是差分测试：缓存值必须与
// BuildPreview 本体逐一一致，覆盖尾部空行、字节上限、CJK 与边界行数。
func TestFoldOmissionCacheMatchesUncachedJudgement(t *testing.T) {
	cases := []struct {
		name   string
		source string
	}{
		{"empty", ""},
		{"single-line", "• Completed shell in 12ms"},
		{"exactly-budget-lines", "a\nb\nc\nd"},
		{"trailing-empty-lines", "a\nb\nc\nd\n\n\n"},
		{"over-budget-lines", numberedToolLines("diff", 60)},
		{"over-byte-cap-single-line", strings.Repeat("x", 9*1024)},
		{"cjk-lines", strings.Repeat("中文输出行\n", 12)},
		{"crlf", "a\r\nb\r\nc\r\nd\r\ne\r\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cache := newFoldOmissionCache()
			want := toolFoldOmitsUncached(tc.source)
			if got := cache.omits(tc.source); got != want {
				t.Fatalf("cache.omits = %v, toolFoldOmitsUncached = %v", got, want)
			}
			// 第二次必须是缓存命中且结论不变。
			if got := cache.omits(tc.source); got != want {
				t.Fatalf("cache.omits (cached) = %v, want %v", got, want)
			}
		})
	}
}

// TestToolFoldTargetRowsChecksEachCellOnce 锁定逐行去重：同一个 cell 占据多行
// 时只做一次 omission 判定（否则一个长 cell 会把同一次 BuildPreview 判定重复
// N 遍）；mutable cell 完全跳过。
func TestToolFoldTargetRowsChecksEachCellOnce(t *testing.T) {
	sharedFoldOmissions.reset()
	t.Cleanup(sharedFoldOmissions.reset)

	cells := map[scene.CellID]scene.TranscriptCell{
		1: committedToolCell(1, numberedToolLines("one", 60)),
		2: committedToolCell(2, numberedToolLines("two", 60)),
		3: committedToolCell(3, "• Completed shell in 12ms"),
		4: committedToolCell(4, numberedToolLines("mutable", 60)),
	}
	mutable := map[scene.CellID]struct{}{4: {}}

	rows := make([]scene.LayoutRow, 0, 40*4)
	for round := 0; round < 10; round++ {
		for id := scene.CellID(1); id <= 4; id++ {
			rows = append(rows,
				scene.LayoutRow{CellID: id, Gap: 1, Index: len(rows)},
				scene.LayoutRow{CellID: id, Index: len(rows)},
			)
		}
	}

	if got := toolFoldTargetRows(rows, cells, mutable); got != 2 {
		t.Fatalf("toolFoldTargetRows = %d, want 2（最近一次折叠是 cell 2）", got)
	}
	hits, misses, _, entries, _ := sharedFoldOmissions.stats()
	// 去重后每个非 mutable cell 恰好查一次：cell 1/2/3 → 3 miss；若逐行判定，
	// 同 source 的后续查询会变成 cache hit（hits 远大于 0）。
	if misses != 3 || hits != 0 || entries != 3 {
		t.Fatalf("stats hits=%d misses=%d entries=%d，want 0/3/3（逐行去重失效）", hits, misses, entries)
	}
}

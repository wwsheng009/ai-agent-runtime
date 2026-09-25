package scene

import "testing"

func TestSplitSourceLinesCacheHitAndInvalidation(t *testing.T) {
	c := newSplitSourceLinesCache(4)
	src := "a\nbb\nccc"
	c.linesFor(1, 1, src)
	if lines := c.linesFor(1, 1, src); len(lines) != 3 || lines[1] != "bb" {
		t.Fatalf("hit failed: %#v", lines)
	}
	if c.hits != 1 || c.misses != 1 {
		t.Fatalf("hits=%d misses=%d, want 1/1", c.hits, c.misses)
	}
	// revision 变化 → miss（内容可能已变，必须重算）
	if _, ok := c.entries[1]; !ok {
		t.Fatal("entry missing")
	}
	c.linesFor(1, 2, src)
	if c.misses != 2 {
		t.Fatalf("misses=%d, want 2 (revision change must miss)", c.misses)
	}
	// 同 ID/Revision 不同内容 → miss（hash 键会误命中，字符串校验必须 miss）
	c.linesFor(1, 2, "a\nbb\nzzz")
	if c.misses != 3 {
		t.Fatalf("misses=%d, want 3 (same id/revision different source must miss)", c.misses)
	}
}

func TestSplitSourceLinesCacheEviction(t *testing.T) {
	c := newSplitSourceLinesCache(2)
	c.linesFor(1, 1, "a")
	c.linesFor(2, 1, "b")
	c.linesFor(3, 1, "c")
	if _, ok := c.entries[1]; ok {
		t.Fatalf("expected oldest evicted")
	}
	if _, ok := c.entries[3]; !ok {
		t.Fatalf("expected newest present")
	}
	if c.evictions != 1 {
		t.Fatalf("evictions=%d, want 1", c.evictions)
	}
	if c.lines != 2 {
		t.Fatalf("lines=%d, want 2 (evicted entry must be subtracted)", c.lines)
	}
}

// TestSplitSourceLinesCacheGrowthKeepsOneEntryPerCell 钉住「增长 cell 的内存上界」：
// 同一个 cell 反复增长（每次 revision++）后，缓存里只能有它一份条目——
// 旧实现用 (ID, Revision, source) 三元组键，会把每个 revision 的整份 source
// 都钉在内存里（上万行 cell × 上千 revision 的 O(n²) 内存）。
func TestSplitSourceLinesCacheGrowthKeepsOneEntryPerCell(t *testing.T) {
	c := newSplitSourceLinesCache(4)
	src := ""
	for i := 0; i < 100; i++ {
		src += "line\n"
		c.linesFor(7, uint64(i+1), src)
	}
	if len(c.entries) != 1 {
		t.Fatalf("entries=%d, want 1 (one entry per cell)", len(c.entries))
	}
	if c.reuses != 99 {
		t.Fatalf("reuses=%d, want 99 (pure append must reuse the prefix)", c.reuses)
	}
}

// TestLayoutSplitSourceLinesCacheRoundTrip 验证缓存路径与直接切分语义一致。
func TestLayoutSplitSourceLinesCacheRoundTrip(t *testing.T) {
	sharedSplitSourceCache.clear()
	cell := &TranscriptCell{ID: 9, Revision: 3, Kind: KindUser, Source: "l1\nl2\n\nl4\n"}
	direct := splitSourceLines(cell.Source)
	cached := layoutSplitSourceLines(cell)
	if len(direct) != len(cached) {
		t.Fatalf("len mismatch: direct=%d cached=%d", len(direct), len(cached))
	}
	for i := range direct {
		if direct[i] != cached[i] {
			t.Fatalf("line %d mismatch: %q vs %q", i, direct[i], cached[i])
		}
	}
	// 空 source：不缓存、返回 nil
	if got := layoutSplitSourceLines(&TranscriptCell{ID: 9, Revision: 3, Kind: KindUser}); got != nil {
		t.Fatalf("empty source should return nil, got %#v", got)
	}
	// nil cell：与空 source 同语义（不 panic）
	if got := layoutSplitSourceLines(nil); got != nil {
		t.Fatalf("nil cell should return nil, got %#v", got)
	}
}

// TestSplitSourceLinesCacheIncrementalMatchesFullSplit 是增量前缀复用的 oracle：
// 「只追加」的 source 序列上，缓存结果必须与 splitSourceLines 逐元素相同。
// 覆盖三种边界：无换行的单行、以 '\n' 结尾（空尾巴）、追加不完整行。
func TestSplitSourceLinesCacheIncrementalMatchesFullSplit(t *testing.T) {
	sequences := [][]string{
		{"a", "a\n", "a\nb", "a\nb\n", "a\nb\nc", "a\nb\nc\n"},
		{"x\ny\n", "x\ny\n\n", "x\ny\n\nz"},
		{"单行无换行", "单行无换行\n第二行", "单行无换行\n第二行\n"},
		{"a\nb\n", "a\nb\n"}, // 同内容不同 revision：仍必须等价
	}
	for si, seq := range sequences {
		c := newSplitSourceLinesCache(4)
		for i, src := range seq {
			got := c.linesFor(CellID(si+1), uint64(i+1), src)
			want := splitSourceLines(src)
			if len(got) != len(want) {
				t.Fatalf("seq %d step %d (%q): len=%d want %d", si, i, src, len(got), len(want))
			}
			for j := range want {
				if got[j] != want[j] {
					t.Fatalf("seq %d step %d (%q): line %d = %q want %q", si, i, src, j, got[j], want[j])
				}
			}
		}
	}
	// 反向：非追加（截断/替换）必须回落全量切分，且结果仍然正确。
	c := newSplitSourceLinesCache(4)
	c.linesFor(1, 1, "long\nsource\nhere\n")
	got := c.linesFor(1, 2, "short\n")
	want := splitSourceLines("short\n")
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("shrink: got %#v want %#v", got, want)
	}
}

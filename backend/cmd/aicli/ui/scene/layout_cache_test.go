package scene

import "testing"

func TestSplitSourceLinesCacheHitAndInvalidation(t *testing.T) {
	c := &splitSourceLinesCache{entries: make(map[splitSourceKey][]string), max: 4}
	src := "a\nbb\nccc"
	c.put(1, 1, src, []string{"a", "bb", "ccc"})
	if lines, ok := c.get(1, 1, src); !ok || len(lines) != 3 || lines[1] != "bb" {
		t.Fatalf("hit failed: %#v ok=%v", lines, ok)
	}
	// revision 变化 → miss
	if _, ok := c.get(1, 2, src); ok {
		t.Fatalf("expected miss after revision change")
	}
	// 同 ID/Revision 不同内容 → miss（hash 键会误命中，字符串键必须 miss）
	if _, ok := c.get(1, 1, "a\nbb\nzzz"); ok {
		t.Fatalf("expected miss after source change with same id/revision")
	}
}

func TestSplitSourceLinesCacheEviction(t *testing.T) {
	c := &splitSourceLinesCache{entries: make(map[splitSourceKey][]string), max: 2}
	c.put(1, 1, "a", []string{"a"})
	c.put(2, 1, "b", []string{"b"})
	c.put(3, 1, "c", []string{"c"})
	if _, ok := c.get(1, 1, "a"); ok {
		t.Fatalf("expected oldest evicted")
	}
	if _, ok := c.get(3, 1, "c"); !ok {
		t.Fatalf("expected newest present")
	}
}

// TestLayoutSplitSourceLinesCacheRoundTrip 验证缓存路径与直接切分语义一致。
func TestLayoutSplitSourceLinesCacheRoundTrip(t *testing.T) {
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
}

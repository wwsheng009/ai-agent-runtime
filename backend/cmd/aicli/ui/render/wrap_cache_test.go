package render

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// testWrapCases returns a spread of lines exercising the wrap paths that the
// memo must not alter: multi-span styled lines, tabs, wide graphemes,
// over-wide words, prose with spaces, empty text, and long single spans.
func testWrapCases() []Line {
	styleA := Style{Foreground: Color{Kind: ColorRGB, R: 1, G: 2, B: 3}, Bold: true}
	styleB := Style{Background: Color{Kind: ColorANSI, Index: 7}, Italic: true, Role: "code"}
	long := strings.Repeat("word-", 120)
	cjk := strings.Repeat("中文文本块", 60)
	return []Line{
		{Spans: []Span{{Text: strings.Repeat("a", 300), Style: styleA}}},
		{Spans: []Span{{Text: long}}},
		{Spans: []Span{{Text: cjk}}},
		{Spans: []Span{{Text: "\t\t" + long + "\t"}}},
		{Spans: []Span{{Text: "short"}, {Text: strings.Repeat("b", 200), Style: styleB}, {Text: "tail", Link: "https://example.com"}}},
		{Spans: []Span{{Text: "x"}, {Text: strings.Repeat("y", 500), Style: styleA}, {Text: "z"}}},
		{Spans: []Span{{Text: strings.Repeat("超长单词", 80)}}},
		{Spans: []Span{{Text: "hello world " + strings.Repeat("prose sentence ", 40)}}},
		{Spans: []Span{}},
		{Spans: []Span{{Text: strings.Repeat("w", 1000), Style: styleB}}},
	}
}

func TestWrapCacheConsistentWithUncached(t *testing.T) {
	// The memo must never change observable results: a cold (miss) wrap and a
	// warm (hit) wrap of the same line must produce identical output for a
	// variety of lines, widths, and options.
	for _, width := range []int{8, 20, 40, 80} {
		for _, breakWord := range []bool{false, true} {
			opts := WrapOptions{BreakWord: breakWord, TabWidth: 4}
			// Reset the shared cache so the first Wrap is a cold miss.
			wrapMemo = &wrapCacheMemo{byKey: make(map[wrapCacheKey]*wrapCacheEntry), max: 1024}
			for index, line := range testWrapCases() {
				reference := Wrap(line, width, opts)
				second := Wrap(line, width, opts)
				if !linesEqual(second, reference) {
					t.Fatalf("width=%d breakWord=%v case=%d: cached wrap differs from cold wrap", width, breakWord, index)
				}
			}
		}
	}
}

func TestWrapCacheHitAccounting(t *testing.T) {
	wrapMemo = &wrapCacheMemo{byKey: make(map[wrapCacheKey]*wrapCacheEntry), max: 1024}
	line := Line{Spans: []Span{{Text: strings.Repeat("hit-me ", 50)}}}
	opts := WrapOptions{BreakWord: true}
	_ = Wrap(line, 30, opts)
	if wrapMemo.hits != 0 || wrapMemo.miss != 1 {
		t.Fatalf("first wrap: hits=%d miss=%d, want 0/1", wrapMemo.hits, wrapMemo.miss)
	}
	_ = Wrap(line, 30, opts)
	if wrapMemo.hits != 1 || wrapMemo.miss != 1 {
		t.Fatalf("second wrap: hits=%d miss=%d, want 1/1", wrapMemo.hits, wrapMemo.miss)
	}
	// A different width must not hit.
	_ = Wrap(line, 31, opts)
	if wrapMemo.hits != 1 || wrapMemo.miss != 2 {
		t.Fatalf("different width: hits=%d miss=%d, want 1/2", wrapMemo.hits, wrapMemo.miss)
	}
	// Mutating the returned lines must not poison the cache.
	_ = Wrap(line, 30, opts)
	clean := Wrap(line, 30, opts)
	if len(clean) == 0 || len(clean[0].Spans) == 0 || clean[0].Spans[0].Text == "POISONED" {
		t.Fatalf("cache was corrupted by caller mutation")
	}
}

func TestWrapCacheCollisionDegradesToMiss(t *testing.T) {
	wrapMemo = &wrapCacheMemo{byKey: make(map[wrapCacheKey]*wrapCacheEntry), max: 1024}
	lineA := Line{Spans: []Span{{Text: strings.Repeat("a", 200)}}}
	opts := WrapOptions{BreakWord: true}
	refA := Wrap(lineA, 25, opts)
	// Corrupt the stored entry so its expanded content no longer matches
	// lineA: the next Wrap must detect the mismatch and recompute instead of
	// returning the stale wrapped lines.
	for _, entry := range wrapMemo.byKey {
		if len(entry.expanded) > 0 {
			entry.expanded[0].Text = strings.Repeat("c", 200)
		}
	}
	got := Wrap(lineA, 25, opts)
	if !linesEqual(got, refA) {
		t.Fatalf("collision validation failed: wrap returned stale lines")
	}
}

func TestWrapCacheEvictionBounded(t *testing.T) {
	wrapMemo = &wrapCacheMemo{byKey: make(map[wrapCacheKey]*wrapCacheEntry), max: 64}
	opts := WrapOptions{BreakWord: true}
	for i := 0; i < 300; i++ {
		line := Line{Spans: []Span{{Text: strings.Repeat(fmt.Sprintf("row-%03d ", i), 30)}}}
		_ = Wrap(line, 40, opts)
	}
	if len(wrapMemo.byKey) > 64 {
		t.Fatalf("cache exceeded max: %d entries", len(wrapMemo.byKey))
	}
}

func TestWrapCacheMissResultOwnership(t *testing.T) {
	// The miss path returns the freshly computed lines while the cache stores
	// its own clone: mutating the returned lines must not corrupt later hits.
	wrapMemo = &wrapCacheMemo{byKey: make(map[wrapCacheKey]*wrapCacheEntry), max: 1024}
	line := Line{Spans: []Span{{Text: strings.Repeat("owner-", 60)}}}
	opts := WrapOptions{BreakWord: true}
	miss := Wrap(line, 20, opts)
	if len(miss) == 0 || len(miss[0].Spans) == 0 {
		t.Fatalf("miss produced no lines")
	}
	miss[0].Spans[0].Text = "POISONED"
	if len(miss[0].Spans) > 1 {
		miss[0].Spans[1].Style = Style{Bold: true}
	}
	hit := Wrap(line, 20, opts)
	for _, l := range hit {
		for _, span := range l.Spans {
			if span.Text == "POISONED" || span.Style.Bold {
				t.Fatalf("cache was corrupted by mutating the miss result")
			}
		}
	}
}

func TestWrapCacheKeyIsolation(t *testing.T) {
	wrapMemo = &wrapCacheMemo{byKey: make(map[wrapCacheKey]*wrapCacheEntry), max: 1024}
	line := Line{Spans: []Span{{Text: "\t" + strings.Repeat("tabbed ", 40)}}}
	// Same text and width, different tab expansion: must not share entries.
	ref4 := Wrap(line, 30, WrapOptions{BreakWord: true, TabWidth: 4})
	ref8 := Wrap(line, 30, WrapOptions{BreakWord: true, TabWidth: 8})
	if linesEqual(ref4, ref8) {
		t.Fatalf("tab width did not affect wrapping; key isolation unverified")
	}
	if wrapMemo.miss != 2 {
		t.Fatalf("expected two misses for distinct tab widths, got %d", wrapMemo.miss)
	}
	_ = Wrap(line, 30, WrapOptions{BreakWord: true, TabWidth: 4})
	_ = Wrap(line, 30, WrapOptions{BreakWord: true, TabWidth: 8})
	if wrapMemo.hits != 2 {
		t.Fatalf("expected two hits for distinct tab widths, got %d", wrapMemo.hits)
	}
}

func TestWrapCacheConcurrent(t *testing.T) {
	wrapMemo = &wrapCacheMemo{byKey: make(map[wrapCacheKey]*wrapCacheEntry), max: 1024}
	// All goroutines wrap the same lines and mutate the results they get, so
	// -race exercises both the shared map and the miss/hit ownership contract.
	shared := []Line{
		{Spans: []Span{{Text: strings.Repeat("shared-line-a ", 40)}}},
		{Spans: []Span{{Text: strings.Repeat("shared-line-b ", 40), Style: Style{Bold: true}}}},
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			opts := WrapOptions{BreakWord: seed%2 == 0}
			for i := 0; i < 200; i++ {
				line := shared[i%len(shared)]
				got := Wrap(line, 30+seed%20, opts)
				if len(got) == 0 {
					t.Errorf("goroutine %d: empty wrap result", seed)
					return
				}
				got[0].Spans[0].Text = "MUTATED"
			}
		}(g)
	}
	wg.Wait()
}

// linesEqual compares two wrapped outputs field by field.
func linesEqual(a, b []Line) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !lineEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

func lineEqual(a, b Line) bool {
	if a.Style != b.Style || len(a.Spans) != len(b.Spans) {
		return false
	}
	for i := range a.Spans {
		if a.Spans[i] != b.Spans[i] {
			return false
		}
	}
	return true
}

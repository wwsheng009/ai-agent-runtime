package syntax

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
)

// resetHighlightMemo gives every test a pristine cache.
func resetHighlightMemo(t *testing.T) {
	t.Helper()
	highlightMemo = &highlightCacheMemo{
		byKey:      make(map[highlightCacheKey]*highlightCacheEntry),
		maxEntries: 256,
		maxBytes:   8 * 1024 * 1024,
	}
}

func goSample() string {
	return "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\", 42)\n}\n"
}

func TestHighlightCacheConsistentAndHit(t *testing.T) {
	resetHighlightMemo(t)
	h := NewChromaHighlighter()
	req := HighlightRequest{Code: goSample(), Language: "go", Theme: "monokai"}
	first, firstMeta := h.Highlight(req)
	if !firstMeta.Highlighted {
		t.Fatalf("expected highlighted result, got fallback %q", firstMeta.FallbackReason)
	}
	if highlightMemo.hits != 0 || highlightMemo.miss != 1 {
		t.Fatalf("first highlight: hits=%d miss=%d, want 0/1", highlightMemo.hits, highlightMemo.miss)
	}
	second, secondMeta := h.Highlight(req)
	if !highlightLinesEqual(second, first) || secondMeta != firstMeta {
		t.Fatalf("cached highlight differs from cold highlight")
	}
	if highlightMemo.hits != 1 || highlightMemo.miss != 1 {
		t.Fatalf("second highlight: hits=%d miss=%d, want 1/1", highlightMemo.hits, highlightMemo.miss)
	}
}

func TestHighlightCacheKeyIsolation(t *testing.T) {
	resetHighlightMemo(t)
	h := NewChromaHighlighter()
	code := goSample()
	base := HighlightRequest{Code: code, Language: "go", Theme: "monokai"}
	_, _ = h.Highlight(base)
	if highlightMemo.miss != 1 {
		t.Fatalf("expected one miss for base request, got %d", highlightMemo.miss)
	}
	variants := []HighlightRequest{
		{Code: code, Language: "go", Theme: "github"},
		{Code: code, Language: "python", Theme: "monokai"},
		{Code: code, Language: "go", Theme: "monokai", Filename: "main.go"},
	}
	for _, v := range variants {
		_, _ = h.Highlight(v)
	}
	if highlightMemo.miss != uint64(1+len(variants)) {
		t.Fatalf("expected %d misses for isolated keys, got %d", 1+len(variants), highlightMemo.miss)
	}
}

func TestHighlightCacheBudgetIsolation(t *testing.T) {
	resetHighlightMemo(t)
	req := HighlightRequest{Code: goSample(), Language: "go", Theme: "monokai"}
	// Both budgets comfortably highlight the tiny sample, so both results are
	// stored; distinct budgets must still map to distinct cache entries.
	short := &ChromaHighlighter{Budget: 10 * time.Millisecond, DefaultTheme: "auto"}
	long := &ChromaHighlighter{Budget: 300 * time.Millisecond, DefaultTheme: "auto"}
	_, metaShort := short.Highlight(req)
	if !metaShort.Highlighted {
		t.Fatalf("short budget failed to highlight: %+v", metaShort)
	}
	if highlightMemo.miss != 1 {
		t.Fatalf("short-budget first: miss=%d, want 1", highlightMemo.miss)
	}
	_, _ = long.Highlight(req)
	if highlightMemo.miss != 2 {
		t.Fatalf("different budgets must not share entries: miss=%d, want 2", highlightMemo.miss)
	}
	_, _ = short.Highlight(req)
	if highlightMemo.hits != 1 {
		t.Fatalf("same budget must hit: hits=%d, want 1", highlightMemo.hits)
	}
}

func TestHighlightCacheLimitFallbackCached(t *testing.T) {
	resetHighlightMemo(t)
	h := NewChromaHighlighter()
	h.Limits = Limits{MaxBytes: 100, MaxLines: 5}
	big := strings.Repeat("x := 1\n", 30)
	req := HighlightRequest{Code: big, Language: "go", Theme: "monokai"}
	first, firstMeta := h.Highlight(req)
	if firstMeta.FallbackReason != "limit_exceeded" {
		t.Fatalf("expected limit_exceeded fallback, got %q", firstMeta.FallbackReason)
	}
	if highlightMemo.miss != 1 {
		t.Fatalf("limit fallback first: miss=%d, want 1", highlightMemo.miss)
	}
	second, secondMeta := h.Highlight(req)
	if !highlightLinesEqual(second, first) || secondMeta != firstMeta {
		t.Fatalf("cached limit fallback differs from cold")
	}
	if highlightMemo.hits != 1 {
		t.Fatalf("limit fallback second: hits=%d, want 1", highlightMemo.hits)
	}
	// A highlighter with the same budget but different limits must not share
	// the fallback entry.
	unlimited := &ChromaHighlighter{Budget: h.highlightBudget(), DefaultTheme: "auto"}
	_, unlimitedMeta := unlimited.Highlight(req)
	if unlimitedMeta.FallbackReason == "limit_exceeded" || !unlimitedMeta.Highlighted {
		t.Fatalf("unlimited highlighter should highlight, got %+v", unlimitedMeta)
	}
	if highlightMemo.miss != 2 {
		t.Fatalf("limits not isolated in key: miss=%d, want 2", highlightMemo.miss)
	}
}

func TestHighlightCacheBudgetExceededNotCached(t *testing.T) {
	resetHighlightMemo(t)
	// A pathological code block plus a tiny budget forces the budget-exceeded
	// fallback. The degraded result must not be memoized: the next render
	// must retry highlighting instead of pinning plain output forever.
	pathological := "a" + strings.Repeat("b", 400) + "\n" + strings.Repeat("c := 1\n", 200)
	h := &ChromaHighlighter{Budget: 1 * time.Nanosecond, DefaultTheme: "auto"}
	_, meta := h.Highlight(HighlightRequest{Code: pathological, Language: "go", Theme: "monokai"})
	if meta.FallbackReason != "highlight_budget_exceeded" {
		t.Skipf("lexer finished within 1ns budget; cannot exercise budget fallback deterministically (reason=%q)", meta.FallbackReason)
	}
	if len(highlightMemo.byKey) != 0 {
		t.Fatalf("budget-exceeded fallback was cached (%d entries)", len(highlightMemo.byKey))
	}
}

func TestHighlightCacheCollisionDegradesToMiss(t *testing.T) {
	resetHighlightMemo(t)
	h := NewChromaHighlighter()
	req := HighlightRequest{Code: goSample(), Language: "go", Theme: "monokai"}
	ref, _ := h.Highlight(req)
	// Corrupt the stored entry's authoritative code: the next lookup must
	// detect the mismatch and recompute instead of returning stale lines.
	for _, entry := range highlightMemo.byKey {
		entry.code = "corrupted"
	}
	got, _ := h.Highlight(req)
	if !highlightLinesEqual(got, ref) {
		t.Fatalf("collision validation failed: highlight returned stale lines")
	}
}

func TestHighlightCacheLargeBlockSkipped(t *testing.T) {
	resetHighlightMemo(t)
	h := NewChromaHighlighter()
	big := "// filler\n" + strings.Repeat("x := 1\n", 6000) // > 64KB
	req := HighlightRequest{Code: big, Language: "go", Theme: "monokai"}
	_, _ = h.Highlight(req)
	if len(highlightMemo.byKey) != 0 {
		t.Fatalf("oversized block was cached (%d entries)", len(highlightMemo.byKey))
	}
	// A normal block still caches after the oversized one.
	_, _ = h.Highlight(HighlightRequest{Code: goSample(), Language: "go", Theme: "monokai"})
	if len(highlightMemo.byKey) != 1 {
		t.Fatalf("normal block missing after oversized skip: %d entries", len(highlightMemo.byKey))
	}
}

func TestHighlightCacheEvictionBounded(t *testing.T) {
	resetHighlightMemo(t)
	highlightMemo.maxEntries = 32
	highlightMemo.maxBytes = 1 << 20
	h := NewChromaHighlighter()
	for i := 0; i < 120; i++ {
		code := "package p\n\n// block " + strings.Repeat(string(rune('a'+i%26)), 200+i) + "\n"
		_, _ = h.Highlight(HighlightRequest{Code: code, Language: "go", Theme: "monokai"})
	}
	if len(highlightMemo.byKey) > 32 {
		t.Fatalf("entry count exceeded maxEntries: %d", len(highlightMemo.byKey))
	}
	if highlightMemo.bytes > 1<<20 {
		t.Fatalf("byte budget exceeded: %d", highlightMemo.bytes)
	}
}

func TestHighlightCacheResultOwnership(t *testing.T) {
	resetHighlightMemo(t)
	h := NewChromaHighlighter()
	req := HighlightRequest{Code: goSample(), Language: "go", Theme: "monokai"}
	miss, _ := h.Highlight(req)
	if len(miss) == 0 || len(miss[0].Spans) == 0 {
		t.Fatalf("miss produced no lines")
	}
	miss[0].Spans[0].Text = "POISONED"
	hit, _ := h.Highlight(req)
	for _, line := range hit {
		for _, span := range line.Spans {
			if span.Text == "POISONED" {
				t.Fatalf("cache was corrupted by mutating the miss result")
			}
		}
	}
}

func TestHighlightCacheConcurrent(t *testing.T) {
	resetHighlightMemo(t)
	h := NewChromaHighlighter()
	samples := []string{goSample(), "def f(x):\n    return x * 2\n", "SELECT * FROM t WHERE id = 1;\n"}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			lang := []string{"go", "python", "sql"}[seed%3]
			for i := 0; i < 100; i++ {
				code := samples[i%len(samples)]
				lines, meta := h.Highlight(HighlightRequest{Code: code, Language: lang, Theme: "monokai"})
				if len(lines) == 0 {
					t.Errorf("goroutine %d: empty highlight", seed)
					return
				}
				lines[0].Spans[0].Text = "MUTATED"
				_ = meta
			}
		}(g)
	}
	wg.Wait()
}

func highlightLinesEqual(a, b []render.Line) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i].Spans) != len(b[i].Spans) {
			return false
		}
		for j := range a[i].Spans {
			if a[i].Spans[j] != b[i].Spans[j] {
				return false
			}
		}
	}
	return true
}

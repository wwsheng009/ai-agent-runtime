package renderengine

import (
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/markdown"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/syntax"
)

func testTheme() style.ThemeContext {
	return style.BuildThemeContext(style.ThemeSelection{
		PaletteName: style.PaletteFocus,
		SyntaxName:  syntax.GlobalDefaultTheme(),
		Mode:        style.ThemeModeDark,
	}, style.ColorProfile{
		ColorProfile: render.ColorProfile{
			Enabled: true,
			Depth:   render.ColorTrueColor,
			Forced:  true,
		},
		Background: style.BackgroundDark,
	})
}

func bandOptions(width int) markdown.Options {
	return markdown.ActiveBandBodyOptions(width, syntax.GlobalDefaultTheme(), syntax.Default)
}

// TestRenderCacheHitThenMiss 验证：相同 (mode, source, opts) 命中且文档一致；
// width / theme / source / mode 任一变化都导致 miss（内容寻址完整性）。
func TestRenderCacheHitThenMiss(t *testing.T) {
	c := NewRenderCache(16)
	source := "# 标题\n\n```go\nfunc main() {}\n```\n\n- 列表项\n\n| a | b |\n| - | - |\n| 1 | 2 |\n"

	docA, hit := c.Render("band", source, bandOptions(80))
	if hit {
		t.Fatal("first render must miss")
	}
	docB, hit := c.Render("band", source, bandOptions(80))
	if !hit {
		t.Fatal("identical render must hit")
	}
	if !reflect.DeepEqual(docA, docB) {
		t.Fatal("cached document differs from freshly rendered one")
	}

	cases := []struct {
		name string
		doc  func() (render.Document, bool)
	}{
		{"width", func() (render.Document, bool) { return c.Render("band", source, bandOptions(100)) }},
		{"theme", func() (render.Document, bool) {
			opts := bandOptions(80)
			opts.SyntaxTheme = "github"
			return c.Render("band", source, opts)
		}},
		{"source", func() (render.Document, bool) { return c.Render("band", source+"\n", bandOptions(80)) }},
		{"mode", func() (render.Document, bool) {
			opts := markdown.DefaultOptions(80, testTheme())
			opts.SyntaxTheme = syntax.GlobalDefaultTheme()
			return c.Render("assistant", source, opts)
		}},
	}
	for _, tc := range cases {
		if _, hit := tc.doc(); hit {
			t.Fatalf("%s variant must miss", tc.name)
		}
	}

	hits, misses, _ := c.Stats()
	if hits != 1 {
		t.Fatalf("hits = %d, want 1", hits)
	}
	if misses != uint64(1+len(cases)) {
		t.Fatalf("misses = %d, want %d", misses, 1+len(cases))
	}
}

// TestRenderCacheLRUEviction 验证容量上限与 LRU 驱逐。
func TestRenderCacheLRUEviction(t *testing.T) {
	c := NewRenderCache(4)
	for i := 0; i < 8; i++ {
		src := strings.Repeat("# ", 1) + "section" + string(rune('a'+i)) + "\n\nbody\n"
		c.Render("band", src, bandOptions(80))
	}
	if n := c.Len(); n > 4 {
		t.Fatalf("cache grew past capacity: %d entries", n)
	}
	if _, hits, _, _ := statsOf(c); hits != 0 {
		t.Fatalf("expected no hits during fill, got %d", hits)
	}
}

func TestRenderCacheByteBudgetEvictsLeastRecentlyUsedDocument(t *testing.T) {
	sourceA := "# " + strings.Repeat("a", 512) + "\n"
	sourceB := "# " + strings.Repeat("b", 512) + "\n"
	probe := NewRenderCache(4)
	probe.Render("band", sourceA, bandOptions(80))
	entryBytes := probe.Bytes()
	if entryBytes <= 0 {
		t.Fatal("rendered cache entry has no byte estimate")
	}

	cache := NewRenderCacheWithBudget(4, entryBytes*2-1)
	cache.Render("band", sourceA, bandOptions(80))
	cache.Render("band", sourceB, bandOptions(80))
	if got, max := cache.Bytes(), cache.MaxBytes(); got > max {
		t.Fatalf("cache bytes = %d, exceeds budget %d", got, max)
	}
	if got := cache.Len(); got != 1 {
		t.Fatalf("cache entries = %d, want the newest entry only", got)
	}
	if _, hit := cache.Render("band", sourceA, bandOptions(80)); hit {
		t.Fatal("oldest document remained cached after byte-budget eviction")
	}
}

func TestRenderCacheSkipsDocumentLargerThanByteBudget(t *testing.T) {
	source := "# " + strings.Repeat("x", 512) + "\n"
	probe := NewRenderCache(4)
	probe.Render("band", source, bandOptions(80))
	budget := probe.Bytes() - 1
	if budget <= 0 {
		t.Fatal("invalid probe cache budget")
	}

	cache := NewRenderCacheWithBudget(4, budget)
	if _, hit := cache.Render("band", source, bandOptions(80)); hit {
		t.Fatal("first oversized document render must miss")
	}
	if got := cache.Len(); got != 0 {
		t.Fatalf("oversized document was cached: %d entries", got)
	}
	if got := cache.Bytes(); got != 0 {
		t.Fatalf("oversized document consumed cache budget: %d bytes", got)
	}
	if _, hit := cache.Render("band", source, bandOptions(80)); hit {
		t.Fatal("oversized document must not become a cache hit")
	}
}

func statsOf(c *RenderCache) (int, uint64, uint64, uint64) {
	h, m, e := c.Stats()
	return c.Len(), h, m, e
}

// TestRenderCacheSharedInstance 验证共享实例为单例且可用。
func TestRenderCacheSharedInstance(t *testing.T) {
	a := SharedRenderCache()
	b := SharedRenderCache()
	if a != b {
		t.Fatal("SharedRenderCache must return the same instance")
	}
	if a == nil {
		t.Fatal("SharedRenderCache must not be nil")
	}
}

// TestRenderCacheReset 验证 Reset 清空计数与条目。
func TestRenderCacheReset(t *testing.T) {
	c := NewRenderCache(16)
	c.Render("band", "# t\n", bandOptions(80))
	c.Render("band", "# t\n", bandOptions(80))
	c.Reset()
	if n := c.Len(); n != 0 {
		t.Fatalf("Len after Reset = %d, want 0", n)
	}
	if _, hit := c.Render("band", "# t\n", bandOptions(80)); hit {
		t.Fatal("Render after Reset must miss")
	}
}

// TestRenderCacheHitRate 验证命中率计算。
func TestRenderCacheHitRate(t *testing.T) {
	c := NewRenderCache(16)
	if c.HitRate() != 0 {
		t.Fatal("HitRate on empty cache must be 0")
	}
	c.Render("band", "# t\n", bandOptions(80))
	c.Render("band", "# t\n", bandOptions(80))
	c.Render("band", "# other\n", bandOptions(80))
	if got := c.HitRate(); got != 1.0/3.0 {
		t.Fatalf("HitRate = %v, want 1/3", got)
	}
}

func TestRenderCacheConcurrentMissesPublishSafely(t *testing.T) {
	c := NewRenderCache(16)
	source := strings.Repeat("# heading\n\nbody\n", 8)
	const callers = 8
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.Render("band", source, bandOptions(80))
		}()
	}
	wg.Wait()
	if got := c.Len(); got != 1 {
		t.Fatalf("cache length = %d, want 1", got)
	}
	hits, misses, _ := c.Stats()
	if misses != 1 {
		t.Fatalf("cache misses = %d, want 1", misses)
	}
	if hits != callers-1 {
		t.Fatalf("cache hits = %d, want %d", hits, callers-1)
	}
	if total := hits + misses; total != callers {
		t.Fatalf("cache stats total = %d, want %d", total, callers)
	}
}

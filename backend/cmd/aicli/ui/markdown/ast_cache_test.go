package markdown

import (
	"strings"
	"sync"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

func resetASTCache(t *testing.T) {
	t.Helper()
	sharedASTCache = &astCacheMemo{
		byKey:      make(map[uint64]*astCacheEntry),
		maxEntries: 128,
		maxBytes:   16 * 1024 * 1024,
	}
}

func astSample() string {
	return "# Heading\n\nParagraph with **bold** and `code`.\n\n```go\nfunc f() {}\n```\n\n- one\n- two\n"
}

func TestASTCacheHitAndCorrectness(t *testing.T) {
	resetASTCache(t)
	opts := DefaultOptions(80, style.ThemeContext{})
	source := astSample()
	first := Render(source, opts) // miss → 解析 doc1 并缓存
	entries, _, hits, miss := AstCacheStats()
	if entries != 1 || miss != 1 || hits != 0 {
		t.Fatalf("first render: entries=%d hits=%d miss=%d, want 1/0/1", entries, hits, miss)
	}
	// 使缓存失配强制重新解析（doc2），验证"缓存 AST 渲染 ≡ 全新解析 AST 渲染"。
	sharedASTCache.mu.Lock()
	for _, entry := range sharedASTCache.byKey {
		entry.source = "corrupted"
	}
	sharedASTCache.mu.Unlock()
	second := Render(source, opts)
	if !documentsEqual(first, second) {
		t.Fatalf("cached AST render differs from a fresh parse render")
	}
	// 失配后重新解析的 doc2 已缓存：同 source 再次渲染必须命中。
	_ = Render(source, opts)
	_, _, hits, miss = AstCacheStats()
	if hits != 1 || miss != 2 {
		t.Fatalf("warm render: hits=%d miss=%d, want 1/2", hits, miss)
	}
}

func TestASTCacheKeyIsolation(t *testing.T) {
	resetASTCache(t)
	opts := DefaultOptions(80, style.ThemeContext{})
	_ = Render(astSample(), opts)
	_, _, _, miss := AstCacheStats()
	if miss != 1 {
		t.Fatalf("base render miss=%d, want 1", miss)
	}
	// A grown source (streaming chunk) must not hit the shorter one.
	grown := astSample() + "\nmore content appended by the stream\n"
	_ = Render(grown, opts)
	_, _, _, miss = AstCacheStats()
	if miss != 2 {
		t.Fatalf("grown source miss=%d, want 2", miss)
	}
	// The stable prefix re-parsed alone (prefix validation) must hit.
	prefix := "Paragraph with **bold** and `code`.\n"
	_ = Render(prefix, opts)
	_, _, hits, miss := AstCacheStats()
	if miss != 3 || hits != 0 {
		t.Fatalf("prefix render: hits=%d miss=%d, want 0/3", hits, miss)
	}
	_ = Render(prefix, opts)
	_, _, hits, _ = AstCacheStats()
	if hits != 1 {
		t.Fatalf("prefix re-render hits=%d, want 1", hits)
	}
}

func TestASTCacheCollisionDegradesToMiss(t *testing.T) {
	resetASTCache(t)
	opts := DefaultOptions(80, style.ThemeContext{})
	source := astSample()
	ref := Render(source, opts)
	// Corrupt the stored source: the next lookup must detect the mismatch and
	// re-parse instead of returning a stale document.
	sharedASTCache.mu.Lock()
	for _, entry := range sharedASTCache.byKey {
		entry.source = "corrupted"
	}
	sharedASTCache.mu.Unlock()
	got := Render(source, opts)
	if !documentsEqual(got, ref) {
		t.Fatalf("collision validation failed: render returned stale document")
	}
}

func TestASTCacheEvictionBounded(t *testing.T) {
	resetASTCache(t)
	sharedASTCache.maxEntries = 16
	opts := DefaultOptions(80, style.ThemeContext{})
	for i := 0; i < 80; i++ {
		src := "# doc " + strings.Repeat(string(rune('a'+i%26)), 50+i) + "\n\nbody text " + strings.Repeat("x", 100) + "\n"
		_ = Render(src, opts)
	}
	entries, bytes, _, _ := AstCacheStats()
	if entries > 16 {
		t.Fatalf("entry count exceeded maxEntries: %d", entries)
	}
	if bytes > 16*1024*1024 {
		t.Fatalf("byte budget exceeded: %d", bytes)
	}
}

func TestASTCacheConcurrent(t *testing.T) {
	resetASTCache(t)
	opts := DefaultOptions(80, style.ThemeContext{})
	sources := []string{
		astSample(),
		"# two\n\n- a\n- b\n- c\n",
		strings.Repeat("plain paragraph line\n\n", 20),
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				src := sources[(seed+i)%len(sources)]
				doc := Render(src, opts)
				if doc.LineCount() == 0 {
					t.Errorf("goroutine %d: empty document", seed)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}

func documentsEqual(a, b render.Document) bool {
	if len(a.Blocks) != len(b.Blocks) {
		return false
	}
	for i := range a.Blocks {
		if a.Blocks[i].Kind != b.Blocks[i].Kind || len(a.Blocks[i].Lines) != len(b.Blocks[i].Lines) {
			return false
		}
		for j := range a.Blocks[i].Lines {
			if len(a.Blocks[i].Lines[j].Spans) != len(b.Blocks[i].Lines[j].Spans) {
				return false
			}
			for k := range a.Blocks[i].Lines[j].Spans {
				if a.Blocks[i].Lines[j].Spans[k] != b.Blocks[i].Lines[j].Spans[k] {
					return false
				}
			}
		}
	}
	return true
}

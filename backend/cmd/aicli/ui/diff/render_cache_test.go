package diff

import (
	"fmt"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/syntax"
)

const benchmarkDiffSource = `--- a/example.go
+++ b/example.go
@@ -1,4 +1,4 @@
 package main
` + " \n" + `-func oldName() string {
-	return "old"
+func newName() string {
+	return "new"
 }
`

func benchLargeDiffSource() string {
	var builder strings.Builder
	builder.WriteString("--- a/large.go\n+++ b/large.go\n@@ -1,200 +1,200 @@\n")
	for index := 0; index < 200; index++ {
		fmt.Fprintf(&builder, "-old line %d\n+new line %d\n", index, index)
	}
	return builder.String()
}

func TestRenderTextCacheReusesAndIsolatesDocuments(t *testing.T) {
	sharedRenderTextCache.Reset()
	t.Cleanup(sharedRenderTextCache.Reset)

	opts := DefaultRenderOptions(80, plainThemeContext())
	first := RenderText(benchmarkDiffSource, opts)
	second := RenderText(benchmarkDiffSource, opts)
	firstText := (render.PlainBackend{}).Render(first)
	if secondText := (render.PlainBackend{}).Render(second); secondText != firstText {
		t.Fatalf("cached render changed plain output:\nfirst=%q\nsecond=%q", firstText, secondText)
	}
	if got := sharedRenderTextCache.Len(); got != 1 {
		t.Fatalf("render cache entries = %d, want 1", got)
	}

	// The caller owns the returned document. Mutating it must not corrupt the
	// cached IR used by later hits.
	mutated := false
	for blockIndex := range first.Blocks {
		for lineIndex := range first.Blocks[blockIndex].Lines {
			spans := first.Blocks[blockIndex].Lines[lineIndex].Spans
			for spanIndex := range spans {
				if spans[spanIndex].Text != "" {
					spans[spanIndex].Text = "corrupted"
					mutated = true
					break
				}
			}
			if mutated {
				break
			}
		}
		if mutated {
			break
		}
	}
	if !mutated {
		t.Fatal("fixture did not produce a render span to mutate")
	}
	third := RenderText(benchmarkDiffSource, opts)
	if thirdText := (render.PlainBackend{}).Render(third); thirdText != firstText {
		t.Fatalf("cached document was mutated by caller:\nwant=%q\ngot=%q", firstText, thirdText)
	}
}

func TestRenderTextCacheKeysIncludeRenderOptions(t *testing.T) {
	sharedRenderTextCache.Reset()
	t.Cleanup(sharedRenderTextCache.Reset)

	theme := plainThemeContext()
	wide := DefaultRenderOptions(100, theme)
	narrow := DefaultRenderOptions(40, theme)
	_ = RenderText(benchmarkDiffSource, wide)
	_ = RenderText(benchmarkDiffSource, narrow)
	if got := sharedRenderTextCache.Len(); got != 2 {
		t.Fatalf("render cache entries = %d, want 2 for different widths", got)
	}

	custom := wide
	custom.Highlighter = newTestOnlyHighlighter()
	_ = RenderText(benchmarkDiffSource, custom)
	if got := sharedRenderTextCache.Len(); got != 2 {
		t.Fatalf("custom highlighter path must bypass cache, entries = %d", got)
	}
}

type testOnlyHighlighter struct{}

func newTestOnlyHighlighter() *testOnlyHighlighter { return &testOnlyHighlighter{} }

func (h *testOnlyHighlighter) Highlight(request syntax.HighlightRequest) ([]render.Line, syntax.HighlightMeta) {
	return []render.Line{{Spans: []render.Span{{Text: request.Code}}}}, syntax.HighlightMeta{}
}

func BenchmarkRenderTextUncached(b *testing.B) {
	source := benchLargeDiffSource()
	opts := DefaultRenderOptions(100, plainThemeContext())
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = renderTextUncached(source, opts)
	}
}

func BenchmarkRenderTextCached(b *testing.B) {
	source := benchLargeDiffSource()
	opts := DefaultRenderOptions(100, plainThemeContext())
	sharedRenderTextCache.Reset()
	b.Cleanup(sharedRenderTextCache.Reset)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = RenderText(source, opts)
	}
}

package renderengine

import (
	"fmt"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/markdown"
)

// benchmarkSource 模拟典型 assistant 流式回复：标题 + 代码块 + 列表 + 表格。
var benchmarkSource = fmt.Sprintf(`# 阶段 D 缓存基准

%s

- 命中路径只做 hash + 查找
- 未命中才走 goldmark 解析

| 项目 | 说明 |
| ---- | ---- |
| key  | hash+width+theme+mode |
`, "```go\nfunc Render(mode string, src string, opts Options) (Document, bool) {\n\tkey := docKey(mode, src, opts)\n\tif cd, ok := cache[key]; ok && cd.source == src {\n\t\treturn cd.doc, true\n\t}\n\tdoc := markdown.Render(src, opts)\n\tcache[key] = cachedDoc{source: src, doc: doc}\n\treturn doc, false\n}\n```")

// BenchmarkRenderCacheHit 衡量完全命中路径：相同 (mode, source, opts)
// 反复渲染。goldmark 解析被完全跳过，耗时应接近纯 map 查找。
func BenchmarkRenderCacheHit(b *testing.B) {
	c := NewRenderCache(64)
	opts := bandOptions(80)
	c.Render("band", benchmarkSource, opts) // warm up

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, hit := c.Render("band", benchmarkSource, opts); !hit {
			b.Fatal("expected cache hit")
		}
	}
}

// BenchmarkRenderCacheMiss 衡量未命中路径（每次不同源码），作为对照基线。
func BenchmarkRenderCacheMiss(b *testing.B) {
	c := NewRenderCache(64)
	opts := bandOptions(80)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		c.Render("band", fmt.Sprintf("# %d\n\nbody\n", i), opts)
	}
}

// BenchmarkMarkdownRenderRaw 是 goldmark 直接渲染基线，用于量化缓存收益。
func BenchmarkMarkdownRenderRaw(b *testing.B) {
	opts := bandOptions(80)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		markdown.Render(benchmarkSource, opts)
	}
}

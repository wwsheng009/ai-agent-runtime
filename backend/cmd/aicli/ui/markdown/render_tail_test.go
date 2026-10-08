package markdown

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/syntax"
)

// renderTailCorpus 覆盖常见块构造的完整文档。刻意包含跨块交互用例：引用式
// 链接的定义出现在使用之后、setext 标题、嵌套列表、开/闭代码栅栏、表格、
// 块引用、HTML 块、CJK、CRLF 与未闭合栅栏。
var renderTailCorpus = []string{
	"# 标题\n\n段落一。\n\n- 列表项一\n- 列表项二\n\n```go\nfunc main() {}\n```\n\n结尾段落。\n",
	"普通段落。\n\n> 引用行\n> 第二行\n\n---\n\n`code` 与 **强调**。\n",
	"| 列一 | 列二 |\n|---|---|\n| a | b |\n| c | d |\n",
	"段落标题\n========\n\n正文。\n",
	"[使用][ref] 出现在定义之前。\n\n[ref]: https://example.com\n",
	"1. 有序一\n2. 有序二\n   - 嵌套\n\n结束。\n",
	"<!-- 注释块 -->\n\n正文。\n",
	"第一行\n\n\n\n最后一行",
	"",
	"   ",
	"```\n未闭合栅栏\n",
	"a\r\nb\r\n\r\nc\r\n",
	"中文段落一。\n中文段落二，含行内 `代码`。\n",
}

func documentLines(doc render.Document) []render.Line {
	var lines []render.Line
	for _, block := range doc.Blocks {
		lines = append(lines, block.Lines...)
	}
	return lines
}

// assertTailSuffix 校验 tail 是全量渲染的精确行后缀，且覆盖预算（或整篇）。
func assertTailSuffix(t *testing.T, label string, tail, full []render.Line, minLines int) {
	t.Helper()
	if len(tail) > len(full) {
		t.Fatalf("%s: tail lines %d > full lines %d", label, len(tail), len(full))
	}
	if !render.LinesEqual(tail, full[len(full)-len(tail):]) {
		t.Fatalf("%s: tail is not an exact suffix of the full render", label)
	}
	if minLines > 0 && len(tail) < minLines && len(tail) != len(full) {
		t.Fatalf("%s: tail covers %d lines, want >= %d or the whole document (%d)",
			label, len(tail), minLines, len(full))
	}
}

func TestRenderTailMatchesFullRenderSuffix(t *testing.T) {
	opts := DefaultOptions(80, style.ThemeContext{})
	for caseIndex, source := range renderTailCorpus {
		full := documentLines(Render(source, opts))
		for _, minLines := range []int{1, 2, 4, 8, 16, 64, 1000} {
			tail := documentLines(RenderTail(source, opts, minLines))
			assertTailSuffix(t, "case-"+string(rune('a'+caseIndex)), tail, full, minLines)
		}
	}
}

func TestRenderTailNonPositiveBudgetRendersWholeDocument(t *testing.T) {
	opts := DefaultOptions(80, style.ThemeContext{})
	for caseIndex, source := range renderTailCorpus {
		full := documentLines(Render(source, opts))
		tail := documentLines(RenderTail(source, opts, 0))
		if !render.LinesEqual(tail, full) {
			t.Fatalf("case %d: minLines=0 must render the complete document", caseIndex)
		}
	}
}

// TestRenderTailStreamingAppendEquivalence 逐步追加构造（含跨块拆分的栅栏、
// 表格、setext 标题与引用式定义），每一步都要求尾部窗口与全量渲染的尾部
// 逐行一致——这是窗口边界不引入块上下文漂移的核心判据。
func TestRenderTailStreamingAppendEquivalence(t *testing.T) {
	opts := DefaultOptions(72, style.ThemeContext{})
	chunks := []string{
		"## 流式小节\n\n", "第一段文字。\n\n",
		"- 列表项一\n", "- 列表项二\n\n",
		"```go\n", "func f() error {\n", "\treturn nil\n", "}\n", "```\n\n",
		"| 列一 | 列二 |\n", "|---|---|\n", "| a | b |\n",
		"\n> 引用\n> 续行\n\n",
		"段落标题\n", "===\n\n",
		"[链接][ref] 与定义。\n\n[ref]: https://example.com\n\n",
		"结尾。\n",
	}
	var source strings.Builder
	for step, chunk := range chunks {
		source.WriteString(chunk)
		full := documentLines(Render(source.String(), opts))
		for _, minLines := range []int{1, 4, 12} {
			tail := documentLines(RenderTail(source.String(), opts, minLines))
			assertTailSuffix(t, "stream-step-"+string(rune('a'+step)), tail, full, minLines)
		}
	}
}

// countingHighlighter 记录 Highlight 调用次数：用于钉住「尾部窗口不渲染其
// 之前的块」这一性能契约（栅栏在文档头部、尾部是纯段落时零调用）。
type countingHighlighter struct {
	calls int
}

func (c *countingHighlighter) Highlight(req syntax.HighlightRequest) ([]render.Line, syntax.HighlightMeta) {
	c.calls++
	return nil, syntax.HighlightMeta{}
}

func TestRenderTailSkipsLeadingBlockRendering(t *testing.T) {
	opts := DefaultOptions(80, style.ThemeContext{})
	opts.Highlighter = &countingHighlighter{}

	source := "```go\nfunc main() {}\n```\n\n" + strings.Repeat("尾部的纯段落行，不含任何高亮需求。\n\n", 6)

	fullHighlighter := &countingHighlighter{}
	fullOpts := opts
	fullOpts.Highlighter = fullHighlighter
	_ = Render(source, fullOpts)
	if fullHighlighter.calls == 0 {
		t.Fatalf("full render did not reach the fenced block highlighter")
	}

	tailHighlighter := &countingHighlighter{}
	tailOpts := opts
	tailOpts.Highlighter = tailHighlighter
	tail := documentLines(RenderTail(source, tailOpts, 4))
	if tailHighlighter.calls != 0 {
		t.Fatalf("tail render touched the leading fenced block: %d highlighter calls", tailHighlighter.calls)
	}
	if len(tail) < 4 {
		t.Fatalf("tail lines = %d, want >= 4", len(tail))
	}
}

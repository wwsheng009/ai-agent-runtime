package markdown

import (
	"strings"
	"testing"
)

// streamIncrementalCorpus 是增量 Push 与全量 SetContent 必须逐步等价的推流
// 语料：开/闭/不匹配栅栏、跨块表格、列表、setext、CRLF、短尾与结构引导行。
var streamIncrementalCorpus = [][]string{
	{"intro\n", "```go\nfunc() {\n", "}\n```\n", "after\n"},
	{"before\n````go\ncode\n", "```\n", "````\n", "tail\n"},
	{"| 列一 | 列二 |\n", "|---|---|\n", "| a | b |\n", "\n", "after\n"},
	{"# 标题\n", "段落一。\n\n", "- 列表\n", "- 第二项\n\n", "结尾\n"},
	{"部分", "文字", "继续", "。\n", "\n", "新段落\n"},
	{"a\r\n", "b\r\n", "\r\n", "c\r\n"},
	{"```\n未闭合\n", "still inside\n"},
	{"| 半行", "| 表格 |\n", "|---|---|\n"},
	{"- \n", " 内容\n", "\n"},
	{"## ", "标题\n", "\n"},
	{"引用\n> ````go\n> code\n", "> ```\n", "> ````\n", "尾\n"},
}

// 增量稳定切分（recompute 只扫描上一稳定点之后的尾部）必须与「每步全量重建
// 收集器」的结果逐步一致。
func TestStreamCollectorIncrementalMatchesFullRecompute(t *testing.T) {
	for caseIndex, pushes := range streamIncrementalCorpus {
		var incremental StreamCollector
		var full strings.Builder
		for step, delta := range pushes {
			full.WriteString(delta)
			_ = incremental.Push(delta)
			var reference StreamCollector
			_ = reference.SetContent(full.String())
			if incremental.Stable() != reference.Stable() ||
				incremental.Holdback() != reference.Holdback() ||
				incremental.Raw() != reference.Raw() {
				t.Fatalf("case %d step %d: incremental stable=%q holdback=%q, want stable=%q holdback=%q",
					caseIndex, step, incremental.Stable(), incremental.Holdback(),
					reference.Stable(), reference.Holdback())
			}
		}
	}
}

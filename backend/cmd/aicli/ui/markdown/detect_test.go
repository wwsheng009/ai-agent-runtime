package markdown

import (
	"math/rand"
	"regexp"
	"strings"
	"testing"
)

// 旧实现的参考副本（regexp 版）：仅用于等价性对照，不得在生产路径使用。
var (
	refListPrefix    = regexp.MustCompile(`^\s*[-*]\s+`)
	refOrderedPrefix = regexp.MustCompile(`^\s*\d+\.\s+`)
	refLinkPattern   = regexp.MustCompile(`\[.*?\]\(.*?\)`)
)

func refLooksLikeMarkdown(source string) bool {
	if source == "" {
		return false
	}
	if strings.Contains(source, "```") || strings.Contains(source, "**") ||
		strings.Count(source, "`") >= 2 || refLinkPattern.MatchString(source) {
		return true
	}
	for _, line := range strings.Split(source, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if refListPrefix.MatchString(line) || refOrderedPrefix.MatchString(line) ||
			strings.HasPrefix(trimmed, "# ") || strings.HasPrefix(trimmed, "## ") ||
			strings.HasPrefix(trimmed, "### ") || strings.HasPrefix(trimmed, "> ") ||
			(strings.HasPrefix(strings.TrimSpace(line), "|") && strings.HasSuffix(strings.TrimSpace(line), "|")) {
			return true
		}
	}
	return false
}

// TestLooksLikeMarkdownMatchesRegexpReference 把无 regexp 实现与旧正则实现
// 逐例对照。该函数在流式每帧路径上被调用两次，语义等价是接线的硬前提。
func TestLooksLikeMarkdownMatchesRegexpReference(t *testing.T) {
	corpus := []string{
		"",
		"普通散文，没有标记。",
		"多行普通文本。\n第二行也没有标记。\n第三行。",
		"```go\nfmt.Println()\n```",
		"``",
		"`code`",
		"`a` 和 `b`",
		"**bold**",
		"**",
		"*",
		"- 项目",
		"  - 缩进项目",
		"\t- 制表符项目",
		"\r- 回车前缀项目",
		"-x 不是列表",
		"-",
		"* ",
		"*  ",
		"1. 有序",
		"  12. 缩进有序",
		"\t7. 制表符有序",
		"1.x 不是有序列表",
		"1. ",
		"١. 阿拉伯数字不是 \\d",
		"# 标题",
		" # 缩进标题",
		"## 二级",
		"### 三级",
		"#### 四级不是",
		"#标题无空格不是",
		"> 引用",
		"| a | b |",
		"|a|",
		"a | b",
		"[x](y)",
		"[x] (y) 空格不是链接",
		"[]( )",
		"[a]b](c)",
		"[x](y",
		"x](y)",
		"[",
		"](",
		"[a](b))",
		"[a]((b))",
		"前置 [a](b) 后置",
		"第一行\n[a](b)\n第三行",
		"第一行\r\n第二行",
		"[中](文)",
		"中文 [链 接](目 标) 混合",
		"[[嵌套]](目标)",
		"[未闭合\n[](x)",
		"换行前的 [\n](x) 跨行不成链接",
		"竖线 | 在中间",
		"| 以竖线结尾 |",
		"| 开头但结尾不是",
		"开头不是但 |",
		"1.有序\n2. 有序",
		"1..2 不是",
		"9. ",
		"0. ",
		"   ",
		"\t",
		"\n",
		"\n\n",
		"a\n\nb",
		"# ",
		"> ",
		"| |",
	}
	for _, source := range corpus {
		want := refLooksLikeMarkdown(source)
		got := LooksLikeMarkdown(source)
		if got != want {
			t.Errorf("LooksLikeMarkdown(%q) = %v, regexp 参考实现 = %v", source, got, want)
		}
	}
}

// TestLooksLikeMarkdownKeyCases 直接钉住若干代表性真值，防止两份实现同错。
func TestLooksLikeMarkdownKeyCases(t *testing.T) {
	cases := []struct {
		source string
		want   bool
	}{
		{"普通中文流式输出。", false},
		{"多行\n普通文本", false},
		{"- 列表项", true},
		{"1. 有序项", true},
		{"# 标题", true},
		{"| a | b |", true},
		{"看 [文档](https://example.com)", true},
		{"[未闭合链接", false},
		{"代码 `x` 与 `y`", true},
		{"```", true},
	}
	for _, tc := range cases {
		if got := LooksLikeMarkdown(tc.source); got != tc.want {
			t.Errorf("LooksLikeMarkdown(%q) = %v, want %v", tc.source, got, tc.want)
		}
	}
}

// TestLooksLikeMarkdownRandomizedEquivalence 固定种子的随机串对照，覆盖正则
// 边界组合（\s 含 \f/\r、\d 边界、贪婪/非贪婪、跨行 '.' 限制），防止手写
// 扫描与旧正则语义漂移。
func TestLooksLikeMarkdownRandomizedEquivalence(t *testing.T) {
	alphabet := []byte("[]()*-  \t#>`|1a0.\r\n\f")
	rng := rand.New(rand.NewSource(20261008))
	for index := 0; index < 20000; index++ {
		length := rng.Intn(28)
		buffer := make([]byte, length)
		for position := range buffer {
			buffer[position] = alphabet[rng.Intn(len(alphabet))]
		}
		source := string(buffer)
		if got, want := LooksLikeMarkdown(source), refLooksLikeMarkdown(source); got != want {
			t.Fatalf("case %d: LooksLikeMarkdown(%q) = %v, regexp = %v", index, source, got, want)
		}
	}
}

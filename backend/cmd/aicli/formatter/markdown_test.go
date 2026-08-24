package formatter

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

func TestIsMarkdown(t *testing.T) {
	formatter := NewMarkdownFormatter(true)

	tests := []struct {
		name     string
		text     string
		expected bool
	}{
		{"代码块", "```go\nfmt.Println(\"hello\")\n```", true},
		{"内联代码", "这是 `代码` 示例", true},
		{"标题", "# 标题一\n## 标题二", true},
		{"粗体", "这是 **粗体** 文字", true},
		{"列表", "- 项目1\n- 项目2", true},
		{"有序列表", "1. 项目1\n2. 项目2", true},
		{"引用", "> 这是引用\n> 第二行", true},
		{"缩进引用", "  > 这是引用\n  > 第二行", true},
		{"链接", "[链接](https://example.com)", true},
		{"缩进表格", "  | 列名1 | 列名2 |\n  | --- | --- |\n  | A | B |", true},
		{"普通文本", "这是普通文本，不是 Markdown", false},
		{"混合", "普通文本\n```go\n代码\n```", true},
		{"裸星号串", "breaks****Inspecting Markdown\ndetection logic", false},
		{"未闭合粗体", "这种 **粗体没有闭合", false},
		{"三连星非成对", "*** 星号装饰 ***", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatter.IsMarkdown(tt.text)
			if result != tt.expected {
				t.Errorf("IsMarkdown() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestFormatUsesInjectedANSI16ThemeContext(t *testing.T) {
	f := NewMarkdownFormatter(true)
	f.ThemeContextProvider = func() style.ThemeContext {
		return style.BuildThemeContext(style.ThemeSelection{
			PaletteName: style.PaletteFocus,
			SyntaxName:  "monokai",
			Mode:        style.ThemeModeDark,
		}, style.ColorProfile{ColorProfile: render.ColorProfile{
			Enabled: true,
			Depth:   render.ColorANSI16,
			Forced:  true,
		}})
	}
	got := f.Format("# Title\n\n```go\nconst n = 1\n```")
	if !strings.Contains(got, "\x1b[") {
		t.Fatalf("expected ANSI output, got %q", got)
	}
	if strings.Contains(got, "38;5;") || strings.Contains(got, "38;2;") {
		t.Fatalf("injected ANSI-16 profile leaked high-depth SGR: %q", got)
	}
}

func TestFormatCodeBlock(t *testing.T) {
	formatter := NewMarkdownFormatter(true)
	formatter.Width = 80

	code := "```go\nfunc main() {\n    fmt.Println(\"Hello\")\n}\n```"
	result := formatter.Format(code)
	plain := render.ANSIToPlain(result)

	// 检查代码块是否被正确处理（不包含边框）
	if plain == "" {
		t.Errorf("代码块格式化结果不应为空")
	}

	// Token highlighting inserts SGR between tokens; assert on plain projection.
	if !strings.Contains(plain, "func") || !strings.Contains(plain, "main") {
		t.Errorf("代码块应该包含原始代码内容, plain=%q", plain)
	}
	if !strings.Contains(result, "\x1b[") {
		t.Errorf("彩色模式下代码块应包含 ANSI 高亮序列")
	}

	// 检查不包含边框字符
	borderChars := []string{"┌", "┐", "└", "┘", "│", "─"}
	for _, char := range borderChars {
		if strings.Contains(plain, char) {
			t.Errorf("代码块不应该包含边框字符: %s", char)
		}
	}
}

func TestFormatCodeBlock_IndentedClosingFence(t *testing.T) {
	formatter := NewMarkdownFormatter(false)
	formatter.Width = 80

	input := "```go\nfmt.Println(\"hello\")\n ```\nafter"
	result := formatter.Format(input)

	if strings.Contains(result, "```") {
		t.Errorf("代码块格式化结果不应包含 ``` 结束符, got %q", result)
	}
	if !strings.Contains(result, "fmt.Println") || !strings.Contains(result, "hello") {
		t.Errorf("代码块应该包含原始代码内容, got %q", result)
	}
	if !strings.Contains(result, "after") {
		t.Errorf("代码块结束后内容应该保留, got %q", result)
	}
}

func TestFormatHeading(t *testing.T) {
	formatter := NewMarkdownFormatter(true)
	formatter.Width = 80

	tests := []struct {
		name  string
		text  string
		level int
		check string
	}{
		{"一级标题", "标题1", 1, "▶"},
		{"二级标题", "标题2", 2, "▷"},
		{"三级标题", "标题3", 3, "◉"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := strings.Repeat("#", tt.level) + " " + tt.text
			doc := formatter.FormatDocument(input)
			if plain := doc.PlainText(); !strings.Contains(plain, tt.check) || !strings.Contains(plain, tt.text) {
				t.Fatalf("标题结构化渲染缺少前缀或文本: %q", plain)
			}
			foundAccent := false
			for _, span := range doc.Blocks[0].Lines[0].Spans {
				if span.Style.Role == string(style.RoleAccent) && span.Style.Bold {
					foundAccent = true
				}
			}
			if !foundAccent {
				t.Fatalf("标题缺少 accent/bold 语义 span: %+v", doc)
			}
		})
	}
}

func TestFormatList(t *testing.T) {
	formatter := NewMarkdownFormatter(true)

	text := "- 项目1\n  - 子项目1\n- 项目2"
	result := formatter.Format(text)

	// 检查列表是否被处理
	if result == "" {
		t.Errorf("列表格式化结果不应为空")
	}
}

func TestGetPlain(t *testing.T) {
	formatter := NewMarkdownFormatter(true)

	tests := []struct {
		name     string
		markdown string
		expected string
	}{
		{"代码块", "```go\ncode\n```", " [代码块] "},
		{"内联代码", "`code`", "code"},
		{"粗体", "**bold**", "bold"},
		{"标题", "# 标题", "标题"},
		{"链接", "[text](url)", "text"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatter.GetPlain(tt.markdown)
			if result != tt.expected {
				t.Errorf("GetPlain() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestFormatMixedContent(t *testing.T) {
	formatter := NewMarkdownFormatter(true)

	mixedText := `# 示例文档

这是一段普通文本，包含 **粗体** 和 ` + "`" + `内联代码` + "`" + `。

## 代码示例

` + "```javascript" + `
function hello() {
    console.log("Hello World");
}
` + "```" + `

- 列表项 1
- 列表项 2

> 这是引用内容`

	result := formatter.Format(mixedText)

	if result == "" {
		t.Errorf("混合内容格式化结果不应为空")
	}

	// 检查是否保持了原始文本的结构
	if len(result) == 0 {
		t.Errorf("格式化结果长度应该 > 0")
	}
}

// TestFormatTable 测试表格格式化
func TestFormatTable(t *testing.T) {
	formatter := NewMarkdownFormatter(true)

	tableMD := `| 域名 | Fake-IP |
|------|---------|
| edge.microsoft.com | 198.18.0.15 |
| api.ipapi.is | 198.18.0.18 |`

	result := formatter.Format(tableMD)

	if result == "" {
		t.Errorf("表格格式化结果不应为空")
	}

	// 检查是否包含域名
	if !strings.Contains(result, "edge.microsoft.com") {
		t.Errorf("表格应该包含域名")
	}

	if !strings.Contains(result, "api.ipapi.is") {
		t.Errorf("表格应该包含域名")
	}
}

// TestFormatTableWithInlineMarkdown 测试包含 inline markdown 的表格
func TestFormatTableWithInlineMarkdown(t *testing.T) {
	formatter := NewMarkdownFormatter(true)

	tableMD := `| 名称 | 提供商 | 状态 |
|------|--------|------|
| ` + "`" + `OpenAI` + "`" + ` | Anthropic | ✅ |
| **Claude** | Anthropic | 🔴 |`

	result := formatter.Format(tableMD)

	if result == "" {
		t.Errorf("表格格式化结果不应为空")
	}

	// 检查是否包含内容
	if !strings.Contains(result, "OpenAI") {
		t.Errorf("表格应该包含 Inline 代码")
	}

	if !strings.Contains(result, "Claude") {
		t.Errorf("表格应该包含粗体文本")
	}
}

// TestFormatTableComplex 测试复杂表格格式化
func TestFormatTableComplex(t *testing.T) {
	formatter := NewMarkdownFormatter(true)

	tableMD := `| 功能 | 说明 | 示例 |
|------|------|------|
| DNS 污染防护 | 使用 Fake-IP 避免 | ` + "`" + `198.18.0.1` + "`" + ` |
| 域名分流 | 基于规则匹配 | API 请求走代理 |
| 性能优化 | 缓存解析结果 | 加快连接速度 |
| 隐私保护 | 不泄露真实 DNS | 防止追踪 |`

	result := formatter.Format(tableMD)

	if result == "" {
		t.Errorf("复杂表格格式化结果不应为空")
	}

	// 检查是否包含所有内容
	expectedItems := []string{"DNS 污染防护", "域名分流", "性能优化", "隐私保护"}
	for _, item := range expectedItems {
		if !strings.Contains(result, item) {
			t.Errorf("表格应该包含: %s", item)
		}
	}
}

// TestFormatTableFromUserExample 测试用户提供的实际例子
func TestFormatTableFromUserExample(t *testing.T) {
	formatter := NewMarkdownFormatter(false) // 无颜色模式

	tableMD := `| 域名 | Fake-IP |
|------|---------|
| edge.microsoft.com | 198.18.0.15 |
| api.ipapi.is | 198.18.0.18 |
| ipwho.is | 198.18.0.20 |`

	result := formatter.Format(tableMD)

	if result == "" {
		t.Errorf("表格格式化结果不应为空")
	}

	// 检查表格行数（应该包含分隔线）
	lines := strings.Split(result, "\n")
	if len(lines) < 3 {
		t.Errorf("表格至少应该有3行（表头+分隔+数据），实际: %d", len(lines))
	}

	// 检查不包含原始 markdown 表格语法
	if strings.Contains(result, "|--|") || strings.Contains(result, "|---|") {
		t.Errorf("表格不应该包含原始分隔行语法")
	}
}

func TestFormatTable_RepairsBrokenUserModelRowWrap(t *testing.T) {
	formatter := NewMarkdownFormatter(false)

	tableMD := `下面再给你一个 **Markdown 表格** 示例格式：

| 字段 | 类型 | 说明 |
| ---- | ---- | ---- |
| id | int | 主键 |
| name |
 varchar | 姓名 |
| created_at | datetime | 创建时间 |`

	result := formatter.Format(tableMD)

	if !strings.Contains(result, "Markdown 表格") {
		t.Fatalf("expected inline markdown to be formatted, got %q", result)
	}
	if !strings.Contains(result, "字段") || !strings.Contains(result, "类型") || !strings.Contains(result, "说明") || !strings.Contains(result, "┼") {
		t.Fatalf("expected repaired table header, got %q", result)
	}
	if !strings.Contains(result, "name") || !strings.Contains(result, "varchar") || !strings.Contains(result, "姓名") {
		t.Fatalf("expected wrapped row to be repaired, got %q", result)
	}
}

func TestFormatTable_WithIndentedRowsFormatsAsTable(t *testing.T) {
	formatter := NewMarkdownFormatter(false)

	tableMD := `下面是一个 Markdown 表格格式示例：

  | 列名1 | 列名2 | 列名3 |
  |------|------|------|
  | 数据A | 数据B | 数据C |
  | 数据D | 数据E | 数据F |`

	result := formatter.Format(tableMD)

	if !strings.Contains(result, "列名1 │ 列名2 │ 列名3") {
		t.Fatalf("expected formatted table header, got %q", result)
	}
	if !strings.Contains(result, "数据A") || !strings.Contains(result, "数据F") {
		t.Fatalf("expected formatted table rows, got %q", result)
	}
}

func TestFormatTable_WithOnlyIndentedRowsFormatsAsTable(t *testing.T) {
	formatter := NewMarkdownFormatter(false)

	tableMD := `  | 列名1 | 列名2 | 列名3 |
  |------|------|------|
  | 数据A | 数据B | 数据C |
  | 数据D | 数据E | 数据F |`

	result := formatter.Format(tableMD)

	if !strings.Contains(result, "列名1 │ 列名2 │ 列名3") {
		t.Fatalf("expected formatted table header, got %q", result)
	}
}

func TestFormatCodeBlock_MarkdownFenceRendersInnerMarkdown(t *testing.T) {
	formatter := NewMarkdownFormatter(false)
	formatter.Width = 120

	input := "```markdown\n| 列名1 | 列名2 | 列名3 |\n|------|------|------|\n| 数据A | 数据B | 数据C |\n| 数据D | 数据E | 数据F |\n```"
	result := formatter.Format(input)

	if strings.Contains(result, "```") {
		t.Fatalf("expected markdown fence to be unwrapped, got %q", result)
	}
	// Grid uses " │ " separators; accept either grid or records fallback.
	if !strings.Contains(result, "列名1") || !strings.Contains(result, "数据A") {
		t.Fatalf("expected inner markdown table to be formatted, got %q", result)
	}
	if !strings.Contains(result, "│") && !strings.Contains(result, "列名1:") {
		t.Fatalf("expected table grid or records layout, got %q", result)
	}
}

func TestFormatCodeBlock_MarkdownFenceRendersInnerHeadingAndList(t *testing.T) {
	formatter := NewMarkdownFormatter(false)
	formatter.Width = 80

	input := "```md\n# 标题\n- 项目1\n- 项目2\n```"
	result := formatter.Format(input)

	if strings.Contains(result, "```") {
		t.Fatalf("expected md fence to be unwrapped, got %q", result)
	}
	// New renderer uses ▶ prefix for h1 rather than raw "# ".
	if !strings.Contains(result, "标题") {
		t.Fatalf("expected inner heading to be formatted, got %q", result)
	}
	if !strings.Contains(result, "• 项目1") || !strings.Contains(result, "• 项目2") {
		t.Fatalf("expected inner list to be formatted, got %q", result)
	}
}

// TestFormatLineItems 测试列表项的各种格式
func TestFormatLineItems(t *testing.T) {
	formatter := NewMarkdownFormatter(false)

	tests := []struct {
		name     string
		input    string
		contains []string
	}{
		{
			name:     "简单列表",
			input:    "- 项目1\n- 项目2\n- 项目3",
			contains: []string{"• 项目1", "• 项目2", "• 项目3"},
		},
		{
			name:     "有序列表",
			input:    "1. 第一\n2. 第二\n3. 第三",
			contains: []string{"1. 第一", "2. 第二", "3. 第三"},
		},
		{
			name:     "嵌套列表",
			input:    "- 主项目\n  - 子项目1\n  - 子项目2",
			contains: []string{"• 主项目", "• 子项目1", "• 子项目2"},
		},
		{
			name:     "列表包含 inline 代码",
			input:    "- 使用 `go run` 运行\n- 使用 `go test` 测试",
			contains: []string{"go run", "go test"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatter.Format(tt.input)

			for _, expected := range tt.contains {
				if !strings.Contains(result, expected) {
					t.Errorf("列表应该包含: %s\n实际结果: %s", expected, result)
				}
			}
		})
	}
}

// TestFormatInlineCode 测试内联代码格式化
func TestFormatInlineCode(t *testing.T) {
	formatter := NewMarkdownFormatter(false)

	tests := []struct {
		name  string
		input string
		check string // 检查包含的关键词（反引号在非彩色模式下会被保留）
	}{
		{
			name:  "简单代码",
			input: "使用 `fmt.Println` 输出",
			check: "fmt.Println",
		},
		{
			name:  "代码在开头",
			input: "`code` 是代码",
			check: "code",
		},
		{
			name:  "多个代码",
			input: "`go` 和 `python` 都是语言",
			check: "go",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := formatter.FormatDocument(tt.input)
			result := doc.PlainText()
			if !strings.Contains(result, tt.check) {
				t.Errorf("inline code 格式化错误\n输入: %s\n期望包含: %s\n实际: %s", tt.input, tt.check, result)
			}
			foundCode := false
			for _, block := range doc.Blocks {
				for _, line := range block.Lines {
					for _, span := range line.Spans {
						foundCode = foundCode || span.Style.Role == string(style.RoleCodeInline)
					}
				}
			}
			if !foundCode {
				t.Fatalf("inline code 缺少语义 span: %+v", doc)
			}
		})
	}
}

// TestFormatBold 测试粗体格式化
func TestFormatBold(t *testing.T) {
	formatter := NewMarkdownFormatter(false)

	tests := []struct {
		name  string
		input string
		check string
	}{
		{
			name:  "简单粗体",
			input: "这是 **粗体** 文字",
			check: "粗体",
		},
		{
			name:  "多个粗体",
			input: "**粗体1** 和 **粗体2**",
			check: "粗体1",
		},
		{
			name:  "粗体包含链接",
			input: "**[链接](https://example.com)**",
			check: "链接",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := formatter.FormatDocument(tt.input)
			result := doc.PlainText()
			if !strings.Contains(result, tt.check) {
				t.Errorf("粗体格式化应该包含: %s\n实际: %s", tt.check, result)
			}

			// 检查不应该包含 ** 符号
			if strings.Contains(result, "**") {
				t.Errorf("格式化后不应该包含 ** 符号")
			}
		})
	}
}

// TestFormatLinks 测试链接格式化
func TestFormatLinks(t *testing.T) {
	formatter := NewMarkdownFormatter(false)

	input := "访问 [GitHub](https://github.com) 和 [Google](https://google.com)"
	result := formatter.Format(input)

	if !strings.Contains(result, "GitHub") {
		t.Errorf("应该包含链接文本: GitHub")
	}

	if !strings.Contains(result, "Google") {
		t.Errorf("应该包含链接文本: Google")
	}

	// 检查不应该包含 markdown 链接语法
	if strings.Contains(result, "](") {
		t.Errorf("不应该包含 markdown 链接语法 ](")
	}
}

func TestFormatLinks_FileLinkRendersTerminalHyperlink(t *testing.T) {
	formatter := NewMarkdownFormatter(true)
	formatter.ThemeContextProvider = func() style.ThemeContext {
		return style.BuildThemeContext(style.ThemeSelection{
			PaletteName: style.PaletteFocus,
			Mode:        style.ThemeModeDark,
		}, style.ColorProfile{ColorProfile: render.ColorProfile{
			Enabled:    true,
			Depth:      render.ColorANSI16,
			Hyperlinks: true,
		}})
	}

	input := `打开 [C:\tmp\img.png](file:///C:/tmp/img.png)`
	doc := formatter.FormatDocument(input)
	visible := doc.PlainText()
	foundLink := false
	for _, block := range doc.Blocks {
		for _, line := range block.Lines {
			for _, span := range line.Spans {
				foundLink = foundLink || span.Link == "file:///C:/tmp/img.png"
			}
		}
	}
	if !foundLink {
		t.Fatalf("结构化文档缺少文件超链接目标: %+v", doc)
	}
	if !strings.Contains(visible, `C:\tmp\img.png`) {
		t.Fatalf("可见文本应该包含文件路径，实际: %q", visible)
	}
	if strings.Contains(visible, "file:///C:/tmp/img.png") {
		t.Fatalf("文件链接渲染后不应再显示 file URL，实际: %q", visible)
	}
}

// TestFormatQuote 测试引用格式化
func TestFormatQuote(t *testing.T) {
	formatter := NewMarkdownFormatter(false)

	input := "> 这是引用内容\n第二行引用"
	result := formatter.Format(input)

	if !strings.Contains(result, "│") {
		t.Errorf("引用应该包含 │ 符号")
	}

	if !strings.Contains(result, "这是引用内容") {
		t.Errorf("引用应该包含原始内容")
	}
}

// TestEmptyAndWhitespace 测试空文本和空白符处理
func TestEmptyAndWhitespace(t *testing.T) {
	formatter := NewMarkdownFormatter(true)

	// 空字符串
	result := formatter.Format("")
	if result != "" {
		t.Errorf("空字符串应该返回空字符串")
	}

	// 只有空白
	result = formatter.Format("   \n\n  ")
	if strings.TrimSpace(result) != "" {
		t.Errorf("只有空白的文本应该返回空白")
	}

	// 多个空行
	result = formatter.Format("\n\n\n")
	if result != "\n\n\n" {
		t.Errorf("多个空行应该被保留")
	}
}

// TestComplexMarkdownDocument 测试复杂的 Markdown 文档
func TestComplexMarkdownDocument(t *testing.T) {
	formatter := NewMarkdownFormatter(false)

	doc := `# Mihomo DNS 服务分析

## 1. 核心特性

Mihomo (Clash Meta) 提供了强大的 DNS 服务功能。

### 1.1 Fake-IP 模式

| 特性 | 说明 |
|------|------|
| 避免 DNS 污染 | 使用虚拟 IP |


### 1.2 性能优化

- DNS 缓存
- 并发查询
- 快速解析

` + "```bash" + `
# 查看 DNS 配置
cat config.yaml | grep dns
` + "```" + `

> 💡 提示：使用 ` + "`" + `sudo` + "`" + ` 获取完全权限

**重要提示**：配置完成后需要重启服务。`

	result := formatter.Format(doc)

	if result == "" {
		t.Errorf("复杂文档格式化结果不应为空")
	}

	// 检查标题
	if !strings.Contains(result, "Mihomo DNS 服务分析") {
		t.Errorf("应该包含主标题")
	}

	// 检查表格
	if !strings.Contains(result, "特性") || !strings.Contains(result, "说明") {
		t.Errorf("应该包含表格内容")
	}

	// 检查列表
	if !strings.Contains(result, "DNS 缓存") {
		t.Errorf("应该包含列表内容")
	}

	// 检查代码块
	if !strings.Contains(result, "cat config.yaml") {
		t.Errorf("应该包含代码块内容")
	}

	// 检查引用
	if !strings.Contains(result, "│") {
		t.Errorf("应该包含引用符号")
	}
}

// TestFormatTableEdgeCases 测试表格边界情况
func TestFormatTableEdgeCases(t *testing.T) {
	formatter := NewMarkdownFormatter(false)

	tests := []struct {
		name     string
		input    string
		expected bool // should succeed
	}{
		{
			name:     "单列表格",
			input:    "| |\n|---|\n| value |",
			expected: true,
		},
		{
			name:     "空单元格",
			input:    "| A | B |\n|---|---|\n| | value2 |",
			expected: true,
		},
		{
			name:     "特殊字符",
			input:    "| 名称 | 值 |\n|------|------|\n| `code` | **bold** |",
			expected: true,
		},
		{
			name:     "超长单元格",
			input:    "| 列1 |\n|------|\n| 这是一个非常非常长的单元格内容，应该被正确处理 |",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatter.Format(tt.input)
			success := result != ""
			if success != tt.expected {
				t.Errorf("期望成功: %v, 实际: %v\n结果: %s", tt.expected, success, result)
			}
		})
	}
}

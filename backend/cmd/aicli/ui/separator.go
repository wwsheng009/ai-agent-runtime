package ui

import (
	"fmt"
	"os"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// SeparatorType 分隔线类型
type SeparatorType int

const (
	SeparatorRegular SeparatorType = iota // 普通分隔线
	SeparatorThin                         // 细分隔线
	SeparatorThick                        // 粗分隔线
	SeparatorDashed                       // 虚线分隔线
	SeparatorDouble                       // 双线分隔线
)

// Separator 分隔线组件
type Separator struct {
	theme   *Theme
	sType   SeparatorType
	width   int
	title   string
	padding int
}

// NewSeparator 创建新的分隔线
func NewSeparator() *Separator {
	return &Separator{
		theme:   GetTheme(ThemeAuto),
		sType:   SeparatorRegular,
		width:   GetTerminalWidth(),
		padding: 0,
	}
}

// SetType 设置分隔线类型
func (s *Separator) SetType(sType SeparatorType) *Separator {
	s.sType = sType
	return s
}

// SetWidth 设置分隔线宽度
func (s *Separator) SetWidth(width int) *Separator {
	s.width = width
	return s
}

// SetTitle 设置分隔线标题
func (s *Separator) SetTitle(title string) *Separator {
	s.title = title
	return s
}

// SetPadding 设置标题两边的填充
func (s *Separator) SetPadding(padding int) *Separator {
	s.padding = padding
	return s
}

// SetTheme 设置主题
func (s *Separator) SetTheme(theme *Theme) *Separator {
	s.theme = theme
	return s
}

// Build 构建分隔线字符串
func (s *Separator) Build() string {
	theme := s.theme
	if theme == nil {
		theme = GetTheme(ThemeAuto)
	}
	fill := theme.Separator
	kind := style.SeparatorRegular
	switch s.sType {
	case SeparatorThin:
		fill = theme.Separator
		kind = style.SeparatorThin
	case SeparatorThick:
		fill = theme.BorderHorizontal
		kind = style.SeparatorThick
	case SeparatorDashed:
		fill = "-"
		kind = style.SeparatorDashed
	case SeparatorDouble:
		fill = "═"
		kind = style.SeparatorDouble
	}

	doc := style.SeparatorDocument(style.SeparatorModel{
		Kind:    kind,
		Width:   s.width,
		Title:   s.title,
		Padding: s.padding,
		Fill:    fill,
	})
	return renderDocumentWithProfile(doc, theme)
}

// Print 打印分隔线
func (s *Separator) Print() {
	_, _ = WriteTerminalLine(os.Stdout, s.Build())
}

// PrintEmptyLine 打印空行
func PrintEmptyLine() {
	_, _ = WriteTerminalLine(os.Stdout, "")
}

// PrintEmptyLines 打印多行空行
func PrintEmptyLines(count int) {
	for i := 0; i < count; i++ {
		PrintEmptyLine()
	}
}

// PrintSeparator 快捷方法：打印普通分隔线
func PrintSeparator() {
	NewSeparator().Print()
}

// PrintThickSeparator 快捷方法：打印粗分隔线
func PrintThickSeparator() {
	NewSeparator().SetType(SeparatorThick).Print()
}

// PrintThinSeparator 快捷方法：打印细分隔线
func PrintThinSeparator() {
	NewSeparator().SetType(SeparatorThin).Print()
}

// PrintTitledSeparator 快捷方法：打印带标题的分隔线
func PrintTitledSeparator(title string) {
	NewSeparator().SetTitle(title).Print()
}

// PrintSection 打印一个节标题
func PrintSection(title string) {
	PrintEmptyLine()
	PrintTitledSeparator(fmt.Sprintf(" %s ", title))
	PrintEmptyLine()
}

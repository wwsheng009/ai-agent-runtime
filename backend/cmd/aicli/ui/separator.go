package ui

import (
	"fmt"
	"strings"
)

// SeparatorType 分隔线类型
type SeparatorType int

const (
	SeparatorRegular SeparatorType = iota // 普通分隔线
	SeparatorThin                           // 细分隔线
	SeparatorThick                          // 粗分隔线
	SeparatorDashed                         // 虚线分隔线
	SeparatorDouble                         // 双线分隔线
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
	var separator string
	switch s.sType {
	case SeparatorThin:
		separator = s.theme.Separator
	case SeparatorThick:
		separator = s.theme.BorderHorizontal
	case SeparatorDashed:
		separator = "-"
	case SeparatorDouble:
		separator = "═"
	default:
		separator = s.theme.Separator
	}

	if s.title == "" {
		// 无标题，绘制整行
		return s.theme.SeparatorColor.Sprint(strings.Repeat(separator, s.width))
	}

	// 有标题，在标题两侧绘制
	titleLen := len(s.title)
	if titleLen+2*s.padding >= s.width {
		// 标题太长，只显示标题
		return s.theme.SeparatorColor.Sprint(s.title)
	}

	leftWidth := (s.width - titleLen - 2*s.padding) / 2
	rightWidth := s.width - titleLen - 2*s.padding - leftWidth

	var builder strings.Builder
	builder.WriteString(strings.Repeat(separator, leftWidth))
	builder.WriteString(strings.Repeat(" ", s.padding))
	builder.WriteString(s.title)
	builder.WriteString(strings.Repeat(" ", s.padding))
	builder.WriteString(strings.Repeat(separator, rightWidth))

	return s.theme.SeparatorColor.Sprint(builder.String())
}

// Print 打印分隔线
func (s *Separator) Print() {
	fmt.Println(s.Build())
}

// PrintEmptyLine 打印空行
func PrintEmptyLine() {
	fmt.Println()
}

// PrintEmptyLines 打印多行空行
func PrintEmptyLines(count int) {
	for i := 0; i < count; i++ {
		fmt.Println()
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

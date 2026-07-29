package ui

import (
	"os"
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// clearToEOL is the CSI sequence that clears from cursor to end of line.
const clearToEOL = "\033[K"

// LayoutType 布局类型
type LayoutType int

const (
	LayoutSimple   LayoutType = iota // 简单布局（消息区域 + 状态栏）
	LayoutAdvanced                   // 高级布局（消息区域 + 状态栏 + 输入框）
)

// LayoutArea 屏幕区域
type LayoutArea struct {
	Row    int // 起始行（1-based）
	Col    int // 起始列（1-based）
	Width  int // 宽度
	Height int // 高度
}

// Layout 屏幕布局管理器
type Layout struct {
	terminal   *Terminal
	layoutType LayoutType
	statusBar  *StatusBar
	theme      *Theme
	mu         sync.RWMutex

	// 屏幕区域
	chatArea   *LayoutArea
	inputArea  *LayoutArea
	statusArea *LayoutArea

	// 配置
	statusBarHeight int
	inputHeight     int
	enabled         bool
}

// NewLayout 创建新的布局
func NewLayout(layoutType LayoutType) *Layout {
	term := NewTerminal()

	layout := &Layout{
		terminal:        term,
		layoutType:      layoutType,
		theme:           GetTheme(ThemeAuto),
		statusBar:       NewStatusBar(term.Height()),
		statusBarHeight: 2,
		inputHeight:     1,
		enabled:         false,
	}

	layout.calculateAreas()
	layout.statusBar.SetTerminal(term).SetRow(layout.statusArea.Row)

	return layout
}

// SetTerminal 设置终端控制器
func (l *Layout) SetTerminal(term *Terminal) *Layout {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.terminal = term
	l.calculateAreas()
	if l.statusBar != nil && l.statusArea != nil {
		l.statusBar.SetTerminal(term).SetRow(l.statusArea.Row)
	}
	return l
}

// SetStatusBar 设置状态栏
func (l *Layout) SetStatusBar(sb *StatusBar) *Layout {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.statusBar = sb
	return l
}

// SetTheme 设置主题
func (l *Layout) SetTheme(theme *Theme) *Layout {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.theme = theme
	return l
}

// SetStatusBarHeight 设置状态栏高度
func (l *Layout) SetStatusBarHeight(height int) *Layout {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.statusBarHeight = height
	l.calculateAreas()
	return l
}

// SetInputHeight 设置输入框高度
func (l *Layout) SetInputHeight(height int) *Layout {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.inputHeight = height
	l.calculateAreas()
	return l
}

// SetEnabled 启用或禁用布局
func (l *Layout) SetEnabled(enabled bool) *Layout {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.enabled = enabled
	return l
}

// IsEnabled 返回是否启用布局
func (l *Layout) IsEnabled() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.enabled
}

// calculateAreas 计算各区域位置
func (l *Layout) calculateAreas() {
	if l.terminal == nil {
		l.terminal = NewTerminal()
	}
	l.terminal.RefreshSize()
	height := l.terminal.Height()
	width := l.terminal.Width()
	if height <= 0 {
		height = 24
	}
	if width <= 0 {
		width = 80
	}

	// 从底部开始计算
	// 底部: 状态栏 + 输入框
	bottomRows := l.statusBarHeight
	if l.layoutType == LayoutAdvanced {
		bottomRows += l.inputHeight
	}
	if bottomRows < 1 {
		bottomRows = 1
	}
	if bottomRows >= height {
		bottomRows = height - 1
	}
	if bottomRows < 1 {
		bottomRows = 1
	}

	// 状态栏区域（底部）
	l.statusArea = &LayoutArea{
		Row:    height - bottomRows + 1,
		Col:    1,
		Width:  width,
		Height: l.statusBarHeight,
	}

	// 输入框区域（状态栏下方）
	if l.layoutType == LayoutAdvanced && l.inputHeight > 0 {
		l.inputArea = &LayoutArea{
			Row:    height,
			Col:    1,
			Width:  width,
			Height: l.inputHeight,
		}
	} else {
		l.inputArea = nil
	}

	// 聊天区域（剩余区域）
	chatHeight := height - bottomRows
	if chatHeight < 1 {
		chatHeight = 1
	}
	l.chatArea = &LayoutArea{
		Row:    1,
		Col:    1,
		Width:  width,
		Height: chatHeight,
	}
}

// ChatArea 返回聊天区域
func (l *Layout) ChatArea() *LayoutArea {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.chatArea
}

// InputArea 返回输入区域
func (l *Layout) InputArea() *LayoutArea {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.inputArea
}

// StatusArea 返回状态区域
func (l *Layout) StatusArea() *LayoutArea {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.statusArea
}

// themeOrDefault returns the layout theme or the process auto theme.
func (l *Layout) themeOrDefault() *Theme {
	if l != nil && l.theme != nil {
		return l.theme
	}
	return GetTheme(ThemeAuto)
}

// ChatChromeDocument builds the muted chat-area placeholder label.
func (l *Layout) ChatChromeDocument() render.Document {
	return render.SingleLineDoc(render.Span{
		Text:  "聊天区域",
		Style: render.Style{Role: string(style.RoleTextMuted)},
	})
}

// SeparatorLineDocument builds the horizontal rule between chat and status areas.
func (l *Layout) SeparatorLineDocument() render.Document {
	width := 80
	if l != nil && l.terminal != nil {
		if w := l.terminal.Width(); w > 0 {
			width = w
		}
	}
	fill := "─"
	if theme := l.themeOrDefault(); theme != nil && theme.Separator != "" {
		fill = theme.Separator
	}
	return style.SeparatorDocument(style.SeparatorModel{
		Kind:  style.SeparatorRegular,
		Width: width,
		Fill:  fill,
	})
}

// InputAreaDocument builds the prompt + input Document for the input row.
// Untrusted input is sanitized so control sequences cannot escape into the TTY.
func (l *Layout) InputAreaDocument(prompt, input string) render.Document {
	var spans []render.Span
	if prompt != "" {
		spans = append(spans, render.Span{
			Text:  SanitizeTerminalText(prompt),
			Style: render.Style{Role: string(style.RoleUser)},
		})
	}
	if input != "" {
		spans = append(spans, render.Span{
			Text:  SanitizeTerminalText(input),
			Style: render.Style{Role: string(style.RoleTextPrimary)},
		})
	}
	if len(spans) == 0 {
		return render.Document{}
	}
	return render.LinesDoc(render.Line{Spans: spans})
}

// LayoutMessageDocument builds a multi-line plain message Document (sanitized).
func LayoutMessageDocument(content string) render.Document {
	safe := SanitizeTerminalText(content)
	parts := strings.Split(safe, "\n")
	if len(parts) == 0 {
		parts = []string{""}
	}
	lines := make([]render.Line, 0, len(parts))
	for _, part := range parts {
		lines = append(lines, render.Line{
			Spans: []render.Span{{
				Text:  part,
				Style: render.Style{Role: string(style.RoleTextPrimary)},
			}},
		})
	}
	return render.LinesDoc(lines...)
}

// FormatInputArea returns the styled prompt+input string without writing.
func (l *Layout) FormatInputArea(prompt, input string) string {
	return strings.TrimRight(renderDocumentWithProfile(l.InputAreaDocument(prompt, input), l.themeOrDefault()), "\n")
}

// FormatChatChrome returns the styled chat-area label without writing.
func (l *Layout) FormatChatChrome() string {
	return strings.TrimRight(renderDocumentWithProfile(l.ChatChromeDocument(), l.themeOrDefault()), "\n")
}

// FormatSeparatorLine returns the styled separator without writing.
func (l *Layout) FormatSeparatorLine() string {
	return strings.TrimRight(renderDocumentWithProfile(l.SeparatorLineDocument(), l.themeOrDefault()), "\n")
}

// FormatMessage returns sanitized multi-line message text without writing.
func FormatLayoutMessage(content string) string {
	return strings.TrimRight(renderDocumentWithProfile(LayoutMessageDocument(content), GetTheme(ThemeAuto)), "\n")
}

func (l *Layout) writeDoc(doc render.Document) {
	text := renderDocumentWithProfile(doc, l.themeOrDefault())
	if text == "" {
		return
	}
	_, _ = WriteTerminalText(os.Stdout, text)
}

func (l *Layout) writeRightAligned(row int, plainWidth int, styled string) {
	if l == nil || l.terminal == nil {
		return
	}
	width := l.terminal.Width()
	if width <= 0 {
		width = 80
	}
	pad := width - plainWidth
	if pad < 0 {
		pad = 0
	}
	l.terminal.MoveToRow(row)
	_, _ = WriteTerminalText(os.Stdout, strings.Repeat(" ", pad)+styled)
}

// Render 渲染整个布局
func (l *Layout) Render() {
	if !l.enabled {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.calculateAreas()

	// 保存当前光标位置
	l.terminal.SaveCursor()

	// 清屏
	l.terminal.Clear()

	// 绘制聊天区域边界（Document + role adapter）
	if l.chatArea != nil {
		l.writeRightAligned(l.chatArea.Row, DisplayWidth("聊天区域"), l.FormatChatChrome())
	}

	// 绘制分隔线
	if l.chatArea != nil && l.statusArea != nil {
		separatorRow := l.statusArea.Row - 1
		l.terminal.MoveToRow(separatorRow)
		l.terminal.ClearFromCursor()
		_, _ = WriteTerminalText(os.Stdout, l.FormatSeparatorLine()+clearToEOL)
	}

	// 渲染状态栏
	if l.statusBar != nil {
		l.statusBar.Clear()
		l.statusBar.SetRow(l.statusArea.Row)
		l.statusBar.WithDefaultStatus()
		l.statusBar.Render()
	}

	// 恢复光标位置
	l.terminal.RestoreCursor()
}

// RenderStatusBar 仅渲染状态栏
func (l *Layout) RenderStatusBar() {
	if !l.enabled || l.statusBar == nil {
		return
	}

	l.terminal.SaveCursor()
	l.statusBar.Render()
	l.terminal.RestoreCursor()
}

// RenderInputArea 渲染输入区域（Document + WriteTerminal*）
func (l *Layout) RenderInputArea(prompt, input string) {
	if !l.enabled || l.inputArea == nil {
		// 未启用布局：仍走 Document 消毒/角色着色，避免 raw ESC 直喷。
		if prompt != "" || input != "" {
			l.writeDoc(l.InputAreaDocument(prompt, input))
		}
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.calculateAreas()

	l.terminal.SaveCursor()

	row := l.inputArea.Row
	l.terminal.MoveTo(row, 1)
	l.terminal.ClearFromCursor()

	text := l.FormatInputArea(prompt, input)
	_, _ = WriteTerminalText(os.Stdout, text+clearToEOL)

	l.terminal.RestoreCursor()
}

// PrintMessage 打印消息到聊天区域（先消毒再写出）
func (l *Layout) PrintMessage(content string) {
	text := FormatLayoutMessage(content)
	if !l.enabled {
		_, _ = WriteTerminalLine(os.Stdout, text)
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.terminal.SaveCursor()
	_, _ = WriteTerminalLine(os.Stdout, text)
	l.terminal.RestoreCursor()
}

// PrintToChat 在聊天区域指定位置打印（先消毒再写出）
func (l *Layout) PrintToChat(row, col int, text string) {
	safe := SanitizeTerminalText(text)
	if !l.enabled {
		_, _ = WriteTerminalLine(os.Stdout, safe)
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.chatArea == nil {
		return
	}

	l.terminal.SaveCursor()

	actualRow := l.chatArea.Row + row - 1
	if actualRow < l.chatArea.Row {
		actualRow = l.chatArea.Row
	}
	if actualRow > l.chatArea.Row+l.chatArea.Height-1 {
		actualRow = l.chatArea.Row + l.chatArea.Height - 1
	}

	actualCol := l.chatArea.Col + col - 1
	if actualCol < l.chatArea.Col {
		actualCol = l.chatArea.Col
	}
	if actualCol > l.chatArea.Col+l.chatArea.Width-1 {
		actualCol = l.chatArea.Col + l.chatArea.Width - 1
	}

	l.terminal.PrintAt(actualRow, actualCol, safe)
	l.terminal.RestoreCursor()
}

// ClearChatArea 清空聊天区域
func (l *Layout) ClearChatArea() {
	if !l.enabled || l.chatArea == nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.terminal.SaveCursor()

	for i := 0; i < l.chatArea.Height; i++ {
		t := l.terminal
		t.MoveToRow(l.chatArea.Row + i)
		t.ClearFromCursor()
		_, _ = WriteTerminalText(os.Stdout, clearToEOL)
	}

	l.terminal.RestoreCursor()
}

// MoveToInput 移动光标到输入区域
func (l *Layout) MoveToInput() {
	if !l.enabled || l.inputArea == nil {
		return
	}

	l.terminal.SaveCursor()
	l.terminal.MoveTo(l.inputArea.Row, DisplayWidth(UserPromptText(0))+1) // 跳过提示符
	l.terminal.RestoreCursor()
}

// MoveToChat 移动光标到聊天区域
func (l *Layout) MoveToChat() {
	if !l.enabled || l.chatArea == nil {
		return
	}

	l.terminal.SaveCursor()
	l.terminal.MoveTo(l.chatArea.Row+l.chatArea.Height, 1) // 移动到聊天区域底部
	l.terminal.RestoreCursor()
}

// GetStatusBar 获取状态栏
func (l *Layout) GetStatusBar() *StatusBar {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.statusBar
}

// UpdateStatus 更新状态栏信息
func (l *Layout) UpdateStatus(key string, value interface{}) *Layout {
	if l.statusBar != nil {
		l.statusBar.Update(key, value)
	}
	return l
}

// UpdateStatusRole 使用语义角色更新状态栏信息。
func (l *Layout) UpdateStatusRole(key string, value interface{}, role style.Role) *Layout {
	if l.statusBar != nil {
		l.statusBar.UpdateRole(key, value, role)
	}
	return l
}

// Refresh 刷新显示
func (l *Layout) Refresh() {
	if !l.enabled {
		return
	}

	l.Render()
}

// Enable 启用布局
func (l *Layout) Enable() {
	l.SetEnabled(true)
}

// Disable 禁用布局
func (l *Layout) Disable() {
	l.SetEnabled(false)
}

// Terminal 返回终端控制器
func (l *Layout) Terminal() *Terminal {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.terminal
}

// Width 返回终端宽度
func (l *Layout) Width() int {
	return l.terminal.Width()
}

// Height 返回终端高度
func (l *Layout) Height() int {
	return l.terminal.Height()
}

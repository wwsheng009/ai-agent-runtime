package ui

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// StatusItem 状态栏的一项
type StatusItem struct {
	Key   string      // 键名
	Value interface{} // 值
	Role  style.Role  // 值的语义角色
	Width int         // 最小宽度（终端单元格）
}

// StatusBar 状态栏组件。
//
// P0 fenced-dead 清理（G9/A1-7 后续）：legacy 直写渲染面（Render/RenderWithLayout/
// RenderSimple/RenderIfChanged/ForceRender）已删除——生产唯一物理写路径是
// unified TerminalSession；如需重新接线，必须先接入统一写端并过 writer inventory 门禁。
type StatusBar struct {
	terminal *Terminal
	theme    *Theme
	items    []*StatusItem
	row      int // 状态栏所在的行号
	height   int // 状态栏高度
	mu       sync.RWMutex
}

// NewStatusBar 创建新的状态栏
func NewStatusBar(row int) *StatusBar {
	return &StatusBar{
		terminal: NewTerminal(),
		theme:    GetTheme(ThemeAuto),
		items:    make([]*StatusItem, 0),
		row:      row,
		height:   1,
	}
}

// SetRow 设置状态栏位置
func (s *StatusBar) SetRow(row int) *StatusBar {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.row = row
	return s
}

// SetHeight 设置状态栏高度
func (s *StatusBar) SetHeight(height int) *StatusBar {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.height = height
	return s
}

// SetTerminal 设置终端控制器
func (s *StatusBar) SetTerminal(term *Terminal) *StatusBar {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.terminal = term
	return s
}

// SetTheme 设置主题
func (s *StatusBar) SetTheme(theme *Theme) *StatusBar {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.theme = theme
	return s
}

// Update 更新状态项
func (s *StatusBar) Update(key string, value interface{}) *StatusBar {
	return s.updateItem(key, value, style.RoleTextPrimary, 0, false)
}

// UpdateRole 使用语义角色更新状态项。
func (s *StatusBar) UpdateRole(key string, value interface{}, role style.Role) *StatusBar {
	return s.updateItem(key, value, role, 0, false)
}

func (s *StatusBar) updateItem(key string, value interface{}, role style.Role, width int, setWidth bool) *StatusBar {
	s.mu.Lock()
	defer s.mu.Unlock()
	if role == "" {
		role = style.RoleTextPrimary
	}
	for _, item := range s.items {
		if item.Key == key {
			item.Value = value
			item.Role = role
			if setWidth {
				item.Width = width
			}
			return s
		}
	}
	s.items = append(s.items, &StatusItem{
		Key:   key,
		Value: value,
		Role:  role,
		Width: width,
	})
	return s
}

// UpdateWithWidth 更新状态项并设置宽度
func (s *StatusBar) UpdateWithWidth(key string, value interface{}, width int) *StatusBar {
	return s.updateItem(key, value, style.RoleTextPrimary, width, true)
}

// UpdateWithWidthRole 使用语义角色和终端单元格宽度更新状态项。
func (s *StatusBar) UpdateWithWidthRole(key string, value interface{}, role style.Role, width int) *StatusBar {
	return s.updateItem(key, value, role, width, true)
}

// Remove 移除状态项
func (s *StatusBar) Remove(key string) *StatusBar {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, item := range s.items {
		if item.Key == key {
			s.items = append(s.items[:i], s.items[i+1:]...)
			return s
		}
	}
	return s
}

// Clear 清空所有状态项
func (s *StatusBar) Clear() *StatusBar {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = make([]*StatusItem, 0)
	return s
}

func statusItemLine(item *StatusItem, spaced bool) render.Line {
	if item == nil {
		return render.Line{}
	}
	key := strings.Join(strings.Fields(SanitizeTerminalText(item.Key)), " ")
	value := strings.Join(strings.Fields(SanitizeTerminalText(formatValue(item.Value))), " ")
	separator := ":"
	if spaced {
		separator = ": "
	}
	role := item.Role
	if role == "" {
		role = style.RoleTextPrimary
	}
	line := render.Line{Spans: []render.Span{
		semanticSpan(key, style.RoleMetaLabel, true),
		semanticSpan(separator, style.RoleTextMuted, false),
		semanticSpan(value, role, false),
	}}
	if item.Width > 0 {
		line = render.Pad(line, item.Width, render.AlignLeft)
	}
	return line
}

func (s *StatusBar) documentLocked(width int) render.Document {
	line := render.Line{}
	for i, item := range s.items {
		if i > 0 {
			line.Spans = append(line.Spans, semanticSpan(" | ", style.RoleBorder, false))
		}
		line.Spans = append(line.Spans, statusItemLine(item, false).Spans...)
	}
	if width > 0 && render.LineWidth(line) > width {
		line = render.Truncate(line, width, "…")
	}
	return render.Document{Blocks: []render.Block{{Kind: render.BlockStatus, Lines: []render.Line{line}}}}
}

// Document 返回单行状态栏模型，并按终端单元格宽度截断。
func (s *StatusBar) Document(width int) render.Document {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.documentLocked(width)
}

func (s *StatusBar) renderDocumentLocked(doc render.Document) string {
	if s.terminal != nil && s.terminal.driver != nil {
		return renderDocumentWithThemeProfile(doc, s.theme, s.terminal.driver.ColorProfile())
	}
	return renderDocumentWithProfile(doc, s.theme)
}

// Row 返回状态栏所在的行号
func (s *StatusBar) Row() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.row
}

// Height 返回状态栏高度
func (s *StatusBar) Height() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.height
}

// formatValue 格式化值
func formatValue(value interface{}) string {
	switch v := value.(type) {
	case string:
		return v
	case int, int32, int64:
		return fmt.Sprintf("%d", v)
	case uint, uint32, uint64:
		return fmt.Sprintf("%d", v)
	case float32:
		return fmt.Sprintf("%.1f", v)
	case float64:
		return fmt.Sprintf("%.1f", v)
	case bool:
		if v {
			return "on"
		}
		return "off"
	case time.Duration:
		return fmt.Sprintf("%.1fs", v.Seconds())
	default:
		return fmt.Sprintf("%v", v)
	}
}

// WithDefaultStatus 设置默认状态项
func (s *StatusBar) WithDefaultStatus() *StatusBar {
	return s.
		UpdateWithWidthRole("Model", "gpt-4", style.RoleCommand, 12).
		UpdateWithWidthRole("Tokens", 0, style.RoleSuccess, 12).
		UpdateWithWidthRole("Msgs", 0, style.RoleInfo, 8).
		UpdateWithWidthRole("Stream", "off", style.RoleInfo, 8)
}

// WithAIThinking 设置 AI 思考状态
func (s *StatusBar) WithAIThinking(thinking bool) *StatusBar {
	if thinking {
		return s.UpdateRole("Status", "Thinking...", style.RoleWarning)
	}
	return s.UpdateRole("Status", "Ready", style.RoleSuccess)
}

// GetModel 获取当前模型
func (s *StatusBar) GetModel() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.items {
		if item.Key == "Model" {
			return formatValue(item.Value)
		}
	}
	return ""
}

// SetModel 设置当前模型
func (s *StatusBar) SetModel(model string) *StatusBar {
	return s.UpdateWithWidthRole("Model", model, style.RoleCommand, 12)
}

// GetTokens 获取 token 数量
func (s *StatusBar) GetTokens() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.items {
		if item.Key == "Tokens" {
			if v, ok := item.Value.(int); ok {
				return v
			}
			return 0
		}
	}
	return 0
}

// SetTokens 设置 token 数量
func (s *StatusBar) SetTokens(tokens int) *StatusBar {
	return s.UpdateWithWidthRole("Tokens", tokens, style.RoleSuccess, 12)
}

// GetMsgCount 获取消息数量
func (s *StatusBar) GetMsgCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.items {
		if item.Key == "Msgs" {
			if v, ok := item.Value.(int); ok {
				return v
			}
			return 0
		}
	}
	return 0
}

// SetMsgCount 设置消息数量
func (s *StatusBar) SetMsgCount(count int) *StatusBar {
	return s.UpdateWithWidthRole("Msgs", count, style.RoleInfo, 8)
}

// SetStreamMode 设置流式模式
func (s *StatusBar) SetStreamMode(enabled bool) *StatusBar {
	if enabled {
		return s.UpdateRole("Stream", "on", style.RoleWarning)
	}
	return s.UpdateRole("Stream", "off", style.RoleInfo)
}

// SetThinking 设置 AI 思考状态
func (s *StatusBar) SetThinking(thinking bool) *StatusBar {
	if thinking {
		return s.UpdateRole("Status", "Thinking...", style.RoleWarning)
	}
	return s.UpdateRole("Status", "Ready", style.RoleSuccess)
}

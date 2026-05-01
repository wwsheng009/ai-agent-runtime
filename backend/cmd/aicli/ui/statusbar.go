package ui

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// StatusItem 状态栏的一项
type StatusItem struct {
	Key   string              // 键名
	Value interface{}         // 值
	Color func(string) string // 颜色函数
	Width int                 // 最小宽度
}

// StatusBar 状态栏组件
type StatusBar struct {
	terminal *Terminal
	theme    *Theme
	items    []*StatusItem
	row      int // 状态栏所在的行号
	height   int // 状态栏高度
	mu       sync.RWMutex
	force    bool // 是否强制刷新
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
func (s *StatusBar) Update(key string, value interface{}, colorFunc func(string) string) *StatusBar {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 查找是否已存在该键
	for _, item := range s.items {
		if item.Key == key {
			item.Value = value
			if colorFunc != nil {
				item.Color = colorFunc
			}
			return s
		}
	}

	// 不存在则添加
	s.items = append(s.items, &StatusItem{
		Key:   key,
		Value: value,
		Color: colorFunc,
	})
	return s
}

// UpdateWithWidth 更新状态项并设置宽度
func (s *StatusBar) UpdateWithWidth(key string, value interface{}, colorFunc func(string) string, width int) *StatusBar {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, item := range s.items {
		if item.Key == key {
			item.Value = value
			if colorFunc != nil {
				item.Color = colorFunc
			}
			item.Width = width
			return s
		}
	}

	s.items = append(s.items, &StatusItem{
		Key:   key,
		Value: value,
		Color: colorFunc,
		Width: width,
	})
	return s
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

// Render 渲染状态栏
func (s *StatusBar) Render() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.terminal == nil {
		return
	}

	// 保存当前光标位置
	s.terminal.SaveCursor()

	// 计算每个项目的显示
	for i := 0; i < s.height; i++ {
		s.terminal.MoveTo(s.row+i, 1)
		s.terminal.ClearLine()

		if i < len(s.items) {
			item := s.items[i]
			value := formatValue(item.Value)

			var display string
			if item.Color != nil {
				display = item.Color(fmt.Sprintf("%s: %s", item.Key, value))
			} else {
				display = fmt.Sprintf("%s: %s", item.Key, value)
			}

			// 添加填充
			if item.Width > 0 && len(display) < item.Width {
				display = fmt.Sprintf("%-*s", item.Width, display)
			}

			fmt.Print(display)
		}
		s.terminal.ClearLine()
	}

	// 恢复光标位置
	s.terminal.RestoreCursor()
}

// RenderWithLayout 使用布局方式渲染状态栏
func (s *StatusBar) RenderWithLayout() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.terminal == nil || len(s.items) == 0 {
		return false
	}

	row := s.row

	// 清除状态栏区域
	s.terminal.SaveCursor()
	for i := 0; i < s.height; i++ {
		s.terminal.MoveTo(row+i, 1)
		s.terminal.ClearLine()
	}

	// 构建状态行
	currentLine := 0
	for i, item := range s.items {
		value := formatValue(item.Value)
		display := fmt.Sprintf("%s:%v", item.Key, value)

		if item.Color != nil {
			display = item.Color(display)
		}

		// 检查当前行是否有足够空间
		if currentLine >= s.height {
			break // 超出状态栏高度
		}

		// 简单布局：每个项目用 " | " 分隔
		if i > 0 && currentLine < s.height {
			fmt.Print(" | ")
		}

		fmt.Print(display)

		// 简单判断是否需要换行（实际应该计算实际显示宽度）
		// 这里简化处理
	}

	s.terminal.RestoreCursor()
	return true
}

// RenderSimple 简化版渲染（单行）
func (s *StatusBar) RenderSimple() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.terminal == nil {
		return
	}

	// 保存光标
	s.terminal.SaveCursor()

	// 清除并移动到状态栏行
	s.terminal.MoveTo(s.row, 1)
	s.terminal.ClearLine()

	// 构建显示字符串
	var parts []string
	for _, item := range s.items {
		value := formatValue(item.Value)
		display := fmt.Sprintf("%s:%s", item.Key, value)
		if item.Color != nil {
			display = item.Color(display)
		}
		parts = append(parts, display)
	}

	line := strings.Join(parts, " | ")
	fmt.Print(line)
	s.terminal.ClearLine()

	// 恢复光标
	s.terminal.RestoreCursor()
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
	commandColor := s.theme.CommandColor
	successColor := s.theme.SuccessColor
	infoColor := s.theme.InfoColor

	return s.
		UpdateWithWidth("Model", "gpt-4", func(text string) string {
			return commandColor.Sprint(text)
		}, 12).
		UpdateWithWidth("Tokens", 0, func(text string) string {
			return successColor.Sprint(text)
		}, 12).
		UpdateWithWidth("Msgs", 0, func(text string) string {
			return infoColor.Sprint(text)
		}, 8).
		UpdateWithWidth("Stream", "off", nil, 8)
}

// WithAIThinking 设置 AI 思考状态
func (s *StatusBar) WithAIThinking(thinking bool) *StatusBar {
	if thinking {
		warnColor := s.theme.WarningColor
		return s.Update("Status", "Thinking...", func(text string) string {
			return warnColor.Sprint(text)
		})
	}
	successColor := s.theme.SuccessColor
	return s.Update("Status", "Ready", func(text string) string {
		return successColor.Sprint(text)
	})
}

// RenderIfChanged 如果内容有变化则渲染
func (s *StatusBar) RenderIfChanged() {
	s.Render()
}

// ForceRender 强制渲染
func (s *StatusBar) ForceRender() {
	s.force = true
	s.Render()
	s.force = false
}

// GetModel 获取当前模型
func (s *StatusBar) GetModel() string {
	for _, item := range s.items {
		if item.Key == "Model" {
			return formatValue(item.Value)
		}
	}
	return ""
}

// SetModel 设置当前模型
func (s *StatusBar) SetModel(model string) *StatusBar {
	commandColor := s.theme.CommandColor
	return s.UpdateWithWidth("Model", model, func(text string) string {
		return commandColor.Sprint(text)
	}, 12)
}

// GetTokens 获取 token 数量
func (s *StatusBar) GetTokens() int {
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
	successColor := s.theme.SuccessColor
	return s.UpdateWithWidth("Tokens", tokens, func(text string) string {
		return successColor.Sprint(text)
	}, 12)
}

// GetMsgCount 获取消息数量
func (s *StatusBar) GetMsgCount() int {
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
	infoColor := s.theme.InfoColor
	return s.UpdateWithWidth("Msgs", count, func(text string) string {
		return infoColor.Sprint(text)
	}, 8)
}

// SetStreamMode 设置流式模式
func (s *StatusBar) SetStreamMode(enabled bool) *StatusBar {
	if enabled {
		warnColor := s.theme.WarningColor
		return s.Update("Stream", "on", func(text string) string {
			return warnColor.Sprint(text)
		})
	}
	infoColor := s.theme.InfoColor
	return s.Update("Stream", "off", func(text string) string {
		return infoColor.Sprint(text)
	})
}

// SetThinking 设置 AI 思考状态
func (s *StatusBar) SetThinking(thinking bool) *StatusBar {
	if thinking {
		warnColor := s.theme.WarningColor
		return s.Update("Status", "Thinking...", func(text string) string {
			return warnColor.Sprint(text)
		})
	}
	successColor := s.theme.SuccessColor
	return s.Update("Status", "Ready", func(text string) string {
		return successColor.Sprint(text)
	})
}

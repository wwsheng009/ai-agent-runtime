package ui

import (
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

// Terminal 终端控制组件
type Terminal struct {
	width  int
	height int
	theme  *Theme
}

// NewTerminal 创建新的终端控制组件
func NewTerminal() *Terminal {
	term := &Terminal{
		theme: GetTheme(ThemeAuto),
	}
	term.updateSize()
	return term
}

// updateSize 更新终端尺寸
func (t *Terminal) updateSize() {
	t.width = GetTerminalWidth()
	t.height = 24 // 默认高度，后续可以通过 escape code 获取实际高度
}

// Width 获取终端宽度
func (t *Terminal) Width() int {
	return t.width
}

// Height 获取终端高度
func (t *Terminal) Height() int {
	return t.height
}

// Clear 清屏
func (t *Terminal) Clear() {
	fmt.Print("\033[2J")
	t.MoveTo(1, 1)
}

// ClearFromCursor 从光标到行尾清除
func (t *Terminal) ClearFromCursor() {
	fmt.Print("\033[K")
}

// ClearFromCursorToEnd 从光标到屏幕结尾清除
func (t *Terminal) ClearFromCursorToEnd() {
	fmt.Print("\033[0J")
}

// MoveTo 移动光标到指定行列（1-based）
func (t *Terminal) MoveTo(row, col int) {
	fmt.Printf("\033[%d;%dH", row, col)
}

// MoveToRow 移动光标到指定行
func (t *Terminal) MoveToRow(row int) {
	fmt.Printf("\033[%d;0H", row)
}

// MoveUp 向上移动 n 行
func (t *Terminal) MoveUp(n int) {
	if n < 1 {
		n = 1
	}
	fmt.Printf("\033[%dA", n)
}

// MoveDown 向下移动 n 行
func (t *Terminal) MoveDown(n int) {
	if n < 1 {
		n = 1
	}
	fmt.Printf("\033[%dB", n)
}

// MoveLeft 向左移动 n 列
func (t *Terminal) MoveLeft(n int) {
	if n < 1 {
		n = 1
	}
	fmt.Printf("\033[%dD", n)
}

// MoveRight 向右移动 n 列
func (t *Terminal) MoveRight(n int) {
	if n < 1 {
		n = 1
	}
	fmt.Printf("\033[%dC", n)
}

// SaveCursor 保存光标位置
func (t *Terminal) SaveCursor() {
	fmt.Print("\033[s")
}

// RestoreCursor 恢复光标位置
func (t *Terminal) RestoreCursor() {
	fmt.Print("\033[u")
}

// HideCursor 隐藏光标
func (t *Terminal) HideCursor() {
	fmt.Print("\033[?25l")
}

// ShowCursor 显示光标
func (t *Terminal) ShowCursor() {
	fmt.Print("\033[?25h")
}

// EnableAltScreen 启用备用屏幕（避免历史记录滚动）
func (t *Terminal) EnableAltScreen() {
	fmt.Print("\033[?1049h")
}

// DisableAltScreen 禁用备用屏幕
func (t *Terminal) DisableAltScreen() {
	fmt.Print("\033[?1049l")
}

// EnableLineWrap 启用自动换行
func (t *Terminal) EnableLineWrap() {
	fmt.Print("\033[?7h")
}

// DisableLineWrap 禁用自动换行
func (t *Terminal) DisableLineWrap() {
	fmt.Print("\033[?7l")
}

// NewLine 插入新行
func (t *Terminal) NewLine() {
	fmt.Println()
}

// PrintAt 在指定位置打印
func (t *Terminal) PrintAt(row, col int, text string) {
	t.SaveCursor()
	t.MoveTo(row, col)
	fmt.Print(text)
	t.RestoreCursor()
}

// PrintWithColor 使用指定颜色在指定位置打印
func (t *Terminal) PrintWithColor(row, col int, colorFunc func(string) string, text string) {
	t.SaveCursor()
	t.MoveTo(row, col)
	fmt.Print(colorFunc(text))
	t.RestoreCursor()
}

// PrintRight 在指定行右侧打印
func (t *Terminal) PrintRight(row int, text string) {
	t.SaveCursor()
	t.MoveToRow(row)
	padding := t.width - len(text)
	fmt.Printf("%*s%s", padding, "", text)
	t.RestoreCursor()
}

// PrintCenter 在指定行居中打印
func (t *Terminal) PrintCenter(row int, text string) {
	t.SaveCursor()
	t.MoveToRow(row)
	centered := CenterText(text, t.width)
	fmt.Print(centered)
	t.RestoreCursor()
}

// DrawBox 绘制矩形框
func (t *Terminal) DrawBox(row, col, width, height int, title string) {
	t.SaveCursor()

	// 顶边
	t.MoveTo(row, col)
	fmt.Print("┌")
	fmt.Print(strings.Repeat("─", width-2))
	if title != "" && len(title) < width-4 {
		fmt.Printf("[%s]", title)
	} else {
		fmt.Print("─")
	}
	fmt.Println("┐")

	// 侧边
	for i := 1; i < height-1; i++ {
		t.MoveTo(row+i, col)
		fmt.Println("│" + strings.Repeat(" ", width-2) + "│")
	}

	// 底边
	t.MoveTo(row+height-1, col)
	fmt.Println("└" + strings.Repeat("─", width-2) + "┘")

	t.RestoreCursor()
}

// GetTerminalSize 获取终端实际大小（通过 escape code 查询）
func GetTerminalSize() (width, height int) {
	if runtime.GOOS == "windows" {
		// Windows: 使用默认值
		return 80, 24
	}

	// Unix-like: 使用 ANSI escape code 查询
	// 注意：这需要终端支持，可能在某些环境下不工作
	return 80, 24
}

// SetupTerminal 设置终端，支持退出时还原
func SetupTerminal() (cleanup func()) {
	// 保存原始终端设置
	if runtime.GOOS != "windows" {
		// Unix-like: 可以设置原始模式
		// 但为了简单起见，这里不做复杂的终端设置
	}

	// 设置清理函数
	cleanup = func() {
		// 恢复光标
		fmt.Print("\033[?25h")
		// 禁用备用屏幕
		fmt.Print("\033[?1049l")
	}

	return cleanup
}

// EnsureExitOnSigInt 确保 Ctrl+C 时退出并清理
func EnsureExitOnSigInt(cleanup func()) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		if cleanup != nil {
			cleanup()
		}
		os.Exit(0)
	}()
}

// RawMode 进入原始模式（仅 Unix-like）
func RawMode() func() {
	if runtime.GOOS == "windows" {
		return func() {}
	}

	fd := int(os.Stdin.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return func() {}
	}
	return func() {
		_ = term.Restore(fd, state)
	}
}

// DisableEcho 禁用回显（用于密码输入）
func DisableEcho() func() {
	if runtime.GOOS == "windows" {
		// Windows: 使用 syscall 设置
		return func() {}
	}

	// Unix-like: 简化处理
	return func() {}
}

// IsInteractiveTerminal 返回 stdin/stdout 是否都连接到交互式终端
// 用于仅在 TTY 场景启用逐键 line editor。
func IsInteractiveTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// FlushOutput 刷新输出缓冲区
func FlushOutput() {
	// Go 的 fmt.Println 会自动刷新，不需要额外操作
	// 但如果使用 buffered writer，可能需要Flush()
}

// Delay 延迟执行
func (t *Terminal) Delay(ms int) {
	time.Sleep(time.Duration(ms) * time.Millisecond)
}

// IsKeyPressed 检测是否有键按下（非阻塞，简化版）
func IsKeyPressed(key string) bool {
	// 简化版，实际应该使用 select + syscall
	return false
}

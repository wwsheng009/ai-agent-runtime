//go:build windows

package commands

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// legacyConsoleLineEditor 是降级模式（win7 conhost、无 ANSI）下的传统控制台
// 行编辑器：
//
//   - 读：ReadConsoleInputW 直接读按键记录（VK 码 + UnicodeChar），与终端
//     输出代码页无关，中文（BMP）天然可用；backspace/delete/方向键/Home/
//     End 全部可用。
//   - 写：WriteConsoleW（UTF-16，conhost 内部按 WCHAR 渲染，同样避开
//     65001 字节解码问题）+ SetConsoleCursorPosition 定位，纯 Win32 经典
//     API，win7 原生支持。
//
// 文本区起点 = 每次按键时实时光标位置：协调器先写出 `> ` 提示符，用户敲键
// 后编辑器以该位置为基准重绘文本区，即使模型输出滚动导致光标行位移，编辑
// 行也会跟随光标行自我修复。

const (
	// 键盘虚拟键码（win7 原生常量，x/sys/windows 未导出）。
	vkBack       = 0x08
	vkTab        = 0x09
	vkReturn     = 0x0D
	vkEscape     = 0x1B
	vkHome       = 0x24
	vkLeft       = 0x25
	vkUp         = 0x26
	vkRight      = 0x27
	vkDown       = 0x28
	vkEnd        = 0x23
	vkDelete     = 0x2E
	vkProcessKey = 0xE5

	// IBM PC/AT set-1 scan code used by Windows KEY_EVENT_RECORD for the
	// physical Backspace key. Old conhost/remote-console compatibility layers
	// can preserve this scan code even when VirtualKeyCode is translated
	// incorrectly (for example, to VK_LEFT).
	scanBackspace = 0x0E
)

var procReadConsoleInputW = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReadConsoleInputW")
var procWriteConsoleW = windows.NewLazySystemDLL("kernel32.dll").NewProc("WriteConsoleW")
var procWriteConsoleOutputCharacterW = windows.NewLazySystemDLL("kernel32.dll").NewProc("WriteConsoleOutputCharacterW")
var procGetConsoleScreenBufferInfo = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleScreenBufferInfo")
var procSetConsoleCursorPosition = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetConsoleCursorPosition")

var legacyConsoleDebugf = func(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format, args...)
}

var legacyConsoleDebugln = func(args ...any) {
	_, _ = fmt.Fprintln(os.Stderr, args...)
}

// consoleCoord 对应 COORD。
type consoleCoord struct {
	X int16
	Y int16
}

// consoleScreenBufferInfo 对应 CONSOLE_SCREEN_BUFFER_INFO。
type consoleScreenBufferInfo struct {
	Size              consoleCoord
	CursorPosition    consoleCoord
	Attributes        uint16
	Window            struct{ Left, Top, Right, Bottom int16 }
	MaximumWindowSize consoleCoord
}

type legacyConsoleLineEditor struct {
	in  windows.Handle
	out windows.Handle

	inputMode uint32
	buf       []rune
	cur       int

	// 重绘锚点：x0 = 编辑文本区起始列（首次 redraw 时锁定，不随每次
	// 重绘漂移）；y = 编辑行所在行（跟随输出滚动刷新）。
	anchored bool
	x0       int
	y0       int16
}

// readLegacyConsoleLine 在降级模式下读取一行交互输入。
// ok=false 表示 stdin 不是控制台或初始化失败，调用方应回退 buffered 读取。
func readLegacyConsoleLine(ctx context.Context) (line string, ok bool, err error) {
	in, err := stdinConsoleHandle()
	if err != nil {
		return "", false, nil
	}
	out := windows.Handle(os.Stdout.Fd())
	var outMode uint32
	if werr := windows.GetConsoleMode(out, &outMode); werr != nil {
		// stdout 不是控制台（重定向）时尽量保持简单，回退 buffered 读取。
		return "", false, nil
	}
	ed := &legacyConsoleLineEditor{in: in, out: out}
	if err := windows.GetConsoleMode(in, &ed.inputMode); err == nil {
		// 关闭行缓冲与回显，保留 PROCESSED_INPUT（Ctrl+C 仍走信号路径）。
		next := ed.inputMode &^ (windows.ENABLE_ECHO_INPUT | windows.ENABLE_LINE_INPUT)
		if err := windows.SetConsoleMode(in, next); err == nil {
			defer windows.SetConsoleMode(in, ed.inputMode)
			if chatDebugFlagEnabled() {
				legacyConsoleDebugln("[aicli-diag] legacy console line editor active")
			}
			text, err := ed.readLine(ctx)
			return text, true, err
		}
	}
	return "", false, nil
}

func (ed *legacyConsoleLineEditor) readLine(ctx context.Context) (string, error) {
	for {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			default:
			}
		}
		avail := 0
		if recs, perr := peekConsoleInput(ed.in); perr == nil {
			avail = len(recs)
		}
		if avail <= 0 {
			time.Sleep(platformInputPasteSettleDelay())
			continue
		}
		recs := make([]consoleInputRecord, avail)
		read, err := ed.readRecords(recs)
		if err != nil {
			return "", err
		}
		submitted := ed.dispatch(recs[:read])
		if submitted {
			line := string(ed.buf)
			ed.buf = ed.buf[:0]
			ed.cur = 0
			return line, nil
		}
	}
}

// dispatch 处理一批按键记录，返回是否提交了输入行。
func (ed *legacyConsoleLineEditor) readRecords(recs []consoleInputRecord) (int, error) {
	var n uint32
	r1, _, err := procReadConsoleInputW.Call(
		uintptr(ed.in),
		uintptr(unsafe.Pointer(&recs[0])),
		uintptr(len(recs)),
		uintptr(unsafe.Pointer(&n)),
	)
	if r1 == 0 {
		return 0, err
	}
	return int(n), nil
}

func (ed *legacyConsoleLineEditor) dispatch(recs []consoleInputRecord) bool {
	for i := range recs {
		rec := &recs[i]
		if rec.EventType != 0x0001 {
			continue
		}
		key := (*consoleKeyEventRecord)(unsafe.Pointer(&rec.Event[0]))
		if key.KeyDown == 0 {
			continue
		}
		virtualKey := normalizedLegacyConsoleVirtualKey(key)
		// 归一化命中诊断：win7/旧 conhost 把物理 Backspace 翻译成 VK_LEFT
		// 时（常见于旧版控制台/远程会话），无条件打一次告警，方便不
		// 开 --debug 也能取证原始字段（scan/uni 哪一个救回了按键）。
		if key.VirtualKeyCode == vkLeft && virtualKey == vkBack {
			legacyConsoleDebugf(
				"[aicli-diag] Console layer translated Backspace to VK_LEFT; rawVK=0x%02X scan=0x%02X uni=%#04x rep=%d ctrl=0x%04X -> normalized VK_BACK\n",
				key.VirtualKeyCode, key.VirtualScanCode, key.UnicodeChar,
				key.RepeatCount, key.ControlKeyState)
		}
		// 诊断探针：同时打印原始与兼容归一化后的按键字段（仅 --debug）。
		if chatDebugFlagEnabled() && (virtualKey == vkBack ||
			virtualKey == vkLeft || virtualKey == vkDelete ||
			key.UnicodeChar == '\b' || key.UnicodeChar == 0x7F) {
			legacyConsoleDebugf(
				"[aicli-diag] key rawVK=0x%02X normalizedVK=0x%02X scan=0x%02X uni=%#04x rep=%d ctrl=0x%04X\n",
				key.VirtualKeyCode, virtualKey, key.VirtualScanCode, key.UnicodeChar,
				key.RepeatCount, key.ControlKeyState)
		}
		repeat := int(key.RepeatCount)
		if repeat < 1 {
			repeat = 1
		}
		switch virtualKey {
		case vkReturn:
			// 提交：结束编辑行（\r\n），保证后续协调器输出/LLM 响应从
			// 新行开始，不会接在用户输入同一行。
			ed.advanceLine()
			return true
		case vkBack:
			for j := 0; j < repeat && ed.cur > 0; j++ {
				ed.cur--
				ed.buf = append(ed.buf[:ed.cur], ed.buf[ed.cur+1:]...)
			}
			ed.redrawIfConsole()
		case vkDelete:
			for j := 0; j < repeat && ed.cur < len(ed.buf); j++ {
				ed.buf = append(ed.buf[:ed.cur], ed.buf[ed.cur+1:]...)
			}
			ed.redrawIfConsole()
		case vkLeft:
			if ed.cur > 0 {
				ed.cur--
				ed.redrawIfConsole()
			}
		case vkRight:
			if ed.cur < len(ed.buf) {
				ed.cur++
				ed.redrawIfConsole()
			}
		case vkHome:
			if ed.cur != 0 {
				ed.cur = 0
				ed.redrawIfConsole()
			}
		case vkEnd:
			if ed.cur != len(ed.buf) {
				ed.cur = len(ed.buf)
				ed.redrawIfConsole()
			}
		case vkEscape:
			if len(ed.buf) > 0 {
				ed.buf = ed.buf[:0]
				ed.cur = 0
				ed.redrawIfConsole()
			}
		case vkTab, vkUp, vkDown, vkProcessKey:
			// 传统降级模式无补全/历史/IME 合成键语义，忽略。
		default:
			ch := rune(key.UnicodeChar)
			if isEditableConsoleRune(ch) {
				for j := 0; j < repeat; j++ {
					ed.buf = append(ed.buf, 0)
					copy(ed.buf[ed.cur+1:], ed.buf[ed.cur:])
					ed.buf[ed.cur] = ch
					ed.cur++
				}
				ed.redrawIfConsole()
			}
		}
	}
	return false
}

// normalizedLegacyConsoleVirtualKey reconciles inconsistent KEY_EVENT_RECORD
// fields produced by old Windows console stacks. Only the observed stale
// VK_LEFT candidate may be overridden by a physical Backspace scan code (0x0E)
// or translated '\b'; unrelated keys retain their explicit virtual-key code. A
// real Left key uses scan 0x4B and Unicode 0, so it remains VK_LEFT. Unlike
// terminal byte streams, Unicode 0x7F is not a Windows virtual-key code (VK 0x7F
// is F16), so it is never used to override an explicit Windows key identity.
func normalizedLegacyConsoleVirtualKey(key *consoleKeyEventRecord) uint16 {
	if key == nil {
		return 0
	}
	switch key.VirtualKeyCode {
	case vkBack:
		return vkBack
	case vkLeft:
		if key.UnicodeChar == '\b' || key.VirtualScanCode == scanBackspace {
			return vkBack
		}
	case 0:
		// Some injected/remote-console records omit the VK. Require both
		// independent Backspace fields before accepting such a synthetic event.
		if key.UnicodeChar == '\b' && key.VirtualScanCode == scanBackspace {
			return vkBack
		}
	}
	return key.VirtualKeyCode
}

// isEditableConsoleRune 判定字符是否应插入编辑缓冲。
// 排除控制字符与非 BMP 代理对（传统 conhost 单 UTF-16 单元注入，中文全在 BMP）。
func isEditableConsoleRune(ch rune) bool {
	if ch < 0x20 || ch == 0x7F {
		return false
	}
	if ch >= 0xD800 && ch <= 0xDFFF {
		return false
	}
	return true
}

func (ed *legacyConsoleLineEditor) redrawIfConsole() {
	// A zero output handle is used by dispatch-only unit tests. Skipping the
	// Win32 redraw keeps key normalization/RepeatCount tests deterministic while
	// production editors always carry a valid console handle.
	if ed.out != 0 {
		ed.redraw()
	}
}

// redraw 以自持锚点 (y0, x0) 为基准重绘文本区并定位光标。
// 锚点在首次 redraw 时锁定（协调器刚写出 `> ` 提示符），整个编辑会话
// 期间固定；清行用 WriteConsoleOutputCharacterW（不移动光标、不触发
// conhost 自动换行），彻底避免“写满行尾 → 光标卷到下一行行首”的漂移。
func (ed *legacyConsoleLineEditor) redraw() {
	info, err := ed.screenInfo()
	if err != nil {
		return
	}
	curX := int(info.CursorPosition.X)
	curY := info.CursorPosition.Y
	if !ed.anchored {
		ed.x0 = curX
		ed.y0 = curY
		ed.anchored = true
	}
	// 清文本区：直接覆盖字符缓冲，不动光标、不 wrap。
	ed.clearTextArea()
	// 写文本 + 定位输入光标。
	ed.writeTextAt(string(ed.buf))
	ed.setCursor(ed.y0, ed.x0+displayWidthOfRunes(ed.buf[:ed.cur]))
	// 诊断探针：重绘后实际光标落点（仅 --debug）。
	if chatDebugFlagEnabled() {
		if info2, err2 := ed.screenInfo(); err2 == nil {
			legacyConsoleDebugf("[redraw] y=%d x0=%d curX=%d right=%d text=%q cur=%d after=(%d,%d)\n",
				ed.y0, ed.x0, curX, int(info.Window.Right), string(ed.buf), ed.cur,
				info2.CursorPosition.Y, info2.CursorPosition.X)
		}
	}
}

// clearTextArea 用空格覆盖 x0..right 列（WriteConsoleOutputCharacterW
// 写字符缓冲，光标位置不变，不会触发行尾自动换行）。
func (ed *legacyConsoleLineEditor) clearTextArea() {
	info, err := ed.screenInfo()
	if err != nil {
		return
	}
	right := int(info.Window.Right)
	if ed.x0 > right {
		return
	}
	n := right - ed.x0 + 1
	chars := make([]uint16, n)
	for i := range chars {
		chars[i] = ' '
	}
	var written uint32
	coord := uint32(uint16(ed.x0)) | uint32(uint16(ed.y0))<<16
	procWriteConsoleOutputCharacterW.Call(
		uintptr(ed.out),
		uintptr(unsafe.Pointer(&chars[0])),
		uintptr(n),
		uintptr(coord),
		uintptr(unsafe.Pointer(&written)),
	)
}

// writeTextAt 先定位到 (y0, x0) 再写文本。
func (ed *legacyConsoleLineEditor) writeTextAt(text string) {
	if err := ed.setCursor(ed.y0, ed.x0); err != nil {
		if chatDebugFlagEnabled() {
			legacyConsoleDebugf("[redraw] setCursor(%d,%d) failed: %v\n", ed.y0, ed.x0, err)
		}
		return
	}
	ed.writeText(text)
}

func displayWidthOfRunes(runes []rune) int {
	return ui.DisplayWidth(string(runes))
}

// advanceLine 在提交时输出 \r\n，把光标移到下一行行首。
// 编辑行本身保留在屏幕上作为用户输入的可见记录（协调器后续
// 会按自己的会话模型追加用户消息与响应）。
func (ed *legacyConsoleLineEditor) advanceLine() {
	var nl = []uint16{'\r', '\n'}
	var written uint32
	procWriteConsoleW.Call(
		uintptr(ed.out),
		uintptr(unsafe.Pointer(&nl[0])),
		uintptr(len(nl)),
		uintptr(unsafe.Pointer(&written)),
		0,
	)
}

func (ed *legacyConsoleLineEditor) screenInfo() (*consoleScreenBufferInfo, error) {
	var info consoleScreenBufferInfo
	r1, _, err := procGetConsoleScreenBufferInfo.Call(
		uintptr(ed.out),
		uintptr(unsafe.Pointer(&info)),
	)
	if r1 == 0 {
		return nil, err
	}
	return &info, nil
}

func (ed *legacyConsoleLineEditor) setCursor(y int16, x int) error {
	// SetConsoleCursorPosition 的 COORD 是**值参数**（4 字节 SHORT X/Y），
	// 不是指针！传指针会被当作 8 字节坐标读取，光标定位完全失效。
	coord := uint32(uint16(x)) | uint32(uint16(y))<<16
	r1, _, err := procSetConsoleCursorPosition.Call(uintptr(ed.out), uintptr(coord))
	if r1 == 0 {
		return err
	}
	return nil
}

func (ed *legacyConsoleLineEditor) writeText(text string) {
	if text == "" {
		return
	}
	u16, err := syscall.UTF16FromString(text)
	if err != nil {
		return
	}
	var written uint32
	procWriteConsoleW.Call(
		uintptr(ed.out),
		uintptr(unsafe.Pointer(&u16[0])),
		uintptr(len(u16)),
		uintptr(unsafe.Pointer(&written)),
		0,
	)
}

func (ed *legacyConsoleLineEditor) writeSpaces(n int) {
	if n <= 0 {
		return
	}
	u16 := make([]uint16, n)
	for i := range u16 {
		u16[i] = ' '
	}
	var written uint32
	procWriteConsoleW.Call(
		uintptr(ed.out),
		uintptr(unsafe.Pointer(&u16[0])),
		uintptr(len(u16)),
		uintptr(unsafe.Pointer(&written)),
		0,
	)
}

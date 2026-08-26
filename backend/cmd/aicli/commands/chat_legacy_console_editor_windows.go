//go:build windows

package commands

import (
	"context"
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
)

var procReadConsoleInputW = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReadConsoleInputW")
var procWriteConsoleW = windows.NewLazySystemDLL("kernel32.dll").NewProc("WriteConsoleW")
var procGetConsoleScreenBufferInfo = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleScreenBufferInfo")
var procSetConsoleCursorPosition = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetConsoleCursorPosition")

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
		repeat := int(key.RepeatCount)
		if repeat < 1 {
			repeat = 1
		}
		switch key.VirtualKeyCode {
		case vkReturn:
			return true
		case vkBack:
			for j := 0; j < repeat && ed.cur > 0; j++ {
				ed.cur--
				ed.buf = append(ed.buf[:ed.cur], ed.buf[ed.cur+1:]...)
			}
			ed.redraw()
		case vkDelete:
			for j := 0; j < repeat && ed.cur < len(ed.buf); j++ {
				ed.buf = append(ed.buf[:ed.cur], ed.buf[ed.cur+1:]...)
			}
			ed.redraw()
		case vkLeft:
			if ed.cur > 0 {
				ed.cur--
				ed.redraw()
			}
		case vkRight:
			if ed.cur < len(ed.buf) {
				ed.cur++
				ed.redraw()
			}
		case vkHome:
			if ed.cur != 0 {
				ed.cur = 0
				ed.redraw()
			}
		case vkEnd:
			if ed.cur != len(ed.buf) {
				ed.cur = len(ed.buf)
				ed.redraw()
			}
		case vkEscape:
			if len(ed.buf) > 0 {
				ed.buf = ed.buf[:0]
				ed.cur = 0
				ed.redraw()
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
				ed.redraw()
			}
		}
	}
	return false
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

// redraw 以当前光标位置为基准重绘文本区并定位光标。
func (ed *legacyConsoleLineEditor) redraw() {
	info, err := ed.screenInfo()
	if err != nil {
		return
	}
	x := int(info.CursorPosition.X)
	y := info.CursorPosition.Y
	right := int(info.Window.Right)
	// 清掉从文本起点到行尾的残留（含流式输出残余）。
	if x <= right {
		ed.setCursor(y, x)
		ed.writeSpaces(right - x + 1)
	}
	// 写当前文本。
	ed.setCursor(y, x)
	ed.writeText(string(ed.buf))
	// 光标定位到编辑位。
	ed.setCursor(y, x+ui.DisplayWidth(string(ed.buf[:ed.cur])))
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

func (ed *legacyConsoleLineEditor) setCursor(y int16, x int) {
	var c consoleCoord
	c.Y = y
	c.X = int16(x)
	procSetConsoleCursorPosition.Call(uintptr(ed.out), uintptr(unsafe.Pointer(&c)))
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

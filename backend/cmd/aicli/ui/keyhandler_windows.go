//go:build windows

package ui

import (
	"os"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	windowsEscapeVirtualKeyCode = 0x1B
	windowsEscapePollInterval   = 50 * time.Millisecond
	windowsEscapePeekBytes      = 8
)

var (
	windowsReadSessionEscapeFunc        = windowsReadSessionEscape
	windowsConsumeConsoleInputRecordsFn = consumeConsoleInputRecords
	windowsReadConsoleInputRecordsFn    = readConsoleInputRecords
	windowsWriteConsoleInputRecordsFn   = writeConsoleInputRecords
)

// Start 启动键盘监听（Windows 系统）。
// Windows 控制台没有 Unix SIGUSR2 这类可复用的 ESC 信号。按键必须从
// 当前进程自己的 console/PTY stdin 读取；GetAsyncKeyState 是桌面全局
// 状态，会让同一 Windows Terminal 中的其他 aicli 会话一起收到 ESC。
func (kh *KeyHandler) Start() <-chan bool {
	if kh == nil {
		return nil
	}
	if kh.started.Swap(true) {
		return kh.notifyChan
	}

	kh.enabled.Store(true)

	go func() {
		defer close(kh.doneChan)
		var ticker *time.Ticker
		var ticks <-chan time.Time
		defer func() {
			if ticker != nil {
				ticker.Stop()
			}
		}()

		for {
			polling := kh.armed.Load() && !kh.suspended.Load()
			if polling && ticker == nil {
				ticker = time.NewTicker(windowsEscapePollInterval)
				ticks = ticker.C
			} else if !polling && ticker != nil {
				ticker.Stop()
				ticker = nil
				ticks = nil
			}

			select {
			case <-ticks:
				if kh.armed.Load() && !kh.suspended.Load() && windowsReadSessionEscapeFunc() {
					kh.Notify()
				}
			case <-kh.pollState:
			case <-kh.quitChan:
				return
			}
		}
	}()

	return kh.notifyChan
}

func windowsReadSessionEscape() bool {
	if os.Stdin == nil {
		return false
	}
	handle := windows.Handle(os.Stdin.Fd())
	if records, err := peekConsoleInputRecords(handle, maxSpecialConsoleInputScan); err == nil {
		return consumeLeadingConsoleEscape(handle, records)
	}
	return consumeLeadingPipeEscape(handle, os.Stdin)
}

func consumeLeadingConsoleEscape(handle windows.Handle, records []consoleInputRecord) bool {
	escIndex := -1
	for i := range records {
		if records[i].EventType != consoleKeyEventType {
			continue
		}
		key := (*consoleKeyEventRecord)(unsafe.Pointer(&records[i].Event[0]))
		if key.KeyDown != 0 && key.VirtualKeyCode == windowsEscapeVirtualKeyCode {
			escIndex = i
			break
		}
	}
	if escIndex < 0 {
		return false
	}
	// Drain through the ESC record. Ordinary input queued before it must be
	// preserved instead of being silently discarded together with the ESC:
	// re-inject the prefix after the drain so a typed draft survives the
	// interrupt (plan doc P1-4). A failed re-injection keeps the interrupt
	// (the ESC was already consumed) at the cost of the prefix bytes.
	prefix := records[:escIndex]
	drained, err := windowsReadConsoleInputRecordsFn(handle, escIndex+1)
	if err != nil {
		return false
	}
	if len(drained) < escIndex+1 {
		return false
	}
	if len(prefix) > 0 {
		_ = windowsWriteConsoleInputRecordsFn(handle, prefix)
	}
	return true
}

func consumeLeadingPipeEscape(handle windows.Handle, reader *os.File) bool {
	if reader == nil {
		return false
	}
	buffer := make([]byte, windowsEscapePeekBytes)
	var peeked uint32
	ret, _, callErr := procPeekNamedPipe.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(len(buffer)),
		uintptr(unsafe.Pointer(&peeked)),
		0,
		0,
	)
	if ret == 0 || callErr != syscall.Errno(0) || peeked == 0 || buffer[0] != windowsEscapeVirtualKeyCode {
		return false
	}
	if peeked != 1 {
		// Escape-prefixed multi-byte input is an ANSI navigation/Alt sequence,
		// not a bare ESC cancellation. Leave the complete sequence untouched.
		return false
	}
	var consumed [1]byte
	n, err := reader.Read(consumed[:])
	return err == nil && n == 1 && consumed[0] == windowsEscapeVirtualKeyCode
}

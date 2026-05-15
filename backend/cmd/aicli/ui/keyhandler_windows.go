//go:build windows

package ui

import (
	"time"

	"golang.org/x/sys/windows"
)

const (
	windowsEscapeVirtualKeyCode = 0x1B
	windowsEscapePollInterval   = 50 * time.Millisecond
)

var procGetAsyncKeyState = windows.NewLazySystemDLL("user32.dll").NewProc("GetAsyncKeyState")

// Start 启动键盘监听（Windows 系统）。
// Windows 控制台没有 Unix SIGUSR2 这类可复用的 ESC 信号，因此这里用
// GetAsyncKeyState 做轻量轮询；真正是否取消当前 turn 由调用方决定。
func (kh *KeyHandler) Start() <-chan bool {
	if kh.enabled {
		return kh.notifyChan
	}

	kh.enabled = true

	go func() {
		ticker := time.NewTicker(windowsEscapePollInterval)
		defer ticker.Stop()

		wasDown := false
		for {
			select {
			case <-ticker.C:
				down := windowsEscapeKeyDown()
				if down && !wasDown {
					kh.Notify()
				}
				wasDown = down
			case <-kh.quitChan:
				return
			}
		}
	}()

	return kh.notifyChan
}

func windowsEscapeKeyDown() bool {
	state, _, _ := procGetAsyncKeyState.Call(uintptr(windowsEscapeVirtualKeyCode))
	return state&0x8000 != 0
}

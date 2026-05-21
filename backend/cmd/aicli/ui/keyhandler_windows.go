//go:build windows

package ui

import (
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	windowsEscapeVirtualKeyCode = 0x1B
	windowsEscapePollInterval   = 50 * time.Millisecond
)

var (
	procGetAsyncKeyState         = windows.NewLazySystemDLL("user32.dll").NewProc("GetAsyncKeyState")
	procGetConsoleWindow         = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetConsoleWindow")
	procGetForegroundWindow      = windows.NewLazySystemDLL("user32.dll").NewProc("GetForegroundWindow")
	procGetWindowThreadProcessID = windows.NewLazySystemDLL("user32.dll").NewProc("GetWindowThreadProcessId")

	windowsEscapeKeyDownFunc     = windowsEscapeKeyDown
	windowsConsoleForegroundFunc = windowsConsoleForeground
)

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
				down := windowsConsoleForegroundFunc() && windowsEscapeKeyDownFunc()
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

func windowsConsoleForeground() bool {
	consoleWindow, _, _ := procGetConsoleWindow.Call()
	if consoleWindow == 0 {
		return false
	}
	foregroundWindow, _, _ := procGetForegroundWindow.Call()
	if foregroundWindow == 0 {
		return false
	}
	if foregroundWindow == consoleWindow {
		return true
	}
	foregroundPID := windowsForegroundWindowProcessID(foregroundWindow)
	return foregroundPID != 0 && windowsProcessIsAncestor(uint32(os.Getpid()), foregroundPID)
}

func windowsForegroundWindowProcessID(hwnd uintptr) uint32 {
	var pid uint32
	procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	return pid
}

func windowsProcessIsAncestor(pid, ancestorPID uint32) bool {
	if pid == 0 || ancestorPID == 0 || pid == ancestorPID {
		return pid != 0 && ancestorPID != 0
	}

	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if entry.ProcessID != pid {
			continue
		}
		parentPID := entry.ParentProcessID
		if parentPID == ancestorPID {
			return true
		}
		if parentPID == 0 || parentPID == pid {
			return false
		}
		pid = parentPID
	}
	return false
}

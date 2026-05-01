//go:build windows

package ui

import (
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const maxPeekConsoleInputRecords = 1 << 20

var procPeekConsoleInputW = windows.NewLazySystemDLL("kernel32.dll").NewProc("PeekConsoleInputW")

type consoleInputRecord struct {
	EventType uint16
	_         uint16
	Event     [16]byte
}

type consoleKeyEventRecord struct {
	KeyDown         int32
	RepeatCount     uint16
	VirtualKeyCode  uint16
	VirtualScanCode uint16
	UnicodeChar     uint16
	ControlKeyState uint32
}

func waitForInteractiveInputReady(fd int, timeout time.Duration) (bool, error) {
	if timeout < 0 {
		timeout = 0
	}
	if fd <= 0 {
		return false, nil
	}

	handle := windows.Handle(uintptr(fd))
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}

	for {
		ready, err := hasPendingConsoleKeyEvent(handle)
		if err != nil {
			// If the handle is not a real console input handle, fall back to a
			// conservative timeout path instead of breaking the editor loop.
			if timeout > 0 {
				remaining := time.Until(deadline)
				if remaining > 0 {
					time.Sleep(remaining)
				}
			}
			return false, nil
		}
		if ready {
			return true, nil
		}
		if timeout <= 0 {
			return false, nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false, nil
		}
		sleep := 5 * time.Millisecond
		if remaining < sleep {
			sleep = remaining
		}
		time.Sleep(sleep)
	}
}

func hasPendingConsoleKeyEvent(handle windows.Handle) (bool, error) {
	var eventCount uint32
	if err := windows.GetNumberOfConsoleInputEvents(handle, &eventCount); err != nil {
		return false, err
	}
	if eventCount == 0 {
		return false, nil
	}
	if eventCount > maxPeekConsoleInputRecords {
		eventCount = maxPeekConsoleInputRecords
	}

	records := make([]consoleInputRecord, eventCount)
	var read uint32
	ret, _, callErr := procPeekConsoleInputW.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&records[0])),
		uintptr(eventCount),
		uintptr(unsafe.Pointer(&read)),
	)
	if ret == 0 {
		if callErr != syscall.Errno(0) {
			return false, callErr
		}
		return false, windows.GetLastError()
	}

	for i := 0; i < int(read); i++ {
		if records[i].EventType != windows.KEY_EVENT {
			continue
		}
		key := (*consoleKeyEventRecord)(unsafe.Pointer(&records[i].Event[0]))
		if key.KeyDown != 0 {
			return true, nil
		}
	}
	return false, nil
}

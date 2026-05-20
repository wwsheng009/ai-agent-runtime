//go:build windows

package ui

import (
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const maxPeekConsoleInputRecords = 1 << 20

var (
	procPeekConsoleInputW = windows.NewLazySystemDLL("kernel32.dll").NewProc("PeekConsoleInputW")
	procPeekNamedPipe     = windows.NewLazySystemDLL("kernel32.dll").NewProc("PeekNamedPipe")
)

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

func platformWaitForInteractiveInputReady(fd int, timeout time.Duration) (bool, error) {
	if timeout < 0 {
		timeout = 0
	}
	if fd <= 0 {
		return false, errInteractiveInputReadinessUnsupported
	}

	handle := windows.Handle(uintptr(fd))
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}

	for {
		ready, err := hasPendingConsoleKeyEvent(handle)
		if err != nil {
			// Some Windows terminals expose stdin as a pipe/PTY instead of a
			// console input handle. Console event peeking fails there, but
			// PeekNamedPipe can still provide non-blocking readiness.
			ready, err = hasPendingPipeInput(handle)
			if err != nil {
				// If neither console nor pipe peeking works, callers must
				// choose between direct blocking reads and timeout-based
				// fallbacks instead of looping forever as if there were no input.
				return false, errInteractiveInputReadinessUnsupported
			}
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

func hasPendingPipeInput(handle windows.Handle) (bool, error) {
	var available uint32
	ret, _, callErr := procPeekNamedPipe.Call(
		uintptr(handle),
		0,
		0,
		0,
		uintptr(unsafe.Pointer(&available)),
		0,
	)
	if ret == 0 {
		if callErr != syscall.Errno(0) {
			return false, callErr
		}
		return false, windows.GetLastError()
	}
	return available > 0, nil
}

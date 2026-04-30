//go:build windows

package commands

import (
	"fmt"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const maxPeekConsoleInputRecords = 1 << 20

const windowsInputPasteSettleDelay = 120 * time.Millisecond

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

func platformDiscardPendingConsoleInput() (int, error) {
	handle, err := stdinConsoleHandle()
	if err != nil {
		return 0, err
	}
	var eventCount uint32
	if err := windows.GetNumberOfConsoleInputEvents(handle, &eventCount); err != nil {
		return 0, err
	}
	if err := windows.FlushConsoleInputBuffer(handle); err != nil {
		return 0, err
	}
	return int(eventCount), nil
}

func platformPendingConsoleInputCount() (int, error) {
	handle, err := stdinConsoleHandle()
	if err != nil {
		return 0, err
	}
	var eventCount uint32
	if err := windows.GetNumberOfConsoleInputEvents(handle, &eventCount); err != nil {
		return 0, err
	}
	return int(eventCount), nil
}

func platformPendingConsoleLineInput() (bool, error) {
	handle, err := stdinConsoleHandle()
	if err != nil {
		return false, err
	}
	records, err := peekConsoleInput(handle)
	if err != nil {
		return false, err
	}
	for i := range records {
		if records[i].EventType != windows.KEY_EVENT {
			continue
		}
		key := (*consoleKeyEventRecord)(unsafe.Pointer(&records[i].Event[0]))
		if key.KeyDown == 0 {
			continue
		}
		if key.UnicodeChar == '\r' || key.UnicodeChar == '\n' {
			return true, nil
		}
	}
	return false, nil
}

func platformPendingConsoleTextInput() (bool, error) {
	handle, err := stdinConsoleHandle()
	if err != nil {
		return false, err
	}
	records, err := peekConsoleInput(handle)
	if err != nil {
		return false, err
	}
	for i := range records {
		if records[i].EventType != windows.KEY_EVENT {
			continue
		}
		key := (*consoleKeyEventRecord)(unsafe.Pointer(&records[i].Event[0]))
		if key.KeyDown != 0 && key.UnicodeChar != 0 {
			return true, nil
		}
	}
	return false, nil
}

func platformInputPasteSettleDelay() time.Duration {
	return windowsInputPasteSettleDelay
}

func stdinConsoleHandle() (windows.Handle, error) {
	handle, err := windows.GetStdHandle(windows.STD_INPUT_HANDLE)
	if err != nil {
		return 0, err
	}
	if handle == windows.InvalidHandle || handle == 0 {
		return 0, fmt.Errorf("stdin console handle is invalid")
	}
	return handle, nil
}

func peekConsoleInput(handle windows.Handle) ([]consoleInputRecord, error) {
	var eventCount uint32
	if err := windows.GetNumberOfConsoleInputEvents(handle, &eventCount); err != nil {
		return nil, err
	}
	if eventCount == 0 {
		return nil, nil
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
			return nil, callErr
		}
		return nil, fmt.Errorf("PeekConsoleInputW failed")
	}
	return records[:read], nil
}

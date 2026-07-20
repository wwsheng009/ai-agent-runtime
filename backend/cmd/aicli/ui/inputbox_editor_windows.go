//go:build windows

package ui

import (
	"errors"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	maxPeekConsoleInputRecords  = 1 << 20
	maxSpecialConsoleInputScan  = 64
	windowsClipboardUnicodeText = 13
	windowsVKV                  = 0x56
	windowsVKReturn             = 0x0D
	windowsShiftPressed         = 0x0010
	windowsLeftCtrlPressed      = 0x0008
	windowsRightCtrlPressed     = 0x0004
	windowsClipboardOpenRetries = 5
	windowsClipboardRetryDelay  = 10 * time.Millisecond
	windowsInputPollInterval    = 20 * time.Millisecond
)

var (
	procPeekConsoleInputW          = windows.NewLazySystemDLL("kernel32.dll").NewProc("PeekConsoleInputW")
	procReadConsoleInputW          = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReadConsoleInputW")
	procPeekNamedPipe              = windows.NewLazySystemDLL("kernel32.dll").NewProc("PeekNamedPipe")
	procOpenClipboard              = windows.NewLazySystemDLL("user32.dll").NewProc("OpenClipboard")
	procCloseClipboard             = windows.NewLazySystemDLL("user32.dll").NewProc("CloseClipboard")
	procGetClipboardData           = windows.NewLazySystemDLL("user32.dll").NewProc("GetClipboardData")
	procIsClipboardFormatAvailable = windows.NewLazySystemDLL("user32.dll").NewProc("IsClipboardFormatAvailable")
	procGlobalLock                 = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalLock")
	procGlobalUnlock               = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalUnlock")
	procLstrlenW                   = windows.NewLazySystemDLL("kernel32.dll").NewProc("lstrlenW")
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
		sleep := windowsInputPollInterval
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
	if eventCount > maxSpecialConsoleInputScan {
		eventCount = maxSpecialConsoleInputScan
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
		if consoleKeyEventCanProduceInput(key) {
			return true, nil
		}
	}
	return false, nil
}

func consoleKeyEventCanProduceInput(key *consoleKeyEventRecord) bool {
	if key == nil || key.KeyDown == 0 {
		return false
	}
	if key.UnicodeChar != 0 {
		return true
	}
	if key.VirtualKeyCode == windowsVKV && key.ControlKeyState&(windowsLeftCtrlPressed|windowsRightCtrlPressed) != 0 {
		return true
	}
	switch key.VirtualKeyCode {
	case 0x08, // VK_BACK
		0x09, // VK_TAB
		0x0D, // VK_RETURN
		0x1B, // VK_ESCAPE
		0x21, // VK_PRIOR
		0x22, // VK_NEXT
		0x23, // VK_END
		0x24, // VK_HOME
		0x25, // VK_LEFT
		0x26, // VK_UP
		0x27, // VK_RIGHT
		0x28, // VK_DOWN
		0x2D, // VK_INSERT
		0x2E: // VK_DELETE
		return true
	default:
		return false
	}
}

func platformConsumeSpecialInteractiveKey(fd int) (editorKey, bool, error) {
	if fd <= 0 {
		return editorKey{}, false, nil
	}
	handle := windows.Handle(uintptr(fd))
	records, err := peekConsoleInputRecords(handle, maxSpecialConsoleInputScan)
	if err != nil || len(records) == 0 {
		return editorKey{}, false, err
	}
	noiseRecords := 0
	for i := range records {
		if records[i].EventType == windows.KEY_EVENT {
			key := (*consoleKeyEventRecord)(unsafe.Pointer(&records[i].Event[0]))
			if consoleKeyEventIsModifiedEnterDown(key) {
				for consumed := 0; consumed <= i; consumed++ {
					if err := readConsoleInputRecord(handle); err != nil {
						return editorKey{}, false, err
					}
				}
				return editorKey{kind: editorKeyInsertNewline}, true, nil
			}
			if consoleKeyEventIsCtrlVDown(key) {
				for consumed := 0; consumed <= i; consumed++ {
					if err := readConsoleInputRecord(handle); err != nil {
						return editorKey{}, false, err
					}
				}
				return editorKey{kind: editorKeyPasteClipboard, fromConsoleCtrlV: true}, true, nil
			}
		}
		if consoleInputRecordCanProduceInput(records[i]) {
			if err := consumeConsoleInputRecords(handle, noiseRecords); err != nil {
				return editorKey{}, false, err
			}
			return editorKey{}, false, nil
		}
		noiseRecords++
	}
	if err := consumeConsoleInputRecords(handle, noiseRecords); err != nil {
		return editorKey{}, false, err
	}
	return editorKey{}, false, nil
}

func consoleKeyEventIsModifiedEnterDown(key *consoleKeyEventRecord) bool {
	return key != nil &&
		key.KeyDown != 0 &&
		key.VirtualKeyCode == windowsVKReturn &&
		key.ControlKeyState&windowsShiftPressed != 0
}

func consoleInputRecordCanProduceInput(record consoleInputRecord) bool {
	if record.EventType != windows.KEY_EVENT {
		return false
	}
	key := (*consoleKeyEventRecord)(unsafe.Pointer(&record.Event[0]))
	return consoleKeyEventCanProduceInput(key)
}

func consoleKeyEventIsCtrlVDown(key *consoleKeyEventRecord) bool {
	return key != nil &&
		key.KeyDown != 0 &&
		key.UnicodeChar == 0 &&
		key.VirtualKeyCode == windowsVKV &&
		key.ControlKeyState&(windowsLeftCtrlPressed|windowsRightCtrlPressed) != 0
}

func readConsoleInputRecord(handle windows.Handle) error {
	records := []consoleInputRecord{{}}
	var read uint32
	ret, _, callErr := procReadConsoleInputW.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&records[0])),
		uintptr(1),
		uintptr(unsafe.Pointer(&read)),
	)
	if ret == 0 {
		if callErr != syscall.Errno(0) {
			return callErr
		}
		return windows.GetLastError()
	}
	return nil
}

func consumeConsoleInputRecords(handle windows.Handle, count int) error {
	for i := 0; i < count; i++ {
		if err := readConsoleInputRecord(handle); err != nil {
			return err
		}
	}
	return nil
}

func peekConsoleInputRecords(handle windows.Handle, limit uint32) ([]consoleInputRecord, error) {
	var eventCount uint32
	if err := windows.GetNumberOfConsoleInputEvents(handle, &eventCount); err != nil {
		return nil, err
	}
	if eventCount == 0 {
		return nil, nil
	}
	if limit > 0 && eventCount > limit {
		eventCount = limit
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
		return nil, windows.GetLastError()
	}
	return records[:read], nil
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

func platformClipboardText() (string, error) {
	available, _, _ := procIsClipboardFormatAvailable.Call(windowsClipboardUnicodeText)
	if available == 0 {
		return "", nil
	}
	clipboardOpened := false
	var callErr syscall.Errno
	for attempt := 0; attempt <= windowsClipboardOpenRetries; attempt++ {
		opened, _, err := procOpenClipboard.Call(0)
		if opened != 0 {
			clipboardOpened = true
			callErr = 0
			break
		}
		if errno, ok := err.(syscall.Errno); ok {
			callErr = errno
		}
		if attempt < windowsClipboardOpenRetries {
			time.Sleep(windowsClipboardRetryDelay)
		}
	}
	if callErr != 0 {
		return "", callErr
	}
	if !clipboardOpened {
		return "", errors.New("open clipboard failed")
	}
	defer procCloseClipboard.Call()

	handle, _, err := procGetClipboardData.Call(windowsClipboardUnicodeText)
	if handle == 0 {
		if err != syscall.Errno(0) {
			return "", err
		}
		return "", errors.New("clipboard unicode text unavailable")
	}
	ptr, _, err := procGlobalLock.Call(handle)
	if ptr == 0 {
		if err != syscall.Errno(0) {
			return "", err
		}
		return "", errors.New("lock clipboard data failed")
	}
	defer procGlobalUnlock.Call(handle)

	length, _, _ := procLstrlenW.Call(ptr)
	if length == 0 {
		return "", nil
	}
	text := make([]uint16, int(length))
	byteLength := uintptr(len(text)) * unsafe.Sizeof(text[0])
	var bytesRead uintptr
	if err := windows.ReadProcessMemory(
		windows.CurrentProcess(),
		ptr,
		(*byte)(unsafe.Pointer(&text[0])),
		byteLength,
		&bytesRead,
	); err != nil {
		return "", err
	}
	if bytesRead != byteLength {
		return "", errors.New("read clipboard data returned partial UTF-16 text")
	}
	return windows.UTF16ToString(text), nil
}

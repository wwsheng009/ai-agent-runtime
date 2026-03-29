//go:build windows

package commands

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func platformDiscardPendingConsoleInput() (int, error) {
	handle, err := windows.GetStdHandle(windows.STD_INPUT_HANDLE)
	if err != nil {
		return 0, err
	}
	if handle == windows.InvalidHandle || handle == 0 {
		return 0, fmt.Errorf("stdin console handle is invalid")
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
	handle, err := windows.GetStdHandle(windows.STD_INPUT_HANDLE)
	if err != nil {
		return 0, err
	}
	if handle == windows.InvalidHandle || handle == 0 {
		return 0, fmt.Errorf("stdin console handle is invalid")
	}
	var eventCount uint32
	if err := windows.GetNumberOfConsoleInputEvents(handle, &eventCount); err != nil {
		return 0, err
	}
	return int(eventCount), nil
}

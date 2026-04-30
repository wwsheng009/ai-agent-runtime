//go:build !windows

package commands

import "time"

func platformDiscardPendingConsoleInput() (int, error) {
	return 0, nil
}

func platformPendingConsoleInputCount() (int, error) {
	return 0, nil
}

func platformPendingConsoleLineInput() (bool, error) {
	return false, nil
}

func platformPendingConsoleTextInput() (bool, error) {
	return false, nil
}

func platformInputPasteSettleDelay() time.Duration {
	return 0
}

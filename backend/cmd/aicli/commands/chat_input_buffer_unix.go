//go:build !windows

package commands

func platformDiscardPendingConsoleInput() (int, error) {
	return 0, nil
}

func platformPendingConsoleInputCount() (int, error) {
	return 0, nil
}

//go:build !windows

package consolehost

func platformSupported() bool {
	return false
}

func platformHasConsole() bool {
	return false
}

func platformRunInNewConsole(string, []string) (int, error) {
	return 1, ErrUnsupported
}

//go:build windows

package ui

import "testing"

func TestKeyHandlerStart_DoesNotPrintStartupHint(t *testing.T) {
	kh := NewKeyHandler()
	defer kh.Stop()
	output := captureUIStdout(t, func() {
		if ch := kh.Start(); ch == nil {
			t.Fatal("expected start to return a notification channel")
		}
	})

	if output != "" {
		t.Fatalf("expected no startup output, got %q", output)
	}
}

func TestKeyHandlerStart_IgnoresEscapeWhenConsoleNotForeground(t *testing.T) {
	origEscapeKeyDown := windowsEscapeKeyDownFunc
	origConsoleForeground := windowsConsoleForegroundFunc
	windowsEscapeKeyDownFunc = func() bool { return true }
	windowsConsoleForegroundFunc = func() bool { return false }
	t.Cleanup(func() {
		windowsEscapeKeyDownFunc = origEscapeKeyDown
		windowsConsoleForegroundFunc = origConsoleForeground
	})

	kh := NewKeyHandler()
	defer kh.Stop()
	kh.Start()

	if kh.WaitForESC(windowsEscapePollInterval * 3) {
		t.Fatal("expected ESC polling to ignore key state while console is not foreground")
	}
}

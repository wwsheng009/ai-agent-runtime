//go:build windows

package ui

import "testing"

func TestKeyHandlerStart_DoesNotPrintStartupHint(t *testing.T) {
	kh := NewKeyHandler()
	output := captureUIStdout(t, func() {
		if ch := kh.Start(); ch == nil {
			t.Fatal("expected start to return a notification channel")
		}
	})

	if output != "" {
		t.Fatalf("expected no startup output, got %q", output)
	}
}

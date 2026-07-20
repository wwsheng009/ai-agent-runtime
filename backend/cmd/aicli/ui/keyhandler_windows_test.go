//go:build windows

package ui

import (
	"bytes"
	"io"
	"os"
	"sync/atomic"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

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

func TestKeyHandlerStart_IgnoresMissingSessionEscape(t *testing.T) {
	originalRead := windowsReadSessionEscapeFunc
	windowsReadSessionEscapeFunc = func() bool { return false }
	t.Cleanup(func() {
		windowsReadSessionEscapeFunc = originalRead
	})

	kh := NewKeyHandler()
	defer kh.Stop()
	kh.Start()

	if kh.WaitForESC(windowsEscapePollInterval * 3) {
		t.Fatal("expected handler to ignore ESC absent from this session's stdin")
	}
}

func TestKeyHandlerStart_DoesNotPollWhileDisarmed(t *testing.T) {
	originalRead := windowsReadSessionEscapeFunc
	var reads atomic.Int32
	windowsReadSessionEscapeFunc = func() bool {
		reads.Add(1)
		return true
	}
	t.Cleanup(func() {
		windowsReadSessionEscapeFunc = originalRead
	})

	kh := NewKeyHandler()
	defer kh.Stop()
	kh.Start()

	if kh.WaitForESC(windowsEscapePollInterval * 3) {
		t.Fatal("expected ordinary composer input to keep ESC polling disarmed")
	}
	if got := reads.Load(); got != 0 {
		t.Fatalf("expected no stdin polling while disarmed, got %d reads", got)
	}
}

func TestKeyHandlerStart_NotifiesForSessionEscape(t *testing.T) {
	originalRead := windowsReadSessionEscapeFunc
	windowsReadSessionEscapeFunc = func() bool { return true }
	t.Cleanup(func() {
		windowsReadSessionEscapeFunc = originalRead
	})

	kh := NewKeyHandler()
	defer kh.Stop()
	kh.Start()
	kh.Arm()

	if !kh.WaitForESC(windowsEscapePollInterval * 3) {
		t.Fatal("expected ESC from this session's stdin to be reported")
	}
}

func TestKeyHandlerStart_DoesNotPollSessionInputWhileSuspended(t *testing.T) {
	originalRead := windowsReadSessionEscapeFunc
	var reads atomic.Int32
	windowsReadSessionEscapeFunc = func() bool {
		reads.Add(1)
		return true
	}
	t.Cleanup(func() {
		windowsReadSessionEscapeFunc = originalRead
	})

	kh := NewKeyHandler()
	defer kh.Stop()
	kh.Suspend()
	kh.Start()
	kh.Arm()

	if kh.WaitForESC(windowsEscapePollInterval * 3) {
		t.Fatal("expected suspended handler not to consume the composer's stdin")
	}
	if got := reads.Load(); got != 0 {
		t.Fatalf("expected no stdin polling while suspended, got %d reads", got)
	}
	kh.Resume()
	if !kh.WaitForESC(windowsEscapePollInterval * 3) {
		t.Fatal("expected resumed handler to poll its session input")
	}
}

func TestConsumeLeadingConsoleEscapeConsumesOnlyThroughLeadingEscape(t *testing.T) {
	originalConsume := windowsConsumeConsoleInputRecordsFn
	consumed := 0
	windowsConsumeConsoleInputRecordsFn = func(_ windows.Handle, count int) error {
		consumed = count
		return nil
	}
	t.Cleanup(func() {
		windowsConsumeConsoleInputRecordsFn = originalConsume
	})

	records := []consoleInputRecord{
		{},
		consoleKeyRecord(true, windowsEscapeVirtualKeyCode, 27),
	}
	if !consumeLeadingConsoleEscape(1, records) {
		t.Fatal("expected leading session ESC to be consumed")
	}
	if consumed != 2 {
		t.Fatalf("expected noise plus ESC record to be consumed, got %d", consumed)
	}

	consumed = 0
	records = []consoleInputRecord{
		consoleKeyRecord(true, 'A', 'a'),
		consoleKeyRecord(true, windowsEscapeVirtualKeyCode, 27),
	}
	if consumeLeadingConsoleEscape(1, records) {
		t.Fatal("expected ordinary queued input before ESC to be preserved")
	}
	if consumed != 0 {
		t.Fatalf("expected no records to be consumed before ordinary input, got %d", consumed)
	}
}

func TestConsumeLeadingPipeEscapeOnlyConsumesBareEscape(t *testing.T) {
	tests := []struct {
		name        string
		input       []byte
		wantEscape  bool
		wantPending []byte
	}{
		{
			name:       "bare escape",
			input:      []byte{windowsEscapeVirtualKeyCode},
			wantEscape: true,
		},
		{
			name:        "ANSI navigation sequence",
			input:       []byte{windowsEscapeVirtualKeyCode, '[', 'A'},
			wantPending: []byte{windowsEscapeVirtualKeyCode, '[', 'A'},
		},
		{
			name:        "ordinary input before escape",
			input:       []byte{'a', windowsEscapeVirtualKeyCode},
			wantPending: []byte{'a', windowsEscapeVirtualKeyCode},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatalf("create pipe: %v", err)
			}
			defer reader.Close()

			if _, err := writer.Write(tt.input); err != nil {
				writer.Close()
				t.Fatalf("write pipe input: %v", err)
			}
			if err := writer.Close(); err != nil {
				t.Fatalf("close pipe writer: %v", err)
			}

			gotEscape := consumeLeadingPipeEscape(windows.Handle(reader.Fd()), reader)
			if gotEscape != tt.wantEscape {
				t.Fatalf("consumeLeadingPipeEscape() = %v, want %v", gotEscape, tt.wantEscape)
			}
			pending, err := io.ReadAll(reader)
			if err != nil {
				t.Fatalf("read pending pipe input: %v", err)
			}
			if !bytes.Equal(pending, tt.wantPending) {
				t.Fatalf("pending pipe input = %v, want %v", pending, tt.wantPending)
			}
		})
	}
}

func consoleKeyRecord(down bool, virtualKeyCode uint16, unicodeChar uint16) consoleInputRecord {
	record := consoleInputRecord{EventType: windows.KEY_EVENT}
	key := (*consoleKeyEventRecord)(unsafe.Pointer(&record.Event[0]))
	if down {
		key.KeyDown = 1
	}
	key.VirtualKeyCode = virtualKeyCode
	key.UnicodeChar = unicodeChar
	return record
}

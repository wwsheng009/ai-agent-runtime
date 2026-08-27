//go:build windows

package commands

import (
	"testing"
	"unsafe"
)

func TestNormalizedLegacyConsoleVirtualKey(t *testing.T) {
	tests := []struct {
		name string
		key  *consoleKeyEventRecord
		want uint16
	}{
		{
			name: "nil",
			key:  nil,
			want: 0,
		},
		{
			name: "unicode backspace overrides stale left key",
			key: &consoleKeyEventRecord{
				VirtualKeyCode:  vkLeft,
				VirtualScanCode: 0x4B,
				UnicodeChar:     '\b',
			},
			want: vkBack,
		},
		{
			name: "physical backspace scan code overrides stale left key",
			key: &consoleKeyEventRecord{
				VirtualKeyCode:  vkLeft,
				VirtualScanCode: scanBackspace,
				UnicodeChar:     0,
			},
			want: vkBack,
		},
		{
			name: "real left arrow is preserved",
			key: &consoleKeyEventRecord{
				VirtualKeyCode:  vkLeft,
				VirtualScanCode: 0x4B,
				UnicodeChar:     0,
			},
			want: vkLeft,
		},
		{
			name: "DEL character with VK_BACK remains backspace",
			key: &consoleKeyEventRecord{
				VirtualKeyCode: vkBack,
				UnicodeChar:    0x7F,
			},
			want: vkBack,
		},
		{
			name: "DEL character does not override another physical key",
			key: &consoleKeyEventRecord{
				VirtualKeyCode:  vkDelete,
				VirtualScanCode: 0x53,
				UnicodeChar:     0x7F,
			},
			want: vkDelete,
		},
		{
			name: "scan code cannot override an unrelated key",
			key: &consoleKeyEventRecord{
				VirtualKeyCode:  vkReturn,
				VirtualScanCode: scanBackspace,
				UnicodeChar:     0,
			},
			want: vkReturn,
		},
		{
			name: "control H character is not a physical backspace",
			key: &consoleKeyEventRecord{
				VirtualKeyCode: 0x48,
				UnicodeChar:    '\b',
			},
			want: 0x48,
		},
		{
			name: "backspace scan cannot override delete",
			key: &consoleKeyEventRecord{
				VirtualKeyCode:  vkDelete,
				VirtualScanCode: scanBackspace,
				UnicodeChar:     0,
			},
			want: vkDelete,
		},
		{
			name: "synthetic zero VK requires scan and unicode evidence",
			key: &consoleKeyEventRecord{
				VirtualKeyCode:  0,
				VirtualScanCode: scanBackspace,
				UnicodeChar:     '\b',
			},
			want: vkBack,
		},
		{
			name: "synthetic zero VK with scan only stays unknown",
			key: &consoleKeyEventRecord{
				VirtualKeyCode:  0,
				VirtualScanCode: scanBackspace,
				UnicodeChar:     0,
			},
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizedLegacyConsoleVirtualKey(tt.key); got != tt.want {
				t.Fatalf("normalized key = 0x%02X, want 0x%02X", got, tt.want)
			}
		})
	}
}

func TestLegacyConsoleDispatchPreservesDeleteWithDELCharacter(t *testing.T) {
	editor := &legacyConsoleLineEditor{
		buf: []rune("abcd"),
		cur: 1,
	}
	record := consoleInputRecord{EventType: consoleKeyEventType}
	key := (*consoleKeyEventRecord)(unsafe.Pointer(&record.Event[0]))
	*key = consoleKeyEventRecord{
		KeyDown:         1,
		RepeatCount:     2,
		VirtualKeyCode:  vkDelete,
		VirtualScanCode: 0x53,
		UnicodeChar:     0x7F,
	}

	if submitted := editor.dispatch([]consoleInputRecord{record}); submitted {
		t.Fatal("delete must not submit the input line")
	}
	if got := string(editor.buf); got != "ad" {
		t.Fatalf("buffer = %q, want %q", got, "ad")
	}
	if editor.cur != 1 {
		t.Fatalf("cursor = %d, want 1", editor.cur)
	}
}

func TestLegacyConsoleDispatchTreatsZeroRepeatAsOne(t *testing.T) {
	editor := &legacyConsoleLineEditor{
		buf: []rune("abc"),
		cur: 3,
	}
	record := consoleInputRecord{EventType: consoleKeyEventType}
	key := (*consoleKeyEventRecord)(unsafe.Pointer(&record.Event[0]))
	*key = consoleKeyEventRecord{
		KeyDown:         1,
		RepeatCount:     0,
		VirtualKeyCode:  vkBack,
		VirtualScanCode: scanBackspace,
		UnicodeChar:     '\b',
	}

	editor.dispatch([]consoleInputRecord{record})
	if got := string(editor.buf); got != "ab" {
		t.Fatalf("buffer = %q, want %q", got, "ab")
	}
}

func TestLegacyConsoleDispatchIgnoresKeyUp(t *testing.T) {
	editor := &legacyConsoleLineEditor{
		buf: []rune("abc"),
		cur: 3,
	}
	record := consoleInputRecord{EventType: consoleKeyEventType}
	key := (*consoleKeyEventRecord)(unsafe.Pointer(&record.Event[0]))
	*key = consoleKeyEventRecord{
		KeyDown:         0,
		RepeatCount:     1,
		VirtualKeyCode:  vkBack,
		VirtualScanCode: scanBackspace,
		UnicodeChar:     '\b',
	}

	editor.dispatch([]consoleInputRecord{record})
	if got := string(editor.buf); got != "abc" || editor.cur != 3 {
		t.Fatalf("key-up changed editor: buffer=%q cursor=%d", got, editor.cur)
	}
}

func TestLegacyConsoleDispatchDeletesRepeatedRunesForWin7BackspaceMismatch(t *testing.T) {
	editor := &legacyConsoleLineEditor{
		buf: []rune("a界bc"),
		cur: 3,
	}
	record := consoleInputRecord{EventType: 0x0001}
	key := (*consoleKeyEventRecord)(unsafe.Pointer(&record.Event[0]))
	*key = consoleKeyEventRecord{
		KeyDown:         1,
		RepeatCount:     2,
		VirtualKeyCode:  vkLeft,
		VirtualScanCode: scanBackspace,
		UnicodeChar:     '\b',
	}

	if submitted := editor.dispatch([]consoleInputRecord{record}); submitted {
		t.Fatal("backspace must not submit the input line")
	}
	if got := string(editor.buf); got != "ac" {
		t.Fatalf("buffer = %q, want %q", got, "ac")
	}
	if editor.cur != 1 {
		t.Fatalf("cursor = %d, want 1", editor.cur)
	}
}

func TestLegacyConsoleDispatchPreservesRealLeftArrow(t *testing.T) {
	editor := &legacyConsoleLineEditor{
		buf: []rune("abc"),
		cur: 3,
	}
	record := consoleInputRecord{EventType: consoleKeyEventType}
	key := (*consoleKeyEventRecord)(unsafe.Pointer(&record.Event[0]))
	*key = consoleKeyEventRecord{
		KeyDown:         1,
		RepeatCount:     1,
		VirtualKeyCode:  vkLeft,
		VirtualScanCode: 0x4B,
		UnicodeChar:     0,
	}

	if submitted := editor.dispatch([]consoleInputRecord{record}); submitted {
		t.Fatal("left arrow must not submit the input line")
	}
	if got := string(editor.buf); got != "abc" {
		t.Fatalf("buffer = %q, want unchanged input", got)
	}
	if editor.cur != 2 {
		t.Fatalf("cursor = %d, want 2", editor.cur)
	}
}

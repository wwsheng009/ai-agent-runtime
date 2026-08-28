//go:build windows

package commands

import (
	"context"
	"testing"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestDecodeSystemConsoleLinePreservesUnicodeAndRemovesCRLF(t *testing.T) {
	units := utf16.Encode([]rune("中文输入🙂\r\n"))
	if got := decodeSystemConsoleLine(units); got != "中文输入🙂" {
		t.Fatalf("decodeSystemConsoleLine() = %q, want %q", got, "中文输入🙂")
	}
}

func TestSystemConsoleUnitsContainLineEnding(t *testing.T) {
	if systemConsoleUnitsContainLineEnding([]uint16{'中', '文'}) {
		t.Fatal("plain Unicode input must not report a line ending")
	}
	if !systemConsoleUnitsContainLineEnding([]uint16{'中', '\r'}) {
		t.Fatal("carriage return must report a line ending")
	}
}

func TestReadSystemConsoleUTF16LineAccumulatesUnicodeChunks(t *testing.T) {
	oldRead := readSystemConsoleFn
	chunks := [][]uint16{
		utf16.Encode([]rune("中文")),
		utf16.Encode([]rune("输入🙂\r\n")),
	}
	readSystemConsoleFn = func(
		_ windows.Handle,
		buffer *uint16,
		toRead uint32,
		read *uint32,
		_ *byte,
	) error {
		chunk := chunks[0]
		chunks = chunks[1:]
		n := copy(unsafe.Slice(buffer, int(toRead)), chunk)
		*read = uint32(n)
		return nil
	}
	defer func() { readSystemConsoleFn = oldRead }()

	got, err := readSystemConsoleUTF16Line(context.Background(), windows.Handle(1))
	if err != nil {
		t.Fatalf("readSystemConsoleUTF16Line() error = %v", err)
	}
	if got != "中文输入🙂" {
		t.Fatalf("line = %q, want %q", got, "中文输入🙂")
	}
	if len(chunks) != 0 {
		t.Fatalf("unconsumed chunks = %d", len(chunks))
	}
}

func TestLegacyConsoleDispatchAcceptsCommittedIMEProcessCharacter(t *testing.T) {
	editor := &legacyConsoleLineEditor{
		buf: []rune("开始"),
		cur: 2,
	}
	record := consoleInputRecord{EventType: consoleKeyEventType}
	key := (*consoleKeyEventRecord)(unsafe.Pointer(&record.Event[0]))
	*key = consoleKeyEventRecord{
		KeyDown:        1,
		RepeatCount:    1,
		VirtualKeyCode: vkProcessKey,
		UnicodeChar:    '中',
	}

	if submitted := editor.dispatch([]consoleInputRecord{record}); submitted {
		t.Fatal("committed IME character must not submit the line")
	}
	if got := string(editor.buf); got != "开始中" {
		t.Fatalf("buffer = %q, want %q", got, "开始中")
	}
	if editor.cur != 3 {
		t.Fatalf("cursor = %d, want 3", editor.cur)
	}
}

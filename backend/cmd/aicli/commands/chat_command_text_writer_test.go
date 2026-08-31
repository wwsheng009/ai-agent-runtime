package commands

import (
	"bytes"
	"strings"
	"testing"
)

// TestCommandTextWriterNilWriterFailsFast：协议输出不允许隐式回落，
// nil writer 必须在构造期失败。
func TestCommandTextWriterNilWriterFailsFast(t *testing.T) {
	if _, err := NewCommandTextWriter(nil, CommandOutputPlain); err == nil {
		t.Fatal("nil writer must fail at construction")
	}
}

// TestCommandTextWriterWriteTextNormalizesTrailingNewline：WriteText 自动
// 补齐末尾换行；空文本不产生写入。
func TestCommandTextWriterWriteTextNormalizesTrailingNewline(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewCommandTextWriter(&buf, CommandOutputPlain)
	if err != nil {
		t.Fatalf("NewCommandTextWriter: %v", err)
	}
	if err := w.WriteText("hello"); err != nil {
		t.Fatalf("WriteText: %v", err)
	}
	if got := buf.String(); got != "hello\n" {
		t.Fatalf("got %q, want %q", got, "hello\n")
	}

	buf.Reset()
	if err := w.WriteText("   "); err != nil {
		t.Fatalf("WriteText blank: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("blank text must not write, got %q", buf.String())
	}
}

// TestCommandTextWriterModeMismatch：plain 模式 writer 拒绝 JSON 写入。
func TestCommandTextWriterModeMismatch(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewCommandTextWriter(&buf, CommandOutputPlain)
	if err != nil {
		t.Fatalf("NewCommandTextWriter: %v", err)
	}
	if err := w.WriteJSON(map[string]string{"error": "x"}); err == nil {
		t.Fatal("JSON write on plain-mode writer must fail")
	}
	if buf.Len() != 0 {
		t.Fatalf("failed write must not emit bytes, got %q", buf.String())
	}
}

// TestCommandTextWriterWriteJSON：JSON 模式输出缩进 JSON。
func TestCommandTextWriterWriteJSON(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewCommandTextWriter(&buf, CommandOutputJSON)
	if err != nil {
		t.Fatalf("NewCommandTextWriter: %v", err)
	}
	if err := w.WriteJSON(map[string]string{"ok": "true"}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "\"ok\": \"true\"") {
		t.Fatalf("expected indented JSON, got %q", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("JSON output must end with newline, got %q", out)
	}
}

// TestCommandTextWriterModeAccessors：Mode/Writer 访问器返回构造值。
func TestCommandTextWriterModeAccessors(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewCommandTextWriter(&buf, CommandOutputJSON)
	if err != nil {
		t.Fatalf("NewCommandTextWriter: %v", err)
	}
	if w.Mode() != CommandOutputJSON {
		t.Fatalf("Mode() = %v, want json", w.Mode())
	}
	if w.Writer() != &buf {
		t.Fatal("Writer() must return the constructed writer")
	}

	stdout := NewStdoutCommandTextWriter(CommandOutputPlain)
	if stdout.Mode() != CommandOutputPlain {
		t.Fatalf("stdout writer Mode() = %v, want plain", stdout.Mode())
	}
	if stdout.Writer() == nil {
		t.Fatal("stdout writer must wrap a non-nil writer")
	}
}

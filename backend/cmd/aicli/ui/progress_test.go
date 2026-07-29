package ui

import (
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/motion"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestProgressDocumentClampsValues(t *testing.T) {
	p := NewProgress(10).SetWidth(4)
	p.Set(-3)
	if got := p.Document().PlainText(); got != "[    ] 0.0% 0/10" {
		t.Fatalf("negative progress was not clamped: %q", got)
	}
	p.Set(20)
	if got := p.Document().PlainText(); got != "[████] 100.0% 10/10" {
		t.Fatalf("overflow progress was not clamped: %q", got)
	}
}

func TestProgressANSI16UsesNegotiatedBackend(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("FORCE_COLOR", "1")
	t.Setenv("AICLI_COLOR_DEPTH", "ansi16")

	p := NewProgress(10).SetWidth(4)
	p.Set(5)
	got := p.Format()
	if !strings.Contains(got, "\x1b[") {
		t.Fatalf("expected ANSI-16 output, got %q", got)
	}
	if strings.Contains(got, "38;5;") || strings.Contains(got, "38;2;") {
		t.Fatalf("high-depth color leaked into ANSI-16 output: %q", got)
	}
	if plain := render.ANSIToPlain(got); plain != "[██  ] 50.0% 5/10" {
		t.Fatalf("plain projection changed: %q", plain)
	}
}

func TestSpinnerSanitizesMessageAndCanRestart(t *testing.T) {
	oldPolicy := motion.Global()
	motion.SetGlobal(motion.NewPolicy(motion.Config{Forced: motion.ForceMode(motion.ModeOff)}))
	defer motion.SetGlobal(oldPolicy)

	s := NewSpinner("你好\x1b[31m\n完成")
	plain := s.Document().PlainText()
	if strings.Contains(plain, "\x1b") || strings.Contains(plain, "\n") {
		t.Fatalf("spinner message was not sanitized: %q", plain)
	}
	if !strings.Contains(plain, "你好 完成") {
		t.Fatalf("spinner message content changed: %q", plain)
	}

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	defer func() { os.Stdout = oldStdout }()

	s.Start()
	time.Sleep(10 * time.Millisecond)
	s.Stop()
	s.mu.Lock()
	firstStop := s.stopChan
	s.mu.Unlock()
	s.Start()
	time.Sleep(10 * time.Millisecond)
	s.mu.Lock()
	reused := s.stopChan == firstStop
	s.mu.Unlock()
	if reused {
		t.Fatal("spinner restart reused a closed stop channel")
	}
	s.Stop()
	_ = writer.Close()
	_, _ = io.ReadAll(reader)
	_ = reader.Close()
}

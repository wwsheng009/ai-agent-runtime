package commands

import (
	"bytes"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

type fakeChatOutputSurface struct {
	count atomic.Int32
}

func (s *fakeChatOutputSurface) BeginOutput() {
	s.count.Add(1)
}

func TestChatSystemOutputWriter_IndentsEachCompletedLine(t *testing.T) {
	var output bytes.Buffer
	writer := newChatSystemOutputWriter(&output)

	if _, err := writer.Write([]byte("[Manager] MCP 已启动: toolkit (工具: 13)\n[Manager] 加载工具失败: x\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	rendered := output.String()
	for _, expected := range []string{
		ui.FormatAssistantSupplementBlock("[Manager] MCP 已启动: toolkit (工具: 13)"),
		ui.FormatAssistantSupplementBlock("[Manager] 加载工具失败: x"),
	} {
		if !bytes.Contains([]byte(rendered), []byte(expected)) {
			t.Fatalf("expected rendered output to contain %q, got %q", expected, rendered)
		}
	}
}

func TestChatSystemOutputWriter_CollapsesConsecutiveBlankLines(t *testing.T) {
	var output bytes.Buffer
	writer := newChatSystemOutputWriter(&output)

	if _, err := writer.Write([]byte("[Manager] ready\n\n\n[Manager] done\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	rendered := output.String()
	if strings.Contains(rendered, "\n\n\n") {
		t.Fatalf("expected blank lines to collapse, got %q", rendered)
	}
	if !strings.Contains(rendered, ui.FormatAssistantSupplementBlock("[Manager] ready")) {
		t.Fatalf("expected first line to remain visible, got %q", rendered)
	}
	if !strings.Contains(rendered, ui.FormatAssistantSupplementBlock("[Manager] done")) {
		t.Fatalf("expected second line to remain visible, got %q", rendered)
	}
}

func TestChatSystemOutputWriter_BeginsSurfaceOutputForRenderedLines(t *testing.T) {
	var output bytes.Buffer
	surface := &fakeChatOutputSurface{}
	writer := newChatSystemOutputWriterWithSurface(&output, surface)

	if _, err := writer.Write([]byte("[Manager] ready\n[Manager] done\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	if got := surface.count.Load(); got != 2 {
		t.Fatalf("expected surface BeginOutput per rendered line, got %d", got)
	}
}

func TestChatSystemOutputWriter_BeginsSurfaceOutputForVisibleBlankLine(t *testing.T) {
	var output bytes.Buffer
	surface := &fakeChatOutputSurface{}
	writer := newChatSystemOutputWriterWithSurface(&output, surface)

	if _, err := writer.Write([]byte("[Manager] ready\n\n\n[Manager] done\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	if got := surface.count.Load(); got != 3 {
		t.Fatalf("expected collapsed visible lines to begin surface output, got %d", got)
	}
}

func TestChatSystemOutputWriter_FlushesPartialLineAfterDelay(t *testing.T) {
	var output bytes.Buffer
	writer := newChatSystemOutputWriter(&output).(*chatSystemOutputWriter)
	writer.partialFlushDelay = 10 * time.Millisecond

	if _, err := writer.Write([]byte("progress 10%")); err != nil {
		t.Fatalf("write: %v", err)
	}

	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		if strings.Contains(output.String(), ui.FormatAssistantSupplementBlock("progress 10%")) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected partial line to flush after delay, got %q", output.String())
}

func TestChatSystemOutputWriter_TreatsCarriageReturnAsProgressLine(t *testing.T) {
	var output bytes.Buffer
	writer := newChatSystemOutputWriter(&output)

	if _, err := writer.Write([]byte("progress 10%\rprogress 20%")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if flusher, ok := writer.(interface{ Flush() error }); ok {
		if err := flusher.Flush(); err != nil {
			t.Fatalf("flush: %v", err)
		}
	}

	rendered := output.String()
	for _, expected := range []string{"progress 10%", "progress 20%"} {
		if !strings.Contains(rendered, ui.FormatAssistantSupplementBlock(expected)) {
			t.Fatalf("expected rendered output to contain %q, got %q", expected, rendered)
		}
	}
}

package commands

import (
	"bytes"
	"io"
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

type fakeAtomicChatOutputSurface struct {
	beginCount atomic.Int32
	writeCount atomic.Int32
}

func (s *fakeAtomicChatOutputSurface) BeginOutput() {
	s.beginCount.Add(1)
}

func (s *fakeAtomicChatOutputSurface) WriteOutput(writer io.Writer, text string) (int, error, bool) {
	s.writeCount.Add(1)
	n, err := io.WriteString(writer, text)
	return n, err, true
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

func TestChatSystemOutputWriter_BeginsSurfaceOutputOncePerWrite(t *testing.T) {
	var output bytes.Buffer
	surface := &fakeChatOutputSurface{}
	writer := newChatSystemOutputWriterWithSurface(&output, surface)

	if _, err := writer.Write([]byte("[Manager] ready\n[Manager] done\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	if got := surface.count.Load(); got != 1 {
		t.Fatalf("expected surface BeginOutput once per write, got %d", got)
	}
}

func TestChatSystemOutputWriter_BeginsSurfaceOutputForSeparateWrites(t *testing.T) {
	var output bytes.Buffer
	surface := &fakeChatOutputSurface{}
	writer := newChatSystemOutputWriterWithSurface(&output, surface)

	if _, err := writer.Write([]byte("[Manager] ready\n")); err != nil {
		t.Fatalf("write first: %v", err)
	}
	if _, err := writer.Write([]byte("[Manager] done\n")); err != nil {
		t.Fatalf("write second: %v", err)
	}

	if got := surface.count.Load(); got != 2 {
		t.Fatalf("expected separate writes to begin surface output separately, got %d", got)
	}
}

func TestChatSystemOutputWriter_BeginsSurfaceOutputOnceForVisibleBlankLines(t *testing.T) {
	var output bytes.Buffer
	surface := &fakeChatOutputSurface{}
	writer := newChatSystemOutputWriterWithSurface(&output, surface)

	if _, err := writer.Write([]byte("[Manager] ready\n\n\n[Manager] done\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	if got := surface.count.Load(); got != 1 {
		t.Fatalf("expected collapsed visible lines in one write to share surface output begin, got %d", got)
	}
}

func TestChatSystemOutputWriter_UsesAtomicSurfaceOutputWhenAvailable(t *testing.T) {
	var output bytes.Buffer
	surface := &fakeAtomicChatOutputSurface{}
	writer := newChatSystemOutputWriterWithSurface(&output, surface)

	if _, err := writer.Write([]byte("[Manager] ready\n[Manager] done\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	if got := surface.beginCount.Load(); got != 0 {
		t.Fatalf("expected atomic surface path not to use separate BeginOutput, got %d", got)
	}
	if got := surface.writeCount.Load(); got != 2 {
		t.Fatalf("expected one atomic surface write per rendered line, got %d", got)
	}
	if !strings.Contains(output.String(), ui.FormatAssistantSupplementBlock("[Manager] done")) {
		t.Fatalf("expected rendered output to be written, got %q", output.String())
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

func TestChatLimitedSystemOutputWriter_TruncatesByLineLimit(t *testing.T) {
	var output bytes.Buffer
	writer := newLimitedChatSystemOutputWriterWithSurface(&output, nil, 3, 1024)

	if _, err := writer.Write([]byte("line-1\nline-2\nline-3\nline-4\nline-5\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	rendered := output.String()
	for _, expected := range []string{"line-1", "line-2", "line-3", chatLiveToolOutputLimitNotice} {
		if !strings.Contains(rendered, ui.FormatAssistantSupplementBlock(expected)) {
			t.Fatalf("expected rendered output to contain %q, got %q", expected, rendered)
		}
	}
	if strings.Contains(rendered, ui.FormatAssistantSupplementBlock("line-4")) {
		t.Fatalf("expected line after limit to be suppressed, got %q", rendered)
	}
}

func TestChatLimitedSystemOutputWriter_TruncatesSingleLongLineByByteLimit(t *testing.T) {
	var output bytes.Buffer
	writer := newLimitedChatSystemOutputWriterWithSurface(&output, nil, 10, 12)

	if _, err := writer.Write([]byte("abcdefghijklmnopqrstuvwxyz")); err != nil {
		t.Fatalf("write: %v", err)
	}

	rendered := output.String()
	if !strings.Contains(rendered, ui.FormatAssistantSupplementBlock("abcdefghijkl")) {
		t.Fatalf("expected byte-limited prefix to be rendered, got %q", rendered)
	}
	if strings.Contains(rendered, "mnopqrstuvwxyz") {
		t.Fatalf("expected suffix after byte limit to be suppressed, got %q", rendered)
	}
	if !strings.Contains(rendered, ui.FormatAssistantSupplementBlock(chatLiveToolOutputLimitNotice)) {
		t.Fatalf("expected truncation notice, got %q", rendered)
	}
}

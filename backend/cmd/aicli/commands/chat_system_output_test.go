package commands

import (
	"bytes"
	"strings"
	"sync/atomic"
	"testing"

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

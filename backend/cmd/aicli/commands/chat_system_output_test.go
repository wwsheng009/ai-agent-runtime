package commands

import (
	"bytes"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
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

type fakeChatToolOutputStageSink struct {
	mu      sync.Mutex
	callIDs []string
	details []string
}

func (s *fakeChatToolOutputStageSink) SetToolAgentStage(callID, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.callIDs = append(s.callIDs, callID)
	s.details = append(s.details, detail)
}

func (s *fakeChatToolOutputStageSink) snapshot() (callIDs, details []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.callIDs...), append([]string(nil), s.details...)
}

type fakeChatSupplementSink struct {
	mu    sync.Mutex
	lines []string
}

func (s *fakeChatSupplementSink) RenderLocalSupplement(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, line)
}

func (s *fakeChatSupplementSink) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lines...)
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

func TestChatSystemOutputWriter_SemanticSinkPublishesRawLinesWithoutTerminalWriter(t *testing.T) {
	sink := &fakeChatSupplementSink{}
	writer := newChatSystemOutputWriterWithSemanticSink(sink)

	if _, err := writer.Write([]byte("[Manager] MCP started\n\n[Manager] loaded tools\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := sink.snapshot()
	want := []string{"[Manager] MCP started", "[Manager] loaded tools"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("semantic lines=%q, want %q", got, want)
	}
	for _, line := range got {
		if strings.Contains(line, "\x1b[") || strings.Contains(line, "  ") {
			t.Fatalf("semantic line contains immediate-mode formatting: %q", line)
		}
	}
}

func TestChatLimitedSystemOutputWriter_SemanticSinkRetainsLimitPolicy(t *testing.T) {
	sink := &fakeChatSupplementSink{}
	writer := newLimitedChatSystemOutputWriterWithSemanticSink(sink, 1, 1024)

	if _, err := writer.Write([]byte("first\nsecond\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := sink.snapshot()
	want := []string{"first", chatLiveToolOutputLimitNotice}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("semantic limited lines=%q, want %q", got, want)
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

func TestChatSystemOutputWriter_ActiveTurnMirrorSurvivesOwnedViewportRepaint(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	const width, height = 80, 30
	surface := ui.NewFixedBottomSurface(ui.NewTerminal())
	surface.EnableForTest(width, height)
	screen := newScreenVT(width, height)

	stream := captureSurfaceStdout(t, func() {
		_, err, handled := surface.WriteOutput(os.Stdout, "seed-transcript-marker\n")
		if err != nil || !handled {
			t.Fatalf("seed output: handled=%t err=%v", handled, err)
		}
		surface.ShowPrompt("> ")
		surface.SetActiveBand([]string{"• Running shell tool"})

		// This is the production active-turn output mirror constructor. Its
		// output must become retained history before the following repaint.
		mirror := newLimitedChatSystemOutputWriterWithSurface(
			os.Stdout,
			surface,
			maxToolResultPreviewLines,
			maxToolResultPreviewBytes,
		)
		if _, err := mirror.Write([]byte("tool-progress-marker\n")); err != nil {
			t.Fatalf("mirror write: %v", err)
		}

		surface.SetStatusModels(style.StatusLineModel{State: style.RunRunning}, nil)
		surface.SetActiveBand([]string{
			"• Running shell tool",
			"  repaint-marker",
		})
	})
	screen.feed(stream)

	for _, marker := range []string{
		"seed-transcript-marker",
		"tool-progress-marker",
		"repaint-marker",
	} {
		if rows := screen.RowsContaining(marker); len(rows) != 1 {
			t.Fatalf("%q physical rows=%v want exactly one:\n%s", marker, rows, screen.dump())
		}
		if count := strings.Count(composedSurfaceFrameText(surface), marker); count != 1 {
			t.Fatalf("%q composed-frame count=%d want 1:\n%s", marker, count, composedSurfaceFrameText(surface))
		}
	}

	bottomStart := height - len(surface.BottomRowsSnapshot()) + 1
	if run, at := maxBlankRunAboveBottom(screen, bottomStart); run > 2 {
		t.Fatalf("mirror commit followed by repaint left %d blank rows at %d:\n%s", run, at, screen.dump())
	}

	screen.feed(captureSurfaceStdout(t, func() {
		surface.ClearActiveBand()
	}))
	if rows := screen.RowsContaining("tool-progress-marker"); len(rows) != 1 {
		t.Fatalf("active-band shrink lost or duplicated mirror output: rows=%v\n%s", rows, screen.dump())
	}
}

func TestChatSystemOutputWriter_FlushesPartialLineAfterDelay(t *testing.T) {
	// The partial flush runs from the writer's timer goroutine. Use the
	// package's synchronized test buffer so polling the observable output does
	// not race with that write under -race.
	var output synchronizedBuffer
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

func composedSurfaceFrameText(surface *ui.FixedBottomSurface) string {
	var text strings.Builder
	for _, row := range surface.ComposedFrameForTest() {
		for _, cell := range row {
			if !cell.Cont {
				text.WriteString(cell.Text)
			}
		}
		text.WriteByte('\n')
	}
	return text.String()
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

func TestChatLiveToolOutputWriter_UsesActiveStageWithoutCommittingRawBytes(t *testing.T) {
	var output bytes.Buffer
	sink := &fakeChatToolOutputStageSink{}
	writer := newLimitedChatToolOutputWriter(&output, nil, sink, "call-shell-1", "execute_shell_command", 8, 1024)

	if _, err := writer.Write([]byte("raw-tool-marker\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("raw live tool output entered retained writer: %q", output.String())
	}
	callIDs, details := sink.snapshot()
	if len(callIDs) == 0 || callIDs[len(callIDs)-1] != "call-shell-1" {
		t.Fatalf("stage owner call IDs=%v, want stable tool call", callIDs)
	}
	if len(details) == 0 || !strings.Contains(details[len(details)-1], "raw-tool-marker") {
		t.Fatalf("stage details=%v, want raw marker", details)
	}
}

func TestChatLiveToolOutputWriter_LimitNoticeStaysInActiveStage(t *testing.T) {
	var output bytes.Buffer
	sink := &fakeChatToolOutputStageSink{}
	writer := newLimitedChatToolOutputWriter(&output, nil, sink, "call-shell-2", "shell", 1, 1024)

	if _, err := writer.Write([]byte("first\nsecond\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("limit notice or raw bytes entered retained writer: %q", output.String())
	}
	_, details := sink.snapshot()
	if len(details) == 0 || !strings.Contains(strings.Join(details, "\\n"), chatLiveToolOutputLimitNotice) {
		t.Fatalf("active stage details=%v, want limit notice", details)
	}
}

func TestChatLiveToolOutputWriter_WithoutStableOwnerFallsBackToRetainedWriter(t *testing.T) {
	var output bytes.Buffer
	sink := &fakeChatToolOutputStageSink{}
	writer := newLimitedChatToolOutputWriter(&output, nil, sink, "", "shell", 8, 1024)

	if _, ok := writer.(*chatLimitedSystemOutputWriter); !ok {
		t.Fatalf("identity-less mirror type=%T, want retained compatibility writer", writer)
	}
	if _, err := writer.Write([]byte("fallback-marker\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(output.String(), ui.FormatAssistantSupplementBlock("fallback-marker")) {
		t.Fatalf("identity-less output was lost: %q", output.String())
	}
}

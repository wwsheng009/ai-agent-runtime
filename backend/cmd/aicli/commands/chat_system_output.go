package commands

import (
	"io"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

const (
	chatSystemOutputPartialFlushDelay = 150 * time.Millisecond
	chatLiveToolOutputLimitNotice     = "... (实时命令输出已达到显示上限，后续输出已折叠；命令仍会继续执行并由 capture limit 处理结果)"
)

type chatSystemOutputWriter struct {
	writer            io.Writer
	surface           chatOutputSurface
	semanticSink      chatSupplementSink
	buffer            strings.Builder
	mu                sync.Mutex
	lastBlank         bool
	partialFlushDelay time.Duration
	partialTimer      *time.Timer
}

// chatLimitedSystemOutputWriter only caps the live terminal mirror. The
// command capture/result path remains governed by executor capture limits.
type chatLimitedSystemOutputWriter struct {
	writer io.Writer

	mu                        sync.Mutex
	maxLines                  int
	maxBytes                  int
	renderedLines             int
	renderedBytes             int
	suppressed                bool
	noticeWritten             bool
	lastForwardedEndedNewline bool
}

// chatToolOutputStageSink is the live-only projection boundary for raw command
// bytes. It deliberately has no transcript or terminal write method: a tool's
// completed result owns the one durable tool-chain cell, while raw output can
// only update the mutable ActiveBand stage.
type chatToolOutputStageSink interface {
	SetToolAgentStage(callID, detail string)
}

// chatLiveToolOutputWriter mirrors raw shell output into the currently running
// tool stage. Unlike chatSystemOutputWriter it never writes its bytes to the
// terminal/history writer. This prevents the raw mirror and the normalized
// tool_result block from independently committing the same output.
//
// A stable tool_call_id is required. Without it concurrent tools cannot be
// fenced safely, so callers must use the retained legacy writer as a fallback.
type chatLiveToolOutputWriter struct {
	sink       chatToolOutputStageSink
	toolCallID string
	toolName   string

	mu             sync.Mutex
	maxLines       int
	maxBytes       int
	renderedLines  int
	renderedBytes  int
	suppressed     bool
	noticeRendered bool
	partial        strings.Builder
}

type chatOutputSurface interface {
	BeginOutput()
}

// chatSupplementSink is the semantic boundary for system/MCP notices. Its
// implementation owns Scene/AppState publication; this writer only performs
// stream chunking, normalization, and the existing display limits.
type chatSupplementSink interface {
	RenderLocalSupplement(line string)
}

type chatAtomicOutputSurface interface {
	WriteOutput(io.Writer, string) (int, error, bool)
}

func newChatSystemOutputWriter(writer io.Writer) io.Writer {
	return newChatSystemOutputWriterWithSurface(writer, nil)
}

func newChatSystemOutputWriterWithSurface(writer io.Writer, surface chatOutputSurface) io.Writer {
	if writer == nil {
		return nil
	}
	return &chatSystemOutputWriter{
		writer:            writer,
		surface:           surface,
		partialFlushDelay: chatSystemOutputPartialFlushDelay,
	}
}

// newChatSystemOutputWriterWithSemanticSink builds the unified-renderer
// variant of the system/MCP status writer. It never formats or writes terminal
// bytes itself. Completed status lines are submitted as semantic supplement
// cells, so TerminalSession remains the sole physical writer.
func newChatSystemOutputWriterWithSemanticSink(sink chatSupplementSink) io.Writer {
	if sink == nil {
		return nil
	}
	return &chatSystemOutputWriter{
		semanticSink:      sink,
		partialFlushDelay: chatSystemOutputPartialFlushDelay,
	}
}

func newLimitedChatSystemOutputWriterWithSurface(writer io.Writer, surface chatOutputSurface, maxLines, maxBytes int) io.Writer {
	base := newChatSystemOutputWriterWithSurface(writer, surface)
	return newLimitedChatSystemOutputWriter(base, maxLines, maxBytes)
}

// newLimitedChatSystemOutputWriterWithSemanticSink is the capped semantic
// system/MCP writer. It is intentionally separate from the legacy surface
// constructor so callers cannot accidentally retain a direct terminal writer
// while migrating to the unified renderer.
func newLimitedChatSystemOutputWriterWithSemanticSink(sink chatSupplementSink, maxLines, maxBytes int) io.Writer {
	base := newChatSystemOutputWriterWithSemanticSink(sink)
	return newLimitedChatSystemOutputWriter(base, maxLines, maxBytes)
}

func newLimitedChatSystemOutputWriter(base io.Writer, maxLines, maxBytes int) io.Writer {
	if base == nil {
		return nil
	}
	if maxLines <= 0 && maxBytes <= 0 {
		return base
	}
	return &chatLimitedSystemOutputWriter{
		writer:                    base,
		maxLines:                  maxLines,
		maxBytes:                  maxBytes,
		lastForwardedEndedNewline: true,
	}
}

// newLimitedChatToolOutputWriter selects the scoped live-tool projection when
// the caller has a stable runtime identity and the owned interaction surface.
// The fallback intentionally retains the old behavior for non-interactive
// callers: there is no ActiveBand owner in that mode, so dropping raw output
// would be data loss rather than de-duplication.
func newLimitedChatToolOutputWriter(writer io.Writer, surface chatOutputSurface, sink chatToolOutputStageSink, toolCallID, toolName string, maxLines, maxBytes int) io.Writer {
	toolCallID = strings.TrimSpace(toolCallID)
	toolName = strings.TrimSpace(toolName)
	if sink == nil || toolCallID == "" || toolName == "" {
		return newLimitedChatSystemOutputWriterWithSurface(writer, surface, maxLines, maxBytes)
	}
	return &chatLiveToolOutputWriter{
		sink:       sink,
		toolCallID: toolCallID,
		toolName:   toolName,
		maxLines:   maxLines,
		maxBytes:   maxBytes,
	}
}

func (w *chatSystemOutputWriter) Write(p []byte) (int, error) {
	if w == nil || !w.hasOutputTarget() {
		return len(p), nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	chunk := normalizeChatSystemOutputChunk(p)
	w.buffer.WriteString(chunk)
	renderedAny := false
	outputBegun := false
	beginOutput := func() {
		if outputBegun {
			return
		}
		w.beginOutput()
		outputBegun = true
	}
	writeOutput := func(text string) error {
		if atomicSurface, ok := w.surface.(chatAtomicOutputSurface); ok {
			if _, err, handled := atomicSurface.WriteOutput(w.writer, text); handled {
				return err
			}
		}
		beginOutput()
		_, err := ui.WriteTerminalText(w.writer, text)
		return err
	}
	for {
		content := w.buffer.String()
		index := strings.IndexByte(content, '\n')
		if index < 0 {
			break
		}
		line := content[:index]
		remaining := content[index+1:]
		w.buffer.Reset()
		w.buffer.WriteString(remaining)
		if w.semanticSink != nil {
			// Empty lines are only a visual separator in the immediate-mode
			// writer. A standalone empty Scene cell carries no semantic content,
			// so it is deliberately suppressed here.
			if strings.TrimSpace(line) == "" {
				continue
			}
			w.lastBlank = false
			w.semanticSink.RenderLocalSupplement(line)
			renderedAny = true
			continue
		}
		rendered := ui.FormatAssistantSupplementBlock(line)
		if strings.TrimSpace(rendered) == "" {
			if w.lastBlank {
				continue
			}
			w.lastBlank = true
			if err := writeOutput("\n"); err != nil {
				return 0, err
			}
			renderedAny = true
			continue
		}
		w.lastBlank = false
		if err := writeOutput(rendered + "\n"); err != nil {
			return 0, err
		}
		renderedAny = true
	}
	if renderedAny && w.writer != nil {
		_ = flushChatOutputWriter(w.writer)
	}
	if strings.TrimSpace(w.buffer.String()) != "" {
		w.schedulePartialFlushLocked()
	} else {
		w.stopPartialFlushLocked()
	}
	return len(p), nil
}

func (w *chatLimitedSystemOutputWriter) Write(p []byte) (int, error) {
	if w == nil || w.writer == nil {
		return len(p), nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(p) == 0 {
		return 0, nil
	}
	if w.suppressed {
		return len(p), nil
	}

	allowed, exceeded := w.takeAllowedLocked(normalizeChatSystemOutputChunk(p))
	if allowed != "" {
		if _, err := w.writer.Write([]byte(allowed)); err != nil {
			return 0, err
		}
		w.lastForwardedEndedNewline = strings.HasSuffix(allowed, "\n")
	}
	if exceeded {
		w.suppressed = true
		if err := w.writeLimitNoticeLocked(); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (w *chatLiveToolOutputWriter) Write(p []byte) (int, error) {
	if w == nil || w.sink == nil {
		return len(p), nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(p) == 0 || w.suppressed {
		return len(p), nil
	}
	allowed, exceeded := w.takeAllowedLocked(normalizeChatSystemOutputChunk(p))
	if allowed != "" {
		w.publishChunkLocked(allowed)
	}
	if exceeded {
		w.suppressed = true
		w.publishNoticeLocked()
	}
	return len(p), nil
}

func (w *chatLimitedSystemOutputWriter) Flush() error {
	if w == nil || w.writer == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return flushChatOutputWriter(w.writer)
}

// Flush is intentionally a no-op for the ActiveBand-only writer. Raw tool
// chunks are published as they arrive; there is no buffered terminal output to
// commit at process exit.
func (w *chatLiveToolOutputWriter) Flush() error {
	return nil
}

func (w *chatLimitedSystemOutputWriter) takeAllowedLocked(chunk string) (string, bool) {
	if chunk == "" {
		return "", false
	}

	var allowed strings.Builder
	exceeded := false
	for _, r := range chunk {
		next := string(r)
		if w.maxBytes > 0 && w.renderedBytes+len(next) > w.maxBytes {
			exceeded = true
			break
		}
		if w.maxLines > 0 && w.renderedLines >= w.maxLines {
			exceeded = true
			break
		}
		allowed.WriteString(next)
		w.renderedBytes += len(next)
		if r == '\n' {
			w.renderedLines++
		}
	}
	if allowed.Len() < len(chunk) {
		exceeded = true
	}
	return allowed.String(), exceeded
}

func (w *chatLiveToolOutputWriter) takeAllowedLocked(chunk string) (string, bool) {
	if chunk == "" {
		return "", false
	}

	var allowed strings.Builder
	exceeded := false
	for _, r := range chunk {
		next := string(r)
		if w.maxBytes > 0 && w.renderedBytes+len(next) > w.maxBytes {
			exceeded = true
			break
		}
		if w.maxLines > 0 && w.renderedLines >= w.maxLines {
			exceeded = true
			break
		}
		allowed.WriteString(next)
		w.renderedBytes += len(next)
		if r == '\n' {
			w.renderedLines++
		}
	}
	if allowed.Len() < len(chunk) {
		exceeded = true
	}
	return allowed.String(), exceeded
}

func (w *chatLimitedSystemOutputWriter) writeLimitNoticeLocked() error {
	if w.noticeWritten {
		return nil
	}
	w.noticeWritten = true
	if !w.lastForwardedEndedNewline {
		if _, err := w.writer.Write([]byte("\n")); err != nil {
			return err
		}
	}
	if _, err := w.writer.Write([]byte(chatLiveToolOutputLimitNotice + "\n")); err != nil {
		return err
	}
	return flushChatOutputWriter(w.writer)
}

func (w *chatLiveToolOutputWriter) publishChunkLocked(chunk string) {
	for _, r := range chunk {
		if r == '\n' {
			w.publishPartialLocked()
			w.partial.Reset()
			continue
		}
		w.partial.WriteRune(r)
	}
	// Publishing the partial tail keeps long-running commands responsive even
	// when they do not terminate progress lines with a newline.
	w.publishPartialLocked()
}

func (w *chatLiveToolOutputWriter) publishNoticeLocked() {
	if w.noticeRendered {
		return
	}
	w.noticeRendered = true
	w.publishTextLocked(chatLiveToolOutputLimitNotice)
}

func (w *chatLiveToolOutputWriter) publishPartialLocked() {
	if w == nil {
		return
	}
	w.publishTextLocked(w.partial.String())
}

func (w *chatLiveToolOutputWriter) publishTextLocked(text string) {
	if w == nil || w.sink == nil {
		return
	}
	// Raw process output is untrusted. Keep it as one compact stage line; the
	// fixed surface will sanitize it again when composing ActiveBand.
	text = strings.Join(strings.Fields(ui.SanitizeToolOutput(text)), " ")
	if text == "" {
		return
	}
	w.sink.SetToolAgentStage(w.toolCallID, w.toolName+" "+text)
}

func (w *chatSystemOutputWriter) Flush() error {
	if w == nil || !w.hasOutputTarget() {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	w.stopPartialFlushLocked()
	return w.flushPartialLocked()
}

func (w *chatSystemOutputWriter) flushPartialLocked() error {
	line := w.buffer.String()
	w.buffer.Reset()
	if strings.TrimSpace(line) == "" {
		return nil
	}
	w.lastBlank = false
	if w.semanticSink != nil {
		w.semanticSink.RenderLocalSupplement(line)
		return nil
	}
	if err := w.writeOutputTextLocked(ui.FormatAssistantSupplementBlock(line) + "\n"); err != nil {
		return err
	}
	return flushChatOutputWriter(w.writer)
}

func (w *chatSystemOutputWriter) writeOutputTextLocked(text string) error {
	if text == "" {
		return nil
	}
	if w.semanticSink != nil {
		w.semanticSink.RenderLocalSupplement(text)
		return nil
	}
	if atomicSurface, ok := w.surface.(chatAtomicOutputSurface); ok {
		if _, err, handled := atomicSurface.WriteOutput(w.writer, text); handled {
			return err
		}
	}
	w.beginOutput()
	_, err := ui.WriteTerminalText(w.writer, text)
	return err
}

func (w *chatSystemOutputWriter) schedulePartialFlushLocked() {
	if w == nil || w.partialFlushDelay <= 0 || w.partialTimer != nil {
		return
	}
	w.partialTimer = time.AfterFunc(w.partialFlushDelay, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.partialTimer = nil
		_ = w.flushPartialLocked()
	})
}

func (w *chatSystemOutputWriter) stopPartialFlushLocked() {
	if w == nil || w.partialTimer == nil {
		return
	}
	w.partialTimer.Stop()
	w.partialTimer = nil
}

func flushChatOutputWriter(writer io.Writer) error {
	if flusher, ok := writer.(interface{ Flush() error }); ok {
		return flusher.Flush()
	}
	return nil
}

func normalizeChatSystemOutputChunk(p []byte) string {
	chunk := strings.ReplaceAll(string(p), "\r\n", "\n")
	return strings.ReplaceAll(chunk, "\r", "\n")
}

func (w *chatSystemOutputWriter) beginOutput() {
	if w.surface != nil {
		w.surface.BeginOutput()
	}
}

func (w *chatSystemOutputWriter) hasOutputTarget() bool {
	return w != nil && (w.writer != nil || w.semanticSink != nil)
}

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

type chatOutputSurface interface {
	BeginOutput()
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

func newLimitedChatSystemOutputWriterWithSurface(writer io.Writer, surface chatOutputSurface, maxLines, maxBytes int) io.Writer {
	base := newChatSystemOutputWriterWithSurface(writer, surface)
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

func (w *chatSystemOutputWriter) Write(p []byte) (int, error) {
	if w == nil || w.writer == nil {
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
		rendered := ui.FormatAssistantSupplementBlock(line)
		if strings.TrimSpace(rendered) == "" {
			if w.lastBlank {
				continue
			}
			w.lastBlank = true
			beginOutput()
			if _, err := ui.WriteTerminalText(w.writer, "\n"); err != nil {
				return 0, err
			}
			renderedAny = true
			continue
		}
		w.lastBlank = false
		beginOutput()
		if _, err := ui.WriteTerminalLine(w.writer, rendered); err != nil {
			return 0, err
		}
		renderedAny = true
	}
	if renderedAny {
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

func (w *chatLimitedSystemOutputWriter) Flush() error {
	if w == nil || w.writer == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return flushChatOutputWriter(w.writer)
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

func (w *chatSystemOutputWriter) Flush() error {
	if w == nil || w.writer == nil {
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
	w.beginOutput()
	if _, err := ui.WriteTerminalLine(w.writer, ui.FormatAssistantSupplementBlock(line)); err != nil {
		return err
	}
	return flushChatOutputWriter(w.writer)
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

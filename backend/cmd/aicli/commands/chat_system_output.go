package commands

import (
	"io"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

const chatSystemOutputPartialFlushDelay = 150 * time.Millisecond

type chatSystemOutputWriter struct {
	writer            io.Writer
	surface           chatOutputSurface
	buffer            strings.Builder
	mu                sync.Mutex
	lastBlank         bool
	partialFlushDelay time.Duration
	partialTimer      *time.Timer
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

func (w *chatSystemOutputWriter) Write(p []byte) (int, error) {
	if w == nil || w.writer == nil {
		return len(p), nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	chunk := strings.ReplaceAll(string(p), "\r\n", "\n")
	chunk = strings.ReplaceAll(chunk, "\r", "\n")
	w.buffer.WriteString(chunk)
	renderedAny := false
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
			w.beginOutput()
			if _, err := io.WriteString(w.writer, "\n"); err != nil {
				return 0, err
			}
			renderedAny = true
			continue
		}
		w.lastBlank = false
		w.beginOutput()
		if _, err := io.WriteString(w.writer, rendered+"\n"); err != nil {
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
	if _, err := io.WriteString(w.writer, ui.FormatAssistantSupplementBlock(line)+"\n"); err != nil {
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

func (w *chatSystemOutputWriter) beginOutput() {
	if w.surface != nil {
		w.surface.BeginOutput()
	}
}

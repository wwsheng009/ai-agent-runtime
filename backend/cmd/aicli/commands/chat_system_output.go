package commands

import (
	"io"
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

type chatSystemOutputWriter struct {
	writer    io.Writer
	surface   chatOutputSurface
	buffer    strings.Builder
	mu        sync.Mutex
	lastBlank bool
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
	return &chatSystemOutputWriter{writer: writer, surface: surface}
}

func (w *chatSystemOutputWriter) Write(p []byte) (int, error) {
	if w == nil || w.writer == nil {
		return len(p), nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	w.buffer.Write(p)
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
			continue
		}
		w.lastBlank = false
		w.beginOutput()
		if _, err := io.WriteString(w.writer, rendered+"\n"); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (w *chatSystemOutputWriter) beginOutput() {
	if w.surface != nil {
		w.surface.BeginOutput()
	}
}

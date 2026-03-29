package commands

import (
	"io"
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

type chatSystemOutputWriter struct {
	writer io.Writer
	buffer strings.Builder
	mu     sync.Mutex
}

func newChatSystemOutputWriter(writer io.Writer) io.Writer {
	if writer == nil {
		return nil
	}
	return &chatSystemOutputWriter{writer: writer}
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
		if _, err := io.WriteString(w.writer, ui.IndentAssistantContent(line)+"\n"); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

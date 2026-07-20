package ui

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode"
)

const maxTerminalTitleRunes = 240

// TerminalTitleWriter safely manages the terminal title written by aicli.
// It deliberately does not try to restore a previous title because terminals
// do not expose a portable way to read it.
type TerminalTitleWriter struct {
	terminal *Terminal
	writer   io.Writer

	mu      sync.Mutex
	last    string
	managed bool
}

func NewTerminalTitleWriter(terminal *Terminal, writer io.Writer) *TerminalTitleWriter {
	return &TerminalTitleWriter{terminal: terminal, writer: writer}
}

func (w *TerminalTitleWriter) Supported() bool {
	if w == nil || w.terminal == nil || w.writer == nil {
		return false
	}
	return w.terminal.Capabilities().TerminalTitle
}

// Set emits an OSC 0 title update. The bool reports whether a new sequence was
// written; unsupported terminals and duplicate titles are quiet no-ops.
func (w *TerminalTitleWriter) Set(title string) (bool, error) {
	if !w.Supported() {
		return false, nil
	}
	title = sanitizeTerminalTitle(title)
	if title == "" {
		return w.clear()
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.managed && w.last == title {
		return false, nil
	}
	if _, err := WriteTerminalText(w.writer, fmt.Sprintf("\x1b]0;%s\x07", title)); err != nil {
		return false, err
	}
	w.last = title
	w.managed = true
	return true, nil
}

func (w *TerminalTitleWriter) Clear() error {
	if !w.Supported() {
		return nil
	}
	_, err := w.clear()
	return err
}

func (w *TerminalTitleWriter) clear() (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.managed {
		return false, nil
	}
	if _, err := WriteTerminalText(w.writer, "\x1b]0;\x07"); err != nil {
		return false, err
	}
	w.last = ""
	w.managed = false
	return true, nil
}

func sanitizeTerminalTitle(title string) string {
	var out strings.Builder
	count := 0
	pendingSpace := false
	for _, r := range title {
		if unicode.IsSpace(r) {
			pendingSpace = out.Len() > 0
			continue
		}
		if isDisallowedTerminalTitleRune(r) {
			continue
		}
		if pendingSpace && maxTerminalTitleRunes-count > 1 {
			out.WriteByte(' ')
			count++
		}
		pendingSpace = false
		if count >= maxTerminalTitleRunes {
			break
		}
		out.WriteRune(r)
		count++
	}
	return out.String()
}

func isDisallowedTerminalTitleRune(r rune) bool {
	if unicode.IsControl(r) {
		return true
	}
	return r == '\u00ad' || r == '\u034f' || r == '\u061c' || r == '\u180e' ||
		(r >= '\u200b' && r <= '\u200f') ||
		(r >= '\u202a' && r <= '\u202e') ||
		(r >= '\u2060' && r <= '\u206f') ||
		(r >= '\ufe00' && r <= '\ufe0f') || r == '\ufeff' ||
		(r >= '\ufff9' && r <= '\ufffb') ||
		(r >= '\U0001bca0' && r <= '\U0001bca3') ||
		(r >= '\U000e0100' && r <= '\U000e01ef')
}

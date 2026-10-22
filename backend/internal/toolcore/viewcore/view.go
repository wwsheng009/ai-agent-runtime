// Package viewcore implements the shared text-window reader used by every
// view surface. Path resolution and permission checks stay outside; this
// package only reads an already-authorized absolute path.
package viewcore

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	DefaultLimit = 400
	MaxLimit     = 2000
	LineCap      = 4096
)

type Window struct {
	Offset           int
	Limit            int
	Lines            []string
	LinesRead        int
	TotalLines       int
	EOF              bool
	TruncatedByBytes bool
}

// Read reads one line window without loading the whole file. Lines longer than
// LineCap are clamped with an explicit marker.
func Read(path string, offset, limit, maxBytes int) (Window, error) {
	offset, limit = Normalize(offset, limit)
	window := Window{Offset: offset, Limit: limit}
	file, err := os.Open(path)
	if err != nil {
		return window, fmt.Errorf("open %s: %w", filepath.Base(path), err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), LineCap*4)

	lineNo := 0
	totalBytes := 0
	for scanner.Scan() {
		lineNo++
		if lineNo < offset {
			continue
		}
		if len(window.Lines) >= limit {
			continue
		}
		text := scanner.Text()
		if len(text) > LineCap {
			text = text[:LineCap] + fmt.Sprintf("…[line truncated: showing first %d of %d bytes]", LineCap, len(scanner.Text()))
		}
		totalBytes += len(text) + 1
		if maxBytes > 0 && totalBytes > maxBytes && len(window.Lines) > 0 {
			window.TruncatedByBytes = true
			break
		}
		window.Lines = append(window.Lines, text)
	}
	if err := scanner.Err(); err != nil {
		return window, fmt.Errorf("scan %s: %w", filepath.Base(path), err)
	}
	window.TotalLines = lineNo
	window.LinesRead = len(window.Lines)
	window.EOF = offset+window.LinesRead > window.TotalLines
	return window, nil
}

func Normalize(offset, limit int) (int, int) {
	if offset < 1 {
		offset = 1
	}
	if limit < 1 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	return offset, limit
}

// Render formats the window exactly as the local and remote view surfaces have
// always done: stable line numbers plus explicit continuation guidance.
func Render(display string, window Window) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "=== %s (offset %d, limit %d) ===\n", display, window.Offset, window.Limit)
	for i, line := range window.Lines {
		fmt.Fprintf(&builder, "%d: %s\n", window.Offset+i, line)
	}
	if len(window.Lines) == 0 {
		fmt.Fprintf(&builder, "(no lines in this window; total_lines=%d)\n", window.TotalLines)
	}
	if !window.EOF && window.LinesRead > 0 {
		next := window.Offset + window.LinesRead
		fmt.Fprintf(&builder, "\n[more lines available: continue with offset=%d limit<=%d]\n", next, DefaultLimit)
	}
	if window.TruncatedByBytes {
		builder.WriteString("\n[window stopped at the byte budget; narrow the window with offset/limit]\n")
	}
	return builder.String()
}

// Metadata returns the stable view fields shared by all surfaces.
func Metadata(display string, window Window) map[string]interface{} {
	metadata := map[string]interface{}{
		"file_path":    display,
		"offset":       window.Offset,
		"limit":        window.Limit,
		"lines_read":   window.LinesRead,
		"total_lines":  window.TotalLines,
		"eof":          window.EOF,
		"is_truncated": !window.EOF,
	}
	if !window.EOF && window.LinesRead > 0 {
		metadata["suggested_next_offset"] = window.Offset + window.LinesRead
	}
	if window.TruncatedByBytes {
		metadata["truncated_by_bytes"] = true
	}
	return metadata
}

// ValidUTF8 reports whether rendered text is valid UTF-8; callers use it to
// annotate binary-looking windows without changing the bytes they return.
func ValidUTF8(text string) bool {
	return utf8.ValidString(text)
}

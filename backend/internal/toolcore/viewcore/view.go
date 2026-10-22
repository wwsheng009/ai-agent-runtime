// Package viewcore implements the shared text-window reader used by every
// view surface. Path resolution and permission checks stay outside; this
// package only reads an already-authorized absolute path.
package viewcore

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	DefaultLimit = 400
	MaxLimit     = 2000
	LineCap      = 4096

	// ReaderLineCapBytes bounds how much of a single line the streaming reader
	// holds in memory. Longer lines are drained and counted, never buffered in
	// full, so one huge line cannot turn into a scanner error or an unbounded
	// allocation.
	ReaderLineCapBytes = 256 * 1024

	lineReaderBufferBytes = 64 * 1024
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

// LineRead is one logical line produced by LineReader. Bytes is a prefix of
// the line (valid until the next call); ContentBytes is the full on-disk line
// length excluding its CR/LF delimiter. Truncated reports that Bytes is capped
// by ReaderLineCapBytes.
type LineRead struct {
	Bytes        []byte
	HasBytes     bool
	EOF          bool
	ContentBytes int64
	Truncated    bool
}

// LineReader streams lines with bounded memory. Unlike bufio.Scanner it does
// not fail on long lines: the prefix is retained and the remainder is drained
// so line numbers and following lines remain available.
type LineReader struct {
	reader *bufio.Reader
	buf    []byte
}

func NewLineReader(source io.Reader) *LineReader {
	return &LineReader{
		reader: bufio.NewReaderSize(source, lineReaderBufferBytes),
		buf:    make([]byte, 0, ReaderLineCapBytes),
	}
}

// HasMore reports whether at least one more byte is available.
func (r *LineReader) HasMore() (bool, error) {
	if r == nil || r.reader == nil {
		return false, nil
	}
	if _, err := r.reader.Peek(1); err != nil {
		if err == io.EOF {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Next reads one line. A final unterminated line is returned with EOF=true; a
// subsequent call returns HasBytes=false. It mirrors bufio.ScanLines for CRLF
// and trailing-CR handling while keeping memory bounded.
func (r *LineReader) Next() (LineRead, error) {
	if r == nil || r.reader == nil {
		return LineRead{EOF: true}, nil
	}
	r.buf = r.buf[:0]
	var (
		total     int64
		over      bool
		lastChunk []byte
	)
	for {
		chunk, err := r.reader.ReadSlice('\n')
		if len(chunk) > 0 {
			total += int64(len(chunk))
			lastChunk = chunk
			if !over {
				if room := ReaderLineCapBytes - len(r.buf); len(chunk) <= room {
					r.buf = append(r.buf, chunk...)
				} else {
					if room > 0 {
						r.buf = append(r.buf, chunk[:room]...)
					}
					over = true
				}
			}
		}
		switch err {
		case nil:
			delimiter := int64(1)
			if len(lastChunk) > 1 && lastChunk[len(lastChunk)-2] == '\r' {
				delimiter = 2
			}
			if !over {
				r.buf = r.buf[:len(r.buf)-int(delimiter)]
			}
			return LineRead{
				Bytes:        trimIncompleteUTF8Suffix(r.buf),
				HasBytes:     true,
				ContentBytes: nonNegative(total - delimiter),
				Truncated:    over,
			}, nil
		case bufio.ErrBufferFull:
			continue
		case io.EOF:
			if total == 0 {
				return LineRead{EOF: true}, nil
			}
			if !over {
				r.buf = trimTrailingCRBytes(r.buf)
			}
			content := total
			if len(lastChunk) > 0 && lastChunk[len(lastChunk)-1] == '\r' {
				content--
			}
			return LineRead{
				Bytes:        trimIncompleteUTF8Suffix(r.buf),
				HasBytes:     true,
				EOF:          true,
				ContentBytes: nonNegative(content),
				Truncated:    over,
			}, nil
		default:
			return LineRead{}, err
		}
	}
}

func nonNegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func trimTrailingCRBytes(line []byte) []byte {
	if len(line) > 0 && line[len(line)-1] == '\r' {
		return line[:len(line)-1]
	}
	return line
}

// trimIncompleteUTF8Suffix drops a byte-capped rune prefix only when the cap
// landed inside a multi-byte rune. Genuinely invalid bytes are preserved for
// the caller's binary detection.
func trimIncompleteUTF8Suffix(line []byte) []byte {
	if utf8.Valid(line) {
		return line
	}
	for i := len(line) - 1; i >= 0 && len(line)-i <= utf8.UTFMax; i-- {
		lead := line[i]
		if lead < utf8.RuneSelf {
			return line
		}
		if lead&0xC0 == 0x80 {
			continue
		}
		want := 0
		switch {
		case lead >= 0xC2 && lead <= 0xDF:
			want = 2
		case lead >= 0xE0 && lead <= 0xEF:
			want = 3
		case lead >= 0xF0 && lead <= 0xF4:
			want = 4
		default:
			return line
		}
		// Only a valid prefix followed by a cut multi-byte sequence is a
		// truncation artifact. Arbitrary invalid bytes must survive so the
		// caller can still classify the file as binary.
		if want > len(line)-i && utf8.Valid(line[:i]) {
			return line[:i]
		}
		return line
	}
	return line
}

// Read reads one zero-based line window without loading the whole file. Lines
// longer than LineCap are clamped with an explicit marker.
func Read(path string, offset, limit, maxBytes int) (Window, error) {
	offset, limit = Normalize(offset, limit)
	window := Window{Offset: offset, Limit: limit}
	file, err := os.Open(path)
	if err != nil {
		return window, fmt.Errorf("open %s: %w", filepath.Base(path), err)
	}
	defer file.Close()

	reader := NewLineReader(file)
	lineNo := 0
	totalBytes := 0
	for {
		line, readErr := reader.Next()
		if readErr != nil {
			return window, fmt.Errorf("scan %s: %w", filepath.Base(path), readErr)
		}
		if !line.HasBytes {
			break
		}
		lineNo++
		// The public offset is zero-based, matching the builtin view tool.
		if lineNo-1 < offset {
			continue
		}
		if len(window.Lines) >= limit {
			continue
		}
		text := clampViewLine(string(line.Bytes), line.ContentBytes, line.Truncated)
		totalBytes += len(text) + 1
		if maxBytes > 0 && totalBytes > maxBytes && len(window.Lines) > 0 {
			window.TruncatedByBytes = true
			break
		}
		window.Lines = append(window.Lines, text)
	}
	window.TotalLines = lineNo
	window.LinesRead = len(window.Lines)
	window.EOF = offset+window.LinesRead >= window.TotalLines
	return window, nil
}

func Normalize(offset, limit int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if limit < 1 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	return offset, limit
}

// clampViewLine keeps at most LineCap bytes of one line and reports the full
// length so the model can see how much was hidden.
func clampViewLine(text string, contentBytes int64, readerTruncated bool) string {
	if !readerTruncated && len(text) <= LineCap {
		return text
	}
	visible := clampUTF8Prefix(text, LineCap)
	if int64(len(visible)) >= contentBytes {
		return visible
	}
	return visible + fmt.Sprintf("…[line truncated: showing first %d of %d bytes]", len(visible), contentBytes)
}

func clampUTF8Prefix(text string, max int) string {
	if len(text) <= max {
		return text
	}
	text = text[:max]
	for i := 0; i < utf8.UTFMax-1 && len(text) > 0 && !utf8.ValidString(text); i++ {
		text = text[:len(text)-1]
	}
	return text
}

// Render formats the window exactly as the local and remote view surfaces have
// always done: stable line numbers plus explicit continuation guidance.
func Render(display string, window Window) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "=== %s (offset %d, limit %d) ===\n", display, window.Offset, window.Limit)
	for i, line := range window.Lines {
		fmt.Fprintf(&builder, "%d: %s\n", window.Offset+i+1, line)
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

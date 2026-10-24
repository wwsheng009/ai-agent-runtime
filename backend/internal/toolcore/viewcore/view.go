// Package viewcore implements the shared file reader used by every view
// surface. Path resolution and permission checks stay outside; this package
// only reads an already-authorized absolute path.
package viewcore

import (
	"bufio"
	"io"
	"unicode/utf8"
)

const (
	// ReaderLineCapBytes bounds how much of a single line the streaming reader
	// holds in memory. Longer lines are drained and counted, never buffered in
	// full, so one huge line cannot turn into a scanner error or an unbounded
	// allocation.
	ReaderLineCapBytes = 256 * 1024

	lineReaderBufferBytes = 64 * 1024
)

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

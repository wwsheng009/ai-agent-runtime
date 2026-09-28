// Package lsp implements the runtime-side Language Server Protocol
// integration described in docs/lsp (design 02, plan 03).
//
// Boundary rules (ADR-0006):
//   - The runtime-internal (canonical) position is a 0-based line plus a
//     UTF-8 byte column measured from the start of that line.
//   - Conversion to/from the negotiated LSP position encoding happens only
//     here, at the protocol boundary; no other package may convert columns.
//   - A leading UTF-8 BOM is stripped before the document is sent and is
//     accounted for separately (bom_bytes) when mapping back to the file.
package lsp

import (
	"bytes"
	"unicode/utf8"
)

// PositionEncoding is an LSP `positionEncoding` value (LSP 3.17 §general).
type PositionEncoding string

const (
	// EncodingUTF8 counts columns in UTF-8 bytes.
	EncodingUTF8 PositionEncoding = "utf-8"
	// EncodingUTF16 counts columns in UTF-16 code units (protocol default).
	EncodingUTF16 PositionEncoding = "utf-16"
	// EncodingUTF32 counts columns in Unicode code points.
	EncodingUTF32 PositionEncoding = "utf-32"
)

// CanonicalPos is the runtime-internal position: 0-based line, 0-based
// UTF-8 byte column (ADR-0006 §4.1). Ranges are half-open [start, end).
type CanonicalPos struct {
	Line   int
	Column int
}

// ProtocolPosition is an LSP `Position` expressed in the encoding that was
// negotiated during initialize. It must never leak past this package.
type ProtocolPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// ProtocolRange is an LSP `Range` in the negotiated encoding.
type ProtocolRange struct {
	Start ProtocolPosition `json:"start"`
	End   ProtocolPosition `json:"end"`
}

// CanonicalRange is a half-open [Start, End) range in canonical coordinates.
type CanonicalRange struct {
	Start CanonicalPos `json:"start"`
	End   CanonicalPos `json:"end"`
}

// NormalizeEncoding maps a raw `positionEncoding` string to a known value.
// Unknown values fall back to UTF-16, the protocol default (ADR-0006 §4.2).
func NormalizeEncoding(raw string) PositionEncoding {
	switch PositionEncoding(raw) {
	case EncodingUTF8:
		return EncodingUTF8
	case EncodingUTF32:
		return EncodingUTF32
	case EncodingUTF16:
		return EncodingUTF16
	default:
		return EncodingUTF16
	}
}

// NegotiateEncoding picks the encoding a server selected. The empty string
// means the server did not return `capabilities.positionEncoding`, which the
// protocol defines as UTF-16 (ADR-0006 §4.2); never assume UTF-8.
func NegotiateEncoding(serverValue string) PositionEncoding {
	if serverValue == "" {
		return EncodingUTF16
	}
	return NormalizeEncoding(serverValue)
}

// StripBOM removes a leading UTF-8 BOM and reports how many bytes it had.
// All canonical offsets inside this package are relative to the stripped
// content; callers add bomBytes back when reporting positions on disk.
func StripBOM(content []byte) ([]byte, int) {
	if len(content) >= 3 && content[0] == 0xEF && content[1] == 0xBB && content[2] == 0xBF {
		return content[3:], 3
	}
	return content, 0
}

// LineStartOffsets returns the byte offset where each 0-based line starts.
// Lines are split on LF only (ADR-0006 §4.1: CRLF keeps its CR in the line).
func LineStartOffsets(content []byte) []int {
	starts := make([]int, 1, bytes.Count(content, []byte{'\n'})+1)
	starts[0] = 0
	for i, b := range content {
		if b == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

// LineBytes returns the bytes of the 0-based line without its trailing LF.
func LineBytes(content []byte, line int) []byte {
	starts := LineStartOffsets(content)
	if line < 0 || line >= len(starts) {
		return nil
	}
	start := starts[line]
	end := len(content)
	if line+1 < len(starts) {
		end = starts[line+1]
	}
	// Keep CR (if any) inside the line; drop only the LF separator.
	if end > start && content[end-1] == '\n' {
		end--
	}
	return content[start:end]
}

// OffsetToCanonical converts an absolute byte offset into canonical
// coordinates, clamping the offset to the content length.
func OffsetToCanonical(content []byte, offset int) CanonicalPos {
	if offset < 0 {
		offset = 0
	}
	if offset > len(content) {
		offset = len(content)
	}
	line := bytes.Count(content[:offset], []byte{'\n'})
	lineStart := bytes.LastIndexByte(content[:offset], '\n') + 1
	return CanonicalPos{Line: line, Column: offset - lineStart}
}

// CanonicalToOffset converts canonical coordinates back into an absolute
// byte offset, clamping out-of-range positions to the end of the line.
func CanonicalToOffset(content []byte, pos CanonicalPos) int {
	if pos.Line < 0 {
		return 0
	}
	starts := LineStartOffsets(content)
	if pos.Line >= len(starts) {
		return len(content)
	}
	start := starts[pos.Line]
	end := len(content)
	if pos.Line+1 < len(starts) {
		end = starts[pos.Line+1]
		if end > start && content[end-1] == '\n' {
			end--
		}
	}
	offset := start + max(pos.Column, 0)
	if offset > end {
		return end
	}
	return offset
}

// ProtocolToCanonical converts an LSP position (negotiated encoding) into
// canonical coordinates. This is the only inbound conversion point.
func ProtocolToCanonical(content []byte, enc PositionEncoding, pos ProtocolPosition) CanonicalPos {
	line := pos.Line
	if line < 0 {
		line = 0
	}
	lineBytes := LineBytes(content, line)
	if lineBytes == nil {
		return OffsetToCanonical(content, len(content))
	}
	col := pos.Character
	switch enc {
	case EncodingUTF8:
		// already byte columns
	case EncodingUTF32:
		col = runeColumnToByte(lineBytes, col)
	default:
		col = utf16ColumnToByte(lineBytes, col)
	}
	if col < 0 {
		col = 0
	}
	if col > len(lineBytes) {
		col = len(lineBytes)
	}
	return CanonicalPos{Line: line, Column: col}
}

// CanonicalToProtocol converts canonical coordinates into an LSP position in
// the negotiated encoding. This is the only outbound conversion point.
func CanonicalToProtocol(content []byte, enc PositionEncoding, pos CanonicalPos) ProtocolPosition {
	line := pos.Line
	if line < 0 {
		line = 0
	}
	lineBytes := LineBytes(content, line)
	if lineBytes == nil {
		last := OffsetToCanonical(content, len(content))
		lineBytes = LineBytes(content, last.Line)
		if lineBytes == nil {
			return ProtocolPosition{Line: last.Line, Character: 0}
		}
		line = last.Line
		return ProtocolPosition{Line: line, Character: encodeColumn(lineBytes, enc, last.Column)}
	}
	return ProtocolPosition{Line: line, Character: encodeColumn(lineBytes, enc, pos.Column)}
}

func encodeColumn(line []byte, enc PositionEncoding, byteCol int) int {
	switch enc {
	case EncodingUTF8:
		return clampByteColumn(line, byteCol)
	case EncodingUTF32:
		return byteColumnToRune(line, byteCol)
	default:
		return byteColumnToUTF16(line, byteCol)
	}
}

func clampByteColumn(line []byte, col int) int {
	if col < 0 {
		return 0
	}
	if col > len(line) {
		return len(line)
	}
	return col
}

// utf16ColumnToByte maps a UTF-16 code-unit column to a byte column. If the
// unit column would split a surrogate pair it rounds down to the rune start
// (ADR-0006 D5: never cut a non-BMP character in half).
func utf16ColumnToByte(line []byte, col int) int {
	if col <= 0 {
		return 0
	}
	units, offset := 0, 0
	for offset < len(line) {
		r, size := utf8.DecodeRune(line[offset:])
		next := units + 1
		if r >= 0x10000 {
			next = units + 2
		}
		if next > col {
			break
		}
		units = next
		offset += size
		if units == col {
			return offset
		}
	}
	return offset
}

// byteColumnToUTF16 maps a byte column to a UTF-16 code-unit column. A byte
// column inside a multi-byte rune rounds down to the rune start.
func byteColumnToUTF16(line []byte, col int) int {
	col = clampByteColumn(line, col)
	units, offset := 0, 0
	for offset < col {
		r, size := utf8.DecodeRune(line[offset:])
		if offset+size > col {
			break
		}
		if r >= 0x10000 {
			units += 2
		} else {
			units++
		}
		offset += size
	}
	return units
}

// runeColumnToByte maps a code-point column to a byte column, rounding down
// to the rune start if the column falls inside a rune.
func runeColumnToByte(line []byte, col int) int {
	if col <= 0 {
		return 0
	}
	runes, offset := 0, 0
	for offset < len(line) && runes < col {
		_, size := utf8.DecodeRune(line[offset:])
		if runes+1 > col {
			break
		}
		runes++
		offset += size
	}
	return offset
}

// byteColumnToRune maps a byte column to a code-point column.
func byteColumnToRune(line []byte, col int) int {
	col = clampByteColumn(line, col)
	runes, offset := 0, 0
	for offset < col {
		_, size := utf8.DecodeRune(line[offset:])
		if offset+size > col {
			break
		}
		runes++
		offset += size
	}
	return runes
}

// Document is a text document tracked at the protocol boundary. Content is
// the BOM-stripped UTF-8 text that was sent to the server; BOMBytes records
// how many bytes were removed so positions can be mapped back to disk.
type Document struct {
	URI       string
	Path      string
	Language  string
	Version   int
	Content   []byte
	BOMBytes  int
	CRLFBytes int // diagnostic only: number of CRLF line endings observed
}

// NewDocument prepares a document for didOpen. It strips the BOM and records
// it; line endings are deliberately left untouched (ADR-0006 §4.5).
func NewDocument(uri, path, language string, content []byte) *Document {
	stripped, bom := StripBOM(content)
	return &Document{
		URI:       uri,
		Path:      path,
		Language:  language,
		Version:   0,
		Content:   stripped,
		BOMBytes:  bom,
		CRLFBytes: bytes.Count(stripped, []byte{'\r', '\n'}),
	}
}

// Position converts a canonical position to the negotiated encoding.
func (d *Document) Position(enc PositionEncoding, pos CanonicalPos) ProtocolPosition {
	return CanonicalToProtocol(d.Content, enc, pos)
}

// Canonical converts a negotiated position to canonical coordinates.
func (d *Document) Canonical(enc PositionEncoding, pos ProtocolPosition) CanonicalPos {
	return ProtocolToCanonical(d.Content, enc, pos)
}

// FullRange returns a protocol range covering the whole document, used to
// express a full replacement over an incremental sync channel.
func (d *Document) FullRange(enc PositionEncoding) ProtocolRange {
	end := OffsetToCanonical(d.Content, len(d.Content))
	return ProtocolRange{
		Start: ProtocolPosition{Line: 0, Character: 0},
		End:   CanonicalToProtocol(d.Content, enc, end),
	}
}

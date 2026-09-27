package tools

import (
	"bytes"
	"strings"
	"unicode/utf16"
)

// fileEncoding identifies the on-disk text encoding a file carried when it was
// read. Write/edit tools use it to keep BOM and UTF-16 files byte-faithful
// instead of silently rewriting them as BOM-less UTF-8.
type fileEncoding int

const (
	fileEncodingUTF8 fileEncoding = iota
	fileEncodingUTF8BOM
	fileEncodingUTF16LE
	fileEncodingUTF16BE
)

func (e fileEncoding) String() string {
	switch e {
	case fileEncodingUTF8BOM:
		return "utf-8-bom"
	case fileEncodingUTF16LE:
		return "utf-16le"
	case fileEncodingUTF16BE:
		return "utf-16be"
	default:
		return "utf-8"
	}
}

// hasBOM reports whether the encoding carried a byte-order mark.
func (e fileEncoding) hasBOM() bool {
	return e == fileEncodingUTF8BOM || e == fileEncodingUTF16LE || e == fileEncodingUTF16BE
}

// detectFileEncoding inspects a leading BOM and returns the encoding plus the
// payload with the BOM removed. Unknown bytes default to BOM-less UTF-8.
func detectFileEncoding(data []byte) (fileEncoding, []byte) {
	switch {
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		return fileEncodingUTF8BOM, data[3:]
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}):
		return fileEncodingUTF16LE, data[2:]
	case bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		return fileEncodingUTF16BE, data[2:]
	default:
		return fileEncodingUTF8, data
	}
}

// decodeFileBytes converts raw file bytes to UTF-8 text using the detected
// encoding. UTF-16 payloads are transcoded; a UTF-8 BOM is stripped so callers
// can match model-provided strings without a stray \ufeff.
func decodeFileBytes(data []byte) (string, fileEncoding) {
	enc, payload := detectFileEncoding(data)
	switch enc {
	case fileEncodingUTF16LE:
		return decodeUTF16Payload(payload, true), enc
	case fileEncodingUTF16BE:
		return decodeUTF16Payload(payload, false), enc
	default:
		return string(payload), enc
	}
}

func decodeUTF16Payload(payload []byte, littleEndian bool) string {
	units := make([]uint16, 0, (len(payload)+1)/2)
	for i := 0; i+1 < len(payload); i += 2 {
		if littleEndian {
			units = append(units, uint16(payload[i])|uint16(payload[i+1])<<8)
		} else {
			units = append(units, uint16(payload[i])<<8|uint16(payload[i+1]))
		}
	}
	return string(utf16.Decode(units))
}

// encodeFileText encodes text back into the file's original encoding, restoring
// the BOM the file had. A leading \ufeff in text is treated as already present
// and never duplicated.
func encodeFileText(text string, enc fileEncoding) []byte {
	switch enc {
	case fileEncodingUTF8BOM:
		text = strings.TrimPrefix(text, "\ufeff")
		out := make([]byte, 0, len(text)+3)
		out = append(out, 0xEF, 0xBB, 0xBF)
		return append(out, text...)
	case fileEncodingUTF16LE, fileEncodingUTF16BE:
		littleEndian := enc == fileEncodingUTF16LE
		text = strings.TrimPrefix(text, "\ufeff")
		units := utf16.Encode([]rune(text))
		out := make([]byte, 0, len(units)*2+2)
		if littleEndian {
			out = append(out, 0xFF, 0xFE)
		} else {
			out = append(out, 0xFE, 0xFF)
		}
		for _, unit := range units {
			if littleEndian {
				out = append(out, byte(unit), byte(unit>>8))
			} else {
				out = append(out, byte(unit>>8), byte(unit))
			}
		}
		return out
	default:
		return []byte(text)
	}
}

// binaryBytesRatioThreshold mirrors the NUL-byte heuristic used by view: a
// payload with a meaningful share of NUL bytes is treated as binary so tools
// never decode and rewrite it as text.
const binaryBytesRatioThreshold = 0.05

// isBinaryBytes reports whether raw bytes look binary (NUL-byte heuristic).
func isBinaryBytes(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	probe := data
	if len(probe) > 8192 {
		probe = probe[:8192]
	}
	nulls := 0
	for _, b := range probe {
		if b == 0 {
			nulls++
		}
	}
	if nulls == 0 {
		return false
	}
	return float64(nulls)/float64(len(probe)) > binaryBytesRatioThreshold
}

// looksBinaryFileBytes is the write-side binary gate. The NUL-byte heuristic is
// only meaningful for BOM-less UTF-8: UTF-16 text is full of NUL bytes, so
// BOM-marked payloads are decoded first and the decoded text is judged instead.
// A binary blob that merely starts with FF FE / FE FF is still refused, which
// the old "encoding == utf-8 &&" short circuit missed (review m10).
func looksBinaryFileBytes(data []byte) bool {
	enc, _ := detectFileEncoding(data)
	if !enc.hasBOM() {
		return isBinaryBytes(data)
	}
	text, _ := decodeFileBytes(data)
	return isBinaryDecodedText(text)
}

// isBinaryDecodedText flags decoded payloads dominated by control characters: a
// file that claims UTF-16 but decodes into NUL runes or control soup is binary.
func isBinaryDecodedText(text string) bool {
	probed, control := 0, 0
	for _, r := range text {
		probed++
		switch {
		case r == 0:
			return true
		case r == '\n' || r == '\r' || r == '\t':
		case r < 0x20:
			control++
		}
		if probed >= 8192 {
			break
		}
	}
	if probed == 0 {
		return false
	}
	return control*20 > probed
}

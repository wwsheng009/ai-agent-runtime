package mutatecore

import (
	"bytes"
	"strings"
	"unicode/utf16"
)

// Encoding identifies the on-disk text encoding of a mutated file.
type Encoding int

const (
	UTF8 Encoding = iota
	UTF8BOM
	UTF16LE
	UTF16BE
)

func (e Encoding) String() string {
	switch e {
	case UTF8BOM:
		return "utf-8-bom"
	case UTF16LE:
		return "utf-16le"
	case UTF16BE:
		return "utf-16be"
	default:
		return "utf-8"
	}
}

func (e Encoding) HasBOM() bool {
	return e == UTF8BOM || e == UTF16LE || e == UTF16BE
}

// DetectEncoding inspects a leading BOM and returns the encoding plus the
// payload with the BOM removed. Unknown bytes default to BOM-less UTF-8.
func DetectEncoding(data []byte) (Encoding, []byte) {
	switch {
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		return UTF8BOM, data[3:]
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}):
		return UTF16LE, data[2:]
	case bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		return UTF16BE, data[2:]
	default:
		return UTF8, data
	}
}

// Decode converts raw bytes to UTF-8 text using the detected encoding.
func Decode(data []byte) (string, Encoding) {
	enc, payload := DetectEncoding(data)
	switch enc {
	case UTF16LE:
		return decodeUTF16(payload, true), enc
	case UTF16BE:
		return decodeUTF16(payload, false), enc
	default:
		return string(payload), enc
	}
}

func decodeUTF16(payload []byte, littleEndian bool) string {
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

// Encode converts UTF-8 text back to the original encoding and restores the
// BOM the file carried. A leading U+FEFF is treated as already present.
func Encode(text string, enc Encoding) []byte {
	switch enc {
	case UTF8BOM:
		text = strings.TrimPrefix(text, "\ufeff")
		out := make([]byte, 0, len(text)+3)
		out = append(out, 0xEF, 0xBB, 0xBF)
		return append(out, text...)
	case UTF16LE, UTF16BE:
		littleEndian := enc == UTF16LE
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

const binaryBytesRatioThreshold = 0.05

// IsBinaryBytes reports whether raw bytes look binary by the NUL-byte
// heuristic used by the file tools.
func IsBinaryBytes(data []byte) bool {
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

// LooksBinary is the write-side binary gate. BOM-marked UTF-16 text is decoded
// first; a binary blob that merely starts with FF FE / FE FF is still refused.
func LooksBinary(data []byte) bool {
	enc, _ := DetectEncoding(data)
	if !enc.HasBOM() {
		return IsBinaryBytes(data)
	}
	text, _ := Decode(data)
	return IsBinaryText(text)
}

// IsBinaryText flags decoded payloads dominated by control characters.
func IsBinaryText(text string) bool {
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

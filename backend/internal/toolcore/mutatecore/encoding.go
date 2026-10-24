package mutatecore

import (
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/textcodec"
)

// The on-disk codec itself lives in textcodec so the read path and the mutation
// path share one implementation. mutatecore keeps these names because the write
// tools are its callers and the type is part of their vocabulary.

// Encoding identifies the on-disk text encoding of a mutated file.
type Encoding = textcodec.Encoding

const (
	UTF8    = textcodec.UTF8
	UTF8BOM = textcodec.UTF8BOM
	UTF16LE = textcodec.UTF16LE
	UTF16BE = textcodec.UTF16BE
)

// DetectEncoding inspects a leading BOM and returns the encoding plus the
// payload with the BOM removed. Unknown bytes default to BOM-less UTF-8.
func DetectEncoding(data []byte) (Encoding, []byte) {
	return textcodec.DetectEncoding(data)
}

// Decode converts raw bytes to UTF-8 text using the detected encoding.
func Decode(data []byte) (string, Encoding) {
	return textcodec.Decode(data)
}

// Encode converts UTF-8 text back to the original encoding and restores the
// BOM the file carried. A leading U+FEFF is treated as already present.
func Encode(text string, enc Encoding) []byte {
	return textcodec.Encode(text, enc)
}

// IsBinaryBytes reports whether raw bytes look binary by the NUL-byte
// heuristic used by the file tools.
func IsBinaryBytes(data []byte) bool {
	return textcodec.IsBinaryBytes(data)
}

// LooksBinary is the write-side binary gate. BOM-marked UTF-16 text is decoded
// first; a binary blob that merely starts with FF FE / FE FF is still refused.
func LooksBinary(data []byte) bool {
	return textcodec.LooksBinary(data)
}

// IsBinaryText flags decoded payloads dominated by control characters.
func IsBinaryText(text string) bool {
	return textcodec.IsBinaryText(text)
}

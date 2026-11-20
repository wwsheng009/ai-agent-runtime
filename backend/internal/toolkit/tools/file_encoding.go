package tools

import (
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/mutatecore"
)

// fileEncoding identifies the on-disk text encoding a file carried when it was
// read. The implementation lives in mutatecore so local and remote write paths
// share one BOM/UTF-16 codec.
type fileEncoding = mutatecore.Encoding

const (
	fileEncodingUTF8BOM = mutatecore.UTF8BOM
	fileEncodingUTF16LE = mutatecore.UTF16LE
)

func decodeFileBytes(data []byte) (string, fileEncoding) {
	return mutatecore.Decode(data)
}

func encodeFileText(text string, enc fileEncoding) []byte {
	return mutatecore.Encode(text, enc)
}

// looksBinaryFileBytes is the write-side binary gate. The NUL-byte heuristic is
// only meaningful for BOM-less UTF-8; BOM-marked payloads are decoded first.
func looksBinaryFileBytes(data []byte) bool {
	return mutatecore.LooksBinary(data)
}

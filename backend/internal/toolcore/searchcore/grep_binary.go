package searchcore

import (
	"bytes"
	"context"
	"io"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
)

// GrepNulDisplay replaces a NUL byte in model-visible text. A raw NUL inside
// tool output makes the payload unreadable in most clients and is indistinguishable
// from a truncated string, so the byte is shown as its printable symbol instead.
// Column numbers keep referring to the original file bytes.
const GrepNulDisplay = "␀"

// grepBinaryProbeBytes is how much of a file decides whether it is binary. The
// boundary is fixed instead of "however much the search happened to read":
// ripgrep's own verdict depends on its read buffer, which makes the same file
// answer differently depending on page size and file position. A fixed prefix
// keeps one file's classification independent of where a page happens to stop.
const grepBinaryProbeBytes = 8 * 1024

// grepFileIsBinary reports whether a file holds a NUL byte in its first
// grepBinaryProbeBytes. NUL is the marker every tool in this space uses (rg,
// git, our own write-side gate), and it is the only one that matters here:
// invalid UTF-8 without a NUL stays searchable, exactly like ripgrep.
//
// The probe reads through the I/O seam rather than opening the path itself: this is
// the same file the search is about to read, and answering "is this binary?" from a
// different machine than the bytes came from would classify a file by content nobody
// searched. The failure modes stay unchanged - an unreadable probe is not a silent
// "binary", because the real read reports what is actually wrong.
func grepFileIsBinary(ctx context.Context, access toolcore.Access, path string) bool {
	if access == nil {
		// No accessor means no evidence, and the rule of this function is that only
		// an observed NUL makes a file binary. Guessing "binary" here would hide a
		// searchable file; guessing "not binary" only lets the real read report the
		// actual problem.
		return false
	}
	file, err := access.Open(ctx, path)
	if err != nil {
		return false
	}
	defer file.Close()
	buf := make([]byte, grepBinaryProbeBytes)
	read, err := io.ReadFull(file, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		// An unreadable probe must not turn into a silent "binary": the real
		// read below will surface whatever is actually wrong.
		return false
	}
	return bytes.IndexByte(buf[:read], 0) >= 0
}

// sanitizeGrepNulText makes a matched line safe to hand to a model. It only
// allocates when a NUL is actually present, so the ordinary text path stays free
// of copies.
func sanitizeGrepNulText(text string) string {
	if !containsNul(text) {
		return text
	}
	return replaceNul(text)
}

func containsNul(text string) bool {
	for i := 0; i < len(text); i++ {
		if text[i] == 0 {
			return true
		}
	}
	return false
}

func replaceNul(text string) string {
	out := make([]byte, 0, len(text)+8)
	for i := 0; i < len(text); i++ {
		if text[i] != 0 {
			out = append(out, text[i])
			continue
		}
		out = append(out, GrepNulDisplay...)
	}
	return string(out)
}

package executor

import (
	"bytes"
	"fmt"
	"os/exec"
	"sync"
)

const (
	DefaultRetainedOutputBytes = 256 * 1024

	captureOutputMarkerReserve  = 192
	captureOutputMinSegmentSize = 4 * 1024
)

type CombinedOutputCapture struct {
	Output     string
	Truncated  bool
	TotalBytes int
	TotalLines int
}

func CaptureCombinedOutput(cmd *exec.Cmd, maxBytes int) (CombinedOutputCapture, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultRetainedOutputBytes
	}

	writer := newCappedCombinedWriter(maxBytes)
	cmd.Stdout = writer
	cmd.Stderr = writer

	err := cmd.Run()
	return writer.Result(), err
}

type cappedCombinedWriter struct {
	mu        sync.Mutex
	maxBytes  int
	headLimit int
	tailLimit int

	head []byte
	tail []byte

	totalBytes      int
	newlineCount    int
	endsWithNewline bool
	wroteAny        bool
}

func newCappedCombinedWriter(maxBytes int) *cappedCombinedWriter {
	if maxBytes <= 0 {
		maxBytes = DefaultRetainedOutputBytes
	}
	headLimit := maxBytes * 2 / 3
	tailLimit := maxBytes - headLimit
	if maxBytes >= captureOutputMinSegmentSize*2 {
		if headLimit < captureOutputMinSegmentSize {
			headLimit = captureOutputMinSegmentSize
			tailLimit = maxBytes - headLimit
		}
		if tailLimit < captureOutputMinSegmentSize {
			tailLimit = captureOutputMinSegmentSize
			headLimit = maxBytes - tailLimit
		}
	} else {
		headLimit = maxBytes / 2
		tailLimit = maxBytes - headLimit
	}
	return &cappedCombinedWriter{
		maxBytes:  maxBytes,
		headLimit: headLimit,
		tailLimit: tailLimit,
	}
}

func (w *cappedCombinedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(p) == 0 {
		return 0, nil
	}

	if !w.wroteAny {
		w.wroteAny = true
	}
	w.totalBytes += len(p)
	w.newlineCount += bytes.Count(p, []byte{'\n'})
	w.endsWithNewline = len(p) > 0 && p[len(p)-1] == '\n'

	w.appendHeadTail(p)
	return len(p), nil
}

func (w *cappedCombinedWriter) appendHeadTail(p []byte) {
	if w.headLimit > len(w.head) {
		headRoom := w.headLimit - len(w.head)
		if headRoom > len(p) {
			headRoom = len(p)
		}
		w.head = append(w.head, p[:headRoom]...)
		p = p[headRoom:]
	}

	if len(p) == 0 || w.tailLimit <= 0 {
		return
	}
	if len(p) >= w.tailLimit {
		w.tail = append([]byte(nil), p[len(p)-w.tailLimit:]...)
		return
	}
	if len(w.tail)+len(p) <= w.tailLimit {
		w.tail = append(w.tail, p...)
		return
	}

	overflow := len(w.tail) + len(p) - w.tailLimit
	if overflow >= len(w.tail) {
		w.tail = append([]byte(nil), p[len(p)-w.tailLimit:]...)
		return
	}
	next := make([]byte, 0, w.tailLimit)
	next = append(next, w.tail[overflow:]...)
	next = append(next, p...)
	w.tail = next
}

func (w *cappedCombinedWriter) Result() CombinedOutputCapture {
	w.mu.Lock()
	defer w.mu.Unlock()

	output := string(w.head) + string(w.tail)
	truncated := w.totalBytes > len(output)
	totalLines := 0
	if w.totalBytes > 0 {
		totalLines = w.newlineCount
		if !w.endsWithNewline {
			totalLines++
		}
	}
	if truncated {
		omitted := w.totalBytes - len(w.head) - len(w.tail)
		if omitted < 0 {
			omitted = 0
		}
		output = fmt.Sprintf(
			"Total output lines: %d\nTotal output bytes: %d\n\n%s\n\n[exec output truncated at capture limit: omitted %d bytes from the middle]\n\n%s",
			totalLines,
			w.totalBytes,
			string(w.head),
			omitted,
			string(w.tail),
		)
	}

	return CombinedOutputCapture{
		Output:     output,
		Truncated:  truncated,
		TotalBytes: w.totalBytes,
		TotalLines: totalLines,
	}
}

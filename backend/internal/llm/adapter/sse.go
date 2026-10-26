package adapter

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

// sseScannerBufPool 复用 ScanSSEFrames 的 scanner 起始缓冲。
//
// 生产 pprof 显示该函数每次调用 make([]byte, 0, 1024*1024)，而 bufio.Scanner
// 只在遇到超长行时才 grow 到上限——单个 SSE 事件通常只有几百字节。每次 LLM
// 流式响应都会走这里，长会话下累计 640MB+ 分配。池化后常态分配降为 0。
var sseScannerBufPool = sync.Pool{
	New: func() any {
		buf := make([]byte, 0, sseScannerStartBytes)
		return &buf
	},
}

const (
	sseScannerStartBytes   = 64 * 1024
	sseScannerMaxBytes     = 20 * 1024 * 1024
	maxPooledSSEScannerBuf = 1024 * 1024
)

func acquireSSEScannerBuf() *[]byte {
	return sseScannerBufPool.Get().(*[]byte)
}

func releaseSSEScannerBuf(buf *[]byte) {
	if buf == nil || cap(*buf) > maxPooledSSEScannerBuf {
		return
	}
	*buf = (*buf)[:0]
	sseScannerBufPool.Put(buf)
}

// SSEFrame is one fully assembled Server-Sent Events frame. Data contains all
// data fields joined with newlines, as required by the SSE specification.
type SSEFrame struct {
	Event string
	Data  string
	ID    string
	Retry int
}

// ScanSSEFrames decodes an SSE stream into complete events. It accepts both
// LF and CRLF framing, joins repeated data fields, and dispatches the final
// unterminated frame at EOF.
func ScanSSEFrames(reader io.Reader, handle func(SSEFrame) (bool, error)) error {
	if reader == nil {
		return fmt.Errorf("SSE reader is required")
	}

	scanner := bufio.NewScanner(reader)
	buf := acquireSSEScannerBuf()
	scanner.Buffer(*buf, sseScannerMaxBytes)
	defer releaseSSEScannerBuf(buf)
	frame := SSEFrame{}
	dataLines := make([]string, 0, 1)
	hasFields := false
	lastEventID := ""

	dispatch := func() (bool, error) {
		if !hasFields {
			return true, nil
		}
		frame.Data = strings.Join(dataLines, "\n")
		if frame.ID == "" {
			frame.ID = lastEventID
		}
		keepGoing, err := handle(frame)
		frame = SSEFrame{}
		dataLines = dataLines[:0]
		hasFields = false
		return keepGoing, err
	}

	firstLine := true
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if firstLine {
			line = strings.TrimPrefix(line, "\uFEFF")
			firstLine = false
		}
		if line == "" {
			keepGoing, err := dispatch()
			if err != nil || !keepGoing {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}

		field, value, found := strings.Cut(line, ":")
		if !found {
			value = ""
		}
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			frame.Event = strings.TrimSpace(value)
			hasFields = true
		case "data":
			dataLines = append(dataLines, value)
			hasFields = true
		case "id":
			if !strings.ContainsRune(value, '\x00') {
				frame.ID = value
				lastEventID = value
			}
			hasFields = true
		case "retry":
			if retry, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && retry >= 0 {
				frame.Retry = retry
			}
			hasFields = true
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read SSE stream: %w", err)
	}
	_, err := dispatch()
	return err
}

func scanSSEFrames(reader io.Reader, handle func(SSEFrame) (bool, error)) error {
	return ScanSSEFrames(reader, handle)
}

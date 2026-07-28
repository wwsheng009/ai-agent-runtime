package adapter

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

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
	scanner.Buffer(make([]byte, 0, 1024*1024), 20*1024*1024)
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

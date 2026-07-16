package commands

const chatStreamCaptureMaxBytes = 128 * 1024

// tailCaptureBuffer implements io.Writer while retaining only the newest
// bytes. Streaming parsers still consume the full response through TeeReader.
type tailCaptureBuffer struct {
	limit int
	data  []byte
	total int64
}

func newTailCaptureBuffer(limit int) *tailCaptureBuffer {
	if limit < 0 {
		limit = 0
	}
	return &tailCaptureBuffer{limit: limit}
}

func (b *tailCaptureBuffer) Write(payload []byte) (int, error) {
	written := len(payload)
	if b == nil {
		return written, nil
	}
	b.total += int64(written)
	if b.limit == 0 || written == 0 {
		return written, nil
	}
	if written >= b.limit {
		b.data = append(b.data[:0], payload[written-b.limit:]...)
		return written, nil
	}
	overflow := len(b.data) + written - b.limit
	if overflow > 0 {
		copy(b.data, b.data[overflow:])
		b.data = b.data[:len(b.data)-overflow]
	}
	b.data = append(b.data, payload...)
	return written, nil
}

func (b *tailCaptureBuffer) Bytes() []byte {
	if b == nil {
		return nil
	}
	return b.data
}

func (b *tailCaptureBuffer) TotalBytes() int64 {
	if b == nil {
		return 0
	}
	return b.total
}

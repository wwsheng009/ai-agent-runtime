package renderengine

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestFlushCountsWritesAndBytes(t *testing.T) {
	p := NewPresenter()
	var buf bytes.Buffer
	p.Flush(&buf, func(w io.Writer) {
		w.Write([]byte("hello "))
		w.Write([]byte("world"))
	})
	if got := buf.String(); got != "hello world" {
		t.Fatalf("output = %q, want %q", got, "hello world")
	}
	if p.FlushCount() != 1 {
		t.Fatalf("FlushCount = %d, want 1", p.FlushCount())
	}
	if p.LastFrameWriteCount() != 1 {
		t.Fatalf("LastFrameWriteCount = %d, want 1", p.LastFrameWriteCount())
	}
	if p.TotalWriteCount() != 1 {
		t.Fatalf("TotalWriteCount = %d, want 1", p.TotalWriteCount())
	}
	if p.TotalBytes() != 11 {
		t.Fatalf("TotalBytes = %d, want 11", p.TotalBytes())
	}
}

func TestFlushAccumulatesAcrossBatches(t *testing.T) {
	p := NewPresenter()
	var buf bytes.Buffer
	for i := 0; i < 3; i++ {
		p.Flush(&buf, func(w io.Writer) {
			w.Write([]byte("x"))
		})
	}
	if p.FlushCount() != 3 {
		t.Fatalf("FlushCount = %d, want 3", p.FlushCount())
	}
	if p.LastFrameWriteCount() != 1 {
		t.Fatalf("LastFrameWriteCount = %d, want 1", p.LastFrameWriteCount())
	}
	if p.TotalWriteCount() != 3 {
		t.Fatalf("TotalWriteCount = %d, want 3", p.TotalWriteCount())
	}
	if p.TotalBytes() != 3 {
		t.Fatalf("TotalBytes = %d, want 3", p.TotalBytes())
	}
	if !strings.EqualFold(buf.String(), "xxx") {
		t.Fatalf("output = %q, want xxx", buf.String())
	}
}

func TestFlushNilRenderSafe(t *testing.T) {
	p := NewPresenter()
	p.Flush(nil, nil)
	if p.FlushCount() != 0 {
		t.Fatalf("nil Flush counted: %d", p.FlushCount())
	}
}

func TestFlushMultilineBodyIsOneBatch(t *testing.T) {
	// The point of the batch primitive: an entire repaint (many Write calls)
	// counts as exactly one frame batch, so acceptance metrics can assert
	// "one frame = one flush" even when the body writes row by row.
	p := NewPresenter()
	var buf bytes.Buffer
	p.Flush(&buf, func(w io.Writer) {
		for i := 0; i < 24; i++ {
			w.Write([]byte("row\n"))
		}
	})
	if p.FlushCount() != 1 {
		t.Fatalf("FlushCount = %d, want 1 (one batch for a full repaint)", p.FlushCount())
	}
	if p.LastFrameWriteCount() != 1 {
		t.Fatalf("LastFrameWriteCount = %d, want 1 target write", p.LastFrameWriteCount())
	}
}

type recordingWriter struct {
	bytes.Buffer
	writes int
}

func (w *recordingWriter) Write(data []byte) (int, error) {
	w.writes++
	return w.Buffer.Write(data)
}

func TestFlushCoalescesRenderWritesIntoOneTargetWrite(t *testing.T) {
	p := NewPresenter()
	target := &recordingWriter{}
	p.Flush(target, func(w io.Writer) {
		for _, part := range []string{"one", "-", "frame"} {
			_, _ = io.WriteString(w, part)
		}
	})
	if target.writes != 1 {
		t.Fatalf("target writes = %d, want 1", target.writes)
	}
	if got := target.String(); got != "one-frame" {
		t.Fatalf("output = %q, want one-frame", got)
	}
}

func TestFlushEmptyFrameDoesNotWriteTarget(t *testing.T) {
	p := NewPresenter()
	target := &recordingWriter{}
	p.Flush(target, func(io.Writer) {})
	if target.writes != 0 {
		t.Fatalf("target writes = %d, want 0", target.writes)
	}
	if p.FlushCount() != 1 || p.LastFrameWriteCount() != 0 {
		t.Fatalf("empty frame stats = flushes %d writes %d, want 1/0", p.FlushCount(), p.LastFrameWriteCount())
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func TestFlushReportsTargetWriteError(t *testing.T) {
	p := NewPresenter()
	err := p.Flush(failingWriter{}, func(w io.Writer) {
		_, _ = io.WriteString(w, "frame")
	})
	if err == nil {
		t.Fatal("Flush returned nil for target write error")
	}
	if p.LastFrameWriteCount() != 1 {
		t.Fatalf("LastFrameWriteCount = %d, want 1 attempted target write", p.LastFrameWriteCount())
	}
}

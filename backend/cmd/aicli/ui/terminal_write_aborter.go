package ui

import (
	"errors"
	"io"
	"sync"
)

// ErrTerminalWriteAborted is returned after AbortTerminalWrite has been
// requested. The physical writer may still complete the abandoned syscall
// later, but the session refuses every subsequent frame so no second owner can
// interleave bytes with it.
var ErrTerminalWriteAborted = errors.New("terminal write aborted during shutdown")

// TerminalWriteAborter is the optional cancellation contract for physical
// terminal writers. Generic io.Writer cannot be interrupted safely; this
// wrapper makes the shutdown path bounded without giving up single-writer
// ownership.
type TerminalWriteAborter interface {
	AbortTerminalWrite() error
}

type terminalWriteOutcome struct {
	n          int
	err        error
	panicked   bool
	panicValue any
}

// abortableTerminalWriter serializes all writes through one dispatcher
// goroutine. Normal writes are synchronous from the caller's perspective; the
// dispatcher only becomes abandoned when AbortTerminalWrite races an
// underlying Write that never returns. After abort the wrapper rejects every
// new write, so the abandoned syscall is necessarily the last terminal write.
type abortableTerminalWriter struct {
	mu        sync.Mutex
	writer    io.Writer
	aborted   bool
	abortOnce sync.Once
	abort     chan struct{}
	started   sync.Once
	request   chan []byte
	result    chan terminalWriteOutcome
}

func newAbortableTerminalWriter(writer io.Writer) *abortableTerminalWriter {
	return &abortableTerminalWriter{
		writer:  writer,
		abort:   make(chan struct{}),
		request: make(chan []byte),
		result:  make(chan terminalWriteOutcome, 1),
	}
}

func (w *abortableTerminalWriter) Write(data []byte) (int, error) {
	if w == nil {
		return 0, ErrTerminalWriterMissing
	}
	w.mu.Lock()
	if w.aborted {
		w.mu.Unlock()
		return 0, ErrTerminalWriteAborted
	}
	if w.writer == nil {
		w.mu.Unlock()
		return 0, ErrTerminalWriterMissing
	}
	buf := append([]byte(nil), data...)
	w.mu.Unlock()

	w.started.Do(func() { go w.run() })
	select {
	case w.request <- buf:
	case <-w.abort:
		return 0, ErrTerminalWriteAborted
	}
	select {
	case outcome := <-w.result:
		w.mu.Lock()
		aborted := w.aborted
		w.mu.Unlock()
		if aborted {
			return 0, ErrTerminalWriteAborted
		}
		if outcome.panicked {
			panic(outcome.panicValue)
		}
		return outcome.n, outcome.err
	case <-w.abort:
		return 0, ErrTerminalWriteAborted
	}
}

func (w *abortableTerminalWriter) AbortTerminalWrite() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	w.aborted = true
	w.mu.Unlock()
	w.abortOnce.Do(func() { close(w.abort) })
	return nil
}

func (w *abortableTerminalWriter) run() {
	for {
		select {
		case <-w.abort:
			return
		case buf := <-w.request:
			outcome := invokeTerminalWrite(w.writer, buf)
			select {
			case w.result <- outcome:
			case <-w.abort:
			}
		}
	}
}

func invokeTerminalWrite(writer io.Writer, data []byte) (outcome terminalWriteOutcome) {
	defer func() {
		if value := recover(); value != nil {
			outcome.panicked = true
			outcome.panicValue = value
		}
	}()
	outcome.n, outcome.err = writer.Write(data)
	return outcome
}

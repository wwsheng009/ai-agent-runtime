package toolprotocol

import (
	"context"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Stream metadata keys merged into Progress.Payload via Metadata.
const (
	// MetadataStream marks a progress event as a terminal/output stream chunk.
	MetadataStream = "stream"
	// MetadataStreamChannel is stdout|stderr|combined.
	MetadataStreamChannel = "stream_channel"
	// MetadataStreamChunkIndex is a 1-based monotonic chunk counter.
	MetadataStreamChunkIndex = "stream_chunk_index"
	// MetadataOutputMirrored reports that the same bytes were sent to an
	// OutputMirror, allowing local UIs to avoid rendering duplicate output.
	MetadataOutputMirrored = "output_mirrored"
	// MetadataPhase is a coarse lifecycle marker (start|stream|finish).
	MetadataPhase = "phase"
)

// Stream channel values.
const (
	StreamChannelStdout   = "stdout"
	StreamChannelStderr   = "stderr"
	StreamChannelCombined = "combined"
)

// Phase values for progress metadata.
const (
	PhaseStart  = "start"
	PhaseStream = "stream"
	PhaseFinish = "finish"
)

const (
	defaultStreamPartialMaxBytes = 4 * 1024
	defaultStreamFlushMaxBytes   = 2 * 1024
	defaultStreamMaxEvents       = 64
	// Default max size for a single MCP result partial when chunking.
	defaultMCPResultPartialMax = 4 * 1024
)

// TerminalStreamOptions configures NewTerminalStreamWriter.
type TerminalStreamOptions struct {
	// Channel is stream_channel metadata (stdout|stderr|combined). Default combined.
	Channel string
	// Message is the Progress.Message for each chunk (optional).
	Message string
	// ToolID fills Progress.ToolID when non-empty.
	ToolID ToolID
	// CallID fills Progress.CallID when non-empty.
	CallID CallID
	// PartialMaxBytes truncates each Partial payload. Default 4KiB.
	PartialMaxBytes int
	// FlushMaxBytes forces a flush when the buffer reaches this size without a newline.
	// Default 2KiB.
	FlushMaxBytes int
	// MaxEvents caps how many stream progress events are emitted.
	// Zero uses the default 64; a negative value disables the limit.
	MaxEvents int
	// OutputMirrored marks chunks that are also written to an OutputMirror.
	OutputMirrored bool
}

// TerminalStreamWriter is an io.Writer that coalesces bytes into tool.progress
// events (Partial + stream metadata). Safe for concurrent Write/Flush.
//
// Flush policy:
//   - complete lines ending in \n
//   - progress-bar style updates ending in \r
//   - buffer size >= FlushMaxBytes
//   - explicit Flush() (also used by executor output mirrors)
type TerminalStreamWriter struct {
	ctx     context.Context
	opts    TerminalStreamOptions
	mu      sync.Mutex
	buf     strings.Builder
	chunk   int
	emitted int
	stopped bool
}

// NewTerminalStreamWriter creates a writer that reports stream chunks via Report.
// When ctx has no real reporter, Write is a no-op sink.
func NewTerminalStreamWriter(ctx context.Context, opts TerminalStreamOptions) *TerminalStreamWriter {
	if ctx == nil {
		ctx = context.Background()
	}
	opts = normalizeTerminalStreamOptions(opts)
	return &TerminalStreamWriter{ctx: ctx, opts: opts}
}

func normalizeTerminalStreamOptions(opts TerminalStreamOptions) TerminalStreamOptions {
	opts.Channel = strings.TrimSpace(opts.Channel)
	if opts.Channel == "" {
		opts.Channel = StreamChannelCombined
	}
	opts.Message = strings.TrimSpace(opts.Message)
	if opts.PartialMaxBytes <= 0 {
		opts.PartialMaxBytes = defaultStreamPartialMaxBytes
	}
	if opts.FlushMaxBytes <= 0 {
		opts.FlushMaxBytes = defaultStreamFlushMaxBytes
	}
	if opts.MaxEvents < 0 {
		opts.MaxEvents = 0
	} else if opts.MaxEvents == 0 {
		opts.MaxEvents = defaultStreamMaxEvents
	}
	return opts
}

// Write implements io.Writer. Bytes are buffered and flushed as stream progress.
func (w *TerminalStreamWriter) Write(p []byte) (int, error) {
	if w == nil || len(p) == 0 {
		return len(p), nil
	}
	if !HasReporter(w.ctx) {
		return len(p), nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return len(p), nil
	}
	w.buf.Write(p)
	w.flushLocked(false)
	return len(p), nil
}

// Flush emits any buffered remainder as a stream progress event.
func (w *TerminalStreamWriter) Flush() error {
	if w == nil {
		return nil
	}
	if !HasReporter(w.ctx) {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.flushLocked(true)
	return nil
}

// Close flushes remaining buffered data. Safe to call multiple times.
func (w *TerminalStreamWriter) Close() error {
	if w == nil {
		return nil
	}
	_ = w.Flush()
	w.mu.Lock()
	w.stopped = true
	w.mu.Unlock()
	return nil
}

func (w *TerminalStreamWriter) flushLocked(force bool) {
	for {
		if w.stopped || w.opts.MaxEvents > 0 && w.emitted >= w.opts.MaxEvents {
			if w.opts.MaxEvents > 0 && w.emitted >= w.opts.MaxEvents {
				w.buf.Reset()
				w.stopped = true
			}
			return
		}
		raw := w.buf.String()
		if raw == "" {
			return
		}
		chunk, rest, ok := takeStreamChunk(raw, w.opts.FlushMaxBytes, force)
		if !ok {
			return
		}
		w.buf.Reset()
		if rest != "" {
			w.buf.WriteString(rest)
		}
		if strings.TrimSpace(chunk) == "" && !strings.ContainsAny(chunk, "\r\n") {
			// Skip pure whitespace noise unless it was an explicit line break chunk.
			if !force || strings.TrimSpace(chunk) == "" {
				if rest == "" && !force {
					return
				}
				// Keep going if more content remains.
				if rest == "" {
					return
				}
				continue
			}
		}
		w.emitChunkLocked(chunk)
	}
}

func (w *TerminalStreamWriter) emitChunkLocked(chunk string) {
	partial := truncateStreamPartial(chunk, w.opts.PartialMaxBytes)
	if partial == "" {
		return
	}
	w.chunk++
	w.emitted++
	meta := map[string]interface{}{
		MetadataStream:           true,
		MetadataStreamChannel:    w.opts.Channel,
		MetadataStreamChunkIndex: w.chunk,
		MetadataPhase:            PhaseStream,
	}
	if w.opts.OutputMirrored {
		meta[MetadataOutputMirrored] = true
	}
	progress := Progress{
		ToolID:    w.opts.ToolID,
		CallID:    w.opts.CallID,
		Kind:      NotificationProgress,
		Message:   w.opts.Message,
		Partial:   partial,
		Metadata:  meta,
		Timestamp: time.Now().UTC(),
	}
	Report(w.ctx, progress)
}

// takeStreamChunk extracts the next flushable chunk.
// force=true emits the whole buffer (or a FlushMaxBytes prefix).
func takeStreamChunk(raw string, flushMax int, force bool) (chunk, rest string, ok bool) {
	if raw == "" {
		return "", "", false
	}
	if flushMax <= 0 {
		flushMax = defaultStreamFlushMaxBytes
	}
	// Prefer complete lines (\n).
	if idx := strings.IndexByte(raw, '\n'); idx >= 0 {
		return raw[:idx+1], raw[idx+1:], true
	}
	// Progress bars often rewrite the same line with \r.
	if idx := strings.LastIndexByte(raw, '\r'); idx >= 0 && idx < len(raw)-1 {
		// Keep trailing incomplete segment after last \r as buffer; emit up to \r.
		return raw[:idx+1], raw[idx+1:], true
	}
	if idx := strings.IndexByte(raw, '\r'); idx >= 0 {
		return raw[:idx+1], raw[idx+1:], true
	}
	if len(raw) >= flushMax {
		// Avoid splitting multi-byte runes mid-sequence.
		cut := flushMax
		if cut == len(raw) {
			return raw, "", true
		}
		for cut > 0 && !utf8.RuneStart(raw[cut]) {
			cut--
		}
		if cut == 0 {
			cut = flushMax
		}
		return raw[:cut], raw[cut:], true
	}
	if force {
		return raw, "", true
	}
	return "", "", false
}

func truncateStreamPartial(text string, maxBytes int) string {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	// Keep trailing window for long lines (progress bars / log tails).
	if maxBytes <= 3 {
		return text[len(text)-maxBytes:]
	}
	cut := len(text) - (maxBytes - 3)
	for cut < len(text) && !utf8.RuneStart(text[cut]) {
		cut++
	}
	if cut >= len(text) {
		return text[len(text)-maxBytes:]
	}
	return "..." + text[cut:]
}

// multiFlushWriter tees Write/Flush to multiple writers.
type multiFlushWriter struct {
	writers []io.Writer
}

// MultiFlushWriter returns an io.Writer that tees to all non-nil writers.
// Flush() is forwarded to any writer implementing Flush() error.
func MultiFlushWriter(writers ...io.Writer) io.Writer {
	filtered := make([]io.Writer, 0, len(writers))
	for _, w := range writers {
		if w != nil {
			filtered = append(filtered, w)
		}
	}
	switch len(filtered) {
	case 0:
		return io.Discard
	case 1:
		return filtered[0]
	default:
		return &multiFlushWriter{writers: filtered}
	}
}

func (m *multiFlushWriter) Write(p []byte) (int, error) {
	if m == nil || len(m.writers) == 0 {
		return len(p), nil
	}
	for _, w := range m.writers {
		_, _ = w.Write(p)
	}
	return len(p), nil
}

func (m *multiFlushWriter) Flush() error {
	if m == nil {
		return nil
	}
	for _, w := range m.writers {
		if flusher, ok := w.(interface{ Flush() error }); ok {
			_ = flusher.Flush()
		}
	}
	return nil
}

// TeeOutputMirrorWithTerminalStream tees an existing output mirror with a stream
// reporter when a progress Reporter is present on ctx. Returns the mirror to use
// for command capture (may be existing unchanged) and the stream writer (nil when
// no reporter). Callers should store the returned mirror via executor.WithOutputMirror
// or pass it directly to CaptureCombinedOutput* helpers.
//
// This tees rather than replaces any chat terminal writer already on the mirror.
func TeeOutputMirrorWithTerminalStream(ctx context.Context, existingMirror io.Writer, opts TerminalStreamOptions) (io.Writer, *TerminalStreamWriter) {
	if !HasReporter(ctx) {
		return existingMirror, nil
	}
	opts.OutputMirrored = opts.OutputMirrored || existingMirror != nil
	stream := NewTerminalStreamWriter(ctx, opts)
	return MultiFlushWriter(existingMirror, stream), stream
}

// ReportPhase emits a non-stream lifecycle progress event (start/finish).
func ReportPhase(ctx context.Context, phase, message string, extra map[string]interface{}) {
	if !HasReporter(ctx) {
		return
	}
	meta := map[string]interface{}{
		MetadataPhase: strings.TrimSpace(phase),
	}
	for k, v := range extra {
		if strings.TrimSpace(k) == "" || v == nil {
			continue
		}
		meta[k] = v
	}
	Report(ctx, Progress{
		Kind:      NotificationProgress,
		Message:   strings.TrimSpace(message),
		Metadata:  meta,
		Timestamp: time.Now().UTC(),
	})
}

// ReportTextStream emits large text as one or more stream partial progress events.
// Used for MCP results that are not live-streamed from a process.
func ReportTextStream(ctx context.Context, text, channel string) {
	if !HasReporter(ctx) {
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	channel = strings.TrimSpace(channel)
	if channel == "" {
		channel = StreamChannelCombined
	}
	w := NewTerminalStreamWriter(ctx, TerminalStreamOptions{
		Channel:       channel,
		Message:       "result",
		FlushMaxBytes: defaultMCPResultPartialMax,
		// Allow a few chunks for large MCP payloads without flooding.
		MaxEvents: 16,
	})
	_, _ = io.WriteString(w, text)
	_ = w.Flush()
}

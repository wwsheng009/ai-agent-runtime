package toolprotocol

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
)

func TestHasReporter(t *testing.T) {
	if HasReporter(nil) {
		t.Fatal("nil ctx should have no reporter")
	}
	if HasReporter(context.Background()) {
		t.Fatal("background should have no reporter")
	}
	if HasReporter(WithReporter(context.Background(), NopReporter{})) {
		t.Fatal("explicit NopReporter should be treated as absent")
	}
	ctx := WithReporter(context.Background(), ReporterFunc(func(Progress) {}))
	if !HasReporter(ctx) {
		t.Fatal("expected HasReporter true")
	}
}

func TestTerminalStreamWriterLineCoalesce(t *testing.T) {
	var mu sync.Mutex
	var got []Progress
	ctx := WithReporter(context.Background(), ReporterFunc(func(p Progress) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, p)
	}))

	w := NewTerminalStreamWriter(ctx, TerminalStreamOptions{
		Channel: StreamChannelCombined,
		Message: "stdout",
	})
	_, _ = io.WriteString(w, "hello")
	mu.Lock()
	if len(got) != 0 {
		t.Fatalf("expected no flush without newline, got %d", len(got))
	}
	mu.Unlock()

	_, _ = io.WriteString(w, " world\nsecond")
	mu.Lock()
	if len(got) != 1 {
		t.Fatalf("expected 1 line event, got %d: %+v", len(got), got)
	}
	if !strings.Contains(got[0].Partial, "hello world") {
		t.Fatalf("partial=%q", got[0].Partial)
	}
	if got[0].Metadata[MetadataStream] != true {
		t.Fatalf("metadata stream missing: %+v", got[0].Metadata)
	}
	if got[0].Metadata[MetadataStreamChannel] != StreamChannelCombined {
		t.Fatalf("channel=%v", got[0].Metadata[MetadataStreamChannel])
	}
	if got[0].Metadata[MetadataStreamChunkIndex] != 1 {
		t.Fatalf("chunk index=%v", got[0].Metadata[MetadataStreamChunkIndex])
	}
	mu.Unlock()

	_ = w.Flush()
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("expected flush of remainder, got %d", len(got))
	}
	if !strings.Contains(got[1].Partial, "second") {
		t.Fatalf("second partial=%q", got[1].Partial)
	}
}

func TestTerminalStreamWriterSizeFlushAndMaxEvents(t *testing.T) {
	var mu sync.Mutex
	var got []Progress
	ctx := WithReporter(context.Background(), ReporterFunc(func(p Progress) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, p)
	}))
	w := NewTerminalStreamWriter(ctx, TerminalStreamOptions{
		FlushMaxBytes:   8,
		MaxEvents:       2,
		PartialMaxBytes: 64,
	})
	// No newlines; force size-based flushes.
	_, _ = io.WriteString(w, "abcdefghij") // 10 bytes -> first chunk 8, rest 2 buffered
	_, _ = io.WriteString(w, "klmnopqrst") // more data
	_ = w.Flush()

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("expected max 2 events, got %d: %+v", len(got), got)
	}
	payload := got[0].Payload()
	if payload["stream"] != true {
		t.Fatalf("payload stream=%v", payload["stream"])
	}
	if payload["stream_chunk_index"] != 1 {
		t.Fatalf("chunk index payload=%v", payload["stream_chunk_index"])
	}
}

func TestTakeStreamChunkSizeBoundary(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		flushMax  int
		wantChunk string
		wantRest  string
	}{
		{
			name:      "exact ASCII boundary",
			raw:       "abcdefgh",
			flushMax:  8,
			wantChunk: "abcdefgh",
		},
		{
			name:      "UTF-8 boundary",
			raw:       "abcdefg\u4f60x",
			flushMax:  8,
			wantChunk: "abcdefg",
			wantRest:  "\u4f60x",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunk, rest, ok := takeStreamChunk(tt.raw, tt.flushMax, false)
			if !ok || chunk != tt.wantChunk || rest != tt.wantRest {
				t.Fatalf("takeStreamChunk() = (%q, %q, %t), want (%q, %q, true)", chunk, rest, ok, tt.wantChunk, tt.wantRest)
			}
		})
	}
}

func TestNormalizeTerminalStreamOptionsMaxEvents(t *testing.T) {
	if got := normalizeTerminalStreamOptions(TerminalStreamOptions{}).MaxEvents; got != defaultStreamMaxEvents {
		t.Fatalf("zero MaxEvents = %d, want default %d", got, defaultStreamMaxEvents)
	}
	if got := normalizeTerminalStreamOptions(TerminalStreamOptions{MaxEvents: -1}).MaxEvents; got != 0 {
		t.Fatalf("negative MaxEvents = %d, want unlimited internal value 0", got)
	}
	if got := normalizeTerminalStreamOptions(TerminalStreamOptions{MaxEvents: 3}).MaxEvents; got != 3 {
		t.Fatalf("explicit MaxEvents = %d, want 3", got)
	}
}

func TestTerminalStreamWriterCarriageReturn(t *testing.T) {
	var mu sync.Mutex
	var got []Progress
	ctx := WithReporter(context.Background(), ReporterFunc(func(p Progress) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, p)
	}))
	w := NewTerminalStreamWriter(ctx, TerminalStreamOptions{})
	_, _ = io.WriteString(w, "downloading 10%\r")
	mu.Lock()
	if len(got) != 1 {
		t.Fatalf("expected \\r flush, got %d", len(got))
	}
	mu.Unlock()
	_, _ = io.WriteString(w, "downloading 50%\r")
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("expected second \\r flush, got %d", len(got))
	}
}

func TestTerminalStreamWriterNoReporterNoop(t *testing.T) {
	w := NewTerminalStreamWriter(context.Background(), TerminalStreamOptions{})
	n, err := io.WriteString(w, "noise\n")
	if err != nil || n != 6 {
		t.Fatalf("write n=%d err=%v", n, err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
}

func TestMultiFlushWriter(t *testing.T) {
	var a, b bytes.Buffer
	w := MultiFlushWriter(&a, &b)
	_, _ = io.WriteString(w, "xy")
	if a.String() != "xy" || b.String() != "xy" {
		t.Fatalf("a=%q b=%q", a.String(), b.String())
	}
	if flusher, ok := w.(interface{ Flush() error }); !ok {
		t.Fatal("expected Flush")
	} else if err := flusher.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
}

func TestReportPhaseAndTextStream(t *testing.T) {
	var mu sync.Mutex
	var got []Progress
	ctx := WithReporter(context.Background(), ReporterFunc(func(p Progress) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, p)
	}))

	ReportPhase(ctx, PhaseStart, "mcp call started", map[string]interface{}{"source": "mcp"})
	ReportTextStream(ctx, "line1\nline2\n", StreamChannelCombined)
	ReportPhase(ctx, PhaseFinish, "mcp call finished", nil)

	mu.Lock()
	defer mu.Unlock()
	if len(got) < 3 {
		t.Fatalf("expected phase+stream+phase, got %d: %+v", len(got), got)
	}
	if got[0].Metadata[MetadataPhase] != PhaseStart {
		t.Fatalf("first phase=%v", got[0].Metadata[MetadataPhase])
	}
	foundStream := false
	for _, p := range got {
		if p.Metadata[MetadataStream] == true {
			foundStream = true
			break
		}
	}
	if !foundStream {
		t.Fatalf("expected stream partials: %+v", got)
	}
	last := got[len(got)-1]
	if last.Metadata[MetadataPhase] != PhaseFinish {
		t.Fatalf("last phase=%v", last.Metadata[MetadataPhase])
	}
}

func TestTeeOutputMirrorWithTerminalStream(t *testing.T) {
	var mu sync.Mutex
	var reports []Progress
	ctx := WithReporter(context.Background(), ReporterFunc(func(p Progress) {
		mu.Lock()
		defer mu.Unlock()
		reports = append(reports, p)
	}))
	var chat bytes.Buffer
	mirror, stream := TeeOutputMirrorWithTerminalStream(ctx, &chat, TerminalStreamOptions{
		Channel: StreamChannelStdout,
	})
	if stream == nil {
		t.Fatal("expected stream writer")
	}
	_, _ = io.WriteString(mirror, "tee-line\n")
	if flusher, ok := mirror.(interface{ Flush() error }); ok {
		_ = flusher.Flush()
	}
	if chat.String() != "tee-line\n" {
		t.Fatalf("chat mirror=%q", chat.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reports) != 1 {
		t.Fatalf("reports=%d %+v", len(reports), reports)
	}
	if reports[0].Metadata[MetadataOutputMirrored] != true {
		t.Fatalf("expected output_mirrored metadata, got %+v", reports[0].Metadata)
	}
}

func TestTruncateStreamPartialKeepsTail(t *testing.T) {
	long := strings.Repeat("a", 100) + "TAIL"
	out := truncateStreamPartial(long, 10)
	if !strings.HasSuffix(out, "TAIL") {
		t.Fatalf("out=%q", out)
	}
	if !strings.HasPrefix(out, "...") {
		t.Fatalf("out=%q want prefix ...", out)
	}
}

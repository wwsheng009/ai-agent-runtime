package observability

import "testing"

// TestTraceAndSpanIDsStayUniqueWithinOneClockTick reproduces the coarse-clock
// hazard for tracing: span/trace ids are map keys inside the tracer, so two
// traces created inside one clock tick used to overwrite each other and lose
// the second trace entirely.
func TestTraceAndSpanIDsStayUniqueWithinOneClockTick(t *testing.T) {
	const traceCount = 64
	const spansPerTrace = 8

	traceIDs := make(map[string]struct{}, traceCount)
	for i := 0; i < traceCount; i++ {
		trace := NewTrace("root")
		if _, duplicate := traceIDs[trace.TraceID]; duplicate {
			t.Fatalf("trace id %q was minted twice", trace.TraceID)
		}
		traceIDs[trace.TraceID] = struct{}{}

		spanIDs := map[string]struct{}{trace.Root.ID: {}}
		for j := 0; j < spansPerTrace; j++ {
			span := trace.StartSpan("child")
			if _, duplicate := spanIDs[span.ID]; duplicate {
				t.Fatalf("span id %q was minted twice inside trace %q", span.ID, trace.TraceID)
			}
			spanIDs[span.ID] = struct{}{}
		}
	}
}

// TestSpanIDsCarryPrefix keeps the observable part of the old format: ids stay
// namespaced by kind so logs and debug dumps remain greppable.
func TestSpanIDsCarryPrefix(t *testing.T) {
	trace := NewTrace("root")
	if got := trace.TraceID; len(got) < len("trace_") || got[:len("trace_")] != "trace_" {
		t.Fatalf("trace id %q lost the trace_ prefix", got)
	}
	span := trace.StartSpan("child")
	if len(span.ID) < len("span_") || span.ID[:len("span_")] != "span_" {
		t.Fatalf("span id %q lost the span_ prefix", span.ID)
	}
}

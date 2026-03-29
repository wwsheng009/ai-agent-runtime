package observability

import (
	"sync"
	"testing"
	"time"
)

func TestCounter(t *testing.T) {
	t.Run("NewCounter", func(t *testing.T) {
		counter := NewCounter("test_counter", map[string]string{"key": "value"})
		if counter.name != "test_counter" {
			t.Errorf("Expected name 'test_counter', got '%s'", counter.name)
		}
		if counter.value != 0 {
			t.Errorf("Expected value 0, got %v", counter.value)
		}
	})

	t.Run("Inc", func(t *testing.T) {
		counter := NewCounter("test", nil)
		counter.Inc()
		if counter.value != 1 {
			t.Errorf("Expected value 1, got %v", counter.value)
		}
	})
}

func TestGauge(t *testing.T) {
	t.Run("NewGauge", func(t *testing.T) {
		gauge := NewGauge("test_gauge", map[string]string{"source": "test"})
		if gauge.value != 0 {
			t.Errorf("Expected value 0, got %v", gauge.value)
		}
	})

	t.Run("Set", func(t *testing.T) {
		gauge := NewGauge("test", nil)
		gauge.Set(42)
		if gauge.value != 42 {
			t.Errorf("Expected value 42, got %v", gauge.value)
		}
	})
}

func TestHistogram(t *testing.T) {
	t.Run("NewHistogram", func(t *testing.T) {
		histogram := NewHistogram("test_histogram", map[string]string{"op": "compute"}, []float32{0.1, 0.5, 1.0})
		if len(histogram.buckets) != 3 {
			t.Errorf("Expected 3 buckets, got %d", len(histogram.buckets))
		}
	})

	t.Run("Observe", func(t *testing.T) {
		histogram := NewHistogram("test", nil, nil)
		histogram.Observe(0.05)
		if histogram.count != 1 {
			t.Errorf("Expected count 1, got %d", histogram.count)
		}
	})
}

func TestRegistry(t *testing.T) {
	t.Run("GetOrCreateCounter", func(t *testing.T) {
		registry := NewRegistry()

		counter1 := registry.GetOrCreateCounter("test", map[string]string{"label": "1"})
		counter2 := registry.GetOrCreateCounter("test", map[string]string{"label": "1"})

		if counter1 != counter2 {
			t.Error("Expected same counter instance")
		}
		counter1.Inc()
		if counter2.value != 1 {
			t.Errorf("Expected value 1, got %v", counter2.value)
		}
	})

	t.Run("GetOrCreateGauge", func(t *testing.T) {
		registry := NewRegistry()

		gauge1 := registry.GetOrCreateGauge("test", map[string]string{"label": "1"})
		gauge2 := registry.GetOrCreateGauge("test", map[string]string{"label": "1"})

		if gauge1 != gauge2 {
			t.Error("Expected same gauge instance")
		}
	})

	t.Run("GetAllCounters", func(t *testing.T) {
		registry := NewRegistry()

		registry.GetOrCreateCounter("counter1", nil).Inc()
		registry.GetOrCreateCounter("counter2", nil).Inc()

		counters := registry.GetAllCounters()
		if len(counters) != 2 {
			t.Errorf("Expected 2 counters, got %d", len(counters))
		}
	})

	t.Run("LabelOrderReuse", func(t *testing.T) {
		registry := NewRegistry()

		labels1 := map[string]string{"status": "success", "agent": "agent-1"}
		labels2 := map[string]string{"agent": "agent-1", "status": "success"}

		counter1 := registry.GetOrCreateCounter("test", labels1)
		counter2 := registry.GetOrCreateCounter("test", labels2)

		if counter1 != counter2 {
			t.Fatal("Expected same counter instance for equivalent labels")
		}

		counter1.Inc()
		if got := counter2.Get(); got != 1 {
			t.Fatalf("Expected shared counter value 1, got %v", got)
		}
	})

	t.Run("ResetClearsCounters", func(t *testing.T) {
		registry := NewRegistry()

		registry.GetOrCreateCounter("test", map[string]string{"label": "1"}).Inc()
		registry.GetOrCreateGauge("test_gauge", nil).Set(42)
		registry.GetOrCreateHistogram("test_histogram", nil, nil).Observe(0.1)

		registry.Reset()

		if len(registry.GetAllCounters()) != 0 {
			t.Fatal("Expected counters to be cleared after reset")
		}
		if len(registry.GetAllGauges()) != 0 {
			t.Fatal("Expected gauges to be cleared after reset")
		}
		if len(registry.GetAllHistograms()) != 0 {
			t.Fatal("Expected histograms to be cleared after reset")
		}
	})

	t.Run("ConcurrentGetOrCreateCounter", func(t *testing.T) {
		registry := NewRegistry()
		labels := map[string]string{"agent": "agent-1", "status": "success"}

		const goroutines = 32
		results := make([]*Counter, goroutines)
		var wg sync.WaitGroup
		wg.Add(goroutines)

		for i := 0; i < goroutines; i++ {
			go func(index int) {
				defer wg.Done()
				results[index] = registry.GetOrCreateCounter("test", labels)
			}(i)
		}

		wg.Wait()

		for i := 1; i < goroutines; i++ {
			if results[i] != results[0] {
				t.Fatal("Expected all goroutines to receive same counter instance")
			}
		}
	})
}

func TestTracer(t *testing.T) {
	t.Run("NewTrace", func(t *testing.T) {
		trace := NewTrace("test_operation")

		if trace == nil {
			t.Fatal("Expected non-nil trace")
		}
		if trace.TraceID == "" {
			t.Error("Expected non-empty trace ID")
		}
		if trace.Root == nil {
			t.Error("Expected non-nil root span")
		}
		if trace.Root.Name != "test_operation" {
			t.Errorf("Expected name 'test_operation', got '%s'", trace.Root.Name)
		}
	})

	t.Run("StartSpan", func(t *testing.T) {
		trace := NewTrace("root")

		span1 := trace.StartSpan("operation1")
		span2 := trace.StartSpan("operation2")

		if span2.ParentID != span1.ID {
			t.Errorf("Expected parentID '%s', got '%s'", span1.ID, span2.ParentID)
		}

		if len(trace.Spans) != 3 {
			t.Errorf("Expected 3 spans, got %d", len(trace.Spans))
		}
	})
}

func TestSpan(t *testing.T) {
	t.Run("SetAttribute", func(t *testing.T) {
		span := NewSpan("test", "trace1", "")

		span.SetAttribute("key1", "value1")
		span.SetAttribute("key2", "value2")

		if span.Attributes["key1"] != "value1" {
			t.Errorf("Expected 'value1', got '%s'", span.Attributes["key1"])
		}
		if span.Attributes["key2"] != "value2" {
			t.Errorf("Expected 'value2', got '%s'", span.Attributes["key2"])
		}
	})

	t.Run("AddEvent", func(t *testing.T) {
		span := NewSpan("test", "trace1", "")

		span.AddEvent("event1")
		span.AddEvent("event2")

		if len(span.Events) != 2 {
			t.Errorf("Expected 2 events, got %d", len(span.Events))
		}
	})

	t.Run("SetError", func(t *testing.T) {
		span := NewSpan("test", "trace1", "")

		span.SetError("error message")

		if span.Status.Code != SpanStatusError {
			t.Errorf("Expected error status code, got %d", span.Status.Code)
		}
		if span.Status.Message != "error message" {
			t.Errorf("Expected 'error message', got '%s'", span.Status.Message)
		}
	})
}

func TestTiming(t *testing.T) {
	t.Run("StartTiming", func(t *testing.T) {
		timing := StartTiming()
		time.Sleep(10 * time.Millisecond)

		duration := timing.Stop()
		if duration == 0 {
			t.Error("Expected non-zero duration")
		}
	})

	t.Run("Elapsed", func(t *testing.T) {
		timing := StartTiming()
		time.Sleep(10 * time.Millisecond)

		duration := timing.Elapsed()
		if duration == 0 {
			t.Error("Expected non-zero duration")
		}

		// Elapsed 应该不停止计时
		time.Sleep(5 * time.Millisecond)
		duration2 := timing.Elapsed()
		if duration2 <= duration {
			t.Errorf("Expected duration2 > duration, got %v <= %v", duration2, duration)
		}
	})
}

func TestTraceContext(t *testing.T) {
	t.Run("NewTraceContext", func(t *testing.T) {
		ctx := NewTraceContext("trace-123", "span-456")

		if ctx.TraceID != "trace-123" {
			t.Errorf("Expected trace ID 'trace-123', got '%s'", ctx.TraceID)
		}
		if ctx.SpanID != "span-456" {
			t.Errorf("Expected span ID 'span-456', got '%s'", ctx.SpanID)
		}
	})
}

package remotetools

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Metrics is the per-server counter set exposed by the status surface.
//
// Multi-target deployments need to answer "which target is slow or failing"
// (design §13.8). Counters are process-local and monotonic: they are read by
// `doctor --json` and by the audit stream, not exported to a metrics backend.
type Metrics struct {
	callsTotal    atomic.Int64
	callsFailed   atomic.Int64
	callsDenied   atomic.Int64
	bytesOutTotal atomic.Int64
	lastLatencyMS atomic.Int64

	mu           sync.Mutex
	perTool      map[string]*toolCounters
	perToolLimit int
}

// toolCounters is the per-tool slice of Metrics.
type toolCounters struct {
	calls  int64
	failed int64
	denied int64
}

// NewMetrics creates an empty counter set.
func NewMetrics() *Metrics {
	return &Metrics{perTool: map[string]*toolCounters{}, perToolLimit: 64}
}

// Observe records one finished call.
//
// A denied call (policy refusal) is counted separately from a failed call
// (execution error): conflating them hides a misconfigured allowlist behind
// generic error noise.
func (m *Metrics) Observe(tool string, duration time.Duration, bytesOut int, failed, denied bool) {
	if m == nil {
		return
	}
	m.callsTotal.Add(1)
	if failed {
		m.callsFailed.Add(1)
	}
	if denied {
		m.callsDenied.Add(1)
	}
	if bytesOut > 0 {
		m.bytesOutTotal.Add(int64(bytesOut))
	}
	m.lastLatencyMS.Store(duration.Milliseconds())

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.perTool == nil {
		m.perTool = map[string]*toolCounters{}
	}
	counters, ok := m.perTool[tool]
	if !ok {
		// Bound the map so an unexpected tool name cannot grow it without limit.
		if len(m.perTool) >= m.perToolLimit {
			return
		}
		counters = &toolCounters{}
		m.perTool[tool] = counters
	}
	counters.calls++
	if failed {
		counters.failed++
	}
	if denied {
		counters.denied++
	}
}

// Snapshot is one point-in-time reading.
type Snapshot struct {
	CallsTotal    int64                    `json:"callsTotal"`
	CallsFailed   int64                    `json:"callsFailed"`
	CallsDenied   int64                    `json:"callsDenied"`
	BytesOutTotal int64                    `json:"bytesOutTotal"`
	LastLatencyMS int64                    `json:"lastLatencyMs"`
	PerTool       map[string]ToolSnapshot  `json:"perTool,omitempty"`
}

// ToolSnapshot is the per-tool reading.
type ToolSnapshot struct {
	Calls  int64 `json:"calls"`
	Failed int64 `json:"failed"`
	Denied int64 `json:"denied"`
}

// Snapshot returns a consistent copy of the counters.
func (m *Metrics) Snapshot() Snapshot {
	if m == nil {
		return Snapshot{}
	}
	snapshot := Snapshot{
		CallsTotal:    m.callsTotal.Load(),
		CallsFailed:   m.callsFailed.Load(),
		CallsDenied:   m.callsDenied.Load(),
		BytesOutTotal: m.bytesOutTotal.Load(),
		LastLatencyMS: m.lastLatencyMS.Load(),
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.perTool) > 0 {
		snapshot.PerTool = make(map[string]ToolSnapshot, len(m.perTool))
		for name, counters := range m.perTool {
			snapshot.PerTool[name] = ToolSnapshot{
				Calls:  counters.calls,
				Failed: counters.failed,
				Denied: counters.denied,
			}
		}
	}
	return snapshot
}

// isDeniedResult reports whether a tool result is a policy refusal rather than an
// execution failure.
//
// The distinction matters for operators: a spike in denied calls means the
// allowlist or profile is wrong, while a spike in failed calls means the target
// or the command is broken.
func isDeniedResult(metadata map[string]interface{}) bool {
	if metadata == nil {
		return false
	}
	code, _ := metadata["error_code"].(string)
	switch code {
	case CodeCommandDenied, CodePathDenied:
		return true
	default:
		return false
	}
}

// metricsContextKey carries per-request metric labels into a tool execution.
type metricsContextKey struct{}

// WithMetrics attaches the server counter set to a context so the audit wrapper can
// observe without holding a direct reference.
func WithMetrics(ctx context.Context, metrics *Metrics) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if metrics == nil {
		return ctx
	}
	return context.WithValue(ctx, metricsContextKey{}, metrics)
}

// metricsFromContext retrieves the counter set, if any.
func metricsFromContext(ctx context.Context) *Metrics {
	if ctx == nil {
		return nil
	}
	metrics, _ := ctx.Value(metricsContextKey{}).(*Metrics)
	return metrics
}
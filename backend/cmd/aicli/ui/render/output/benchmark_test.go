package output

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Benchmark 基线（11.6）：四种 route 分开报告 ns/op、allocs/op 与
// retained bytes。
//
//   - PhysicalOnly：单 physical primary（MemorySink），无 mirror；
//   - PhysicalPlusMetadata：physical primary + 一个 metadata-only capture
//     mirror（StorePayload=false，默认 hash-only，最贴近生产默认）;
//   - PhysicalPlusWire：physical primary + 一个 full wire capture mirror
//     （StorePayload=true）;
//   - PhysicalPlusVirtual：physical primary + 一个 VirtualTerminalSink。
//
// 阶段门禁（11.6 正文）：以迁移前固定 fixture 为基线，physical-only 中位
// 开销目标不超过 +5% 或 +5µs/batch（取较宽者），额外 allocation 不超过
// 2 allocs/batch。执行：go test -bench BenchmarkGateway -benchmem ./cmd/aicli/ui/render/output/

func benchGatewayRoute(b *testing.B, primary RenderOutputSink, mirrors []RenderMirror) {
	b.Helper()
	opts := gatewayOptions()
	// 生产路径用 SystemClock（time.Timer）；FakeClock 的 timer 结构是
	// 测试注入，不参与性能基线。
	opts.Clock = SystemClock{}
	desc := primary.Descriptor()
	gw, err := NewRenderOutputGateway("bench-"+fmt.Sprint(b.N), opts, RenderRouteConfig{
		Primary:            primary,
		PrimaryOwnership:   SinkOwned,
		ProjectionTargetID: desc.ProjectionTargetID,
		Mirrors:            mirrors,
	})
	if err != nil {
		b.Fatalf("gateway: %v", err)
	}
	gw.Run()
	b.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3e9)
		defer cancel()
		_ = gw.Close(ctx)
	})
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := gw.Submit(ctx, RenderIntent{
			IntentID: fmt.Sprintf("bench-%d", i),
			Kind:     TransactionFrame,
			Bytes:    []byte("benchmark payload bytes with stable length"),
		})
		if r.Admission.Decision != AdmissionAccepted || r.Primary == nil {
			b.Fatalf("submit %d: %+v", i, r.Admission)
		}
	}
	b.StopTimer()
	// retained bytes：journal 中最近的 record + capture retained（有 mirror 时）。
	var retained int
	recs := gw.RecentDeliveries(b.N)
	for _, rec := range recs {
		retained += rec.Batch.BytesLength
	}
	if len(mirrors) > 0 {
		for _, m := range gw.Snapshot().Mirrors {
			retained += m.Sink.RetainedBytes
		}
	}
	b.ReportMetric(float64(retained), "retained_bytes")
}

func BenchmarkGatewayPhysicalOnly(b *testing.B) {
	primary := NewMemorySink(TargetDescriptor{
		SinkID:             "bench-physical",
		Class:              TargetClassPhysical,
		ProjectionTargetID: "pt-bench-physical",
	})
	benchGatewayRoute(b, primary, nil)
}

func BenchmarkGatewayPhysicalPlusMetadata(b *testing.B) {
	primary := NewMemorySink(TargetDescriptor{
		SinkID:             "bench-physical",
		Class:              TargetClassPhysical,
		ProjectionTargetID: "pt-bench-physical",
	})
	capture := NewCaptureSink("pt-bench-capture", CaptureOptions{
		MaxEntries: 256,
		MaxBytes:   1 << 20,
		// StorePayload 默认 false = hash-only（生产默认 metadata）。
	})
	benchGatewayRoute(b, primary, []RenderMirror{{
		Sink:      capture,
		Policy:    MirrorBestEffort,
		ApplyMode: MirrorApplyMetadataOnly,
		Ownership: SinkOwned,
		Timeout:   benchMirrorTimeout,
	}})
}

func BenchmarkGatewayPhysicalPlusWire(b *testing.B) {
	primary := NewMemorySink(TargetDescriptor{
		SinkID:             "bench-physical",
		Class:              TargetClassPhysical,
		ProjectionTargetID: "pt-bench-physical",
	})
	capture := NewCaptureSink("pt-bench-capture", CaptureOptions{
		MaxEntries:   256,
		MaxBytes:     1 << 20,
		StorePayload: true,
	})
	benchGatewayRoute(b, primary, []RenderMirror{{
		Sink:      capture,
		Policy:    MirrorBestEffort,
		ApplyMode: MirrorApplyBytes,
		Ownership: SinkOwned,
		Timeout:   benchMirrorTimeout,
	}})
}

func BenchmarkGatewayPhysicalPlusVirtual(b *testing.B) {
	primary := NewMemorySink(TargetDescriptor{
		SinkID:             "bench-physical",
		Class:              TargetClassPhysical,
		ProjectionTargetID: "pt-bench-physical",
	})
	virtual := NewVirtualTerminalSink("pt-bench-virtual", newFakeEmulator(80, 24), VirtualSinkOptions{})
	benchGatewayRoute(b, primary, []RenderMirror{{
		Sink:      virtual,
		Policy:    MirrorBestEffort,
		ApplyMode: MirrorApplyBytes,
		Ownership: SinkOwned,
		Timeout:   benchMirrorTimeout,
	}})
}

const benchMirrorTimeout = 1 * time.Second

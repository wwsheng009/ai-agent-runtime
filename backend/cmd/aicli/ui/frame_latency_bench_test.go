package ui

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// S0（P3 性能收敛）基线仪器：帧时延分布 / 每帧分配 / 历史规模矩阵。
//
// 依据：docs/plan/aicli-render-p3-performance-plan-20261007.md §2 S0。
//
// 挂具与生产统一路径同构：UIController（内置 reducer）→ presenter.Attach()
// （effect consumer）→ TerminalSessionExecutor → TerminalSession → 计数 writer。
// 每个 delta = 一次 UpdateActiveCellAction；每 delta 的帧栅栏 =
// controller.WaitIdle()（reducer/effects 排空）+ presenter.Request()+WaitIdle()
// （物理帧写完，既有 native scrollback 挂具同款口径）。
//
// 计时口径：本机 Windows 在紧循环内 time.Now() 读数量化为 ~0.5ms（单 delta
// 常落在同一量子内读出 0），因此每个样本测量 frameBenchBatchDeltas 个 delta 的
// 批耗时并摊薄为 per-delta（量子误差 /16 ≈ 30µs，对 ~ms 级帧可忽略）；
// 分布为批均值的 p50/p95/max。跨规模斜率与 16ms 目标线的硬断言在 S1–S4
// 差分验收中启用；本文件只测量与报告。

const (
	frameBenchChunk          = "流式输出的一行内容，包含中英文混合 token 流。\n"
	frameBenchInitialSource  = "初始内容行一。\n初始内容行二。\n初始内容行三。\n初始内容行四。\n"
	frameBenchSampleCapacity = 4096
	frameBenchBatchDeltas    = 16
)

// frameBenchWriter 计数并丢弃物理字节（不引入终端模拟开销）。
type frameBenchWriter struct {
	writes atomic.Uint64
	bytes  atomic.Uint64
}

func (w *frameBenchWriter) Write(data []byte) (int, error) {
	w.writes.Add(1)
	w.bytes.Add(uint64(len(data)))
	return len(data), nil
}

type frameLatencyDistribution struct {
	p50 uint64
	p95 uint64
	max uint64
}

func frameBenchPercentile(sorted []uint64, pct int) uint64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := (len(sorted)*pct + 99) / 100
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

func frameBenchDistribution(samples []uint64) frameLatencyDistribution {
	if len(samples) == 0 {
		return frameLatencyDistribution{}
	}
	sorted := append([]uint64(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return frameLatencyDistribution{
		p50: frameBenchPercentile(sorted, 50),
		p95: frameBenchPercentile(sorted, 95),
		max: sorted[len(sorted)-1],
	}
}

type frameLatencySamples struct {
	perDelta []uint64
	batches  int
	deltas   int
	flushed  int
}

func (s *frameLatencySamples) addBatch(elapsed time.Duration, deltas, flushed int) {
	s.batches++
	s.deltas += deltas
	s.flushed += flushed
	if len(s.perDelta) >= frameBenchSampleCapacity || deltas <= 0 {
		return
	}
	s.perDelta = append(s.perDelta, uint64(elapsed)/uint64(deltas))
}

type frameLatencyHarness struct {
	controller   *UIController
	presenter    *TerminalSessionPresenter
	writer       *frameBenchWriter
	source       strings.Builder
	viewportRows int
	skippedRows  int
}

// newFrameLatencyHarness 构建与生产同构的统一会话挂具。historyCells=0 表示
// 纯 active 流（无 finalized 历史）。
func newFrameLatencyHarness(b *testing.B, historyCells int) *frameLatencyHarness {
	b.Helper()
	controller := NewUIController(UIControllerConfig{}, nil, nil)
	go controller.Run()
	writer := &frameBenchWriter{}
	presenter := NewTerminalSessionPresenterForSession(controller, NewTerminalSession(writer), nil)
	if !presenter.Attach() {
		b.Fatal("attach presenter")
	}
	b.Cleanup(func() {
		presenter.Close()
		controller.Close()
		controller.WaitIdle()
	})

	post := func(actions ...UIAction) {
		b.Helper()
		for _, action := range actions {
			if !controller.Post(action) {
				b.Fatalf("post %T", action)
			}
		}
		controller.WaitIdle()
	}
	post(
		Resize{Width: 100, Height: 24, Generation: 1},
		SetSemanticActiveCellProjectionAction{Enabled: true},
		ShowPromptAction{Line: "> "},
	)
	if historyCells > 0 {
		post(ReplaceTranscriptAction{Snapshot: benchResumedSnapshot(historyCells)})
	}
	post(SetActiveCellAction{Active: benchMutableActive("")})
	presenter.Request()
	presenter.WaitIdle()

	harness := &frameLatencyHarness{controller: controller, presenter: presenter, writer: writer}
	harness.source.WriteString(frameBenchInitialSource)
	// 窗口占比：S1 起每帧只物化 viewport 行；rows 1..Top-1（历史区）不再
	// SGR 编码/VT 展开。此处只记录，循环结束后上报（计时循环前的
	// ReportMetric 会被测试框架丢弃）。
	probe := ComposeTerminalFramePlan(controller.State().AppState)
	harness.viewportRows = terminalFrameViewportArea(probe).Height
	harness.skippedRows = probe.Geometry.Height - harness.viewportRows
	return harness
}

// stepOnce 推进一个流式 delta 并等待其物理帧完成，返回本步是否发生物理写。
func (h *frameLatencyHarness) stepOnce(b *testing.B) bool {
	b.Helper()
	h.source.WriteString(frameBenchChunk)
	current := h.controller.State().Active
	next := benchMutableActive(h.source.String())
	next.Revision = current.Revision + 1

	writesBefore := h.writer.writes.Load()
	if !h.controller.Post(UpdateActiveCellAction{
		ExpectedCellID:   current.CellID,
		ExpectedRevision: current.Revision,
		Active:           next,
	}) {
		b.Fatal("delta post dropped")
	}
	h.controller.WaitIdle()
	h.presenter.Request()
	h.presenter.WaitIdle()
	return h.writer.writes.Load() > writesBefore
}

// runBatch 连续推进 count 个 delta，返回批耗时与本批物理写次数。
func (h *frameLatencyHarness) runBatch(b *testing.B, count int) (time.Duration, int) {
	b.Helper()
	start := time.Now()
	flushed := 0
	for index := 0; index < count; index++ {
		if h.stepOnce(b) {
			flushed++
		}
	}
	return time.Since(start), flushed
}

// runFrameDeltaLatency 在给定历史规模下测量逐 delta 帧时延分布（批均值口径）。
func runFrameDeltaLatency(b *testing.B, historyCells int) {
	b.Helper()
	harness := newFrameLatencyHarness(b, historyCells)
	b.ResetTimer()
	samples := &frameLatencySamples{}
	for b.Loop() {
		elapsed, flushed := harness.runBatch(b, frameBenchBatchDeltas)
		samples.addBatch(elapsed, frameBenchBatchDeltas, flushed)
	}

	dist := frameBenchDistribution(samples.perDelta)
	b.ReportMetric(float64(samples.batches), "batches")
	b.ReportMetric(float64(samples.deltas), "deltas")
	b.ReportMetric(float64(dist.p50), "p50_ns/delta")
	b.ReportMetric(float64(dist.p95), "p95_ns/delta")
	b.ReportMetric(float64(dist.max), "max_ns/delta")
	b.ReportMetric(float64(harness.viewportRows), "viewport_rows")
	b.ReportMetric(float64(harness.skippedRows), "skipped_rows")
	if samples.deltas > 0 {
		b.ReportMetric(float64(samples.flushed)/float64(samples.deltas), "flushed/delta")
	}
	if samples.flushed != samples.deltas {
		b.Fatalf("frame latency harness missed physical frames: flushed=%d deltas=%d", samples.flushed, samples.deltas)
	}
}

// BenchmarkFrameLatencyStreaming：300 个 finalized cell 背景下的逐 delta
// 帧时延分布（S0 主基线）。
func BenchmarkFrameLatencyStreaming(b *testing.B) {
	runFrameDeltaLatency(b, 300)
}

// BenchmarkFrameCostScale：历史规模矩阵。ns/delta 随历史规模的增长斜率是
// 「成本与历史总量脱钩」的直接判据（2× 规模斜率比 ≈1 为目标）。
func BenchmarkFrameCostScale(b *testing.B) {
	for _, cells := range []int{0, 300, 2000, 6719} {
		cells := cells
		b.Run(fmt.Sprintf("history-%d", cells), func(b *testing.B) {
			runFrameDeltaLatency(b, cells)
		})
	}
}

// BenchmarkFrameAllocsPerDelta：每 delta 分配与 GC（ReadMemStats 口径）。
// 每轮迭代 64 个 delta，报告 B/delta、allocs/delta、GC/轮。
func BenchmarkFrameAllocsPerDelta(b *testing.B) {
	const batch = 64
	harness := newFrameLatencyHarness(b, 300)
	b.ResetTimer()
	var before, after runtime.MemStats
	for b.Loop() {
		runtime.ReadMemStats(&before)
		for index := 0; index < batch; index++ {
			harness.stepOnce(b)
		}
		runtime.ReadMemStats(&after)
		b.ReportMetric(float64(after.TotalAlloc-before.TotalAlloc)/batch, "B/delta")
		b.ReportMetric(float64(after.Mallocs-before.Mallocs)/batch, "allocs/delta")
		b.ReportMetric(float64(after.NumGC-before.NumGC), "GC/iter")
	}
}

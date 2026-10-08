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
// 分布为批均值的 p50/p95/max。以下硬断言只在显式运行基准时校验（不进默认
// go test）：p95 < 16ms 帧预算；历史规模矩阵的 6719/0 斜率比 ≤ 2×；分配
// 规模矩阵的 6719/300 比 ≤ 3×（红线，余量见 P3 计划 §5 记录）。

const (
	frameBenchChunk = "流式输出的一行内容，包含中英文混合 token 流。\n"
	// frameBenchP95BudgetNs 是 P3 §3 硬验收的 p95 帧时延红线：16ms 帧预算。
	frameBenchP95BudgetNs = int64(16 * time.Millisecond)
	// frameBenchScaleHistoryBudgetRatio 是成本-历史规模红线：6719 cells 的
	// p50/delta 不得超过 0 cells 基线的 2×（O(delta) 不变量的粗红线）。
	frameBenchScaleHistoryBudgetRatio = 2.0
	frameBenchInitialSource           = "初始内容行一。\n初始内容行二。\n初始内容行三。\n初始内容行四。\n"
	frameBenchSampleCapacity          = 4096
	frameBenchBatchDeltas             = 16
)

// frameBenchMarkdownChunk 是结构化（Markdown + Go 代码栅栏）流式挂具的增量。
// 每个 delta 追加一个完整小节：标题、列表（含行内 code/链接）、闭合代码栅栏、
// 正文段落——覆盖 goldmark 解析 + 行内渲染 + chroma 高亮的典型组合，且保证
// 渲染尾部逐帧变化（物理帧栅栏不会因无变化而跳过）。
const frameBenchMarkdownChunk = "## 流式小节标题\n\n" +
	"- 列表项一：`inline code` 与 **强调** 混排的典型行。\n" +
	"- 列表项二：[链接](https://example.com) 与普通文本。\n\n" +
	"```go\n" +
	"func benchHandler(ctx context.Context) error {\n" +
	"\tif err := ctx.Err(); err != nil {\n" +
	"\t\treturn err\n" +
	"\t}\n" +
	"\treturn nil\n" +
	"}\n" +
	"```\n\n" +
	"正文段落：包含中英文混合 token 与标点的典型流式输出，用于撑起渲染宽度。\n"

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
	chunk        string
	viewportRows int
	skippedRows  int
}

// newFrameLatencyHarness 构建与生产同构的统一会话挂具。historyCells=0 表示
// 纯 active 流（无 finalized 历史）。
func newFrameLatencyHarness(b *testing.B, historyCells int) *frameLatencyHarness {
	b.Helper()
	return newFrameLatencyHarnessWithChunk(b, historyCells, frameBenchChunk)
}

// newFrameLatencyHarnessWithChunk 与 newFrameLatencyHarness 同构，但用给定增量
// 文本驱动 active 流（结构化源基准用）。
func newFrameLatencyHarnessWithChunk(b *testing.B, historyCells int, chunk string) *frameLatencyHarness {
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

	harness := &frameLatencyHarness{controller: controller, presenter: presenter, writer: writer, chunk: chunk}
	harness.source.WriteString(frameBenchInitialSource)
	// 窗口占比：S1 起每帧只物化 viewport 行；rows 1..Top-1（历史区）不再
	// SGR 编码/VT 展开。此处只记录，循环结束后上报（计时循环前的
	// ReportMetric 会被测试框架丢弃）。
	// DiagnosticState 与生产执行器的快照同构：无投递账本。State()/AppState()
	// 会深拷贝整张 HistoryCommitLedger（S2 pprof：300 cells 时占挂具分配的
	// 75%，纯属测量噪声），生产逐帧路径（terminalSessionSchedule/Snapshot）
	// 从不克隆账本。
	probe := ComposeTerminalFramePlan(controller.DiagnosticState().AppState)
	harness.viewportRows = terminalFrameViewportArea(probe).Height
	harness.skippedRows = probe.Geometry.Height - harness.viewportRows
	return harness
}

// stepOnce 推进一个流式 delta 并等待其物理帧完成，返回本步是否发生物理写。
func (h *frameLatencyHarness) stepOnce(b *testing.B) bool {
	b.Helper()
	h.source.WriteString(h.chunk)
	// 只读 active 栅栏：与生产适配器一致地使用无账本访问器；不得用
	// State()/AppState()（它们会克隆整张投递账本）。
	current := h.controller.ActiveCellState()
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
func runFrameDeltaLatency(b *testing.B, historyCells int) frameLatencyDistribution {
	b.Helper()
	return runFrameDeltaLatencyWithChunk(b, historyCells, frameBenchChunk)
}

// runFrameDeltaLatencyWithChunk 是 runFrameDeltaLatency 的增量文本参数化版本。
// 返回分布供规模矩阵汇总断言。
func runFrameDeltaLatencyWithChunk(b *testing.B, historyCells int, chunk string) frameLatencyDistribution {
	b.Helper()
	harness := newFrameLatencyHarnessWithChunk(b, historyCells, chunk)
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
	if int64(dist.p95) >= frameBenchP95BudgetNs {
		b.Fatalf("P3 硬验收失败：p95 %dns/delta 超出 16ms 帧预算（p50=%d max=%d viewport_rows=%d）",
			dist.p95, dist.p50, dist.max, harness.viewportRows)
	}
	return dist
}

// BenchmarkFrameLatencyStreaming：300 个 finalized cell 背景下的逐 delta
// 帧时延分布（S0 主基线）。
func BenchmarkFrameLatencyStreaming(b *testing.B) {
	runFrameDeltaLatency(b, 300)
}

// BenchmarkFrameLatencyStreamingMarkdown：结构化源（Markdown + 代码栅栏）的
// 逐 delta 帧时延分布。量化 active band 每帧全源 markdown.Render 的成本，
// 作为结构化增量（跨帧缓存）的基线与差分判据。
func BenchmarkFrameLatencyStreamingMarkdown(b *testing.B) {
	runFrameDeltaLatencyWithChunk(b, 300, frameBenchMarkdownChunk)
}

// BenchmarkFrameCostScale：历史规模矩阵。ns/delta 随历史规模的增长斜率是
// 「成本与历史总量脱钩」的直接判据（2× 规模斜率比 ≈1 为目标）。
func BenchmarkFrameCostScale(b *testing.B) {
	means := map[int]uint64{}
	for _, cells := range []int{0, 300, 2000, 6719} {
		cells := cells
		b.Run(fmt.Sprintf("history-%d", cells), func(b *testing.B) {
			dist := runFrameDeltaLatency(b, cells)
			means[cells] = dist.p50
		})
	}
	base, ok := means[0]
	if ok && base > 0 {
		if largest := means[6719]; largest > uint64(float64(base)*frameBenchScaleHistoryBudgetRatio) {
			b.Fatalf("P3 硬验收失败：6719 cells 的 p50/delta %dns 超出 0 cells 基线 %dns 的 %.1f×（成本与历史总量脱钩不变量被破坏）",
				largest, base, frameBenchScaleHistoryBudgetRatio)
		}
	}
}

// BenchmarkFrameAllocsPerDelta：每 delta 分配与 GC（ReadMemStats 口径）。
// 每轮迭代 64 个 delta，报告 B/delta、allocs/delta、GC/轮。
func BenchmarkFrameAllocsPerDelta(b *testing.B) {
	runFrameAllocsPerDelta(b, frameBenchChunk)
}

// BenchmarkFrameAllocsPerDeltaMarkdown：结构化源的同口径分配/GC 报告。
func BenchmarkFrameAllocsPerDeltaMarkdown(b *testing.B) {
	runFrameAllocsPerDelta(b, frameBenchMarkdownChunk)
}

// frameAllocsPerDeltaResult 是分配基准的轮均值口径。
type frameAllocsPerDeltaResult struct {
	bytesPerDelta  float64
	allocsPerDelta float64
	gcPerIter      float64
}

// BenchmarkFrameAllocsScale：每 delta 分配的历史规模不变量（P3 §3 硬验收）。
// 6719/300 的 B/delta 与 allocs/delta 比不得超过 3×（红线；实测余量见 §5 记录）。
func BenchmarkFrameAllocsScale(b *testing.B) {
	results := map[int]frameAllocsPerDeltaResult{}
	for _, cells := range []int{300, 6719} {
		cells := cells
		b.Run(fmt.Sprintf("history-%d", cells), func(b *testing.B) {
			results[cells] = runFrameAllocsPerDeltaAt(b, cells, frameBenchChunk)
		})
	}
	small, ok := results[300]
	if !ok || small.bytesPerDelta == 0 || small.allocsPerDelta == 0 {
		return
	}
	large := results[6719]
	const allocsScaleBudgetRatio = 3.0
	if large.bytesPerDelta > allocsScaleBudgetRatio*small.bytesPerDelta ||
		large.allocsPerDelta > allocsScaleBudgetRatio*small.allocsPerDelta {
		b.Fatalf("P3 硬验收失败：6719/300 分配比超 %.1f×（B/delta %.0f→%.0f，allocs/delta %.0f→%.0f）",
			allocsScaleBudgetRatio, small.bytesPerDelta, large.bytesPerDelta,
			small.allocsPerDelta, large.allocsPerDelta)
	}
}

func runFrameAllocsPerDelta(b *testing.B, chunk string) frameAllocsPerDeltaResult {
	return runFrameAllocsPerDeltaAt(b, 300, chunk)
}

func runFrameAllocsPerDeltaAt(b *testing.B, historyCells int, chunk string) frameAllocsPerDeltaResult {
	b.Helper()
	const batch = 64
	harness := newFrameLatencyHarnessWithChunk(b, historyCells, chunk)
	b.ResetTimer()
	var before, after runtime.MemStats
	var bytesSum, allocsSum, gcSum float64
	iterations := 0
	for b.Loop() {
		runtime.ReadMemStats(&before)
		for index := 0; index < batch; index++ {
			harness.stepOnce(b)
		}
		runtime.ReadMemStats(&after)
		bytesSum += float64(after.TotalAlloc - before.TotalAlloc)
		allocsSum += float64(after.Mallocs - before.Mallocs)
		gcSum += float64(after.NumGC - before.NumGC)
		iterations++
	}
	result := frameAllocsPerDeltaResult{}
	if iterations > 0 {
		result.bytesPerDelta = bytesSum / float64(iterations*batch)
		result.allocsPerDelta = allocsSum / float64(iterations*batch)
		result.gcPerIter = gcSum / float64(iterations)
	}
	b.ReportMetric(result.bytesPerDelta, "B/delta")
	b.ReportMetric(result.allocsPerDelta, "allocs/delta")
	b.ReportMetric(result.gcPerIter, "GC/iter")
	return result
}

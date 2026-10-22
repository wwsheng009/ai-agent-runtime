package ui

import (
	"context"
	"errors"
	"math/rand"
	"sort"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// transcript_virtual_pager_test.go —— T5（Spike-3）契约/性能测试：
// 冷区回填（成功/失败退避/Unrebuildable）、预取规划、虚拟列表高度与锚点、
// ExpandAll 估算→校正，以及 VisibleRange 的 p95 性能门禁。

func newT5Residency(t *testing.T, cells int, store CellBodyStore) (*TranscriptResidency, *TranscriptBackfiller) {
	t.Helper()
	residency := newPolicyTestResidency(t, cells, store, true)
	facts := func(int) ResidencyGuardFacts { return policyTestFacts() }
	for len(residency.Window.Cells) > 0 {
		if _, err := residency.SlidePrefix(context.Background(), residency.Window.EndSeq, facts); err != nil {
			t.Fatalf("slide to empty: %v", err)
		}
	}
	return residency, NewTranscriptBackfiller(residency, store)
}

func coldSeqIDs(t *testing.T, residency *TranscriptResidency, from, to int) []scene.CellID {
	t.Helper()
	ids := make([]scene.CellID, 0, to-from)
	for seq := from; seq < to; seq++ {
		id, ok := residency.CellIDAtSeq(seq)
		if !ok {
			t.Fatalf("seq %d 无 cell", seq)
		}
		ids = append(ids, id)
	}
	return ids
}

func TestBackfillLoadsColdBodiesIntoPageCache(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryCellBodyStore()
	residency, backfiller := newT5Residency(t, 300, store)
	ids := coldSeqIDs(t, residency, 10, 20)

	if enqueued := backfiller.EnsureResident(ids...); enqueued != 10 {
		t.Fatalf("入队=%d，期望 10", enqueued)
	}
	if again := backfiller.EnsureResident(ids...); again != 0 {
		t.Fatalf("重复入队未去重：%d", again)
	}
	if state := residency.StateOf(ids[0]); state != Loading {
		t.Fatalf("回填中状态应为 Loading，实际 %v", state)
	}
	processed, err := backfiller.BackfillOnce(ctx)
	if err != nil || processed != 10 {
		t.Fatalf("backfill: processed=%d err=%v", processed, err)
	}
	if state := residency.StateOf(ids[0]); state != Cold {
		t.Fatalf("完成后应回到 Cold（body 在页面缓存），实际 %v", state)
	}
	cell, ok := backfiller.Cache.Get(ids[0])
	if !ok || cell.Source == "" {
		t.Fatalf("页面缓存未装入 body：ok=%v", ok)
	}
	stats := backfiller.Stats()
	if stats.Loaded != 10 || stats.Failed != 0 || stats.Unrebuildable != 0 {
		t.Fatalf("回填统计不符：%+v", stats)
	}
	if pending := backfiller.Pending(); pending != 0 {
		t.Fatalf("队列应清空，剩 %d", pending)
	}
}

type failingGetStore struct{ err error }

func (f failingGetStore) Put(context.Context, string, CellBodyPayload) error { return nil }
func (f failingGetStore) Get(context.Context, string, scene.CellID, uint64) (CellBodyPayload, bool, error) {
	return CellBodyPayload{}, false, f.err
}

func TestBackfillFailureRetriesThenUnrebuildable(t *testing.T) {
	ctx := context.Background()
	// 写入使用内存 store，回填使用注入失败的 store。
	writeStore := NewMemoryCellBodyStore()
	residency, _ := newT5Residency(t, 20, writeStore)
	backfiller := NewTranscriptBackfiller(residency, failingGetStore{err: errors.New("io error")})
	ids := coldSeqIDs(t, residency, 0, 3)
	if enqueued := backfiller.EnsureResident(ids...); enqueued != 3 {
		t.Fatalf("入队=%d", enqueued)
	}
	for attempt := 0; attempt < 3; attempt++ {
		if _, err := backfiller.BackfillOnce(ctx); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
	}
	if state := residency.StateOf(ids[0]); state != Unrebuildable {
		t.Fatalf("连续失败后应为 Unrebuildable，实际 %v", state)
	}
	if stats := backfiller.Stats(); stats.Failed != 9 || stats.Unrebuildable != 3 {
		t.Fatalf("失败统计不符：%+v", stats)
	}
	if enqueued := backfiller.EnsureResident(ids...); enqueued != 0 {
		t.Fatalf("Unrebuildable 不得重试：%d", enqueued)
	}
}

func TestSeqRangesForPrefetchAndBackfillerPrefetch(t *testing.T) {
	ranges := SeqRangesForPrefetch(50, 60, 100, 10, 2, 1)
	want := []SeqRange{{From: 30, To: 50}, {From: 60, To: 70}}
	if len(ranges) != len(want) || ranges[0] != want[0] || ranges[1] != want[1] {
		t.Fatalf("ranges=%+v，期望 %+v", ranges, want)
	}
	// 冷区裁剪：forward 超出冷区应截断。
	ranges = SeqRangesForPrefetch(90, 95, 100, 10, 2, 1)
	if len(ranges) != 2 || ranges[0] != (SeqRange{From: 70, To: 90}) || ranges[1] != (SeqRange{From: 95, To: 100}) {
		t.Fatalf("裁剪语义不符：%+v", ranges)
	}

	store := NewMemoryCellBodyStore()
	_, backfiller := newT5Residency(t, 100, store)
	backfiller.PageCells = 10
	backfiller.PrefetchLookbehind = 2
	backfiller.PrefetchForward = 1
	if enqueued := backfiller.PrefetchVisible(50, 60); enqueued != 30 {
		t.Fatalf("预取入队=%d，期望 30", enqueued)
	}
	if pending := backfiller.Pending(); pending != 30 {
		t.Fatalf("pending=%d，期望 30", pending)
	}
	// 预取全部可回填且内容通过校验。
	if _, err := backfiller.BackfillOnce(context.Background()); err != nil {
		t.Fatalf("backfill: %v", err)
	}
}

func TestVirtualPagerHeightsAnchorsAndExpandAll(t *testing.T) {
	store := NewMemoryCellBodyStore()
	residency, _ := newT5Residency(t, 300, store)
	pager := NewTranscriptVirtualPager(residency)

	// 非 ExpandAll：总高度 = 冷区账本行数（这里是全部 300 cell × 2 行）。
	if total := pager.TotalRows(); total != 600 {
		t.Fatalf("total=%d，期望 600", total)
	}
	// 锚点往返：随机行号 → 锚点 → 可见范围覆盖该 cell。
	rng := rand.New(rand.NewSource(42))
	for probe := 0; probe < 200; probe++ {
		row := rng.Int63n(pager.TotalRows())
		anchor, ok := pager.AnchorAtRow(row)
		if !ok {
			t.Fatalf("row %d 无锚点", row)
		}
		start, end, ok := pager.VisibleRange(anchor, 40)
		if !ok || end <= start {
			t.Fatalf("row %d 可见范围异常：%d..%d ok=%v", row, start, end, ok)
		}
		seq, _ := residency.SeqOf(anchor.CellID)
		if seq < start || seq >= end {
			t.Fatalf("row %d：锚点 seq %d 不在 [%d,%d)", row, seq, start, end)
		}
		if base, _ := residency.GlobalRowStart(seq); base+int64(anchor.Row) != row {
			t.Fatalf("row %d 锚点行号还原失败：base=%d row=%d", row, base, anchor.Row)
		}
	}

	// ExpandAll：估算先于校正；校正后高度生效，锚点仍按 cell 定位。
	id := mustCellID(t, residency, 7)
	ambient := pager.ambientHeight(id)
	pager.ExpandAll = true
	if est := pager.HeightOf(id); est != ambient+1 {
		t.Fatalf("展开态估算=%d，期望 %d", est, ambient+1)
	}
	pager.SetExpandedHeight(id, ambient+10)
	if got := pager.HeightOf(id); got != ambient+10 {
		t.Fatalf("校正后高度=%d，期望 %d", got, ambient+10)
	}
	estimated, corrected := pager.Stats()
	if estimated == 0 || corrected != 1 {
		t.Fatalf("估算/校正计数不符：estimated=%d corrected=%d", estimated, corrected)
	}
	// 展开态总高度会变化，但锚点定位函数仍必须可用。
	base, _ := residency.GlobalRowStart(7)
	anchor, ok := pager.AnchorAtRow(base + 1)
	if !ok || anchor.CellID != id {
		t.Fatalf("展开态锚点定位异常：%+v ok=%v", anchor, ok)
	}
}

func mustCellID(t *testing.T, residency *TranscriptResidency, seq int) scene.CellID {
	t.Helper()
	id, ok := residency.CellIDAtSeq(seq)
	if !ok {
		t.Fatalf("seq %d 无 cell", seq)
	}
	return id
}

func pagerPercentile(values []time.Duration, percentile int) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	index := (percentile*len(sorted) + 99) / 100
	if index < 1 {
		index = 1
	}
	if index > len(sorted) {
		index = len(sorted)
	}
	return sorted[index-1]
}

func TestVirtualPagerVisibleRangePerformance(t *testing.T) {
	store := NewMemoryCellBodyStore()
	residency, _ := newT5Residency(t, 5000, store)
	pager := NewTranscriptVirtualPager(residency)
	total := pager.TotalRows()
	if total <= 0 {
		t.Fatal("总高度为 0")
	}
	rng := rand.New(rand.NewSource(7))
	samples := make([]time.Duration, 0, 10000)
	for probe := 0; probe < 10000; probe++ {
		row := rng.Int63n(total)
		anchor, ok := pager.AnchorAtRow(row)
		if !ok {
			t.Fatalf("row %d 无锚点", row)
		}
		start := time.Now()
		_, _, ok = pager.VisibleRange(anchor, 40)
		samples = append(samples, time.Since(start))
		if !ok {
			t.Fatalf("VisibleRange 失败：row=%d", row)
		}
	}
	p95 := pagerPercentile(samples, 95)
	p99 := pagerPercentile(samples, 99)
	t.Logf("VisibleRange：5000 cells / 10000 probes，p95=%v p99=%v", p95, p99)
	if p95 > 100*time.Microsecond {
		t.Fatalf("VisibleRange p95=%v，超过 100µs 门禁", p95)
	}
}

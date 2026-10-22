package ui

import (
	"context"
	"errors"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// transcript_residency_policy_test.go —— T4 契约测试：
// 逐出守卫决策矩阵、滞回/高水位滑动规划、原子写穿滑动（含 fail-closed）、
// pin/非 Hot 状态截断、步长上限与总开关关闭语义。

func policyTestFacts() ResidencyGuardFacts {
	return ResidencyGuardFacts{
		State:              Hot,
		Finalized:          true,
		AckedAll:           true,
		TombstonePermanent: true,
		EpochOpen:          false,
		Pins:               0,
	}
}

func TestEvaluateEvictionGuardsMatrix(t *testing.T) {
	if ok, reason := EvaluateEvictionGuards(policyTestFacts()); !ok || reason != "evictable" {
		t.Fatalf("基线应为可逐出：ok=%v reason=%s", ok, reason)
	}
	singleFailures := []struct {
		name   string
		mutate func(*ResidencyGuardFacts)
		reason string
	}{
		{"not_finalized", func(f *ResidencyGuardFacts) { f.Finalized = false }, "not_finalized"},
		{"unacked", func(f *ResidencyGuardFacts) { f.AckedAll = false }, "unacked_rows"},
		{"tombstone_not_permanent", func(f *ResidencyGuardFacts) { f.TombstonePermanent = false }, "tombstone_not_permanent"},
		{"epoch_open", func(f *ResidencyGuardFacts) { f.EpochOpen = true }, "epoch_open"},
		{"pinned", func(f *ResidencyGuardFacts) { f.Pins = PinPagerTarget }, "pinned"},
		{"cold", func(f *ResidencyGuardFacts) { f.State = Cold }, "state_not_hot"},
		{"loading", func(f *ResidencyGuardFacts) { f.State = Loading }, "state_not_hot"},
		{"unrebuildable", func(f *ResidencyGuardFacts) { f.State = Unrebuildable }, "state_not_hot"},
	}
	for _, testCase := range singleFailures {
		facts := policyTestFacts()
		testCase.mutate(&facts)
		if ok, reason := EvaluateEvictionGuards(facts); ok || reason != testCase.reason {
			t.Fatalf("%s：ok=%v reason=%s，期望 false/%s", testCase.name, ok, reason, testCase.reason)
		}
	}

	// 穷举 Finalized/Acked/Tombstone/EpochOpen/Pinned 五布尔的全部组合：
	// 仅当五项全部满足（且 State=Hot）时可逐出。
	for mask := 0; mask < 32; mask++ {
		pins := PinReason(0)
		if mask&16 != 0 {
			pins = PinQueuedTokens
		}
		facts := ResidencyGuardFacts{
			State:              Hot,
			Finalized:          mask&1 != 0,
			AckedAll:           mask&2 != 0,
			TombstonePermanent: mask&4 != 0,
			EpochOpen:          mask&8 != 0,
			Pins:               pins,
		}
		ok, reason := EvaluateEvictionGuards(facts)
		// 可逐出 ⟺ finalized ∧ acked ∧ permanent ∧ !epochOpen ∧ !pinned
		//（EpochOpen/Pins 是否定项，因此期望掩码是 0b00111 = 7，而非 31）。
		want := mask == 0b00111
		if ok != want {
			t.Fatalf("mask=%05b：ok=%v，期望 %v", mask, ok, want)
		}
		if !ok && reason == "" {
			t.Fatalf("mask=%05b：拒绝必须带原因码", mask)
		}
	}
}

func TestResidencyPolicyPlanSlideHysteresisAndHighWater(t *testing.T) {
	policy := ResidencyPolicy{
		Enabled: true, EvictPrefix: true,
		MaxResidentCells: 512, MaxResidentRows: 4096,
		HysteresisCells: 128, EvictHighWater: 1.25,
	}
	makeWindow := func(cells int, rowsPerCell int32, start int) WindowLedger {
		window := WindowLedger{StartSeq: start, EndSeq: start + cells, Cells: make([]scene.TranscriptCell, cells), LocalRows: make([]int32, cells)}
		for i := range window.LocalRows {
			window.LocalRows[i] = rowsPerCell
		}
		return window
	}

	// 收益小于滞回且非高水位 → 不动。
	if _, should := policy.PlanSlide(makeWindow(500, 1, 100), 200); should {
		t.Fatal("lag=100 ≤ 滞回 128 且非高水位时不应滑动")
	}
	// 收益超过滞回 → 滑动，但受单次步长上限（maxCells/4=128）约束。
	if target, should := policy.PlanSlide(makeWindow(500, 1, 100), 300); !should || target != 228 {
		t.Fatalf("期望 target=228（步长上限），实际 target=%d should=%v", target, should)
	}
	// 高水位（行数 > 4096×1.25=5120）忽略滞回，强制回收。
	if target, should := policy.PlanSlide(makeWindow(500, 20, 100), 150); !should || target != 150 {
		t.Fatalf("高水位应强制滑动到 desired，实际 target=%d should=%v", target, should)
	}
	// desired 不领先 → 不动。
	if _, should := policy.PlanSlide(makeWindow(100, 1, 100), 100); should {
		t.Fatal("desired ≤ start 不应滑动")
	}
	// 空窗口 → 不动。
	if _, should := policy.PlanSlide(WindowLedger{}, 50); should {
		t.Fatal("空窗口不应滑动")
	}
}

func TestDesiredStartPicksTightestConstraint(t *testing.T) {
	if got := DesiredStart(120, -1, -1, 0); got != 120 {
		t.Fatalf("mustKeep：期望 120，实际 %d", got)
	}
	if got := DesiredStart(120, 80, -1, 0); got != 80 {
		t.Fatalf("pager 更紧：期望 80，实际 %d", got)
	}
	if got := DesiredStart(120, -1, 90, 2); got != 88 {
		t.Fatalf("viewport-margin：期望 88，实际 %d", got)
	}
	if got := DesiredStart(-1, -1, -1, 0); got != 0 {
		t.Fatalf("无约束：期望 0，实际 %d", got)
	}
	if got := DesiredStart(10, -1, 5, 100); got != 0 {
		t.Fatalf("margin 超过 viewport：期望 0，实际 %d", got)
	}
}

func policyTestWindowCells(count int) ([]scene.TranscriptCell, []int32, []int8) {
	cells := make([]scene.TranscriptCell, 0, count)
	rows := make([]int32, 0, count)
	gaps := make([]int8, 0, count)
	for i := 0; i < count; i++ {
		cell := indexTestCell(scene.CellID(i+1), scene.KindAssistant, 1)
		cells = append(cells, cell)
		rows = append(rows, 2)
		if i == 0 {
			gaps = append(gaps, 1)
		} else {
			gaps = append(gaps, 0)
		}
	}
	return cells, rows, gaps
}

func newPolicyTestResidency(t *testing.T, cells int, store CellBodyStore, evict bool) *TranscriptResidency {
	t.Helper()
	policy := ResidencyPolicy{
		Enabled: true, EvictPrefix: evict,
		MaxResidentCells: 512, MaxResidentRows: 4096,
		HysteresisCells: 128, EvictHighWater: 1.25,
	}
	residency := NewTranscriptResidency(policy, store, "t4")
	corpus, rows, gaps := policyTestWindowCells(cells)
	if err := residency.InstallWindow(corpus, rows, gaps, 9, 0); err != nil {
		t.Fatalf("install window: %v", err)
	}
	return residency
}

func TestSlidePrefixEvictsWithWriteThrough(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryCellBodyStore()
	residency := newPolicyTestResidency(t, 300, store, true)
	corpus := append([]scene.TranscriptCell(nil), residency.Window.Cells...)
	facts := func(int) ResidencyGuardFacts { return policyTestFacts() }

	outcome, err := residency.SlidePrefix(ctx, 130, facts)
	if err != nil {
		t.Fatalf("slide: %v", err)
	}
	if outcome.Cells != 128 || outcome.Aborted || outcome.Reason != "evicted" {
		t.Fatalf("期望移交 128（步长上限 512/4），实际 %+v", outcome)
	}
	if residency.Window.StartSeq != 128 || len(residency.Window.Cells) != 172 {
		t.Fatalf("窗口状态不符：start=%d cells=%d", residency.Window.StartSeq, len(residency.Window.Cells))
	}
	if residency.Cold.Len() != 128 || residency.Cold.Rows.Total() != 256 {
		t.Fatalf("冷区账本不符：cells=%d rows=%d", residency.Cold.Len(), residency.Cold.Rows.Total())
	}
	if err := residency.Cold.Validate(); err != nil {
		t.Fatalf("冷区账本自校验：%v", err)
	}
	if residency.Cold.Cells[0].LeadingGapRows != 1 {
		t.Fatalf("首格 leading gap 未随移交固化：%d", residency.Cold.Cells[0].LeadingGapRows)
	}
	if residency.Cold.Cells[127].DeliveredAt != 128 {
		t.Fatalf("交付序不符：%d", residency.Cold.Cells[127].DeliveredAt)
	}
	for index, cell := range corpus {
		want := Hot
		if index < 128 {
			want = Cold
		}
		if got := residency.StateOf(cell.ID); got != want {
			t.Fatalf("cell %d 状态=%v，期望 %v", cell.ID, got, want)
		}
	}
	// 写穿可回读且校验通过。
	body, ok, err := store.Get(ctx, "t4", corpus[5].ID, corpus[5].Revision)
	if err != nil || !ok {
		t.Fatalf("回读失败：ok=%v err=%v", ok, err)
	}
	decoded, err := ValidateTranscriptCellBody(body)
	if err != nil || decoded.Source != corpus[5].Source {
		t.Fatalf("回读校验失败：%v", err)
	}
	// 继续滑动直到清空：target 是绝对 seq，且受单次步长上限（128）约束，
	// 因此 172 个剩余 cell 需要两次调用（128 + 44）。
	evicted := outcome.Cells
	for len(residency.Window.Cells) > 0 {
		outcome, err = residency.SlidePrefix(ctx, residency.Window.EndSeq, facts)
		if err != nil {
			t.Fatalf("滑动至清空：%v", err)
		}
		if outcome.Cells == 0 {
			t.Fatalf("窗口未清空且无法继续滑动：%+v", outcome)
		}
		evicted += outcome.Cells
	}
	if evicted != 300 {
		t.Fatalf("累计移交=%d，期望 300", evicted)
	}
	if residency.Window.EndSeq != 300 || len(residency.Window.Cells) != 0 {
		t.Fatalf("窗口应清空：end=%d cells=%d", residency.Window.EndSeq, len(residency.Window.Cells))
	}
	if residency.Cold.Len() != 300 {
		t.Fatalf("冷区应为全部 300，实际 %d", residency.Cold.Len())
	}
}

func TestSlidePrefixWriteThroughFailureIsAtomic(t *testing.T) {
	ctx := context.Background()
	store := failingPutStore{err: errors.New("disk full")}
	residency := newPolicyTestResidency(t, 10, store, true)
	before := TranscriptResidencyCountersSnapshot()
	facts := func(int) ResidencyGuardFacts { return policyTestFacts() }

	outcome, err := residency.SlidePrefix(ctx, 3, facts)
	if err == nil {
		t.Fatal("写穿失败必须返回错误（fail-closed）")
	}
	if !outcome.Aborted || outcome.Reason != "write_through_failed" || outcome.Cells != 0 {
		t.Fatalf("中止语义不符：%+v", outcome)
	}
	if len(residency.Window.Cells) != 10 || residency.Window.StartSeq != 0 {
		t.Fatalf("中止后窗口必须不变：cells=%d start=%d", len(residency.Window.Cells), residency.Window.StartSeq)
	}
	if residency.Cold.Len() != 0 || residency.Cold.Rows.Total() != 0 {
		t.Fatalf("中止后冷区必须为空：%d/%d", residency.Cold.Len(), residency.Cold.Rows.Total())
	}
	if residency.StateOf(1) != Hot {
		t.Fatalf("中止后状态必须保持 Hot，实际 %v", residency.StateOf(1))
	}
	after := TranscriptResidencyCountersSnapshot()
	if after.BodyStorePutErrors-before.BodyStorePutErrors != 1 {
		t.Fatalf("put 错误计数未累加：%d", after.BodyStorePutErrors-before.BodyStorePutErrors)
	}
}

func TestSlidePrefixStopsAtPinnedOrNonHotCell(t *testing.T) {
	ctx := context.Background()
	residency := newPolicyTestResidency(t, 10, nil, true)

	// 第 0 格被 pin → 完全不动。
	pinnedFacts := func(index int) ResidencyGuardFacts {
		facts := policyTestFacts()
		if index == 0 {
			facts.Pins = PinViewportNeighbor
		}
		return facts
	}
	outcome, err := residency.SlidePrefix(ctx, 5, pinnedFacts)
	if err != nil || !outcome.Pinned || outcome.Cells != 0 || outcome.Reason != "pinned" {
		t.Fatalf("首格 pin 语义不符：%+v err=%v", outcome, err)
	}
	if len(residency.Window.Cells) != 10 {
		t.Fatalf("pin 时窗口不应变化：%d", len(residency.Window.Cells))
	}

	// 第 2 格 pin → 只移交连续前缀 2 格，并标记 Pinned。
	partialFacts := func(index int) ResidencyGuardFacts {
		facts := policyTestFacts()
		if index == 2 {
			facts.Pins = PinPagerTarget
		}
		return facts
	}
	outcome, err = residency.SlidePrefix(ctx, 5, partialFacts)
	if err != nil || outcome.Cells != 2 || !outcome.Pinned {
		t.Fatalf("中途 pin 语义不符：%+v err=%v", outcome, err)
	}

	// 非 Hot（Loading）同样阻止：先把当前首格标记 Loading。
	residency = newPolicyTestResidency(t, 10, nil, true)
	residency.MarkState(residency.Window.Cells[0].ID, Loading)
	outcome, err = residency.SlidePrefix(ctx, 5, func(int) ResidencyGuardFacts { return policyTestFacts() })
	if err != nil || outcome.Cells != 0 || !outcome.Pinned || outcome.Reason != "state_not_hot" {
		t.Fatalf("Loading 语义不符：%+v err=%v", outcome, err)
	}
}

func TestSlidePrefixRespectsStepCapAndDisable(t *testing.T) {
	ctx := context.Background()
	residency := newPolicyTestResidency(t, 100, nil, true)
	residency.Policy.MaxResidentCells = 8 // 步长上限 = 2
	facts := func(int) ResidencyGuardFacts { return policyTestFacts() }
	outcome, err := residency.SlidePrefix(ctx, 100, facts)
	if err != nil || outcome.Cells != 2 {
		t.Fatalf("步长上限语义不符：%+v err=%v", outcome, err)
	}

	disabled := newPolicyTestResidency(t, 10, nil, false)
	outcome, err = disabled.SlidePrefix(ctx, 5, facts)
	if err != nil || outcome.Cells != 0 || outcome.Reason != "evict_disabled" {
		t.Fatalf("总开关关闭语义不符：%+v err=%v", outcome, err)
	}
	if len(disabled.Window.Cells) != 10 || disabled.Cold.Len() != 0 {
		t.Fatal("关闭 evictPrefix 时驻留集合必须恒等于窗口")
	}
}

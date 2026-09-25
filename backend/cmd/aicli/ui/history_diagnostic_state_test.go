package ui

import (
	"errors"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// 本文件锁定 §4.8 的诊断快照契约：/debug/chat/status 与 HistoryCommitExecutor
// 只读生命周期投影，不携带 commit ledger（也就不携带渲染 payload）。
//
// 背景：HistoryCommitLedger.byToken 只增不减（唯一删除路径是 byRange 的
// re-mint），而 Clone() 会逐 entry 深拷贝 Commit.Lines，于是恢复会话的每次
// State() 都是 O(整个历史 x payload)。诊断路径此前正是这么做的，且是在持
// actor 互斥量的情况下做——它挡住了渲染器。

// historyDiagnosticFixture 返回一个已经 mint 过历史 commit 的运行中控制器。
// 这些 commit 带渲染 payload，正是 State() 昂贵的来源。
func historyDiagnosticFixture(t *testing.T, count scene.CellID) *UIController {
	t.Helper()
	controller := newHistoryExecutorController(t, nil)
	postHistoryEffectFixture(t, controller, count)
	controller.WaitIdle()
	return controller
}

func historyPayloadLineCount(state UIControllerState) int {
	total := 0
	for _, entry := range state.HistoryEffects.Entries() {
		total += len(entry.Commit.Lines)
	}
	return total
}

// TestDiagnosticStateDropsHistoryPayload 钉住诊断快照的契约：标量、transcript、
// active、bottom 和 queue scalars 都在，但 commit ledger 完全不在；ledger 的
// counters 由独立的 HistoryEffectDiagnostics 投影提供。
// 没有这条断言，/debug/chat/status 会悄悄回到「每次轮询在 actor 互斥量下付一次
// O(整个历史 x payload) 拷贝」的状态。
func TestDiagnosticStateDropsHistoryPayload(t *testing.T) {
	controller := historyDiagnosticFixture(t, 6)

	full := controller.State()
	diag := controller.DiagnosticState()

	fullEntries := full.HistoryEffects.Entries()
	if len(fullEntries) == 0 {
		t.Fatal("fixture minted no history commits")
	}
	if payload := historyPayloadLineCount(full); payload == 0 {
		t.Fatal("fixture commits carry no render payload; the regression this test guards would be invisible")
	}

	diagEntries := diag.HistoryEffects.Entries()
	if len(diagEntries) != 0 {
		t.Fatalf("diagnostic snapshot retained %d ledger entries; want ledger-free snapshot", len(diagEntries))
	}
	if pending := diag.HistoryEffects.Pending(); len(pending) != 0 {
		t.Fatalf("diagnostic snapshot exposed %d pending commits; want ledger-free snapshot", len(pending))
	}

	// 诊断消费方真正读的字段必须一个不少。
	if diag.Revision != full.Revision || diag.LayoutGeneration != full.LayoutGeneration {
		t.Fatalf("diagnostic scalars diverged: revision %d/%d generation %d/%d",
			diag.Revision, full.Revision, diag.LayoutGeneration, full.LayoutGeneration)
	}
	if diag.Geometry.Width != full.Geometry.Width || diag.Geometry.Height != full.Geometry.Height ||
		diag.Geometry.Generation != full.Geometry.Generation {
		t.Fatalf("diagnostic geometry diverged: %#v vs %#v", diag.Geometry, full.Geometry)
	}
	if diag.Lease.Active != full.Lease.Active || diag.Lease.ID != full.Lease.ID {
		t.Fatalf("diagnostic lease diverged: %#v vs %#v", diag.Lease, full.Lease)
	}
	if diag.HistoryEffects.NextToken != full.HistoryEffects.NextToken ||
		diag.HistoryEffects.TerminalEpoch != full.HistoryEffects.TerminalEpoch ||
		diag.HistoryEffects.Frozen != full.HistoryEffects.Frozen ||
		diag.HistoryEffects.ProjectionUnknown != full.HistoryEffects.ProjectionUnknown ||
		diag.HistoryEffects.ReconciliationRequired != full.HistoryEffects.ReconciliationRequired ||
		diag.HistoryEffects.ScrollbackReplayArmed != full.HistoryEffects.ScrollbackReplayArmed ||
		diag.HistoryEffects.PlanIncomplete != full.HistoryEffects.PlanIncomplete ||
		diag.HistoryEffects.PlanStalled != full.HistoryEffects.PlanStalled {
		t.Fatalf("diagnostic queue scalars diverged: %#v vs %#v", diag.HistoryEffects, full.HistoryEffects)
	}

	projected := controller.HistoryEffectDiagnostics()
	if got, want := projected.Summary, full.HistoryEffects.Summary(); got != want {
		t.Fatalf("history-effect diagnostics summary = %#v, want full-state summary %#v", got, want)
	}
	if projected.Frozen != full.HistoryEffects.Frozen ||
		projected.ProjectionUnknown != full.HistoryEffects.ProjectionUnknown ||
		projected.ReconciliationRequired != full.HistoryEffects.ReconciliationRequired ||
		projected.ScrollbackReplayArmed != full.HistoryEffects.ScrollbackReplayArmed ||
		projected.PlanIncomplete != full.HistoryEffects.PlanIncomplete ||
		projected.PlanStalled != full.HistoryEffects.PlanStalled ||
		projected.NextToken != full.HistoryEffects.NextToken ||
		projected.TerminalEpoch != full.HistoryEffects.TerminalEpoch {
		t.Fatalf("history-effect diagnostics scalars diverged: %#v vs %#v", projected, full.HistoryEffects)
	}
	// DiagnoseHistoryPlan / FrameParityWithAppLayout 读 transcript 与 active。
	if got, want := len(diag.Transcript.Snapshot().Cells), len(full.Transcript.Snapshot().Cells); got != want {
		t.Fatalf("diagnostic transcript cells = %d, want %d", got, want)
	}
	if diag.Active.CellID != full.Active.CellID || diag.Active.Phase != full.Active.Phase {
		t.Fatalf("diagnostic active cell diverged: %#v vs %#v", diag.Active, full.Active)
	}
}

// assertSummaryMatchesEntryWalk 要求 Summary() 与 Entries() 的逐条状态遍历给出
// 完全相同的计数。Summary() 之所以存在，就是因为那次遍历为了数几个整数而深拷贝
// 了全部 payload；两者的等价性必须由测试保证，否则「优化」会悄悄改变诊断读数。
func assertSummaryMatchesEntryWalk(t *testing.T, state UIControllerState) {
	t.Helper()
	entries := state.HistoryEffects.Entries()
	want := HistoryEffectQueueSummary{LedgerEntries: len(entries)}
	// 规划耗时（P16）不是 ledger 属性，不能从逐条遍历里推出来：它以整个 pass
	// 为单位记录，因此这里按状态原值搬运，其余字段仍必须与遍历完全一致。
	want.PlanCount = state.HistoryEffects.PlanCount
	want.LastPlanMs = state.HistoryEffects.LastPlanDuration.Milliseconds()
	want.MaxPlanMs = state.HistoryEffects.MaxPlanDuration.Milliseconds()
	for _, entry := range entries {
		switch entry.State {
		case HistoryCommitPending:
			want.Pending++
			if want.OldestPendingToken == 0 {
				want.OldestPendingToken = entry.Commit.Token
				want.OldestPendingGeneration = entry.Commit.LayoutGeneration
			}
		case HistoryCommitInFlight:
			want.InFlight++
		case HistoryCommitAcked:
			want.Acked++
		case HistoryCommitStateFailed:
			want.Failed++
		case HistoryCommitInvalidated:
			want.Invalidated++
		case HistoryCommitAbandoned:
			want.Abandoned++
		}
	}
	if got := state.HistoryEffects.Summary(); got != want {
		t.Fatalf("Summary() = %#v, want %#v (entries=%#v)", got, want, entries)
	}
}

func TestHistoryEffectQueueSummaryMatchesEntryWalk(t *testing.T) {
	// 未投递：全是 pending，oldest 头必须与 Pending() 的首个元素一致。
	pending := historyDiagnosticFixture(t, 5)
	assertSummaryMatchesEntryWalk(t, pending.State())
	// P16 观测契约：规划 pass 必须被计数并留下耗时，否则 P12 的冻结无法归因。
	summary := pending.State().HistoryEffects.Summary()
	if summary.PlanCount == 0 {
		t.Fatal("a planned transcript must record at least one planning pass (P16 attribution)")
	}
	if summary.MaxPlanMs < 0 || summary.LastPlanMs < 0 {
		t.Fatalf("negative plan timing recorded: %#v", summary)
	}
	if diag := pending.HistoryEffectDiagnostics().Summary; diag.PlanCount != summary.PlanCount ||
		diag.LastPlanMs != summary.LastPlanMs || diag.MaxPlanMs != summary.MaxPlanMs {
		t.Fatalf("diagnostic projection lost plan timing: %#v vs %#v", diag, summary)
	}

	// 混合态：失败一次后 drain 停止，ledger 里同时留下 failed 与 pending。
	failing := newHistoryExecutorController(t, nil)
	postHistoryEffectFixture(t, failing, 4)
	failing.WaitIdle()
	executor := NewHistoryCommitExecutor(failing, historyCommitSinkFunc(func(HistoryCommit) HistoryCommitResult {
		return HistoryCommitResult{Err: errors.New("sink refused")}
	}))
	t.Cleanup(executor.Close)
	executor.Request()
	executor.WaitIdle()
	state := failing.State()
	if summary := state.HistoryEffects.Summary(); summary.Failed == 0 {
		t.Fatalf("expected a failed entry after a refusing sink: %#v", summary)
	}
	assertSummaryMatchesEntryWalk(t, state)
}

// TestPendingHistoryCommitMatchesPendingHead 要求窄接口与 Pending()[0] 完全同构：
// 同一个头、同一组门控，但 payload 仍然是脱离的（调用方拿不到 actor 的切片）。
func TestPendingHistoryCommitMatchesPendingHead(t *testing.T) {
	controller := historyDiagnosticFixture(t, 5)

	state := controller.State()
	want := state.HistoryEffects.Pending()
	got, ok := controller.PendingHistoryCommit()
	if len(want) == 0 {
		if ok {
			t.Fatalf("PendingHistoryCommit reported %#v while Pending() is empty", got)
		}
		t.Fatal("fixture produced no eligible pending head")
	}
	if !ok {
		t.Fatal("PendingHistoryCommit found no head while Pending() is non-empty")
	}
	head := want[0]
	if got.Token != head.Token || got.LayoutGeneration != head.LayoutGeneration {
		t.Fatalf("head = %#v, want %#v", got, head)
	}
	if len(got.Lines) != len(head.Lines) {
		t.Fatalf("head payload = %d lines, want %d", len(got.Lines), len(head.Lines))
	}

	// 空队列边界：两者都必须没有头（0 是「无 pending」的哨兵，与诊断输出一致）。
	empty := newHistoryExecutorController(t, nil)
	if head, ok := empty.PendingHistoryCommit(); ok {
		t.Fatalf("empty queue reported a pending head: %#v", head)
	}
	if pending := empty.State().HistoryEffects.Pending(); len(pending) != 0 {
		t.Fatalf("empty queue Pending() = %#v", pending)
	}
}

// TestHistoryCommitGateMatchesEntryProjection 要求 runOne 的窄门控投影与
// Entry() 逐字段一致——它是「claim/ack 不再克隆整个 ledger」的安全网。
func TestHistoryCommitGateMatchesEntryProjection(t *testing.T) {
	controller := historyDiagnosticFixture(t, 3)

	state := controller.State()
	entries := state.HistoryEffects.Entries()
	if len(entries) == 0 {
		t.Fatal("fixture minted no history commits")
	}
	for _, entry := range entries {
		gate := controller.historyCommitGateOf(entry.Commit.Token)
		if !gate.EntryFound || gate.EntryState != entry.State ||
			gate.EntryGeneration != entry.Commit.LayoutGeneration {
			t.Fatalf("gate for token %d = %#v, entry = %#v", entry.Commit.Token, gate, entry)
		}
		if gate.LayoutGeneration != state.LayoutGeneration ||
			gate.Frozen != state.HistoryEffects.Frozen ||
			gate.ProjectionUnknown != state.HistoryEffects.ProjectionUnknown {
			t.Fatalf("gate barriers for token %d = %#v, state = %#v", entry.Commit.Token, gate, state)
		}
	}
	if gate := controller.historyCommitGateOf(1 << 40); gate.EntryFound {
		t.Fatalf("unknown token reported an entry: %#v", gate)
	}
}

// 成本对比基准在 history_prepend_replan_bench_test.go 里与
// clone_history_effects_only 成对出现：那里用的是本仓既有的 resume 语料
// （6,720 cells / 数千条 ledger），只有同一份状态上的两个克隆才可比。

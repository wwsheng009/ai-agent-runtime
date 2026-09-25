package ui

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// coverageSnapshot 造一份「resume 规模」的 transcript：每个 cell 撑开几十行物理
// 行，整体行数远超 layoutBudgetCheckRows，因此任何小预算的规划都必然截断。
func coverageSnapshot(revision uint64, cellCount, linesEach int) *scene.Snapshot {
	cells := make([]*scene.TranscriptCell, 0, cellCount)
	for id := 1; id <= cellCount; id++ {
		var builder strings.Builder
		for line := 0; line < linesEach; line++ {
			fmt.Fprintf(&builder, "row-%04d-%03d 这是一行用于撑开物理行的中文回复内容 %s\n",
				id, line, strings.Repeat("x", 32))
		}
		cells = append(cells, &scene.TranscriptCell{
			ID:       scene.CellID(id),
			Sequence: uint64(id),
			Revision: revision,
			Kind:     scene.KindAssistant,
			Source:   builder.String(),
			Phase:    scene.CellCommitted,
		})
	}
	return regressionCommittedSnapshot(revision, cells...)
}

// assertHistoryCoverage 断言每个 finalized cell 都在 ledger 里有终态记录。
// 「pending=0」不是完整性的证明：live 事故里它同时成立而历史缺了大半。
func assertHistoryCoverage(t *testing.T, controller *UIController, session *TerminalSession, want int) {
	t.Helper()
	state := controller.State()
	covered := make(map[scene.CellID]struct{})
	counts := make(map[HistoryCommitState]int)
	for _, entry := range state.HistoryEffects.Entries() {
		counts[entry.State]++
		if entry.State == HistoryCommitInvalidated || entry.State == HistoryCommitAbandoned {
			continue
		}
		covered[entry.Commit.CellID] = struct{}{}
	}
	missing := 0
	firstMissing := scene.CellID(0)
	for _, cell := range state.AppState.Transcript.Cells {
		if !cellIsFinalizedForHistory(cell) || cell.Source == "" {
			continue
		}
		if _, ok := covered[cell.ID]; !ok {
			missing++
			if firstMissing == 0 {
				firstMissing = cell.ID
			}
		}
	}
	if missing > 0 {
		t.Fatalf("history is missing %d/%d finalized cells (first missing %d, ledger states %v, "+
			"next=%d pending=%d planIncomplete=%t planStalled=%t projection=%+v)",
			missing, want, firstMissing, counts, state.HistoryEffects.NextToken,
			historyPendingCount(state), state.HistoryEffects.PlanIncomplete,
			state.HistoryEffects.PlanStalled, session.ProjectionState())
	}
	if state.HistoryEffects.PlanIncomplete {
		t.Fatalf("a fully delivered transcript still reports an incomplete plan: %#v", state.HistoryEffects)
	}
}

// 需求：resume 的销毁式重放（\x1b[3J + 重新投递）之后，规划必须把**整份** transcript
// 交付到原生 scrollback，而不是停在「预算恰好能走到的那个前缀」上。
//
// live 事故（session_20260924072950_ltYRU9tG，端口 56311 的进程）：6622 cell /
// 291842 物理行的会话 resume 之后，ledger 只有 322 条 acked 提交、pending=0、
// in-flight=0、failed=0、invalidated=0，执行器空闲、recovery_actionable=false，
// 屏幕只恢复了最老的一小段历史。缺口既不在 scrollback 对账、也不在投影已知性上，
// 而且不可自愈 —— 空闲会话不会再有 transcript 迁移来触发下一次规划。
//
// 与 history_planning_budget_test.go 里的单元测试不同，这个测试走**真实的**
// controller + TerminalSession + TerminalSessionExecutor 闭环：规划 -> 事务写盘 ->
// publishResult -> ack 归约 -> 续跑。
func TestArmedResumeDeliversWholeTranscriptAcrossBudgetTruncation(t *testing.T) {
	const (
		cellCount = 1200
		linesEach = 40
		width     = 120
		height    = 40
	)
	// 预算压到 0 以**确定性地**制造截断：live 上 250ms 的预算在 291k 行的会话上
	// 同样只能走出最老的一个前缀（next=1288 / acked=322），而真实预算下是否截断
	// 取决于机器速度与布局缓存状态（warm cache 上 250ms 常常够走完整份历史）。
	// 预算 0 让布局在第一个采样点（layoutBudgetCheckRows 行）必然切断，于是
	// 「截断 + 续跑」这条路径一定被测到，而不是碰巧。
	restoreBudget := historyCommitPlanningBudget
	defer func() { historyCommitPlanningBudget = restoreBudget }()
	historyCommitPlanningBudget = 0

	controller := NewUIController(UIControllerConfig{}, nil, nil)
	go controller.Run()
	physical := &bytes.Buffer{}
	session := NewTerminalSession(physical)
	executor := NewTerminalSessionExecutor(controller, session)
	t.Cleanup(func() {
		executor.Close()
		controller.Close()
		controller.WaitIdle()
	})

	post := func(actions ...UIAction) {
		t.Helper()
		for _, action := range actions {
			if !controller.Post(action) {
				t.Fatalf("post %T", action)
			}
		}
		controller.WaitIdle()
	}
	flush := func() {
		executor.Request()
		executor.WaitIdle()
		controller.WaitIdle()
	}
	// converge 反复唤醒执行器直到历史队列既没有待交付 token、也没有对账/投影
	// 义务，并且连续若干轮不再变化 —— 「收敛」在 live 事故里恰恰是一个骗局：
	// pending=0 时缺口仍然存在，所以收敛之后还要单独断言覆盖度。
	converge := func() {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		stable := 0
		for {
			state := controller.State()
			idle := !state.HistoryEffects.ReconciliationRequired &&
				!state.HistoryEffects.ProjectionUnknown &&
				!state.HistoryEffects.HasPending()
			if idle {
				stable++
				if stable >= 3 {
					break
				}
			} else {
				stable = 0
			}
			if time.Now().After(deadline) {
				t.Fatalf("history projection never converged: %#v", state.HistoryEffects)
			}
			flush()
		}
		flush()
	}

	// 阶段 1：普通装载（启动恢复），不得销毁 scrollback。
	post(
		Resize{Width: width, Height: height, Generation: 1},
		ShowPromptAction{Line: "> "},
		ReplaceTranscriptAction{Snapshot: coverageSnapshot(1, cellCount, linesEach)},
	)
	converge()

	// 阶段 2：/resume —— 同一份 transcript 以 armed replay 重新装载。
	post(ReplaceTranscriptAction{Snapshot: coverageSnapshot(2, cellCount, linesEach), ArmScrollbackReplay: true})
	converge()

	assertHistoryCoverage(t, controller, session, cellCount)
}

// 需求：截断计划的续跑不能只挂在 ack 上。
//
// 续跑的那一次尝试会被若干道门挡住（projection unknown、unresolved 交付、settle
// 未跑、pending 未清），而这些门**全部**由非 ack 迁移清掉：HistoryProjectionRecovered
// 清投影、HistoryReconciliationSettled 清 unresolved、epoch 重置退休 ledger。挡住的
// 那一次之后如果不再有 ack，计划就永远停在 pending=0 + PlanIncomplete，执行器空闲，
// 缺失的尾部再也不会被规划 —— 这正是 live 的终态。
//
// 这个测试固化 live 的卡死状态：计划被截断（PlanIncomplete），已铸前缀被整体退休
// （epoch 重置替换 ledger），队列为空且没有任何 ack 会再来。此时执行器被唤醒必须
// 自己把计划续跑到底，而不是空转一轮就退出。
func TestExecutorContinuesIncompletePlanWithoutAckTrigger(t *testing.T) {
	const (
		cellCount = 600
		linesEach = 40
		width     = 120
		height    = 40
	)
	restoreBudget := historyCommitPlanningBudget
	defer func() { historyCommitPlanningBudget = restoreBudget }()
	historyCommitPlanningBudget = 0

	controller := NewUIController(UIControllerConfig{}, nil, nil)
	go controller.Run()
	physical := &bytes.Buffer{}
	session := NewTerminalSession(physical)
	executor := NewTerminalSessionExecutor(controller, session)
	t.Cleanup(func() {
		executor.Close()
		controller.Close()
		controller.WaitIdle()
	})
	flush := func() {
		executor.Request()
		executor.WaitIdle()
		controller.WaitIdle()
	}

	for _, action := range []UIAction{
		Resize{Width: width, Height: height, Generation: 1},
		ShowPromptAction{Line: "> "},
		ReplaceTranscriptAction{Snapshot: coverageSnapshot(1, cellCount, linesEach)},
	} {
		if !controller.Post(action) {
			t.Fatalf("post %T", action)
		}
	}
	controller.WaitIdle()

	// 装载本身要真的铸出待交付的 token，否则下面构造的「缺口」毫无意义。
	loaded := controller.State()
	if !loaded.HistoryEffects.HasPending() {
		t.Fatalf("fixture planned nothing: next=%d pending=%d",
			loaded.HistoryEffects.NextToken, historyPendingCount(loaded))
	}

	// live 的卡死状态，逐项照搬 /debug/chat/status 的读数：已铸前缀被 epoch 重置
	// 整体退休（ledger 被替换，交付记录清零），计划停在 incomplete（尾部从未被
	// 规划），队列因此为空 —— 唯一能触发续跑的 ack 已经不存在了。
	//
	// 「截断会留下 PlanIncomplete」由 history_planning_budget_test.go 的
	// TestTruncatedTranscriptPlanContinuesUntilComplete 证明（预算 0 → 第一个采样点
	// 切断）；这里手工构造的是它之后那个状态：前缀退休 + 队列排空。gate 全部为
	// 干净（projection known、无 unresolved、非 frozen），因此这不是「有什么东西
	// 挡住了续跑」，而是「没有任何东西再来触发它」。
	controller.mu.Lock()
	controller.state.HistoryEffects.ledger = NewHistoryCommitLedger()
	controller.state.HistoryEffects.PlanIncomplete = true
	controller.state.HistoryEffects.PlanStalled = false
	controller.mu.Unlock()

	state := controller.State()
	if state.HistoryEffects.HasPending() || !state.HistoryEffects.PlanIncomplete {
		t.Fatalf("fixture is not in the stranded state: pending=%t planIncomplete=%t",
			state.HistoryEffects.HasPending(), state.HistoryEffects.PlanIncomplete)
	}

	// 只唤醒执行器（没有任何 ack、没有 transcript 迁移）：续跑必须由执行器
	// 侧的 kick 触发，直到整份 transcript 都有终态记录。
	deadline := time.Now().Add(60 * time.Second)
	for {
		state := controller.State()
		if !state.HistoryEffects.planContinuationPending() && !state.HistoryEffects.HasPending() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("an incomplete plan was never continued without an ack trigger: %#v",
				state.HistoryEffects)
		}
		flush()
	}
	flush()

	assertHistoryCoverage(t, controller, session, cellCount)
}

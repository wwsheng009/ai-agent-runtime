package ui

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// coverageSnapshot 造一份「resume 规模」的 transcript：每个 cell 撑开几十行物理行。
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
//
// P2-1 之后 live 账本不再是完整的覆盖判据：终态压缩会把已确认交付的 Acked
// 条目从 byToken 剪除，只留下 compactedTerminalSources tombstone（身份阻断，
// 不再载荷）。因此覆盖必须由「live 条目 ∪ 压缩 tombstone」共同证明，否则任何
// 超过 historyLedgerCompactHighWater 的用例都会把已交付误报成「从未规划」。
// 本用例只做干净 ack（无失败/无部分写入），此处的 tombstone 恰好对应已确认
// 交付；若未来用例引入 Abandoned，需要按状态收紧该判据。
func assertHistoryCoverage(t *testing.T, controller *UIController, session *TerminalSession, want int) {
	t.Helper()
	state := controller.State()
	covered := make(map[scene.CellID]struct{})
	counts := make(map[HistoryCommitState]int)
	for _, entry := range state.HistoryEffects.Entries() {
		counts[entry.State]++
		if entry.State == HistoryCommitQuarantined {
			continue
		}
		covered[entry.Commit.CellID] = struct{}{}
	}
	if ledger := state.HistoryEffects.ledger; ledger != nil {
		for key := range ledger.compactedTerminalSources {
			covered[key.cellID] = struct{}{}
		}
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
			"next=%d pending=%d projection=%+v)",
			missing, want, firstMissing, counts, state.HistoryEffects.NextToken,
			historyPendingCount(state), session.ProjectionState())
	}
}

// 需求：resume 装载（append-only：不清屏、按序追加）之后，规划必须把**整份**
// transcript 交付到原生 scrollback，而不是停在「预算恰好能走到的那个前缀」上。
//
// live 事故（session_20260924072950_ltYRU9tG，端口 56311 的进程）：6622 cell /
// 291842 物理行的会话 resume 之后，ledger 只有 322 条 acked 提交、pending=0、
// in-flight=0、failed=0、invalidated=0，执行器空闲、recovery_actionable=false，
// 屏幕只恢复了最老的一小段历史。缺口既不在 scrollback 对账、也不在投影已知性上，
// 而且不可自愈 —— 空闲会话不会再有 transcript 迁移来触发下一次规划。
//
// P1-1 Stage 2 起规划是无预算单遍，本用例退化为端到端回归：整份 transcript 一次
// 规划交付。测试走**真实的** controller + TerminalSession + TerminalSessionExecutor
// 闭环：规划 -> 事务写盘 -> publishResult -> ack 归约。
func TestArmedResumeDeliversWholeTranscriptAcrossBudgetTruncation(t *testing.T) {
	const (
		cellCount = 1200
		linesEach = 40
		width     = 120
		height    = 40
	)
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
		// 120s（race 下 ×6）是"单次 49200 行交付在负载机器上的墙钟上限"，不是
		// 性能断言：deadline 只在**非 idle** 的迭代上检查。此前在 idle 迭代上
		// 也检查 deadline，一旦某次 flush() 的长交付刚好跨过 60s，下一轮读到
		// 已经收敛的 idle 状态仍会被误报为"never converged"（现场 dump：
		// pending=0、全终态、Projection/Reconciliation 干净）。
		deadline := time.Now().Add(raceScaledDeadline(120 * time.Second))
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
				if time.Now().After(deadline) {
					head, tokens, byToken, unresolved := uint64(0), 0, 0, 0
					if ledger := state.HistoryEffects.ledger; ledger != nil {
						head, tokens, byToken = ledger.queueHeadToken, len(ledger.tokens), len(ledger.byToken)
						unresolved = ledger.unresolvedCount
					}
					t.Fatalf("history projection never converged: pending=%d head=%d tokens=%d byToken=%d unresolved=%d summary=%+v state=%#v",
						historyPendingCount(state), head, tokens, byToken, unresolved,
						state.HistoryEffects.Summary(), state.HistoryEffects)
				}
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

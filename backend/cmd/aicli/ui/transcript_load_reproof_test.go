package ui

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// P2 replay 切片 S1：会话装载（ReplaceTranscriptAction + ArmScrollbackReplay）不再
// 授权销毁式重放。装载只做一件事：从源重证明计划（memo 失效，覆盖 no-op 安装）。
// delivery ledger 与语义 epoch 保持权威 —— native scrollback 是 append-only，
// 同身份内容绝不重复追加；失效已交付内容的替换由既有 invalidatesAcked 路径触发
// 一次非破坏性 settle，而不是清屏。

func scrollbackGrantSnapshot(revision uint64, source string) *scene.Snapshot {
	return &scene.Snapshot{Revision: revision, Cells: []*scene.TranscriptCell{
		{ID: 1, Revision: 1, Kind: scene.KindAssistant, Source: source, Phase: scene.CellCommitted},
	}}
}

func scrollbackGrantRecoveryPlan(state UIControllerState) TerminalTransactionPlan {
	return terminalHistoryRecoveryPlan(terminalSessionControllerSnapshot{
		appState:               terminalViewportAppState(state.AppState),
		projectionUnknown:      state.HistoryEffects.ProjectionUnknown,
		reconciliationRequired: state.HistoryEffects.ReconciliationRequired,
	})
}

func TestReplaceTranscriptActionLoadKeepsAppendOnlyRecovery(t *testing.T) {
	loaded := reduceUIControllerState(UIControllerState{}, ReplaceTranscriptAction{
		Snapshot:            scrollbackGrantSnapshot(1, "loaded session"),
		ArmScrollbackReplay: true,
	}, 1)
	if loaded.HistoryEffects.TerminalEpoch != 0 {
		t.Fatalf("load started a terminal epoch without a physical act: %d", loaded.HistoryEffects.TerminalEpoch)
	}
	if len(loaded.Transcript.Cells) != 1 || loaded.Transcript.Cells[0].Source != "loaded session" {
		t.Fatalf("load transcript = %+v, want the replacement snapshot installed by the same reduction", loaded.Transcript)
	}
	if plan := scrollbackGrantRecoveryPlan(loaded); !plan.SettleHistoryProjection {
		t.Fatalf("load recovery plan = %+v, want non-destructive settle only", plan)
	}
}

// 装载不得退休 ledger：已交付记录是 append-only 去重的唯一依据。替换若丢弃了
// 已交付的 cell，则必须由既有 invalidatesAcked 路径请求一次 settle（而不是清屏）。
func TestLoadKeepsDeliveryLedgerAuthoritative(t *testing.T) {
	state := historyEffectTestState(t, 2)
	pending := state.HistoryEffects.Pending()
	token := pending[0].Token
	state = reduceUIControllerState(state, BeginHistoryCommit{
		Token: token, LayoutGeneration: state.Geometry.Generation,
	}, 3)
	state = reduceUIControllerState(state, HistoryCommitAcknowledged{
		Token: token, Frame: 9, LayoutGeneration: state.Geometry.Generation,
	}, 4)
	if entry := historyCommitEntry(t, state, token); entry.State != HistoryCommitDelivered {
		t.Fatalf("fixture token not delivered: %#v", entry)
	}

	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot:            scrollbackGrantSnapshot(9, "loaded session"),
		ArmScrollbackReplay: true,
	}, 5)

	if _, ok := state.HistoryEffects.Entry(token); !ok {
		t.Fatal("load retired a delivered record; append-only replay would duplicate its rows")
	}
	if state.HistoryEffects.TerminalEpoch != 0 {
		t.Fatalf("load advanced the terminal epoch without a physical act: %d", state.HistoryEffects.TerminalEpoch)
	}
	if !state.HistoryEffects.ProjectionUnknown || !state.HistoryEffects.ReconciliationRequired {
		t.Fatalf("replacement that drops delivered cells must request a settle: %#v", state.HistoryEffects)
	}
	if plan := scrollbackGrantRecoveryPlan(state); !plan.SettleHistoryProjection {
		t.Fatalf("invalidating replacement recovery plan = %+v, want non-destructive settle", plan)
	}
}

// no-op 安装（快照已装配）同样必须重证明：memo 看不到 ledger 被外部替换的形状，
// 装载不能信任指纹。
func TestLoadReproofOnNoOpInstallRePlansFromSource(t *testing.T) {
	state := reduceUIControllerState(UIControllerState{}, Resize{Width: 72, Height: 12, Generation: 1}, 1)
	snapshot := scrollbackGrantSnapshot(1, "loaded session")
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: snapshot}, 2)
	planned := state.HistoryEffects.NextToken
	if planned == 0 {
		t.Fatal("fixture did not plan the loaded transcript")
	}

	// 白盒构造「memo 仍有效、ledger 已被换掉」的形状：这正是装载可以留下的状态。
	state.HistoryEffects.ledger = NewHistoryCommitLedger()
	recordTranscriptPlanMemo(&state, 1)

	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot:            snapshot,
		ArmScrollbackReplay: true,
	}, 3)

	if state.HistoryEffects.NextToken <= planned {
		t.Fatalf("no-op load trusted a memo over an empty ledger: next=%d planned=%d",
			state.HistoryEffects.NextToken, planned)
	}
	if historyPendingCount(state) == 0 {
		t.Fatalf("no-op load left nothing planned: %#v", state.HistoryEffects)
	}
}

// B：授权式装载重证明跑完一遍计划却零新增提交、且转录非空时，必须置
// ArmedReplayNoop，命令侧据此给出「历史已在本终端输出过」的显式提示；
// 真正追加了历史（新交付代次）或转录为空时不得误报。
func TestArmedLoadReplayNoopFlagTracksZeroRowProof(t *testing.T) {
	state := reduceUIControllerState(UIControllerState{}, Resize{Width: 72, Height: 12, Generation: 1}, 1)
	snapshot := scrollbackGrantSnapshot(1, "loaded session")
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: snapshot}, 2)
	firstToken := queuedHistoryTokenForCell(t, state, 1)
	state = deliverHistoryToken(state, firstToken, 3)
	if state.HistoryEffects.armedReplayProofPending || state.HistoryEffects.armedReplayNoop {
		t.Fatalf("non-armed install must not arm the load replay proof: %#v", state.HistoryEffects)
	}

	// 同会话授权式重载：同一来源已在当前代次交付，计划零新增提交。
	tokensBefore := state.HistoryEffects.NextToken
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot:            snapshot,
		ArmScrollbackReplay: true,
	}, 5)
	if state.HistoryEffects.NextToken != tokensBefore {
		t.Fatalf("same-session no-op load minted rows: next=%d want %d",
			state.HistoryEffects.NextToken, tokensBefore)
	}
	if state.HistoryEffects.armedReplayProofPending {
		t.Fatalf("load replay proof did not settle: %#v", state.HistoryEffects)
	}
	if !state.HistoryEffects.armedReplayNoop {
		t.Fatalf("zero-row re-proof over non-empty transcript must set ArmedReplayNoop: %#v",
			state.HistoryEffects)
	}

	// 新交付代次会真正重铸同一来源：不得误报 no-op。
	tokensBeforeSwitch := state.HistoryEffects.NextToken
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot:            snapshot,
		ArmScrollbackReplay: true,
		NewDeliveryEpoch:    true,
	}, 7)
	if state.HistoryEffects.NextToken <= tokensBeforeSwitch {
		t.Fatalf("new delivery epoch did not re-mint: next=%d before=%d",
			state.HistoryEffects.NextToken, tokensBeforeSwitch)
	}
	if state.HistoryEffects.armedReplayNoop {
		t.Fatalf("load replay that appended rows must not report no-op: %#v", state.HistoryEffects)
	}

	// 空转录：零新增提交也不得提示「历史已输出过」。
	empty := reduceUIControllerState(UIControllerState{}, Resize{Width: 72, Height: 12, Generation: 1}, 20)
	empty = reduceUIControllerState(empty, ReplaceTranscriptAction{
		Snapshot:            &scene.Snapshot{Revision: 1},
		ArmScrollbackReplay: true,
	}, 21)
	if empty.HistoryEffects.armedReplayNoop {
		t.Fatalf("empty transcript must not report a history no-op: %#v", empty.HistoryEffects)
	}
}

// A→B→A 会话切换回归：cell 身份跨会话复用（同一 cellID/revision/来源区间），
// 上一交付代次的 Delivered 记录不得阻断切回后的重铸。append-only 语义下旧行
// 保留，新会话历史追加在其下方；同会话的授权式重载仍按同代次去重，不重复打印。
func TestSessionSwitchReAppendsHistoryAcrossDeliveryEpochs(t *testing.T) {
	state := reduceUIControllerState(UIControllerState{}, Resize{Width: 72, Height: 12, Generation: 1}, 1)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot: scrollbackGrantSnapshot(1, "session A"),
	}, 2)
	firstToken := queuedHistoryTokenForCell(t, state, 1)
	state = deliverHistoryToken(state, firstToken, 3)
	if entry := historyCommitEntry(t, state, firstToken); entry.State != HistoryCommitDelivered {
		t.Fatalf("session A token not delivered: %#v", entry)
	}
	tokensAfterA := state.HistoryEffects.NextToken

	// A → B：cellID/revision/来源长度与 A 完全一致（跨会话复用的最坏形状）。
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot:            scrollbackGrantSnapshot(2, "session B"),
		ArmScrollbackReplay: true,
		NewDeliveryEpoch:    true,
	}, 4)
	if state.HistoryEffects.NextToken <= tokensAfterA {
		t.Fatalf("switch to session B did not re-mint the reused source: next=%d before=%d",
			state.HistoryEffects.NextToken, tokensAfterA)
	}
	// 替换丢掉了上一会话已交付的 cell：投影未知由一次非破坏性 settle 收敛，
	// 与生产恢复计划（SettleHistoryProjection）一致。
	state = settleSessionSwitch(state, 5)
	secondToken := queuedHistoryTokenForCell(t, state, 1)
	state = deliverHistoryToken(state, secondToken, 7)

	// B → A：切回同一份历史，必须再次追加。
	tokensAfterB := state.HistoryEffects.NextToken
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot:            scrollbackGrantSnapshot(3, "session A"),
		ArmScrollbackReplay: true,
		NewDeliveryEpoch:    true,
	}, 9)
	if state.HistoryEffects.NextToken <= tokensAfterB {
		t.Fatalf("switch back to session A did not re-append its history: next=%d before=%d",
			state.HistoryEffects.NextToken, tokensAfterB)
	}
	state = settleSessionSwitch(state, 10)
	thirdToken := queuedHistoryTokenForCell(t, state, 1)
	state = deliverHistoryToken(state, thirdToken, 12)

	// append-only：三次交付都还在账本里，没有任何一次被销毁。
	for _, token := range []uint64{firstToken, secondToken, thirdToken} {
		if entry := historyCommitEntry(t, state, token); entry.State != HistoryCommitDelivered {
			t.Fatalf("append-only ledger lost token %d: %#v", token, entry)
		}
	}
	if state.HistoryEffects.TerminalEpoch != 0 {
		t.Fatalf("session switch advanced the physical terminal epoch: %d", state.HistoryEffects.TerminalEpoch)
	}

	// 同会话的授权式重载：不开启新代次，已交付来源仍被去重。
	tokensBeforeReload := state.HistoryEffects.NextToken
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot:            state.Transcript.Snapshot(),
		ArmScrollbackReplay: true,
	}, 14)
	if state.HistoryEffects.NextToken != tokensBeforeReload {
		t.Fatalf("same-session reload re-minted a delivered source: next=%d want %d",
			state.HistoryEffects.NextToken, tokensBeforeReload)
	}
}

// settleSessionSwitch 模拟生产恢复计划对「替换丢掉了已交付 cell」的收敛：
// 先结算未决投影（非破坏性），再宣告主帧已从语义源重建。
func settleSessionSwitch(state UIControllerState, seq uint64) UIControllerState {
	state = reduceUIControllerState(state, HistoryReconciliationSettled{
		LayoutGeneration: state.Geometry.Generation,
	}, seq)
	return reduceUIControllerState(state, HistoryProjectionRecovered{
		LayoutGeneration: state.Geometry.Generation,
	}, seq+1)
}

func queuedHistoryTokenForCell(t *testing.T, state UIControllerState, cellID scene.CellID) uint64 {
	t.Helper()
	for _, entry := range state.HistoryEffects.Entries() {
		if entry.Commit.CellID == cellID && entry.State == HistoryCommitQueued {
			return entry.Commit.Token
		}
	}
	t.Fatalf("no queued history commit for cell %d: %#v", cellID, state.HistoryEffects.Entries())
	return 0
}

func deliverHistoryToken(state UIControllerState, token, seq uint64) UIControllerState {
	state = reduceUIControllerState(state, BeginHistoryCommit{
		Token: token, LayoutGeneration: state.Geometry.Generation,
	}, seq)
	return reduceUIControllerState(state, HistoryCommitAcknowledged{
		Token: token, Frame: seq, LayoutGeneration: state.Geometry.Generation,
	}, seq+1)
}

// historyPendingCount counts ledger entries still awaiting delivery, without the
// ProjectionUnknown gate that HasPending applies to the scheduler predicate.
func historyPendingCount(state UIControllerState) int {
	pending := 0
	for _, entry := range state.HistoryEffects.Entries() {
		if entry.State == HistoryCommitQueued {
			pending++
		}
	}
	return pending
}

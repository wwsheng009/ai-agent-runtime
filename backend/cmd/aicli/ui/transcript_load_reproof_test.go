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

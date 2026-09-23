package ui

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// These tests pin the atomicity contract of the one-shot scrollback-replay
// authorization: it travels inside the same ReplaceTranscriptAction that
// installs the Scene it authorizes, so an executor can never observe an armed
// grant against the pre-replacement Scene (which would spend the one shot on
// stale content and leave the loaded generation unreplayed).

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
		scrollbackReplayArmed:  state.HistoryEffects.ScrollbackReplayArmed,
	})
}

func TestReplaceTranscriptActionCarriesScrollbackReplayGrant(t *testing.T) {
	plain := reduceUIControllerState(UIControllerState{}, ReplaceTranscriptAction{
		Snapshot: scrollbackGrantSnapshot(1, "regular update"),
	}, 1)
	if plain.HistoryEffects.ScrollbackReplayArmed {
		t.Fatal("a regular replacement authorized a scrollback replay")
	}
	if plan := scrollbackGrantRecoveryPlan(plain); plan.resetScrollback || !plan.SettleHistoryProjection {
		t.Fatalf("regular replacement recovery plan = %+v, want non-destructive settle", plan)
	}

	armed := reduceUIControllerState(UIControllerState{}, ReplaceTranscriptAction{
		Snapshot:            scrollbackGrantSnapshot(1, "loaded session"),
		ArmScrollbackReplay: true,
	}, 1)
	if !armed.HistoryEffects.ScrollbackReplayArmed || !armed.HistoryEffects.ReconciliationRequired {
		t.Fatalf("load replacement grant armed=%t reconciliationRequired=%t, want both true",
			armed.HistoryEffects.ScrollbackReplayArmed, armed.HistoryEffects.ReconciliationRequired)
	}
	if len(armed.Transcript.Cells) != 1 || armed.Transcript.Cells[0].Source != "loaded session" {
		t.Fatalf("armed transcript = %+v, want the replacement snapshot installed by the same reduction", armed.Transcript)
	}
	if plan := scrollbackGrantRecoveryPlan(armed); !plan.resetScrollback || plan.SettleHistoryProjection {
		t.Fatalf("load replacement recovery plan = %+v, want destructive scrollback replay", plan)
	}
}

func TestReplaceTranscriptActionGrantsReplayForAlreadyInstalledSnapshot(t *testing.T) {
	snapshot := scrollbackGrantSnapshot(1, "loaded session")
	state := reduceUIControllerState(UIControllerState{}, ReplaceTranscriptAction{Snapshot: snapshot}, 1)
	if state.HistoryEffects.ScrollbackReplayArmed {
		t.Fatal("pre-replacement update armed the replay")
	}

	// A load may publish a snapshot the controller has already installed. The
	// install is then a no-op, but the authorization must still be granted or the
	// loaded generation would never replace native scrollback.
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot:            snapshot,
		ArmScrollbackReplay: true,
	}, 2)
	if !state.HistoryEffects.ScrollbackReplayArmed || !state.HistoryEffects.ReconciliationRequired {
		t.Fatalf("no-op replacement grant armed=%t reconciliationRequired=%t, want both true",
			state.HistoryEffects.ScrollbackReplayArmed, state.HistoryEffects.ReconciliationRequired)
	}
}

// 需求：一次性授权是**销毁式**的（执行器先清空原生 scrollback 再重放），所以 reducer
// 绝不能让它在「ledger 里没有任何计划」的状态下被观察到 —— 那会清掉屏幕且什么都写不
// 回来（live: pending=0 / acked=0 / history_rows=0，三次 reset 后永久空屏）。
//
// 计划 memo 只看 transcript/layout/theme/epoch 这些**输入**指纹，看不到 ledger 本身：
// reconcileScrollback 会整体换掉 ledger（s.ledger = NewHistoryCommitLedger()），而 memo
// 里的 epoch 字段只在 epoch 真的推进时才失配。因此「memo 声称已规划 + ledger 已空」
// 是可达状态，armed 的 no-op 安装必须重新从源证明计划，而不是信任指纹。
func TestArmedReplayOnNoOpInstallReProvesThePlan(t *testing.T) {
	state := reduceUIControllerState(UIControllerState{}, Resize{Width: 72, Height: 12, Generation: 1}, 1)
	snapshot := scrollbackGrantSnapshot(1, "loaded session")
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: snapshot}, 2)
	if state.HistoryEffects.NextToken == 0 {
		t.Fatal("fixture did not plan the loaded transcript")
	}
	planned := state.HistoryEffects.NextToken

	// 构造 memo 仍然有效、ledger 却已被换掉的形状：这正是 reconcileScrollback
	// 对下一次 armed 安装留下的状态（白盒构造，与 reducer 的行为逐字一致）。
	state.HistoryEffects.ledger = NewHistoryCommitLedger()
	recordTranscriptPlanMemo(&state, 1)

	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot:            snapshot,
		ArmScrollbackReplay: true,
	}, 3)

	if !state.HistoryEffects.ScrollbackReplayArmed {
		t.Fatal("fixture consumed the authorization instead of reducing the no-op install")
	}
	if state.HistoryEffects.NextToken <= planned {
		t.Fatalf("armed replacement minted no fresh token over an empty ledger: next=%d planned=%d",
			state.HistoryEffects.NextToken, planned)
	}
	if pending := historyPendingCount(state); pending == 0 {
		t.Fatalf("armed replacement trusted a memo that no longer matches the ledger: "+
			"the destructive replay would clear scrollback and write nothing back: %#v",
			state.HistoryEffects)
	}
}

func TestScrollbackReplayGrantSurvivesRegularUpdatesUntilConsumed(t *testing.T) {
	state := reduceUIControllerState(UIControllerState{}, ReplaceTranscriptAction{
		Snapshot:            scrollbackGrantSnapshot(1, "loaded session"),
		ArmScrollbackReplay: true,
	}, 1)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot: scrollbackGrantSnapshot(2, "streamed update"),
	}, 2)
	if !state.HistoryEffects.ScrollbackReplayArmed {
		t.Fatal("a regular replacement dropped the pending replay grant")
	}

	// A settle that races the replay must not be allowed to reinterpret the
	// unproven range as resolved and thereby cancel the authorized replacement.
	state = reduceUIControllerState(state, HistoryReconciliationSettled{
		LayoutGeneration: state.LayoutGeneration,
	}, 3)
	if !state.HistoryEffects.ScrollbackReplayArmed {
		t.Fatal("HistoryReconciliationSettled short-circuited an authorized replay")
	}
}

func TestScrollbackReplayGrantIsConsumedExactlyOnce(t *testing.T) {
	state := reduceUIControllerState(UIControllerState{}, ReplaceTranscriptAction{
		Snapshot:            scrollbackGrantSnapshot(1, "loaded session"),
		ArmScrollbackReplay: true,
	}, 1)
	state = reduceUIControllerState(state, HistoryScrollbackReconciled{
		LayoutGeneration: state.LayoutGeneration,
		TerminalEpoch:    1,
	}, 2)
	if state.HistoryEffects.ScrollbackReplayArmed {
		t.Fatal("grant survived the proven scrollback replacement")
	}

	state = reduceUIControllerState(state, HistoryProjectionInvalidated{
		LayoutGeneration: state.LayoutGeneration,
	}, 3)
	if plan := scrollbackGrantRecoveryPlan(state); plan.resetScrollback {
		t.Fatalf("spent grant replayed again: %+v", plan)
	}
}

package ui

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// 窗口化装载（较早页后台补齐）期间必须挂起原生 scrollback 交付：
// 装载按「最新页先到、较早页前插补到」推进，而 append-only 的终端按**交付
// 顺序**落字。若首屏就把最新页铸出去，较早页随后到达只能追加在其后，整段转录
// 会读成「新在前、旧在后」——任何有上限的 scrollback 都会挤掉最早写入的**最新
// 消息**（用户看到「输出被截断、最新消息没有渲染」）。
//
// 契约：
//  1. 挂起期间（含较早页前插的中间态）不铸任何提交；
//  2. 收尾的授权式替换解除挂起后，从源重证明并按 cell 顺序（旧 -> 新）
//     一次性铸出完整 transcript；
//  3. memo 在挂起翻转时失效，释放那一次不会因 memo 命中而跳过整批铸造。
func TestWindowedLoadDefersNativeHistoryDeliveryUntilCompletion(t *testing.T) {
	older := []*scene.TranscriptCell{
		{ID: 1, Sequence: 1, Kind: scene.KindUser, Source: "oldest user turn", Revision: 1, Phase: scene.CellCommitted},
		{ID: 2, Sequence: 2, Kind: scene.KindAssistant, Source: "oldest assistant answer", Revision: 1, Phase: scene.CellCommitted},
		{ID: 3, Sequence: 3, Kind: scene.KindUser, Source: "middle user turn", Revision: 1, Phase: scene.CellCommitted},
	}
	newestPage := []*scene.TranscriptCell{
		{ID: 101, Sequence: 101, Kind: scene.KindUser, Source: "newest page user turn", Revision: 1, Phase: scene.CellCommitted},
		{ID: 102, Sequence: 102, Kind: scene.KindAssistant, Source: "newest page answer", Revision: 1, Phase: scene.CellCommitted},
	}

	state := UIControllerState{}
	state = reduceUIControllerState(state, Resize{Width: 80, Height: 10, Generation: 1}, 1)

	// 1) 装载首屏：最新页安装，交付被挂起 -> 不得铸出任何提交。
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot:             &scene.Snapshot{Revision: 1, Cells: newestPage},
		ArmScrollbackReplay:  true,
		DeferHistoryDelivery: true,
	}, 2)
	if !state.HistoryEffects.DeferHistoryDelivery {
		t.Fatal("load install did not hold native-scrollback delivery")
	}
	if got := len(state.HistoryEffects.Entries()); got != 0 {
		t.Fatalf("held load minted %d commits, want 0", got)
	}

	// 2) 较早页前插（补齐中间态，仍挂起）：依旧不铸。
	completed := make([]*scene.TranscriptCell, 0, len(older)+len(newestPage))
	completed = append(completed, older...)
	completed = append(completed, newestPage...)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot:             &scene.Snapshot{Revision: 2, Cells: completed},
		DeferHistoryDelivery: true,
	}, 3)
	if got := len(state.HistoryEffects.Entries()); got != 0 {
		t.Fatalf("held backfill page minted %d commits, want 0", got)
	}

	// 3) 装载收尾：授权式替换 + 解除挂起 -> 完整 transcript 一次性按 cell 顺序铸造。
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot:             &scene.Snapshot{Revision: 2, Cells: completed},
		ArmScrollbackReplay:  true,
		DeferHistoryDelivery: false,
	}, 4)
	if state.HistoryEffects.DeferHistoryDelivery {
		t.Fatal("load completion did not release the delivery hold")
	}
	entries := state.HistoryEffects.Entries()
	if len(entries) == 0 {
		t.Fatal("load completion minted no commits: the loaded history never reaches the terminal")
	}

	// 队列按 token 升序 claim，因此 token 升序即物理交付顺序：必须与 cell 顺序
	// （旧 -> 新）一致，否则 scrollback 又会读成「新在前」。
	previousCell := scene.CellID(0)
	for _, entry := range entries {
		if entry.Commit.CellID <= previousCell {
			t.Fatalf("minted delivery order is not oldest-first: cell %d after cell %d",
				entry.Commit.CellID, previousCell)
		}
		previousCell = entry.Commit.CellID
	}
	if first := entries[0].Commit.CellID; first != older[0].ID {
		t.Fatalf("first delivered cell = %d, want oldest cell %d", first, older[0].ID)
	}
	if last := entries[len(entries)-1].Commit.CellID; last != newestPage[len(newestPage)-1].ID {
		t.Fatalf("last delivered cell = %d, want newest cell %d", last, newestPage[len(newestPage)-1].ID)
	}
}

// 释放必须独立生效：同一份快照在挂起解除后即使几何/主题都没变，也要重新铸出
// （memo 在翻转时失效）。否则收尾那次会因 memo 命中而整批跳过，装载的历史就
// 永远到不了原生 scrollback。
func TestDeliveryHoldReleaseInvalidatesPlanMemo(t *testing.T) {
	cells := []*scene.TranscriptCell{
		{ID: 1, Sequence: 1, Kind: scene.KindUser, Source: "first", Revision: 1, Phase: scene.CellCommitted},
		{ID: 2, Sequence: 2, Kind: scene.KindAssistant, Source: "second", Revision: 1, Phase: scene.CellCommitted},
	}
	state := UIControllerState{}
	state = reduceUIControllerState(state, Resize{Width: 80, Height: 10, Generation: 1}, 1)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot:             &scene.Snapshot{Revision: 1, Cells: cells},
		ArmScrollbackReplay:  true,
		DeferHistoryDelivery: true,
	}, 2)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot:             &scene.Snapshot{Revision: 1, Cells: cells},
		ArmScrollbackReplay:  true,
		DeferHistoryDelivery: false,
	}, 3)
	if got := len(state.HistoryEffects.Entries()); got == 0 {
		t.Fatal("release with unchanged snapshot was memo-skipped: no delivery planned")
	}
}

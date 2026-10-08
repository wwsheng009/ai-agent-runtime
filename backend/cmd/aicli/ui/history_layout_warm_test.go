package ui

import (
	"fmt"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// 自投递钩子（真实控制器路径）：装载期预热必须能在 follow-up 上自己续跑，不依赖
// 外部再投递。reduce 直调的测试覆盖不到 UIController.Run 的钩子，而生产实测
// 「每页 install 只推进一块、收尾规划仍全冷」正是这条路径的行为。
func TestDeferredWarmSelfDrivesThroughControllerFollowups(t *testing.T) {
	resetSharedCellRows()
	t.Cleanup(resetSharedCellRows)

	c, _ := newP1Controller(t, 8)
	defer c.Close()
	go c.Run()

	const cellCount = 1200
	c.Post(Resize{Width: 100, Height: 30, Generation: 1})
	c.Post(ReplaceTranscriptAction{
		Snapshot:             warmFixtureSnapshot(cellCount),
		ArmScrollbackReplay:  true,
		DeferHistoryDelivery: true,
	})
	c.WaitIdle()

	// 只等待，不再投递：预热应在 follow-up 上推进到全覆盖。
	deadline := time.Now().Add(5 * time.Second)
	warmed := 0
	cont := false
	for time.Now().Before(deadline) {
		c.mu.Lock()
		warmed = c.state.HistoryEffects.transcriptLayoutWarmCells
		cont = c.state.HistoryEffects.transcriptLayoutWarmContinue
		c.mu.Unlock()
		if warmed >= cellCount && !cont {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if warmed < cellCount || cont {
		t.Fatalf("预热未自投递完成：warmed=%d continue=%t, want %d/false", warmed, cont, cellCount)
	}
}

// 装载期布局预热（P16）：挂起窗口内按块把 cell 行缓存渲染到位，单次锁内只付
// 一块（transcriptLayoutWarmChunkCells），收尾全量规划的 screen 相位因此几乎
// 只付缓存命中 + 装配。契约：
//  1. 预热不铸提交、不入队（挂起语义不变）；
//  2. 每块推进游标并请求续跑，覆盖全部 cell 后停止；
//  3. 等价性：预热只复用缓存，不改变投影结果——预热后的全量布局与冷布局逐行
//     一致（见 TestLayoutWarmParityWithColdFullLayout）。
func TestDeferredLoadWarmsLayoutCacheInBoundedChunks(t *testing.T) {
	resetSharedCellRows()
	t.Cleanup(resetSharedCellRows)

	const cellCount = 1200 // > 2 块（chunk = 512）
	snapshot := warmFixtureSnapshot(cellCount)
	state := UIControllerState{}
	state = reduceUIControllerState(state, Resize{Width: 100, Height: 30, Generation: 1}, 1)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot:             snapshot,
		ArmScrollbackReplay:  true,
		DeferHistoryDelivery: true,
	}, 2)

	if got := len(state.HistoryEffects.Entries()); got != 0 {
		t.Fatalf("挂起期间的预热铸出了 %d 条提交，want 0", got)
	}
	if !state.HistoryEffects.transcriptLayoutWarmContinue {
		t.Fatal("首块预热没有请求续跑")
	}
	if got := state.HistoryEffects.transcriptLayoutWarmCells; got != transcriptLayoutWarmChunkCells {
		t.Fatalf("首块预热 cell = %d, want %d", got, transcriptLayoutWarmChunkCells)
	}

	for round := 0; state.HistoryEffects.transcriptLayoutWarmContinue; round++ {
		if round > cellCount/transcriptLayoutWarmChunkCells+2 {
			t.Fatalf("预热未在预期块数内收敛：warmed=%d", state.HistoryEffects.transcriptLayoutWarmCells)
		}
		state = reduceUIControllerState(state, WarmTranscriptLayoutAction{}, uint64(3+round))
	}
	if got, want := state.HistoryEffects.transcriptLayoutWarmRows, len(state.Transcript.LayoutRows(state.Geometry.Generation)); got != want {
		t.Fatalf("预热完成后 warmedRows = %d, want %d", got, want)
	}
	if got := len(state.HistoryEffects.Entries()); got != 0 {
		t.Fatalf("预热全程铸出了 %d 条提交，want 0", got)
	}

	// 收尾：解除挂起后的全量规划几乎全命中——剩余 miss 只允许块边界的折叠提示位
	// 一类（每块至多一个）。
	hitsBefore, missesBefore, _, _, _ := sharedCellRows.stats()
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot:             snapshot,
		ArmScrollbackReplay:  true,
		DeferHistoryDelivery: false,
	}, 100)
	hitsAfter, missesAfter, _, _, _ := sharedCellRows.stats()
	if got := len(state.HistoryEffects.Entries()); got == 0 {
		t.Fatal("收尾没有铸出提交")
	}
	if coldMisses := missesAfter - missesBefore; coldMisses > cellCount/8 {
		t.Fatalf("收尾布局仍然冷渲染了 %d/%d cell（预热未生效）", coldMisses, cellCount)
	}
	if warmHits := hitsAfter - hitsBefore; warmHits < uint64(cellCount*7/8) {
		t.Fatalf("收尾布局命中 = %d, want >= %d", warmHits, cellCount*7/8)
	}
}

// 等价性：预热只做缓存复用，不改变投影结果。预热后的全量布局必须与冷缓存下的
// 全量布局逐行一致（覆盖 gap / 结构化投影 / 折叠工具链 / plain wrap）。
func TestLayoutWarmParityWithColdFullLayout(t *testing.T) {
	resetSharedCellRows()
	t.Cleanup(resetSharedCellRows)

	savedChunk := transcriptLayoutWarmChunkCells
	transcriptLayoutWarmChunkCells = 2 // 混合形态夹具跨多块，压出块边界
	t.Cleanup(func() { transcriptLayoutWarmChunkCells = savedChunk })

	app := tailParityFixtureState()
	// 夹具含一个 mutable cell：预热与全量布局都排除它，等价性同样成立。
	state := UIControllerState{AppState: app}
	state.HistoryEffects.DeferHistoryDelivery = true
	_, warmMissesBefore, _, _, _ := sharedCellRows.stats()
	for round := 0; round < 32 && warmTranscriptLayoutChunk(&state); round++ {
	}
	_, warmMissesAfter, _, _, _ := sharedCellRows.stats()
	if got, want := warmMissesAfter-warmMissesBefore, uint64(len(state.Transcript.Cells)-1); got < want {
		t.Fatalf("预热未把终态 cell 写进布局缓存：misses 增量 = %d, want >= %d（混合形态复现）", got, want)
	}

	rows := state.Transcript.LayoutRows(state.Geometry.Generation)
	byID := transcriptCellsByID(state.Transcript)
	mutable := mutableTranscriptCellIDs(state.Transcript)
	warmed := layoutTranscriptScreenRows(rows, byID, mutable, state.Geometry.Width, state.Theme)

	resetSharedCellRows()
	cold := layoutTranscriptScreenRows(rows, byID, mutable, state.Geometry.Width, state.Theme)
	assertAppScreenRowsEqual(t, "预热后全量布局 vs 冷布局", cold, warmed)
}

// 中间安装会被合并/丢弃（actor 忙时非阻塞投递直接放弃），收尾那次 transcript
// 才是大头。契约：收尾帧不得直接把缺口留给规划（那是一次长锁内持有），必须先
// 按块补齐；最后一块完成的那次直接续跑规划并按 cell 顺序铸出提交。
func TestLoadCompletionWarmsRemainingCellsBeforePlanning(t *testing.T) {
	resetSharedCellRows()
	t.Cleanup(resetSharedCellRows)

	const cellCount = 1200
	snapshot := warmFixtureSnapshot(cellCount)
	state := UIControllerState{}
	state = reduceUIControllerState(state, Resize{Width: 100, Height: 30, Generation: 1}, 1)
	// 首屏只安装尾部 200 cell（较早页尚未补齐）。
	firstPage := &scene.Snapshot{Revision: 1, Cells: snapshot.Cells[cellCount-200:]}
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot:             firstPage,
		ArmScrollbackReplay:  true,
		DeferHistoryDelivery: true,
	}, 2)
	if got, want := state.HistoryEffects.transcriptLayoutWarmRows, len(state.Transcript.LayoutRows(state.Geometry.Generation)); got != want {
		t.Fatalf("首屏预热 rows = %d, want %d（一页不足一块，应全覆盖）", got, want)
	}

	// 收尾：全量替换 + 解除挂起（模拟中间安装被合并）。缺口 = 1000 cell，
	// 收尾帧必须只预热一块，不得直接进入规划。
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot:             snapshot,
		ArmScrollbackReplay:  true,
		DeferHistoryDelivery: false,
	}, 3)
	if got := len(state.HistoryEffects.Entries()); got != 0 {
		t.Fatalf("收尾首帧就铸出了 %d 条提交（未先补齐预热）", got)
	}
	if !state.HistoryEffects.transcriptLayoutWarmContinue {
		t.Fatal("收尾缺口没有请求续跑")
	}

	// 续跑直到缺口补齐：最后一块完成后同一入口续跑规划并铸出提交。
	hitsBefore, _, _, _, _ := sharedCellRows.stats()
	for round := 0; len(state.HistoryEffects.Entries()) == 0; round++ {
		if round > cellCount/transcriptLayoutWarmChunkCells+3 {
			t.Fatalf("收尾预热未收敛：warmed=%d entries=%d",
				state.HistoryEffects.transcriptLayoutWarmCells, len(state.HistoryEffects.Entries()))
		}
		state = reduceUIControllerState(state, WarmTranscriptLayoutAction{}, uint64(4+round))
	}
	if got, want := state.HistoryEffects.transcriptLayoutWarmRows, len(state.Transcript.LayoutRows(state.Geometry.Generation)); got != want {
		t.Fatalf("收尾预热完成后 warmedRows = %d, want %d", got, want)
	}
	hitsAfter, _, _, _, _ := sharedCellRows.stats()
	if got := hitsAfter - hitsBefore; got < uint64(cellCount-200)*3/4 {
		t.Fatalf("收尾规划命中 = %d, want >= %d（预热条目必须被规划复用）", got, (cellCount-200)*3/4)
	}
	entries := state.HistoryEffects.Entries()
	if first, last := entries[0].Commit.CellID, entries[len(entries)-1].Commit.CellID; first != snapshot.Cells[0].ID || last != snapshot.Cells[cellCount-1].ID {
		t.Fatalf("规划顺序错误：首=%d 末=%d（want 首=%d 末=%d）", first, last, snapshot.Cells[0].ID, snapshot.Cells[cellCount-1].ID)
	}
}

// warmFixtureSnapshot 生成 cellCount 个内容互不相同的终态 cell（缓存键按内容
// 寻址，重复内容会互相命中，测不出块进度）。
func warmFixtureSnapshot(cellCount int) *scene.Snapshot {
	cells := make([]*scene.TranscriptCell, 0, cellCount)
	for index := 0; index < cellCount; index++ {
		cells = append(cells, &scene.TranscriptCell{
			ID:       scene.CellID(index + 1),
			Sequence: uint64(index + 1),
			Kind:     scene.KindAssistant,
			Source:   fmt.Sprintf("warm cell %d unique body line", index),
			Revision: 1,
			Phase:    scene.CellCommitted,
		})
	}
	return &scene.Snapshot{Revision: 1, Cells: cells}
}

// 生产量级复现：1200 个 20 行的混合形态 cell、chunk=512（> 并行渲染门限 64），
// 预热必须把这些 cell 全部写进布局缓存。混合形态夹具在小规模下已验证，这条把
// 「大块 + 并行渲染路径 + 多行 cell」叠上去，防止块规模相关的静默失效。
func TestLayoutWarmPopulatesCacheAtProductionScale(t *testing.T) {
	resetSharedCellRows()
	t.Cleanup(resetSharedCellRows)

	const cellCount = 1200
	cells := make([]*scene.TranscriptCell, 0, cellCount)
	for index := 0; index < cellCount; index++ {
		body := ""
		for line := 0; line < 20; line++ {
			body += fmt.Sprintf("cell %d line %d body text\n", index, line)
		}
		kind := scene.KindAssistant
		switch index % 4 {
		case 1:
			kind = scene.KindUser
		case 2:
			kind = scene.KindReasoning
		case 3:
			kind = scene.KindToolChain
			body = numberedToolLines(fmt.Sprintf("warm-%04d", index), 24)
		}
		cells = append(cells, &scene.TranscriptCell{
			ID:       scene.CellID(index + 1),
			Sequence: uint64(index + 1),
			Kind:     kind,
			Source:   body,
			Revision: 1,
			Phase:    scene.CellCommitted,
		})
	}
	snapshot := &scene.Snapshot{Revision: 1, Cells: cells}
	state := UIControllerState{}
	state = reduceUIControllerState(state, Resize{Width: 100, Height: 30, Generation: 1}, 1)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot:             snapshot,
		ArmScrollbackReplay:  true,
		DeferHistoryDelivery: true,
	}, 2)

	for round := 0; round < cellCount/transcriptLayoutWarmChunkCells+4 && state.HistoryEffects.transcriptLayoutWarmContinue; round++ {
		state = reduceUIControllerState(state, WarmTranscriptLayoutAction{}, uint64(3+round))
	}
	_, _, _, warmEntries, _ := sharedCellRows.stats()
	if warmEntries < cellCount {
		t.Fatalf("预热写入布局缓存的条目 = %d, want >= %d", warmEntries, cellCount)
	}

	// 生产口径：收尾全量规划必须命中预热条目（layout misses ≈ 0），否则首次渲染
	// 仍会全部压在规划锁内。
	hitsBefore, planMissesBefore, _, _, _ := sharedCellRows.stats()
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot:             snapshot,
		ArmScrollbackReplay:  true,
		DeferHistoryDelivery: false,
	}, 900)
	hitsAfter, planMissesAfter, _, _, _ := sharedCellRows.stats()
	if got := planMissesAfter - planMissesBefore; got > uint64(cellCount/8) {
		t.Fatalf("收尾规划重新冷渲染 %d/%d cell（预热未命中）", got, cellCount)
	}
	if got := hitsAfter - hitsBefore; got < uint64(cellCount*7/8) {
		t.Fatalf("收尾规划命中 = %d, want >= %d", got, cellCount*7/8)
	}
}

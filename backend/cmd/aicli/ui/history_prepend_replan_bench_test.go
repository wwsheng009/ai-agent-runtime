package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// 本文件是计划 §4.6（L2.5 减少全量重规划次数）的验收仪器。
//
// 要回答的问题：`resume_history_deferred`（首帧之后补齐**较早页**）会不会再触发
// 一次全量规划，以及这次规划到底贵在哪里。harness 的 P12 只能给出「第二次冻结
// 的窗口数」，且需要安静机器（≥6GB 空闲）才能取数；本文件把这件事搬进单进程，
// 用同一份 reduce/规划代码路径做**可重复**的测量与结构断言。
//
// 测量口径（三个子基准）：
//
//	first_plan               只装最新页（page2）时的首次规划 —— 第二次的对照基线
//	second_plan_prepend      较早页插入到 Scene 头部之后的再次规划 —— L2.5 的目标
//	older_page_layout_only   只布局「较早页」这些新 cell（冷）—— 第二次的**下界**
//
// 结构断言（TestDeferredOlderPagePrepend...）锁定的是正确性而非耗时：较早页
// 必须**真的进入计划**，且排在已交付的最新页之前。这条断言直接否决了「把更老页
// 排除在指纹之外让 memo 命中」的字面实现 —— 那会让较早页永远不被规划/投递。

// prependReplanLine 与 scene 包的规模基准同构：一行典型中文回复，宽度足以触发
// 真实换行。语料构造在计时之外（与 §4.3 基准一致）。
func prependReplanLine(cellID, line int) string {
	return fmt.Sprintf("row-%05d-%02d 这是一行典型的中文回复内容，用于撑起真实的排版宽度与行数。", cellID, line)
}

// prependReplanCorpus 构造一段 finalized 语料（模拟一页历史）。
// firstSeq 让较早页拿到更小的 sequence（真实插入顺序），baseID 只用于命名。
func prependReplanCorpus(baseID int, cells, linesPerCell int, firstSeq uint64) []*scene.TranscriptCell {
	finalizedAt := time.Now()
	out := make([]*scene.TranscriptCell, 0, cells)
	for index := 0; index < cells; index++ {
		var builder strings.Builder
		id := baseID + index
		for line := 0; line < linesPerCell; line++ {
			builder.WriteString(prependReplanLine(id, line))
			builder.WriteByte('\n')
		}
		at := finalizedAt
		out = append(out, &scene.TranscriptCell{
			ID:          scene.CellID(id),
			Sequence:    firstSeq + uint64(index),
			Kind:        scene.KindAssistant,
			Source:      builder.String(),
			Revision:    1,
			Phase:       scene.CellCommitted,
			FinalizedAt: &at,
		})
	}
	return out
}

// prependReplanSnapshot 把 cell 序列包成安装用的快照。
func prependReplanSnapshot(cells []*scene.TranscriptCell) *scene.Snapshot {
	return &scene.Snapshot{Revision: 1, ContentVersion: 1, Cells: cells}
}

// prependReplanMutableCell 是 frontier barrier：生产 resume 安装的是
// 「已 finalize 的历史 + 一个仍在流的 active cell」，规划器只对 frontier 之前的
// finalized cell 产出 commit（planEligibleHistoryCommitsWithin:104）。
func prependReplanMutableCell(id int, seq uint64) *scene.TranscriptCell {
	return &scene.TranscriptCell{
		ID:       scene.CellID(id),
		Sequence: seq,
		Kind:     scene.KindAssistant,
		Source:   "streaming frontier",
		Revision: 1,
		Phase:    scene.CellMutable,
	}
}

// prependReplanInstall 走生产同一入口安装一段历史：resize → 安装快照。
func prependReplanInstall(state UIControllerState, cells []*scene.TranscriptCell, seq uint64, armReplay bool) UIControllerState {
	state = reduceUIControllerState(state, ReplaceTranscriptAction{
		Snapshot:            prependReplanSnapshot(cells),
		ArmScrollbackReplay: armReplay,
	}, seq)
	return state
}

// prependReplanGeometry 是恢复会话上的典型终端几何。
func prependReplanGeometry(state UIControllerState) UIControllerState {
	return reduceUIControllerState(state, Resize{Width: 120, Height: 40, Generation: 1}, 1)
}

// prependReplanCountCommits 统计计划里覆盖指定 cell ID 集合的 commit 数。
func prependReplanCountCommits(entries []HistoryCommitEntry, ids map[scene.CellID]struct{}) int {
	count := 0
	for _, entry := range entries {
		if _, ok := ids[entry.Commit.CellID]; ok {
			count++
		}
	}
	return count
}

// prependReplanCountPlanCommits 与上面同义，作用于**新铸的计划**（未进 ledger）。
func prependReplanCountPlanCommits(plan []HistoryCommit, ids map[scene.CellID]struct{}) int {
	count := 0
	for _, commit := range plan {
		if _, ok := ids[commit.CellID]; ok {
			count++
		}
	}
	return count
}

// TestDeferredOlderPagePrependReplansAndCoversOlderCells 锁定 L2.5 的正确性前提：
// 较早页插入 Scene 头部之后，规划**必须**把较早页纳入并排在最新页之前。
//
// 这条断言存在的理由：计划 §4.6 的第二条改法（「把更老页排除在指纹之外即可让
// memo 命中」）如果按字面实现，第二次 reduce 会命中 memo、完全跳过规划，于是
// 较早页永远不会被投递到 native scrollback —— 用户滚到顶部仍然看不到它们。
// 因此任何 L2.5 的实现都必须让较早页**真的进入计划**；能省掉的只能是已规划
// 前缀的重复工作，而不是这次规划本身。
func TestDeferredOlderPagePrependReplansAndCoversOlderCells(t *testing.T) {
	const (
		page2Cells = 60
		page1Cells = 40
		lines      = 24
	)
	// page2：最新页，先安装（首帧 seed 的那一页）。
	page2 := prependReplanCorpus(1, page2Cells, lines, 1_000_000)
	// page1：较早页，稍后由 resume_history_deferred 插入到头部；ID 与 sequence
	// 都更晚分配（Scene 分配新 cell 时递增），但 sequence 更小（内容更早）。
	page1 := prependReplanCorpus(10_000, page1Cells, lines, 1)
	active := prependReplanMutableCell(20_000, 2_000_000)

	state := prependReplanGeometry(UIControllerState{})
	state = prependReplanInstall(state, append(append([]*scene.TranscriptCell{}, page2...), active), 2, true)

	page2IDs := map[scene.CellID]struct{}{}
	for _, cell := range page2 {
		page2IDs[cell.ID] = struct{}{}
	}
	firstEntries := state.HistoryEffects.Entries()
	if len(firstEntries) == 0 {
		t.Fatal("fixture produced no plan for the latest page: the corpus is not eligible " +
			"(check finalized/phase/frontier inputs) — the measurement would be meaningless")
	}
	if got := prependReplanCountCommits(firstEntries, page2IDs); got == 0 {
		t.Fatalf("latest page is not covered by its own plan (%d entries)", len(firstEntries))
	}
	t.Logf("first plan: %d commits covering the latest page; memoHit=%t",
		len(firstEntries), transcriptPlanMemoHit(&state))

	// 较早页插入头部：既有 cell 身份不变（bridge 稳定身份），新 cell 追加在 Scene
	// 分配序上，但展示顺序在最前。
	prepended := append(append([]*scene.TranscriptCell{}, page1...), page2...)
	prepended = append(prepended, active)

	// 第二次 reduce 之前，memo 记的是「最新页」那一轮的计划输入。
	fenceBefore := transcriptFinalizedPrefixFence(state.Transcript)
	cellsBefore := state.HistoryEffects.lastPlannedTranscriptCells
	if !transcriptPlanMemoHit(&state) {
		t.Fatal("precondition: the first plan must be memoized before the deferred insert, " +
			"otherwise the second round is not comparable")
	}

	state = prependReplanInstall(state, prepended, 3, true)
	entries := state.HistoryEffects.Entries()

	// 结构证据 1：插入较早页改变了 finalized-prefix 指纹，memo 因此失效。
	if fenceAfter := transcriptFinalizedPrefixFence(state.Transcript); fenceAfter == fenceBefore {
		t.Fatalf("the finalized-prefix fence did not change after inserting the older page "+
			"(before=%d after=%d): the memo would hit and skip the round entirely", fenceBefore, fenceAfter)
	}
	// 结构证据 2：失效的结果是**整份 transcript 被重新规划**（memo 记录的是新的 cell 数）。
	if got := state.HistoryEffects.lastPlannedTranscriptCells; got != len(prepended) {
		t.Fatalf("the deferred insert did not re-plan the whole transcript: "+
			"memo cells %d -> %d, want %d (this is the L2.5 cost)", cellsBefore, got, len(prepended))
	}

	page1IDs := map[scene.CellID]struct{}{}
	for _, cell := range page1 {
		page1IDs[cell.ID] = struct{}{}
	}
	olderCommits := prependReplanCountCommits(entries, page1IDs)
	if olderCommits == 0 {
		t.Fatalf("older page was not planned after the deferred insert (%d entries total): "+
			"the prepended history would never reach native scrollback", len(entries))
	}
	if got := prependReplanCountCommits(entries, page2IDs); got == 0 {
		t.Fatalf("latest page lost its commits after the deferred insert (%d entries total)", len(entries))
	}

	// 顺序断言的对象必须是**这一轮新铸的计划**，而不是 ledger 里的存量条目：ledger
	// 会保留上一轮已经铸好的 token（身份相同的 commit 不会重铸），把新铸的较早页
	// 追加在后面。销毁式重放会整体替换 ledger（reconcileScrollback 的既有语义），
	// 所以交付顺序由计划顺序决定，而不是此刻的 ledger 顺序。
	plan, complete := planEligibleHistoryCommitsWithin(state.AppState, time.Time{})
	if !complete {
		t.Fatal("the post-prepend plan is truncated; the ordering invariant cannot be checked")
	}
	firstOlder, firstNewer := -1, -1
	for index, entry := range plan {
		if _, ok := page1IDs[entry.CellID]; ok && firstOlder < 0 {
			firstOlder = index
		}
		if _, ok := page2IDs[entry.CellID]; ok && firstNewer < 0 {
			firstNewer = index
		}
	}
	if firstOlder < 0 || firstNewer < 0 || firstOlder > firstNewer {
		t.Fatalf("the minted plan must order the older page before the latest page "+
			"(destroyed-and-replayed scrollback follows plan order): firstOlder=%d firstNewer=%d",
			firstOlder, firstNewer)
	}
	t.Logf("second plan: %d minted commits (%d older / %d latest) in plan order "+
		"(firstOlder=%d firstNewer=%d); ledger holds %d entries (%d older / %d latest)",
		len(plan), prependReplanCountPlanCommits(plan, page1IDs), prependReplanCountPlanCommits(plan, page2IDs),
		firstOlder, firstNewer,
		len(entries), olderCommits, prependReplanCountCommits(entries, page2IDs))
}

// 以下 sink 防止计时区域内的调用被优化掉（reduce 有副作用，仅作保险）。
var (
	prependReplanSink        UIControllerState
	prependReplanRowsSink    []scene.LayoutRow
	prependReplanPlanSink    []HistoryCommit
	prependReplanTranscript  TranscriptState
	prependReplanEffectQueue HistoryEffectQueueState
	prependReplanDiagnostics HistoryEffectDiagnostics
)

// BenchmarkDeferredOlderPageReplan 量化「补齐较早页」引发的第二次规划。
//
// 规模：page2 4,000 cells + page1 2,719 cells = 6,719 cells / 24 行 ≈ 161k 行，
// 与生产恢复会话（6,719 cells / 146,535 布局行）同量级。
//
// 读法：
//   - second_plan_prepend 与 first_plan 的差值 = 补齐较早页的额外成本；
//   - second_plan_prepend 与 older_page_layout_only 的差值 = 计划遍历/提交构造的
//     净成本（不是布局）；
//   - 若 second ≈ first + older_layout，说明第二次规划已经是「只布局新 cell」的
//     增量形态，L2.5 的代码改动没有可省的东西；若 second ≫ 该值，则差值是
//     memo 失效导致的重算，L2.5 才有收益空间。
func BenchmarkDeferredOlderPageReplan(b *testing.B) {
	const (
		page2Cells = 4000
		page1Cells = 2719
		lines      = 24
	)
	page2 := prependReplanCorpus(1, page2Cells, lines, 1_000_000)
	page1 := prependReplanCorpus(10_000, page1Cells, lines, 1)
	active := prependReplanMutableCell(20_000, 2_000_000)
	latestCells := append(append([]*scene.TranscriptCell{}, page2...), active)
	allCells := append(append([]*scene.TranscriptCell{}, page1...), latestCells...)
	allSnapshot := prependReplanSnapshot(allCells)
	latestSnapshot := prependReplanSnapshot(latestCells)

	// 归因基线：先装好最新页（含首轮规划），后续子基准都在这份状态上做**单项**测量。
	baseline := prependReplanGeometry(UIControllerState{})
	baseline = prependReplanInstall(baseline, latestCells, 2, true)

	// L2.4（reduce 移出锁）的固定开销：每个动作要在锁外算 nextState，就必须先深拷贝当前状态。
	// secondPlanState 与 second_plan_prepend 的终态同形（6,720 cells + 2,500 ledger）。
	secondPlanState := prependReplanGeometry(UIControllerState{})
	secondPlanState = prependReplanInstall(secondPlanState, latestCells, 2, true)
	secondPlanState = reduceUIControllerState(secondPlanState, ReplaceTranscriptAction{
		Snapshot:            allSnapshot,
		ArmScrollbackReplay: true,
	}, 3)

	b.Run("layout_only", func(b *testing.B) {
		b.ReportAllocs()
		rows := baseline.Transcript.LayoutRows(1) // 预热共享缓存
		if len(rows) == 0 {
			b.Fatal("no rows")
		}
		b.ResetTimer()
		for b.Loop() {
			prependReplanRowsSink = baseline.Transcript.LayoutRows(1)
		}
	})

	b.Run("plan_only", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			prependReplanPlanSink, _ = planEligibleHistoryCommitsWithin(baseline.AppState, time.Time{})
		}
		if len(prependReplanPlanSink) == 0 {
			b.Fatal("plan_only produced no commits")
		}
	})

	// install_only：不给几何（Width<1）→ 规划器在入口直接返回（history_effect_planner.go:46），
	// 因此这里量到的就是 ReplaceTranscriptAction 的安装/克隆/对账本身。
	b.Run("install_only", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			b.StopTimer()
			state := UIControllerState{}
			b.StartTimer()
			prependReplanSink = reduceUIControllerState(state, ReplaceTranscriptAction{
				Snapshot:            latestSnapshot,
				ArmScrollbackReplay: true,
			}, 2)
		}
		if len(prependReplanSink.Transcript.Cells) != len(latestCells) {
			b.Fatal("install_only did not install the transcript")
		}
	})

	b.Run("older_page_layout_only", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			rows := scene.LayoutTranscript(page1, 1)
			if len(rows) == 0 {
				b.Fatal("no rows")
			}
		}
	})

	// L2.4 固定开销：AppState.Clone()（theme + Transcript + Active + Bottom + HistoryEffects + Overlay）。
	b.Run("state_clone", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			prependReplanSink = baseline.Clone()
		}
		if len(prependReplanSink.Transcript.Cells) != len(latestCells) {
			b.Fatal("state_clone did not clone the transcript")
		}
	})

	b.Run("state_clone_after_second_plan", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			prependReplanSink = secondPlanState.Clone()
		}
		if len(prependReplanSink.Transcript.Cells) != len(allCells) {
			b.Fatal("state_clone_after_second_plan did not clone the prepended transcript")
		}
	})

	// state_clone 拆项：生产上「每次动作深拷贝」是否同样昂贵，取决于贵的是
	// transcript（随 cells 增长）还是 ledger（随计划铸出的 commit 数增长）。
	b.Run("clone_transcript_only", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			prependReplanTranscript = baseline.Transcript.Clone()
		}
		if len(prependReplanTranscript.Cells) != len(latestCells) {
			b.Fatal("clone_transcript_only lost cells")
		}
	})

	b.Run("clone_history_effects_only", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			prependReplanEffectQueue = baseline.HistoryEffects.Clone()
		}
		if len(prependReplanEffectQueue.Entries()) == 0 {
			b.Fatal("clone_history_effects_only lost entries")
		}
	})

	// §4.8 诊断投影：与 clone_history_effects_only 同一份 resume 状态，唯一
	// 差别是不碰 ledger。诊断端点（/debug/chat/status）与
	// HistoryCommitExecutor.runOne 读的就是这份投影，两者都在 actor 互斥量下
	// 取值，所以这一对数字之差就是它们每次持锁省下的量。期望 0 allocs：剩下的
	// 只有一次 O(entries) 计数。
	b.Run("history_effects_diagnostics_projection", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			prependReplanDiagnostics = baseline.HistoryEffects.Diagnostics()
		}
		if prependReplanDiagnostics.Summary.LedgerEntries == 0 {
			b.Fatal("diagnostics projection lost the ledger inventory")
		}
	})

	b.Run("first_plan", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			b.StopTimer()
			state := prependReplanGeometry(UIControllerState{})
			b.StartTimer()
			prependReplanSink = prependReplanInstall(state, latestCells, 2, true)
		}
		if entries := prependReplanSink.HistoryEffects.Entries(); len(entries) == 0 {
			b.Fatal("first plan produced no commits")
		}
	})

	b.Run("second_plan_prepend", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			b.StopTimer()
			// 首帧：安装最新页并完成第一次规划（不计时）。
			state := prependReplanGeometry(UIControllerState{})
			state = prependReplanInstall(state, latestCells, 2, true)
			b.StartTimer()
			// 较早页插入 → 第二次规划（计时）。
			prependReplanSink = reduceUIControllerState(state, ReplaceTranscriptAction{
				Snapshot:            allSnapshot,
				ArmScrollbackReplay: true,
			}, 3)
		}
		if entries := prependReplanSink.HistoryEffects.Entries(); len(entries) == 0 {
			b.Fatal("second plan produced no commits")
		}
	})
}

package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/boundary"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// resumeUnmappableSource 含 tab：plain wrap 会拒绝含控制字符/tab 的文本
// （wrapPlainAppScreenText 注释），规划器因此走 whole-cell fallback —— 这正是
// 续跑前缀最容易永久缺失的一类 cell。
const resumeUnmappableSource = "制表符\t开头的行\n第二行\n"

// resumeParityFixtureState 构造跨多轮布局采样所需的长历史：
//   - cell 1 是 3000 行纯文本（plain 路径整段消费，采样只在 cell 起点命中）；
//   - cell 2 是结构化 markdown（renderedStructured 整块 append）；
//   - cell 3 是不可逐行映射的 plain cell（tab），走 whole-cell fallback；
//   - cell 4 是 6000 行折叠工具链，其语义行跨度必然覆盖多个采样点，使预算
//     命中在该 cell 内部——这是"补完当前 cell 再返回"截断语义的场景；
//   - 末尾 cell 保持 mutable，作为 frontier 屏障（布局豁免）。
func resumeParityFixtureState() AppState {
	return AppState{
		Revision:   4,
		Geometry:   GeometryState{Width: 90, Height: 40, Generation: 1},
		Transcript: NewTranscriptState(resumeParitySnapshot()),
	}
}

func resumeParitySnapshot() *scene.Snapshot {
	cells := []scene.TranscriptCell{
		{
			ID: 1, Revision: 1, Sequence: 1, Kind: scene.KindUser,
			Source: strings.Repeat("alpha line\n", 3000),
			Phase:  scene.CellCommitted, Boundary: boundary.BoundaryNormal,
		},
		{
			ID: 2, Revision: 1, Sequence: 2, Kind: scene.KindAssistant,
			Source: "# 大块\n\n" + strings.Repeat("markdown 段落行\n", 200),
			Phase:  scene.CellCommitted, Boundary: boundary.BoundaryNormal,
		},
		{
			ID: 3, Revision: 1, Sequence: 3, Kind: scene.KindUser,
			Source: resumeUnmappableSource,
			Phase:  scene.CellCommitted, Boundary: boundary.BoundaryNormal,
		},
		committedToolCell(4, numberedToolLines("resume", 6000)),
		{
			ID: 5, Revision: 1, Sequence: 5, Kind: scene.KindAssistant,
			Source: "仍在生成", Phase: scene.CellMutable, Boundary: boundary.BoundaryNormal,
		},
	}
	refs := make([]*scene.TranscriptCell, 0, len(cells))
	for index := range cells {
		refs = append(refs, &cells[index])
	}
	return &scene.Snapshot{SceneID: 7, Revision: 4, Cells: refs}
}

// TestLayoutTranscriptScreenRowsFromResumesAtCellBoundaries 锁定续跑布局的
// 核心不变量：以已过期 deadline 逐轮续跑得到的行序列，必须与一次性全量布局
// 逐行一致（无缺失、无重复、结构化 cell 不被重复 append），且游标严格前进、
// 直到 complete。Stage 2/3 的规划游标就建立在这条性质上。
func TestLayoutTranscriptScreenRowsFromResumesAtCellBoundaries(t *testing.T) {
	state := resumeParityFixtureState()
	width := state.Geometry.Width
	rows := state.Transcript.LayoutRows(state.Geometry.Generation)
	byID := transcriptCellsByID(state.Transcript)
	mutable := transcriptSuffixCellIDsFromFirstMutable(state.Transcript)
	// 至少跨两个采样点（第一轮在 4096 附近截断、第二轮在 8192 附近再截断）才
	// 覆盖多轮续跑；两次采样都落在 6000 行工具链 cell 的内部，正好验证"预算
	// 命中后补完当前 cell 再返回"的截断语义。
	if len(rows) < layoutBudgetCheckRows*2 {
		t.Fatalf("fixture 语义行数 %d 不足以覆盖多轮采样", len(rows))
	}

	resetSharedCellRows()
	full, completeFull := layoutTranscriptScreenRowsWithin(rows, byID, mutable, width, time.Time{}, state.Theme)
	if !completeFull || len(full) == 0 {
		t.Fatalf("全量布局非法：complete=%t rows=%d", completeFull, len(full))
	}

	// 冷缓存 + 已过期 deadline：每轮在采样点截断（len(result)>0 保护保证至少
	// 产出一个采样块），因此必然多轮。
	resetSharedCellRows()
	expired := time.Now().Add(-time.Second)
	var resumed []AppScreenRow
	cursor := 0
	passes := 0
	for {
		passes++
		if passes > 64 {
			t.Fatalf("续跑轮数异常：cursor=%d / %d", cursor, len(rows))
		}
		chunk, complete, next := layoutTranscriptScreenRowsFrom(rows, byID, mutable, width, expired, state.Theme, cursor)
		if next <= cursor && !complete {
			t.Fatalf("续跑未前进：pass=%d cursor=%d next=%d complete=%t", passes, cursor, next, complete)
		}
		if next > len(rows) {
			t.Fatalf("续跑游标越界：pass=%d next=%d len=%d", passes, next, len(rows))
		}
		if next > cursor && next < len(rows) && rows[next].Gap == 0 &&
			rows[next-1].CellID == rows[next].CellID && rows[next-1].Gap == 0 {
			// 游标必须落在 cell 边界：要么是 gap 行，要么是其 cell 非 gap 连续段
			// 的第一个语义行。落在 cell 中部会让下一轮重复 append 该 cell。
			t.Fatalf("游标落在 cell %d 中部：cursor=%d next=%d", rows[next].CellID, cursor, next)
		}
		resumed = append(resumed, chunk...)
		cursor = next
		if complete {
			break
		}
	}
	if passes < 2 {
		t.Fatalf("过期 deadline 下只有 %d 轮续跑，未覆盖多轮路径", passes)
	}
	assertAppScreenRowsEqual(t, "resume", full, resumed)
}

// TestPlanEligibleHistoryCommitsResumeUnionMatchesFullPlan 锁定 P1.1b 的核心
// 正确性性质：预算 0 下逐轮续跑产出的提交并集，与无预算全量规划的身份多重集
// 完全一致。身份用 historyCommitKey（含全局 DisplayRange）+ 行数表达，因此这条
// 断言同时覆盖：DisplayRange 全局偏移、不可映射 plain cell 的 whole-cell
// fallback（"cell 完整包含"判据）、游标不漏 cell 也不重复 cell。
func TestPlanEligibleHistoryCommitsResumeUnionMatchesFullPlan(t *testing.T) {
	state := resumeParityFixtureState()
	resetSharedCellRows()
	full := planEligibleHistoryCommits(state)
	if len(full) == 0 {
		t.Fatal("fixture 全量规划为空，测试失去意义")
	}
	// cell 3 必须走 whole-cell fallback：整段 SourceRange 且 fragment=0，
	// 并与续跑轮身份完全一致。
	sawWholeCell := false
	for _, commit := range full {
		if commit.CellID != 3 {
			continue
		}
		if commit.FragmentID == 0 && commit.SourceRange.Start == 0 && commit.SourceRange.End == len(resumeUnmappableSource) {
			sawWholeCell = true
		}
	}
	if !sawWholeCell {
		t.Fatal("fixture 未触发 whole-cell fallback，续跑前缀缺口无法被覆盖")
	}

	restoreBudget := historyCommitPlanningBudget
	defer func() { historyCommitPlanningBudget = restoreBudget }()
	historyCommitPlanningBudget = 0

	resetSharedCellRows()
	var union []HistoryCommit
	cursor, screenRows := 0, 0
	passes := 0
	for {
		passes++
		if passes > 64 {
			t.Fatalf("续跑轮数异常：cursor=%d", cursor)
		}
		commits, complete, next, rows := planEligibleHistoryCommitsWithinFrom(state, time.Now().Add(time.Second), cursor, screenRows)
		if next < cursor || rows < screenRows {
			t.Fatalf("续跑游标回退：pass=%d cursor=%d next=%d screenRows=%d→%d", passes, cursor, next, screenRows, rows)
		}
		if !complete && next == cursor && len(commits) == 0 {
			t.Fatalf("续跑未前进且无产出：pass=%d cursor=%d", passes, cursor)
		}
		union = append(union, commits...)
		cursor, screenRows = next, rows
		if complete {
			break
		}
	}
	if passes < 2 {
		t.Fatalf("预算 0 下只有 %d 轮续跑，未覆盖多轮路径", passes)
	}
	assertHistoryCommitMultisetEqual(t, full, union)
}

// assertHistoryCommitMultisetEqual 比较两份提交列表的身份多重集：身份包含
// historyCommitKey 的全部字段（origin/cell/revision/source/display/fragment/
// generation）与物理行数。缺失（前缀被跳过）、重复（游标回退重渲染）与身份
// 错位（DisplayRange 用了局部下标）都会在这里被区分出来。
func assertHistoryCommitMultisetEqual(t *testing.T, want, got []HistoryCommit) {
	t.Helper()
	describe := func(commit HistoryCommit) string {
		key := historyCommitKey(commit)
		return fmt.Sprintf("origin=%d cell=%d rev=%d src=[%d,%d) frag=%d disp=[%d,%d) gen=%d lines=%d",
			key.origin, key.cellID, key.revision, key.sourceStart, key.sourceEnd, key.fragmentID,
			key.displayStart, key.displayEnd, key.layoutGeneration, len(commit.Lines))
	}
	count := func(commits []HistoryCommit) map[string]int {
		counts := make(map[string]int, len(commits))
		for _, commit := range commits {
			counts[describe(commit)]++
		}
		return counts
	}
	wantCounts, gotCounts := count(want), count(got)
	if len(want) != len(got) {
		t.Fatalf("提交总数不同：want=%d got=%d", len(want), len(got))
	}
	for identity, wantN := range wantCounts {
		gotN, ok := gotCounts[identity]
		if !ok {
			t.Fatalf("续跑并集缺少全量规划的身份：%s", identity)
		}
		if gotN != wantN {
			t.Fatalf("提交身份计数不符 %s：want=%d got=%d", identity, wantN, gotN)
		}
	}
	for identity := range gotCounts {
		if _, ok := wantCounts[identity]; !ok {
			t.Fatalf("续跑并集出现全量规划不存在的身份：%s", identity)
		}
	}
}

// TestTranscriptPlanResumeCursorAdvancesUntilComplete 走 reducer 级生命周期：
// 预算 0 下装载 → 截断并留下游标；随后反复"交付前缀 + 续跑"，断言游标严格前进、
// 每轮只做一个锁内有界的 pass，最终 PlanIncomplete 清除且所有 finalized cell 被
// 覆盖。这条测试锁定的就是 live 事故（next 不变 / PlanStalled 永久置位）的修复
// 路径：续跑吃预算也必须能多轮收敛。
func TestTranscriptPlanResumeCursorAdvancesUntilComplete(t *testing.T) {
	restoreBudget := historyCommitPlanningBudget
	defer func() { historyCommitPlanningBudget = restoreBudget }()
	historyCommitPlanningBudget = 0

	state := reduceUIControllerState(UIControllerState{}, Resize{Width: 90, Height: 40, Generation: 1}, 1)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: resumeParitySnapshot()}, 2)
	if !state.HistoryEffects.PlanIncomplete || !state.HistoryEffects.planResumeValid {
		t.Fatalf("装载没有产生截断游标：incomplete=%t resumeValid=%t pending=%d",
			state.HistoryEffects.PlanIncomplete, state.HistoryEffects.planResumeValid,
			historyPendingCount(state))
	}
	lastRow := state.HistoryEffects.planResumeRow
	if lastRow <= 0 {
		t.Fatalf("首次游标没有前进：row=%d", lastRow)
	}

	for round := 0; round < 32; round++ {
		// 交付当前前缀（等价于 executor 的 claim→write→ack）；per round 的锁内
		// 工作 = 一个 historyCommitPlanningBudget（这里是最坏情形 0）。
		for _, commit := range state.HistoryEffects.Pending() {
			if err := state.HistoryEffects.markInFlight(commit.Token, commit.LayoutGeneration); err != nil {
				t.Fatalf("claim token %d: %v", commit.Token, err)
			}
			if err := state.HistoryEffects.ack(commit.Token, 1, commit.LayoutGeneration); err != nil {
				t.Fatalf("ack token %d: %v", commit.Token, err)
			}
		}
		if !state.HistoryEffects.PlanIncomplete {
			break
		}
		if !continueTruncatedHistoryPlan(&state) {
			t.Fatalf("round %d: 续跑没有运行（pending=%d）", round, historyPendingCount(state))
		}
		if state.HistoryEffects.PlanIncomplete {
			if !state.HistoryEffects.planResumeValid {
				t.Fatalf("round %d: 未完成却没有游标", round)
			}
			if state.HistoryEffects.planResumeRow <= lastRow {
				t.Fatalf("round %d: 游标没有前进 %d → %d", round, lastRow, state.HistoryEffects.planResumeRow)
			}
			lastRow = state.HistoryEffects.planResumeRow
		}
	}
	if state.HistoryEffects.PlanIncomplete {
		t.Fatal("预算 0 下多轮续跑仍未完成整份规划")
	}
	covered := make(map[scene.CellID]struct{})
	for _, entry := range state.HistoryEffects.Entries() {
		if entry.IsInvalidated() || entry.IsSettled() {
			continue
		}
		covered[entry.Commit.CellID] = struct{}{}
	}
	for _, cell := range state.Transcript.Cells {
		if !cellIsFinalizedForHistory(cell) || cell.Source == "" {
			continue
		}
		if _, ok := covered[cell.ID]; !ok {
			t.Fatalf("cell %d 没有被覆盖（plan incomplete=%t resume=%t row=%d）",
				cell.ID, state.HistoryEffects.PlanIncomplete,
				state.HistoryEffects.planResumeValid, state.HistoryEffects.planResumeRow)
		}
	}
}

// TestTranscriptPlanResumeClearedByForcedInvalidation 锁定审查 A.4 的修复：
// 显式 memo 失效（armed replay / no-op install 走的 invalidateTranscriptPlanMemo）
// 必须同时清掉续跑游标——ledger 可能已被整体替换，从旧游标继续会把已被退休的
// 前缀永久漏掉（销毁式重放后的空屏路径）。
func TestTranscriptPlanResumeClearedByForcedInvalidation(t *testing.T) {
	restoreBudget := historyCommitPlanningBudget
	defer func() { historyCommitPlanningBudget = restoreBudget }()
	historyCommitPlanningBudget = 0

	state := reduceUIControllerState(UIControllerState{}, Resize{Width: 90, Height: 40, Generation: 1}, 1)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: resumeParitySnapshot()}, 2)
	if !state.HistoryEffects.planResumeValid {
		t.Fatal("装载没有留下续跑游标")
	}
	state.HistoryEffects.invalidateTranscriptPlanMemo()
	if state.HistoryEffects.planResumeValid {
		t.Fatal("显式 memo 失效后游标仍然有效：ledger 被替换时会漏掉前缀")
	}
	if state.HistoryEffects.lastPlannedTranscriptValid {
		t.Fatal("memo 指纹仍然有效")
	}
}

// TestTranscriptPlanMemoTracksFinalizedActiveAcks 锁定 P1.1c：memo 与续跑游标
// 必须把"已 finalize cell 的 Active-origin 交付"计入判据——它收缩 skipRows，却不
// 改变 transcript/layout/theme 指纹；不判它，陈旧 transcript-origin 候选会一直
// 留在 ledger 里被二次投递（重复行）。
func TestTranscriptPlanMemoTracksFinalizedActiveAcks(t *testing.T) {
	restoreBudget := historyCommitPlanningBudget
	defer func() { historyCommitPlanningBudget = restoreBudget }()
	// 大预算：本用例要的是"完整规划 + 落 memo"，不是截断路径。
	historyCommitPlanningBudget = 30 * time.Second

	state := reduceUIControllerState(UIControllerState{}, Resize{Width: 90, Height: 40, Generation: 1}, 1)
	state = reduceUIControllerState(state, ReplaceTranscriptAction{Snapshot: resumeParitySnapshot()}, 2)
	if state.HistoryEffects.PlanIncomplete || !state.HistoryEffects.lastPlannedTranscriptValid {
		t.Fatalf("fixture 没有落下完整规划 memo：incomplete=%t memoValid=%t",
			state.HistoryEffects.PlanIncomplete, state.HistoryEffects.lastPlannedTranscriptValid)
	}
	if !transcriptPlanMemoHit(&state) {
		t.Fatal("基线应命中 memo")
	}
	state.HistoryEffects.finalizedActiveAckPlanVersion++
	if transcriptPlanMemoHit(&state) {
		t.Fatal("finalized active ack 版本变化必须让 memo 失效")
	}
	state.HistoryEffects.finalizedActiveAckPlanVersion--
	if !transcriptPlanMemoHit(&state) {
		t.Fatal("版本回到已规划值后 memo 应再次命中（判据是等值）")
	}
	inputs := currentTranscriptPlanInputs(&state)
	state.HistoryEffects.finalizedActiveAckPlanVersion++
	if currentTranscriptPlanInputs(&state) == inputs {
		t.Fatal("续跑游标指纹必须包含 finalized active ack 版本")
	}
}

// TestNoteFinalizedActiveAckScopesToFinalizedCells 锁定作用域判据本身：只有
// "不再等于当前活跃可变 cell"的 Active-origin ack 才推进版本。仍活跃的 cell 的
// ack 必须被忽略，否则流式热路径每个 ack 都会全量重规划。
func TestNoteFinalizedActiveAckScopesToFinalizedCells(t *testing.T) {
	state := UIControllerState{}
	state.Active = ActiveCellState{CellID: 7, Phase: ActiveCellMutable}
	noteFinalizedActiveAck(&state, []HistoryCommit{{Origin: HistoryCommitActive, CellID: 7}})
	if state.HistoryEffects.finalizedActiveAckPlanVersion != 0 {
		t.Fatal("活跃可变 cell 的 ack 不得推进版本（否则流式热路径退化为每 ack 全量重规划）")
	}
	noteFinalizedActiveAck(&state, []HistoryCommit{{Origin: HistoryCommitActive, CellID: 8}})
	if state.HistoryEffects.finalizedActiveAckPlanVersion != 1 {
		t.Fatal("已 finalize cell 的 active ack 必须推进版本")
	}
	noteFinalizedActiveAck(&state, []HistoryCommit{{Origin: HistoryCommitTranscript, CellID: 9}})
	if state.HistoryEffects.finalizedActiveAckPlanVersion != 1 {
		t.Fatal("transcript-origin ack 不推进该版本")
	}
	noteFinalizedActiveAck(&state, []HistoryCommit{
		{Origin: HistoryCommitTranscript, CellID: 9},
		{Origin: HistoryCommitActive, CellID: 8},
		{Origin: HistoryCommitActive, CellID: 10},
	})
	if state.HistoryEffects.finalizedActiveAckPlanVersion != 2 {
		t.Fatal("批内多枚 finalized active ack 只需整体推进一次")
	}
	state.Active = ActiveCellState{}
	noteFinalizedActiveAck(&state, []HistoryCommit{{Origin: HistoryCommitActive, CellID: 10}})
	if state.HistoryEffects.finalizedActiveAckPlanVersion != 3 {
		t.Fatal("无活跃 cell 时到达的 active ack 必须推进版本")
	}
}

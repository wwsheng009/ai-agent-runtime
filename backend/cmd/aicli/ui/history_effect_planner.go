package ui

import (
	"errors"
	"sort"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/markdown"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// historyCommitPlanningBudget bounds one handoff-planning pass. Streaming
// updates re-plan on every chunk; with pathological markdown (large code
// fences) a single candidate render can take hundreds of milliseconds, so the
// loop stops committing new rows once the budget is spent and lets the next
// reduce continue from the same enqueued boundary.
//
// 它是 var 而不是 const，只为了让测试能把它压到 0 来**确定性地**走到截断前缀：
// 真实预算下是否截断取决于机器速度与布局缓存状态（warm cache 上 250ms 通常够
// 走完整份历史），而截断路径必须被测到 —— 见 history_planning_budget_test.go。
// 生产代码只读它。
var historyCommitPlanningBudget = 250 * time.Millisecond

// planEligibleHistoryCommits selects finalized display ranges above the retained
// primary transcript viewport. It keeps source/display identity explicit: plain
// cells are split at stable source-line boundaries while structured Markdown
// rows use a renderer fragment identity. A still-mutable active cell contributes
// its own overflow prefix so early rows cross the physical writer before the
// final event arrives.
//
// 这是无预算形式：不截断布局，供只读调用方与测试使用。生产热路径用
// planEligibleHistoryCommitsWithin，让 historyCommitPlanningBudget 覆盖布局本身。
func planEligibleHistoryCommits(state AppState) []HistoryCommit {
	commits, _ := planEligibleHistoryCommitsWithin(state, time.Time{})
	return commits
}

// transcriptPlanSnapshot 是 transcript 规划的**布局输入快照**（P1.2）：screening
// 相位只依赖这组值，不读 ledger / HistoryEffects。Stage A 在锁内构造并同步调用
// 两个相位；Stage B 会把构造留在锁内、screening 交给 plan worker。
//
// 不变量：cells 是 live `Transcript.Cells` 的**值切片别名**（不拷贝元素），因此
// 生产代码不得原地写 `Cells[i]`——由 TestTranscriptCellsNotMutatedInPlaceByReducers
// 守护；layoutRows 由 `LayoutTranscript` 独立派生，归快照独占。
type transcriptPlanSnapshot struct {
	cells      []scene.TranscriptCell
	byID       map[scene.CellID]scene.TranscriptCell
	mutable    map[scene.CellID]struct{}
	layoutRows []scene.LayoutRow
	width      int
	generation uint64
	theme      style.ThemeContext
}

// transcriptPlanMintSnapshotFor 只构造 mint 相位需要的字段（cells 值切片别名 +
// byID + width/generation/theme），**不派生 layoutRows**：screening 的布局前置在
// 异步路径下由 worker 侧完成，结果回收时不得重复这份 O(cells) 工作。
func transcriptPlanMintSnapshotFor(state AppState) transcriptPlanSnapshot {
	width := state.Geometry.Width
	if width < 1 {
		width = 80
	}
	return transcriptPlanSnapshot{
		cells:      state.Transcript.Cells,
		byID:       transcriptCellsByID(state.Transcript),
		width:      width,
		generation: state.LayoutGeneration,
		theme:      state.Theme,
	}
}

// transcriptPlanSnapshotFor 在锁内构造完整快照（同步路径）。layoutRows 的派生
// 必须发生在建立 screening deadline 之前（见 planEligibleHistoryCommitsWithinFrom
// 的顺序注释），这里连同 byID/mutable 一起完成。
func transcriptPlanSnapshotFor(state AppState) transcriptPlanSnapshot {
	snap := transcriptPlanMintSnapshotFor(state)
	snap.mutable = mutableTranscriptCellIDs(state.Transcript)
	snap.layoutRows = state.Transcript.LayoutRows(state.LayoutGeneration)
	return snap
}

// screenTranscriptPlanWindow 是 P1.2 的 screening 相位：纯布局，**不读 ledger、
// 不读 HistoryEffects**，输出与全量布局逐行一致（续跑对齐/截断语义见
// layoutTranscriptScreenRowsFrom）。
func screenTranscriptPlanWindow(snap transcriptPlanSnapshot, deadline time.Time, startRow int) ([]AppScreenRow, bool, int) {
	return layoutTranscriptScreenRowsFrom(snap.layoutRows, snap.byID, snap.mutable, snap.width, deadline, snap.theme, startRow)
}

// transcriptPlanSink 是 reducer 与 plan worker 之间的请求通道（P1.2 Stage B）。
// RequestTranscriptPlanWindow 必须非阻塞且线程安全（worker 可能在忙）；返回 false
// 表示无法受理，reducer 回退到同步内联规划。
type transcriptPlanSink interface {
	RequestTranscriptPlanWindow(req transcriptPlanWindowRequest) bool
}

// transcriptPlanWindowRequest 是一个 screening 窗口的完整输入。cells 是 live
// Transcript.Cells 的值切片别名——"无原地写者"不变量（见 transcriptPlanSnapshot
// 注释与守护测试）保证 worker 与 actor 并发读安全；byID/mutable/layoutRows 由
// worker 侧从 cells 派生，不随请求传递。
type transcriptPlanWindowRequest struct {
	Seq              uint64
	PlanInputsEpoch  uint64
	Inputs           transcriptPlanInputs
	Resume           bool
	StartRow         int
	ScreenRowsBefore int
	Cells            []scene.TranscriptCell
	Width            int
	Height           int
	Generation       uint64
	Theme            style.ThemeContext
	Projection       bool
}

// transcriptPlanSnapshotForRequest 是 worker 侧的请求消费：从请求里的 cells 重建
// screening 需要的快照（byID/mutable/layoutRows 全部在 worker 内派生）。
func transcriptPlanSnapshotForRequest(req transcriptPlanWindowRequest) transcriptPlanSnapshot {
	transcript := TranscriptState{Cells: req.Cells}
	return transcriptPlanSnapshot{
		cells:      req.Cells,
		byID:       transcriptCellsByID(transcript),
		mutable:    mutableTranscriptCellIDs(transcript),
		layoutRows: transcript.LayoutRows(req.Generation),
		width:      req.Width,
		generation: req.Generation,
		theme:      req.Theme,
	}
}

// screenTranscriptPlanWindowRequest 在 worker 侧消费一个窗口请求：预算语义与同步
// 路径一致（从这里起算一个 historyCommitPlanningBudget），返回结果与实测耗时。
func screenTranscriptPlanWindowRequest(req transcriptPlanWindowRequest) ([]AppScreenRow, bool, int, time.Duration) {
	snap := transcriptPlanSnapshotForRequest(req)
	started := time.Now()
	deadline := time.Now().Add(historyCommitPlanningBudget)
	rows, complete, nextRow := screenTranscriptPlanWindow(snap, deadline, req.StartRow)
	return rows, complete, nextRow, time.Since(started)
}

// dispatchTranscriptPlanWindow 在 reducer 侧派发一个窗口请求。seq 只在请求被受理
// 时前进：被拒绝的请求不占用 seq，也不置 in-flight（回退同步）。
func dispatchTranscriptPlanWindow(state *UIControllerState, inputs transcriptPlanInputs) bool {
	sink := state.planSink
	if sink == nil {
		return false
	}
	effects := &state.HistoryEffects
	resume := effects.planResumeValid && effects.planResumeInputs == inputs
	req := transcriptPlanWindowRequest{
		Seq:             effects.planRequestSeq + 1,
		PlanInputsEpoch: effects.planInputsEpoch,
		Inputs:          inputs,
		Resume:          resume,
		Cells:           state.Transcript.Cells,
		Width:           state.Geometry.Width,
		Height:          state.Geometry.Height,
		Generation:      state.LayoutGeneration,
		Theme:           state.Theme,
		Projection:      state.SemanticActiveCellProjection,
	}
	if resume {
		req.StartRow = effects.planResumeRow
		req.ScreenRowsBefore = effects.planResumeScreenRows
	}
	if !sink.RequestTranscriptPlanWindow(req) {
		return false
	}
	effects.planRequestSeq = req.Seq
	effects.planRequestInFlight = true
	return true
}

// mintTranscriptPlanWindow 是 P1.2 的铸 commit 相位（锁内）：按快照的 byID/width/
// theme/generation 解读 rows，其余输入（frontier、Active、ackedActive、settled）
// 一律在 live 状态上求值。返回顺序与旧实现一致：cell 提交在前，activeCommits 追加
// 在尾部。
func mintTranscriptPlanWindow(state AppState, snap transcriptPlanSnapshot, rows []AppScreenRow, screenRowsBefore int) []HistoryCommit {
	frontierCells, frontierActive := canonicalHistoryCommitFrontier(state)
	var activeCommits []HistoryCommit
	if state.SemanticActiveCellProjection && frontierActive {
		activeCommits = planMutableActiveCellHistoryCommitsWithTheme(state.Active, state.Geometry, state.LayoutGeneration, state.Theme)
	}
	// 已结算分片（Acked/Failed/Abandoned/Invalidated）不再参与 reconcile，
	// 规划时直接跳过，避免每次 transcript 迁移都把整段历史重新物化 payload。
	ackedActive := indexAckedActiveHistoryCommits(state.HistoryEffects)
	settled := state.HistoryEffects.hasSettledRecordForSource
	// The primary frame now owns only the mutable/bottom inline viewport.
	// Finalized transcript rows all belong to native terminal history; retaining
	// a screen-sized transcript tail here would make those rows disappear as
	// soon as the viewport-only presenter stops repainting the old whole frame.
	// DisplayRange 是提交身份的一部分（historyCommitKey 含 displayStart/End）：
	// 续跑轮必须用全局行下标，displayStart 为游标基址、窗口内局部下标再叠加；
	// firstVisible 同样按全局已布局行数给出（当前实现下恒等于窗口行数，但预算
	// 窗口的可见性语义恢复后必须保持全局含义）。
	displayStart := screenRowsBefore
	firstVisible := screenRowsBefore + len(rows)
	// Commit eligibility is a physical display decision. Using semantic source
	// lines here would hand off a CJK/wrapped/tab-expanded cell while some of
	// its physical rows are still visible in the primary viewport.
	commits := make([]HistoryCommit, 0)
	for start := 0; start < len(rows); {
		cellID := rows[start].CellID
		end := start + 1
		for end < len(rows) && rows[end].CellID == cellID {
			end++
		}
		cell, found := snap.byID[cellID]
		_, beforeFrontier := frontierCells[cellID]
		if found && beforeFrontier && cellIsFinalizedForHistory(cell) && cell.Source != "" {
			skipRows := activeAckedRenderedPrefixRows(ackedActive, cellID, rows[start:end], snap.byID)
			if cellUsesStructuredPresentation(cell) {
				commits = append(commits, planMarkdownCellHistoryCommits(cell, rows[start:end], displayStart+start, firstVisible, skipRows, snap.generation, snap.byID, settled)...)
			} else if segments, mapped := planPlainCellHistoryCommits(cell, rows[start:end], displayStart+start, firstVisible, skipRows, snap.width, themeFingerprint(snap.theme), snap.generation, snap.byID, settled); mapped {
				commits = append(commits, segments...)
			} else if skipRows == 0 && end <= firstVisible {
				// whole-cell fallback（控制符/tab 等无法逐行映射的 plain cell）的
				// 判据是"cell 完整包含在窗口内"，不再是"整份 transcript 已走完"：
				// 截断窗口只含完整 cell，而游标续跑后前缀不会再被完整 pass 访问 ——
				// 继续要求 complete 会让这些行永久缺失。窗口内 wholeCell 的身份（整
				// SourceRange / fragment 0 / 全局 DisplayRange）与完整 pass 完全一致，
				// 入队去重后不会重复投递。
				if commit, ok := wholeCellHistoryCommit(cell, rows[start:end], displayStart+start, displayStart+end, snap.generation, snap.byID, settled); ok {
					commits = append(commits, commit)
				}
			}
		}
		start = end
	}
	return append(commits, activeCommits...)
}

// planEligibleHistoryCommitsWithin 是带预算的规划实现。complete 为 false 表示
// 布局在预算耗尽时被截断：commits 仍然是合法的（它是完整规划的前缀，不会提交
// 错误内容），但它**不是**全量规划，调用方不得据此记录 memo —— 否则被截断的
// cell 会永远失去被提交的机会。下一次 reduce 会重试，而重试时布局缓存已经装
// 着这一轮算出来的 cell，因此每一轮都在推进而不是重复烧同样的 CPU。
func planEligibleHistoryCommitsWithin(state AppState, deadline time.Time) ([]HistoryCommit, bool) {
	commits, complete, _, _ := planEligibleHistoryCommitsWithinFrom(state, deadline, 0, 0)
	return commits, complete
}

// planEligibleHistoryCommitsWithinFrom 是带预算规划的可续跑形式：从语义行
// startRow 开始（必须对齐 cell 边界，见 layoutTranscriptScreenRowsFrom），
// screenRowsBefore 是 startRow 之前已覆盖的物理行数，用作 DisplayRange 的全局
// 基址。返回 (commits, complete, nextRow, screenRows)。
//
// 语义不变量（P1.1b）：complete=false 时 commits 只是**前缀**，调用方只能走
// syncHistoryEffectCandidatesPrefix 入队，绝不能对它做 membership 踢除
// （syncHistoryEffectCandidates 的语义是"候选列表 == 完整有效集合"，后缀会误杀
// 前缀全部 Pending/InFlight token）。游标走到末尾后，调用方仍须再做一次无预算
// 全量 pass 才能完成 membership reconcile。
//
// DisplayRange 必须全局化：historyCommitKey 把 DisplayRange 计入提交身份，局部
// 下标会在续跑轮产生与全量规划不同的 key（重复 token 或去重失效）。
func planEligibleHistoryCommitsWithinFrom(state AppState, deadline time.Time, startRow, screenRowsBefore int) ([]HistoryCommit, bool, int, int) {
	if state.Geometry.Width < 1 || state.Geometry.Height < 1 {
		return nil, true, startRow, screenRowsBefore
	}
	// 场景语义布局（LayoutTranscript）没有部分结果，是这一轮的前置工作：在 resume
	// 会话上它单独就可能超过 historyCommitPlanningBudget（4497 cell → 159k 行，
	// live 实测 screening 拿到的 deadline 早已过期）。把它算进 screening 预算，
	// screening 循环就只能返回空前缀，规划永远 0 候选（next=0 / pending=0），
	// 授权过的销毁式重放清空 scrollback 后无内容可写 —— 永久空屏。因此预算从
	// screening 自身开始计时：仍然限制最贵的那部分工作，但每一轮都真的前进。
	//
	// 顺序同样重要：**必须先求值 layoutRows，再建立 screenDeadline**。把
	// time.Now().Add(...) 写在实参列表前面（旧实现）时，deadline 在前置工作开始
	// 之前就已经建立，于是它被 LayoutTranscript 整个吃掉，screening 循环在第一个
	// 采样点（index=layoutBudgetCheckRows）拿到的 deadline 已经过期，每一轮都只
	// 返回同一个前缀：前缀全是缓存命中（不产生新的 cell_rows miss），前缀里的 cell
	// 也早已有终态记录（不产生新的 token），continueTruncatedHistoryPlan 因此判定
	// 「无法推进」，永久置位 PlanStalled 并解除执行器的续跑 kick —— 缺失的尾部再也
	// 不会被规划（live: next=1288 / acked=322 / pending=0 / plan_incomplete=true /
	// plan_stalled=true，注入 /status 让 transcript 真的变了也不自愈）。
	snap := transcriptPlanSnapshotFor(state)
	// 预算从这里才开始计时：若在 layoutRows 之前建立 deadline，它会被上面这次
	// 语义布局整个吃掉，screening 的第一个采样点就已过期（见上）。
	screenDeadline := deadline
	if !deadline.IsZero() {
		screenDeadline = time.Now().Add(historyCommitPlanningBudget)
	}
	rows, complete, nextRow := screenTranscriptPlanWindow(snap, screenDeadline, startRow)
	// 本轮覆盖的物理行数累加到全局 DisplayRange 基址上；预算守卫保证截断不会
	// 返回空前缀，所以空窗口只能是"走到末尾/游标越界"，由 complete/nextRow 表达。
	screenRows := screenRowsBefore + len(rows)
	commits := mintTranscriptPlanWindow(state, snap, rows, screenRowsBefore)
	return commits, complete, nextRow, screenRows
}

// ackedActiveHistoryCommitIndex is a planner-local, read-only view of the
// retained mutable-cell payloads needed when that cell becomes finalized. The
// ledger maintains token order per active cell, so planning never asks Entries
// for a sorted, deeply cloned copy of the complete ledger.
type ackedActiveHistoryCommitIndex struct {
	ledger *HistoryCommitLedger
}

func indexAckedActiveHistoryCommits(effects HistoryEffectQueueState) ackedActiveHistoryCommitIndex {
	return ackedActiveHistoryCommitIndex{ledger: effects.ledger}
}

// canonicalHistoryCommitFrontier enforces the transcript's single physical
// ordering frontier. Only the contiguous finalized prefix may enter native
// history. The first mutable cell is a barrier; no later finalized or active
// cell may cross it. A mutable active cell may hand off overflow only when it
// is exactly that first barrier.
func canonicalHistoryCommitFrontier(state AppState) (map[scene.CellID]struct{}, bool) {
	eligible := make(map[scene.CellID]struct{})
	for _, cell := range state.Transcript.Cells {
		if !cellIsFinalizedForHistory(cell) {
			return eligible, state.Active.Phase == ActiveCellMutable &&
				state.Active.CellID == cell.ID && !state.Active.HistoryCommitBlocked
		}
		if cell.Source == "" {
			continue
		}
		eligible[cell.ID] = struct{}{}
	}
	return eligible, state.Active.Phase == ActiveCellMutable &&
		state.Active.CellID != 0 && !state.Active.HistoryCommitBlocked
}

// activeAckedRenderedPrefixRows proves how many leading finalized rows already
// crossed the terminal while the same cell was mutable. History planning uses
// the retained structured payload, not text hashes, and only accepts a
// contiguous source prefix whose lines still match the finalized projection.
func activeAckedRenderedPrefixRows(index ackedActiveHistoryCommitIndex, cellID scene.CellID, rows []AppScreenRow, byID map[scene.CellID]scene.TranscriptCell) int {
	if index.ledger == nil {
		return 0
	}
	frontier, matched, rowIndex := 0, 0, 0
	for _, token := range index.ledger.activeTokensByCell[cellID] {
		entry, exists := index.ledger.byToken[token]
		if !exists {
			continue
		}
		commit := entry.Commit
		if entry.State != HistoryCommitAcked || commit.Origin != HistoryCommitActive ||
			commit.CellID != cellID || commit.SourceRange.Start > frontier ||
			commit.SourceRange.End <= frontier || len(commit.Lines) == 0 {
			continue
		}
		frontier = commit.SourceRange.End
		for _, line := range commit.Lines {
			for rowIndex < len(rows) && rows[rowIndex].TranscriptGap {
				rowIndex++
			}
			if rowIndex >= len(rows) || !historyRenderLineEquivalent(line, appTranscriptRenderLine(rows[rowIndex], byID)) {
				return 0
			}
			matched++
			rowIndex++
		}
	}
	if frontier == 0 || matched == 0 {
		return 0
	}
	return matched
}

func historyRenderLineEquivalent(left, right render.Line) bool {
	if render.LinesEqual([]render.Line{left}, []render.Line{right}) {
		return true
	}
	// Empty source rows may be represented either as an empty structured line
	// or as an empty role span. They produce identical terminal bytes.
	return renderLineText(left) == "" && renderLineText(right) == ""
}

func renderLineText(line render.Line) string {
	var text string
	for _, span := range line.Spans {
		text += span.Text
	}
	return text
}

// planMutableActiveCellHistoryCommits hands off the stable prefix of a still
// mutable cell whose rendered body overflows the adaptive band budget.
// The active projection keeps only the viewport tail (ProjectActiveCellBand);
// every row above it must already have crossed the physical writer so an
// interrupted stream never loses earlier output. Each commit maps one physical
// row back to the exact half-open source range that produced it, mirroring
// planPlainCellHistoryCommits. Markdown is committed as one structured stable
// source fragment because its rendered rows are not byte-bijective.
func planMutableActiveCellHistoryCommits(active ActiveCellState, geometry GeometryState, generation uint64) []HistoryCommit {
	return planMutableActiveCellHistoryCommitsWithTheme(active, geometry, generation, style.ThemeContext{})
}

func planMutableActiveCellHistoryCommitsWithTheme(active ActiveCellState, geometry GeometryState, generation uint64, theme style.ThemeContext) []HistoryCommit {
	if active.Phase != ActiveCellMutable || active.CellID == 0 || active.HistoryCommitBlocked ||
		(active.Kind != scene.KindAssistant && active.Kind != scene.KindSupplement && active.Kind != scene.KindReasoning) || active.Source == "" ||
		geometry.Width < 1 || geometry.Height < 1 {
		return nil
	}
	if active.Kind == scene.KindAssistant && markdown.LooksLikeMarkdown(active.Source) {
		return planMutableMarkdownHistoryCommit(active, geometry, generation, theme)
	}
	if active.Kind == scene.KindReasoning {
		// Every reasoning projection is structured even when its semantic body
		// is plain text: the opening divider is derived chrome and must cross
		// the history handoff exactly once with the first source-backed range.
		return planMutableMarkdownHistoryCommit(active, geometry, generation, theme)
	}
	start := active.Acked.End
	if start < 0 || start > len(active.Source) || !activeCellSourceBoundary(active.Source, start) {
		return nil
	}
	if start == len(active.Source) {
		return nil
	}
	width := geometry.Width
	rows := activeCellBandRows(active.Source[start:], width)
	maxRows := ActiveBandRows(geometry.Height)
	if len(rows) <= maxRows {
		return nil
	}
	firstVisible := len(rows) - maxRows

	role := appTranscriptRenderRole(active.Kind)
	lineRanges := sourceLineRanges(active.Source[start:])
	commits := make([]HistoryCommit, 0, firstVisible)
	displayRow := 0
	for _, line := range lineRanges {
		absolute := sourceLineRange{
			Source: SourceRange{Start: line.Source.Start + start, End: line.Source.End + start},
			Text:   line.Text,
		}
		if line.Text == "" {
			// Blank lines (including the trailing newline of a streamed body)
			// wrap to exactly one empty physical row. handle them explicitly:
			// wrapPlainAppScreenText deliberately declines empty input.
			if displayRow >= firstVisible {
				break
			}
			if rows[displayRow] != "" {
				return nil
			}
			commits = append(commits, HistoryCommit{
				Origin:           HistoryCommitActive,
				CellID:           active.CellID,
				Revision:         active.Revision,
				SourceRange:      absolute.Source,
				DisplayRange:     DisplayRange{Start: displayRow, End: displayRow + 1},
				LayoutGeneration: generation,
				Lines: []render.Line{{
					Spans: []render.Span{{Text: "", Style: render.Style{Role: string(role)}}},
				}},
			})
			displayRow++
			continue
		}
		wrapped, sourceRows, mapped := plainWrappedSourceRanges(absolute, width, false)
		if !mapped || len(wrapped) == 0 || len(sourceRows) != len(wrapped) {
			return nil
		}
		if displayRow >= firstVisible {
			break
		}
		commitRows := len(wrapped)
		if remaining := firstVisible - displayRow; commitRows > remaining {
			commitRows = remaining
		}
		for offset, text := range wrapped[:commitRows] {
			if rows[displayRow+offset] != text {
				return nil
			}
			sourceRange := sourceRows[offset]
			if sourceRange.End > active.Stable.End {
				return nil
			}
			commits = append(commits, HistoryCommit{
				Origin:           HistoryCommitActive,
				CellID:           active.CellID,
				Revision:         active.Revision,
				SourceRange:      sourceRange,
				DisplayRange:     DisplayRange{Start: displayRow + offset, End: displayRow + offset + 1},
				LayoutGeneration: generation,
				Lines: []render.Line{{
					Spans: []render.Span{{Text: text, Style: render.Style{Role: string(role)}}},
				}},
			})
		}
		displayRow += commitRows
		if commitRows < len(wrapped) {
			break
		}
	}
	if displayRow != firstVisible || len(commits) == 0 {
		return nil
	}
	return commits
}

func planMutableMarkdownHistoryCommit(active ActiveCellState, geometry GeometryState, generation uint64, theme style.ThemeContext) []HistoryCommit {
	start, stableEnd := active.Acked.End, active.Stable.End
	if start < 0 || stableEnd <= start || stableEnd > len(active.Source) ||
		!activeCellSourceBoundary(active.Source, start) ||
		!activeCellSourceBoundary(active.Source, stableEnd) {
		return nil
	}
	highlighter := newActiveBandHighlighter()
	// Reasoning renders derived divider rows around its semantic body, so the
	// projector must preserve that exact shape or finalization can re-commit the
	// whole cell and duplicate reasoning in scrollback.
	reasoning := active.Kind == scene.KindReasoning
	// One projector serves the whole handoff loop: prefix (source[:start]) is
	// memoized across every candidate query and the full-source render is done
	// once, so planning N rows costs ~N full renders instead of 2×(N+2).
	proj := newSuffixProjector(active.Source, start, geometry.Width, theme, reasoning, highlighter)
	live, ok := proj.live()
	if !ok || len(live) <= ActiveBandRows(geometry.Height) {
		return nil
	}
	maxCommitRows := len(live) - ActiveBandRows(geometry.Height)
	commitEnd := active.Enqueued.End
	// Only rows past the last enqueued boundary can be newly handed off.
	deadline := time.Now().Add(historyCommitPlanningBudget)
	for _, line := range sourceLineRanges(active.Source[start:stableEnd]) {
		if time.Now().After(deadline) {
			break
		}
		candidateEnd := start + line.Source.End
		if candidateEnd <= commitEnd || candidateEnd > stableEnd {
			continue
		}
		candidateLines, projected := proj.suffix(candidateEnd)
		if !projected || len(candidateLines) == 0 {
			continue
		}
		if len(candidateLines) > maxCommitRows {
			// Rendered row count is monotonic in the source end, so later
			// candidates can only exceed the band budget as well.
			break
		}
		if !render.LinesEqual(candidateLines, live[:len(candidateLines)]) {
			continue
		}
		commitEnd = candidateEnd
	}
	if commitEnd <= start {
		return nil
	}
	boundaries := make([]int, 0, 2)
	if active.Enqueued.End > start && active.Enqueued.End <= commitEnd {
		boundaries = append(boundaries, active.Enqueued.End)
	}
	if commitEnd > active.Enqueued.End {
		boundaries = append(boundaries, commitEnd)
	}
	commits := make([]HistoryCommit, 0, len(boundaries))
	sourceStart, displayStart := start, 0
	for _, sourceEnd := range boundaries {
		projectedPrefix, projected := proj.suffix(sourceEnd)
		if !projected || displayStart >= len(projectedPrefix) {
			return nil
		}
		lines := cloneRenderLines(projectedPrefix[displayStart:])
		if len(lines) == 0 || displayStart+len(lines) > len(live) ||
			!render.LinesEqual(lines, live[displayStart:displayStart+len(lines)]) {
			return nil
		}
		commits = append(commits, HistoryCommit{
			Origin:           HistoryCommitActive,
			CellID:           active.CellID,
			Revision:         active.Revision,
			SourceRange:      SourceRange{Start: sourceStart, End: sourceEnd},
			FragmentID:       uint64(sourceStart) + 1,
			DisplayRange:     DisplayRange{Start: displayStart, End: displayStart + len(lines)},
			LayoutGeneration: generation,
			Lines:            lines,
		})
		sourceStart = sourceEnd
		displayStart += len(lines)
	}
	return commits
}

// planMarkdownCellHistoryCommits hands off hidden rich-rendered rows one at a
// time. Markdown transformations are not source-byte bijective, so every row
// keeps the enclosing immutable source range and receives a stable renderer
// fragment ordinal. The terminal payload remains the renderer's structured
// line, never the raw Markdown source.
func planMarkdownCellHistoryCommits(cell scene.TranscriptCell, rows []AppScreenRow, displayStart, firstVisible, skipRows int, generation uint64, byID map[scene.CellID]scene.TranscriptCell, settled func(historyCommitSourceKey) bool) []HistoryCommit {
	// 容量不再按整 cell 行数预分配（pprof：仅这一行就累计分配 24.5GB）：
	// 一次 pass 只为尚未结算的分片建 commit，通常远少于 cell 行数。
	capacity := len(rows)
	if capacity > 8 {
		capacity = 8
	}
	commits := make([]HistoryCommit, 0, capacity)
	fragmentID := uint64(0)
	for index, row := range rows {
		if row.TranscriptGap {
			continue
		}
		fragmentID++
		if int(fragmentID) <= skipRows {
			continue
		}
		// 已交付/已终结的分片不需要再物化 lines：跳过即可，身份编号
		// （fragmentID）不受影响，未结算分片的语义与顺序完全不变。
		if settled != nil && settled(historyCommitSourceIdentity(HistoryCommit{
			CellID:      cell.ID,
			Revision:    cell.Revision,
			SourceRange: SourceRange{Start: 0, End: len(cell.Source)},
			FragmentID:  fragmentID,
		})) {
			continue
		}
		end := index + 1
		if displayStart+end > firstVisible {
			continue
		}
		start := index
		if fragmentID == 1 {
			// A leading cell-boundary gap belongs to the first rendered row.
			start = 0
		}
		lines := make([]render.Line, 0, end-start)
		for _, displayRow := range rows[start:end] {
			lines = append(lines, appTranscriptRenderLine(displayRow, byID))
		}
		commits = append(commits, HistoryCommit{
			CellID:           cell.ID,
			Revision:         cell.Revision,
			SourceRange:      SourceRange{Start: 0, End: len(cell.Source)},
			FragmentID:       fragmentID,
			DisplayRange:     DisplayRange{Start: displayStart + start, End: displayStart + end},
			LayoutGeneration: generation,
			Lines:            lines,
		})
	}
	return commits
}

// wholeCellHistoryCommit is the conservative fallback for structured rendering
// and unusual source that cannot be bijectively related to plain source lines.
// It is only eligible when no row from the cell remains in the primary frame.
func wholeCellHistoryCommit(cell scene.TranscriptCell, rows []AppScreenRow, start, end int, generation uint64, byID map[scene.CellID]scene.TranscriptCell, settled func(historyCommitSourceKey) bool) (HistoryCommit, bool) {
	if settled != nil && settled(historyCommitSourceIdentity(HistoryCommit{
		CellID:      cell.ID,
		Revision:    cell.Revision,
		SourceRange: SourceRange{Start: 0, End: len(cell.Source)},
	})) {
		return HistoryCommit{}, false
	}
	lines := make([]render.Line, 0, len(rows))
	for _, row := range rows {
		lines = append(lines, appTranscriptRenderLine(row, byID))
	}
	return HistoryCommit{
		CellID:           cell.ID,
		Revision:         cell.Revision,
		SourceRange:      SourceRange{Start: 0, End: len(cell.Source)},
		DisplayRange:     DisplayRange{Start: start, End: end},
		LayoutGeneration: generation,
		Lines:            lines,
	}, true
}

// sourceLineRange is a non-overlapping source range for one logical source
// line. Newlines belong to the preceding line so ranges cover every byte once.
// A final empty source line has a zero-width range. The finalized plain planner
// maps that display row back to the terminating newline byte and gives it a
// fragment identity, because HistoryCommit deliberately rejects empty ranges.
type sourceLineRange struct {
	Source SourceRange
	Text   string
}

func sourceLineRanges(source string) []sourceLineRange {
	if source == "" {
		return nil
	}
	ranges := make([]sourceLineRange, 0, 1)
	start := 0
	for index := 0; index < len(source); index++ {
		if source[index] != '\n' {
			continue
		}
		ranges = append(ranges, sourceLineRange{
			Source: SourceRange{Start: start, End: index + 1},
			Text:   source[start:index],
		})
		start = index + 1
	}
	ranges = append(ranges, sourceLineRange{
		Source: SourceRange{Start: start, End: len(source)},
		Text:   source[start:],
	})
	return ranges
}

// planPlainCellHistoryCommits maps every finalized plain display row back to a
// stable source fragment. A source line can wrap across the primary boundary;
// committing it only as one unit leaves its hidden prefix nowhere to browse.
// Per-row fragments make the handoff continuous without duplicating already
// delivered source when later rows of the same long line become eligible.
//
// Markdown is handled by planMarkdownCellHistoryCommits because its renderer
// can add/remove physical rows and therefore has no source-byte fragment map.
func planPlainCellHistoryCommits(cell scene.TranscriptCell, rows []AppScreenRow, displayStart, firstVisible, skipRows, width int, themeFp string, generation uint64, byID map[scene.CellID]scene.TranscriptCell, settled func(historyCommitSourceKey) bool) ([]HistoryCommit, bool) {
	lineRanges := sourceLineRanges(cell.Source)
	if len(lineRanges) == 0 {
		return nil, false
	}

	row := 0
	leadingGaps := 0
	for leadingGaps < len(rows) && rows[leadingGaps].TranscriptGap {
		leadingGaps++
	}
	row = leadingGaps

	// 缓存命中路径：复用 wrap/source 映射/物化 Lines，仅做轻量对齐校验
	// 与动态字段（DisplayRange / LayoutGeneration / skipRows）组装，把每
	// 次 delta 的全量重新 wrap + clone 降为 O(Δ)。
	key := planCacheKeyFor(cell, width, themeFp)
	cached := sharedHistoryPlan.get(key)
	if cached != nil && leadingGaps+len(cached) == len(rows) {
		aligned := true
		for i, pr := range cached {
			r := rows[leadingGaps+i]
			if r.TranscriptGap || r.Text != pr.text {
				aligned = false
				break
			}
		}
		if aligned {
			return assemblePlainHistoryCommits(cell, cached, leadingGaps, displayStart, firstVisible, skipRows, generation, settled), true
		}
	}

	// miss：全量构建物理行（wrap + source 映射 + 物化 Lines）。
	physical := make([]planPhysicalRow, 0, len(rows)-leadingGaps)
	for _, sourceLine := range lineRanges {
		var (
			wrapped     []string
			sourceRows  []SourceRange
			fragmentIDs []uint64
			mapped      bool
		)
		if sourceLine.Text == "" {
			// The fast plain wrapper deliberately declines empty input, but an
			// internal blank line is still one physical row owned by its newline.
			// A trailing empty line has no bytes of its own, so retain the final
			// newline as its source and distinguish the second presentation row
			// with a stable non-zero fragment identity.
			var sourceRange SourceRange
			var fragmentID uint64
			sourceRange, fragmentID, mapped = plainBlankSourceIdentity(cell.Source, sourceLine)
			if mapped {
				wrapped = []string{""}
				sourceRows = []SourceRange{sourceRange}
				fragmentIDs = []uint64{fragmentID}
			}
		} else {
			wrapped, sourceRows, mapped = plainWrappedSourceRanges(sourceLine, width, cell.Kind == scene.KindUser)
			fragmentIDs = make([]uint64, len(sourceRows))
		}
		if !mapped || len(wrapped) == 0 || row+len(wrapped) > len(rows) {
			return nil, false
		}
		for offset, text := range wrapped {
			if rows[row+offset].TranscriptGap || rows[row+offset].Text != text || len(rows[row+offset].RenderLine.Spans) != 0 {
				return nil, false
			}
		}
		for offset, sourceRange := range sourceRows {
			globalRow := row + offset
			physical = append(physical, planPhysicalRow{
				text:     rows[globalRow].Text,
				source:   sourceRange,
				fragment: fragmentIDs[offset],
				line:     appTranscriptRenderLine(rows[globalRow], byID),
			})
		}
		row += len(wrapped)
	}
	if row != len(rows) {
		return nil, false
	}
	sharedHistoryPlan.put(key, physical)
	return assemblePlainHistoryCommits(cell, physical, leadingGaps, displayStart, firstVisible, skipRows, generation, settled), true
}

// assemblePlainHistoryCommits 按当前动态状态把物理行组装为 HistoryCommit：
// skipRows 跳过已 acked 前缀，DisplayRange 按全局行下标偏移，LayoutGeneration
// 取当前值。lines 共享物理行底层（只读消费，零拷贝）。
func assemblePlainHistoryCommits(cell scene.TranscriptCell, physical []planPhysicalRow, leadingGaps, displayStart, firstVisible, skipRows int, generation uint64, settled func(historyCommitSourceKey) bool) []HistoryCommit {
	// 与 markdown 路径同理：一次 pass 只为尚未结算的分片建 commit，
	// 不再按整 cell 物理行数预分配。
	capacity := len(physical)
	if capacity > 8 {
		capacity = 8
	}
	commits := make([]HistoryCommit, 0, capacity)
	for i, pr := range physical {
		if i < skipRows {
			continue
		}
		start := leadingGaps + i
		if i == 0 {
			// A cell boundary gap is a display artifact belonging to the
			// first source row, not an independent zero-width effect.
			start = 0
		}
		end := leadingGaps + i + 1
		if displayStart+end > firstVisible {
			continue
		}
		if settled != nil && settled(historyCommitSourceIdentity(HistoryCommit{
			CellID:      cell.ID,
			Revision:    cell.Revision,
			SourceRange: pr.source,
			FragmentID:  pr.fragment,
		})) {
			continue
		}
		lines := make([]render.Line, 0, end-start)
		if start < leadingGaps {
			for k := start; k < leadingGaps; k++ {
				lines = append(lines, render.Line{})
			}
		}
		contentStart := start - leadingGaps
		if contentStart < 0 {
			contentStart = 0
		}
		for _, pr := range physical[contentStart : i+1] {
			lines = append(lines, pr.line)
		}
		commits = append(commits, HistoryCommit{
			CellID:           cell.ID,
			Revision:         cell.Revision,
			SourceRange:      pr.source,
			FragmentID:       pr.fragment,
			DisplayRange:     DisplayRange{Start: displayStart + start, End: displayStart + end},
			LayoutGeneration: generation,
			Lines:            lines,
		})
	}
	return commits
}

func plainBlankSourceIdentity(source string, line sourceLineRange) (SourceRange, uint64, bool) {
	if line.Text != "" || !line.Source.Valid() || line.Source.End > len(source) {
		return SourceRange{}, 0, false
	}
	if line.Source.End > line.Source.Start {
		return line.Source, 0, true
	}
	end := line.Source.End
	if end == 0 || end != len(source) || source[end-1] != '\n' {
		return SourceRange{}, 0, false
	}
	return SourceRange{Start: end - 1, End: end}, uint64(end) + 1, true
}

// plainWrappedSourceRanges returns the same rows as wrapAppScreenText together
// with the exact half-open source range responsible for each row. It accepts
// only the source-preserving wrapper path. Control-sequence/tab cases continue
// to use the conservative whole-cell fallback because their terminal state is
// not bijective with source bytes.
// userMessage 为 true 时按用户 prompt 消息处理：内容按 width-2 预算 wrap，
// 与布局层 wrapPlainCellRows 对用户消息的 wrap 保持一致（渲染层会为每行
// 追加 "> " 前缀）。
func plainWrappedSourceRanges(sourceLine sourceLineRange, width int, userMessage bool) ([]string, []SourceRange, bool) {
	if width < 1 {
		width = 80
	}
	wrapWidth := width
	if userMessage {
		wrapWidth = width - 2
		if wrapWidth < 1 {
			wrapWidth = 1
		}
	}
	wrapped, ok := wrapPlainAppScreenText(sourceLine.Text, wrapWidth)
	if !ok || len(wrapped) == 0 {
		return nil, nil, false
	}
	ranges := make([]SourceRange, 0, len(wrapped))
	start := 0
	used := 0
	for offset, value := range sourceLine.Text {
		glyphWidth := render.RuneWidth(value)
		if glyphWidth == 0 {
			if offset == start && used == 0 {
				return nil, nil, false
			}
			continue
		}
		if glyphWidth > wrapWidth {
			return nil, nil, false
		}
		if used > 0 && used+glyphWidth > wrapWidth {
			ranges = append(ranges, SourceRange{Start: sourceLine.Source.Start + start, End: sourceLine.Source.Start + offset})
			start = offset
			used = 0
		}
		used += glyphWidth
	}
	ranges = append(ranges, SourceRange{Start: sourceLine.Source.Start + start, End: sourceLine.Source.End})
	if len(ranges) != len(wrapped) {
		return nil, nil, false
	}
	for _, sourceRange := range ranges {
		if sourceRange.End <= sourceRange.Start {
			return nil, nil, false
		}
	}
	return wrapped, ranges, true
}

func cellIsFinalizedForHistory(cell scene.TranscriptCell) bool {
	switch cell.Phase {
	case scene.CellCommitted, scene.CellPartiallyHandedOff, scene.CellHandedOff:
		return true
	default:
		return false
	}
}

// syncHistoryEffectsForTranscript is invoked by semantic transcript
// transitions. Geometry changes rebase existing pending payloads separately;
// they never mint a new token merely because the viewport resized.
//
// The full plan re-lays-out and re-wraps every finalized cell, so on resumed
// sessions it costs O(entire history) even when the triggering action only
// grew the still-mutable active cell. A fingerprint memo skips the rebuild
// when every planEligibleHistoryCommits input is unchanged; see the
// lastPlannedTranscript* field comment on HistoryEffectQueueState.
func syncHistoryEffectsForTranscript(state *UIControllerState) {
	syncHistoryEffectsForTranscriptWithin(state, time.Now().Add(historyCommitPlanningBudget))
}

// syncHistoryEffectsForTranscriptWithin 是带 deadline 的规划实现。零值 deadline
// 表示**不设预算**：screening 循环不会截断，因此这一轮要么完整覆盖 transcript，
// 要么真的没有可交付的候选；它只用于"游标走到末尾后的最后一次全量 membership
// pass"，不再用于续跑本身。
//
// 返回 (completed, advanced)：completed 表示本轮结束时整份 transcript 已被完整
// 规划（memo 已落）；advanced 表示续跑游标相对本轮之前前进了（含从无到有的首次
// 建立）。位置前进才算进展——新窗口可能整体落在已有终态记录的区域（NextToken
// 不变），那依然是进展；continueTruncatedHistoryPlan 用 advanced 判定 PlanStalled。
func syncHistoryEffectsForTranscriptWithin(state *UIControllerState, deadline time.Time) (completed, advanced bool) {
	if state == nil {
		return false, false
	}
	if transcriptPlanMemoHit(state) {
		// The finalized transcript prefix and every layout input it depends on
		// are unchanged. Only the mutable active cell's handoff inputs can have
		// moved (the executor acks advance Acked/Enqueued on every handoff), so
		// reconcile just that O(viewport) part. Re-laying-out the entire
		// finalized history per ack is what pinned resumed sessions at ~190%
		// CPU: the active handoff loop paid O(entire history) per commit.
		syncHistoryEffectsForActiveCell(state)
		return false, false
	}
	// A memo miss means the plan inputs moved, so whatever the previous
	// continuation attempt could not reach is worth another pass at these
	// inputs. This is also what re-arms the executor-side continuation kick
	// after a pass that could not advance (see HistoryEffectQueueState.
	// PlanStalled).
	effects := &state.HistoryEffects
	effects.PlanStalled = false
	inputs := currentTranscriptPlanInputs(state)
	// P1.2 Stage B：装了 plan worker 时，锁内只派发窗口请求；screening 在 worker
	// 上执行，结果以 HistoryPlanWindowReady 回到 reducer 铸 commit 并收尾。在飞
	// 期间不重复派发：executor kick 门（planContinuationPending）同时把在飞视作
	// 已有进展，避免无 sleep 的热旋转。派发失败（sink 拒绝/无 worker）回退同步。
	if state.planSink != nil {
		if effects.planRequestInFlight {
			return false, false
		}
		if dispatchTranscriptPlanWindow(state, inputs) {
			return false, false
		}
	}
	// 预算必须在布局之前建立：布局本身（遍历全部 cell 并对未命中的结构化
	// cell 重跑 markdown/chroma）就是这一轮 pass 里最贵的部分，旧实现把
	// deadline 建在 commit 循环里，等布局跑完才生效，等于没有预算。
	// P16：规划器本体（screening + 铸 commit，含前缀入队）的耗时归因。这是
	// 锁内最贵的一段，必须能回答「P12 的冻结是不是花在规划上」；memo 命中
	// （上方早退）不算一次规划，因此不记录，避免把空转计成规划。
	planStarted := time.Now()
	defer func() { effects.recordTranscriptPlanTiming(time.Since(planStarted)) }()

	// P1.1b：游标续跑。输入指纹仍匹配时从上次截断的语义行继续，而不是从 0 重走
	// 已规划前缀——后者在冷缓存的大会话上每一轮都停在同一样本点，正是 live 上
	// "next 不变 / PlanStalled 永久置位"的根因。
	if effects.planResumeValid && effects.planResumeInputs == inputs {
		beforeRow := effects.planResumeRow
		commits, complete, nextRow, screenRows := planEligibleHistoryCommitsWithinFrom(
			state.AppState, deadline, beforeRow, effects.planResumeScreenRows)
		return applyTranscriptPlanWindow(state, true, beforeRow, commits, complete, nextRow, screenRows, inputs)
	}

	// 无游标或输入已变：从 0 开始，旧游标作废。
	effects.clearTranscriptPlanResume()
	commits, complete, nextRow, screenRows := planEligibleHistoryCommitsWithinFrom(state.AppState, deadline, 0, 0)
	return applyTranscriptPlanWindow(state, false, 0, commits, complete, nextRow, screenRows, inputs)
}

// applyTranscriptPlanWindow 把一次已铸好的 screening 窗口结果落到队列状态上，
// 同步路径与异步结果 action（handleHistoryPlanWindowReady）共用这一份实现，保证
// 两条路径的投递语义逐字一致。
func applyTranscriptPlanWindow(state *UIControllerState, resume bool, startRow int, commits []HistoryCommit, complete bool, nextRow, screenRows int, inputs transcriptPlanInputs) (completed, advanced bool) {
	effects := &state.HistoryEffects
	if resume {
		if complete {
			// 游标走到末尾的这一轮只拿到后缀；membership 踢除要求"候选列表 ==
			// 完整有效集合"，必须再做一次无预算全量 pass（布局缓存已全热）。
			return finishResumedTranscriptPlan(state), true
		}
		syncHistoryEffectCandidatesPrefix(state, commits)
		effects.PlanIncomplete = true
		effects.storeTranscriptPlanResume(nextRow, screenRows, inputs)
		return false, nextRow > startRow
	}
	if complete {
		effects.PlanIncomplete = false
		syncHistoryEffectCandidates(state, commits, 0)
		recordTranscriptPlanMemo(state, len(commits))
		return true, true
	}
	// 截断的规划结果只能当**前缀**用：它缺少的只是「还没走到」的尾部 cell，
	// 不代表那些 cell 的候选失效。交给 syncHistoryEffectCandidates 会把尾部
	// 已经排队、甚至已经在写的提交全部 invalidate（token 抖动会回退 Enqueued
	// 前沿）。这里只入队不逐出，逐出留给走到末尾后的那次全量 pass。
	//
	// 截断必须留下三件事，否则这个前缀就是这条计划的终点：游标（从截断处继续
	// 而不是从 0 重走）、PlanIncomplete（前缀交付完后由 ack / executor kick 续跑；
	// resume 后的空闲会话没有任何 transcript 迁移），以及 memo 的失效——由
	// transcriptPlanMemoHit 的 PlanIncomplete 早退保证，不再显式 invalidate
	// （显式 invalidate 会连游标一起清掉，见 clearTranscriptPlanResume）。
	effects.PlanIncomplete = true
	effects.storeTranscriptPlanResume(nextRow, screenRows, inputs)
	syncHistoryEffectCandidatesPrefix(state, commits)
	return false, nextRow > 0
}

// handleHistoryPlanWindowReady 是异步 screening 结果的 reducer 端收尾。
//
// 栅栏顺序固定（P1.2 审查）：先看 seq——不等于当前请求即已被更新的请求取代，直接
// 丢弃且不清 in-flight（新请求仍有效）；再看失效序号/输入指纹/恢复门，任何一项不
// 成立都丢弃结果并**立即按当前输入重新派发**——首次（无游标）规划被丢弃时
// PlanIncomplete 仍为 false，executor kick 与空队列唤醒都不会触发，空闲会话（正是
// resume 场景）会永久停摆。全部通过后，在锁内用 mint 快照铸 commit 并走共用收尾。
func handleHistoryPlanWindowReady(state *UIControllerState, a HistoryPlanWindowReady) {
	effects := &state.HistoryEffects
	if a.seq != effects.planRequestSeq {
		return
	}
	effects.planRequestInFlight = false
	inputs := currentTranscriptPlanInputs(state)
	stale := a.planInputsEpoch != effects.planInputsEpoch ||
		a.inputs != inputs ||
		state.Geometry.Width < 1 || state.Geometry.Height < 1 ||
		effects.Frozen || effects.ProjectionUnknown || effects.hasUnresolvedTerminalDelivery()
	if stale {
		effects.PlanStalled = false
		if state.planSink != nil {
			dispatchTranscriptPlanWindow(state, inputs)
		}
		return
	}
	mintStarted := time.Now()
	snap := transcriptPlanMintSnapshotFor(state.AppState)
	commits := mintTranscriptPlanWindow(state.AppState, snap, a.rows, a.screenRowsBefore)
	// 归因：screening 由 worker 侧计时，这里补上锁内 mint + 收尾的部分。
	effects.recordTranscriptPlanTiming(a.screenDuration + time.Since(mintStarted))
	applyTranscriptPlanWindow(state, a.resume, a.startRow, commits, a.complete, a.nextRow,
		a.screenRowsBefore+len(a.rows), a.inputs)
}

// finishResumedTranscriptPlan 在游标走到 transcript 末尾后完成这次被截断的规划：
// membership 踢除要求候选列表是"完整有效集合"，后缀不满足（会把前缀里所有
// Pending/InFlight token 误杀），因此必须再做一次无预算全量 pass —— 此时布局
// 缓存已被前几轮全部热起来，代价远低于首轮冷启动。完成后清游标、落 memo。
func finishResumedTranscriptPlan(state *UIControllerState) bool {
	full, complete := planEligibleHistoryCommitsWithin(state.AppState, time.Time{})
	if !complete {
		// 零 deadline 不会截断；保底不改变投递语义，保留游标等下一次机会。
		syncHistoryEffectCandidatesPrefix(state, full)
		state.HistoryEffects.PlanIncomplete = true
		return false
	}
	syncHistoryEffectCandidates(state, full, 0)
	state.HistoryEffects.clearTranscriptPlanResume()
	state.HistoryEffects.PlanIncomplete = false
	state.HistoryEffects.PlanStalled = false
	recordTranscriptPlanMemo(state, len(full))
	return true
}

// continueTruncatedHistoryPlan carries a budget-truncated transcript plan to
// completion. A truncated plan only ever mints the oldest prefix the layout walk
// could reach inside historyCommitPlanningBudget, and the continuation it relies
// on is a *later* transcript transition — which an idle session never produces.
// The delivered prefix was therefore the whole plan: the executor drained it,
// the ledger read pending=0/acked=N, and every row the walk never reached stayed
// missing from native scrollback (live resume: 356 acked commits for a
// 290,957-row transcript, executor idle, replay authorization already spent).
// Called from the ack handlers, this replaces "wait for a transition that may
// never come" with "continue as soon as the prefix has actually been delivered".
//
// The drain gate is what makes the loop terminate: a continuation pass runs only
// while the queue holds no undelivered token, and each pass either completes the
// plan (clearing PlanIncomplete) or mints the next prefix, which the executor
// must deliver before another continuation is allowed. A pass that mints nothing
// leaves PlanIncomplete set and simply stops — no spinner, no repeated layout of
// the same prefix.
//
// P1.1b: each pass advances the truncated plan's layout-walk cursor with one
// historyCommitPlanningBudget, so the drain gate also bounds the lock hold per
// round; only a pass that cannot move the cursor at all sets PlanStalled.
func continueTruncatedHistoryPlan(state *UIControllerState) bool {
	if state == nil {
		return false
	}
	effects := &state.HistoryEffects
	if !effects.PlanIncomplete {
		return false
	}
	if effects.planRequestInFlight {
		// 在飞请求拥有进展：screening 已委托给 worker，结果 action 回来后由它收尾
		// 或重派发。这里返回 false 且不置 PlanStalled——在飞不是停滞。
		return false
	}
	// While the prefix still has pending work the executor is already carrying
	// the plan forward; re-planning here would burn another layout budget per ack
	// without adding anything the queue does not already hold. In-flight tokens
	// are not observable at this point on purpose: the ack that drains the queue
	// is reduced after its own token was acknowledged, and the executor claims the
	// next token only after that reduction returned.
	if effects.ledger != nil && effects.ledger.pendingCount > 0 {
		return false
	}
	if effects.Frozen || effects.ProjectionUnknown || effects.hasUnresolvedTerminalDelivery() {
		// Recovery owns the queue; the replan that follows recovery
		// (HistoryProjectionRecovered / HistoryScrollbackReconciled) re-arms the
		// obligation on its own.
		return false
	}
	if state.Geometry.Width < 1 || state.Geometry.Height < 1 {
		return false
	}
	// P1.1b：续跑是**有预算的游标推进**——一轮一个 historyCommitPlanningBudget
	// 的锁内工作。游标保证每轮从上次截断处继续，因此预算不会让它停在同一样本点
	// 上（旧实现只能靠无预算的一轮走完整份历史，那正是 live 上单次数秒持锁的
	// 来源）。走到末尾的那一轮内部会自动再做一次无预算全量 pass 完成 membership
	// 踢除，然后清掉 PlanIncomplete。
	//
	// 进展判据是**游标位置**，不是新 token 数：新窗口可能整体落在已有终态记录的
	// 区域（NextToken 不变），那仍是进展；只有游标一动不动（输入已变、从 0 的
	// 保底 pass 也走不动）才是真 stalled，此时停止 kick 空转，等下一次 transcript
	// 迁移重新规划。
	completed, advanced := syncHistoryEffectsForTranscriptWithin(state, time.Now())
	if !completed && !advanced {
		effects.PlanStalled = true
	}
	return true
}

// planContinuationPending reports whether a budget-truncated plan still owes
// cells and has not already been proven unable to advance at the current plan
// inputs. The executor reads it to decide whether a wake with an empty queue
// and no recovery obligation should ask the reducer to continue the plan
// instead of going idle on a transcript it only partly delivered.
func (s HistoryEffectQueueState) planContinuationPending() bool {
	// 在飞请求期间不算 pending：screening 已委托给 worker。若仍为 true，executor
	// 的 kick 分支会每个轮次 Post 一枚 barrier Continue（run 无 sleep），形成
	// actor↔executor 热旋转；结果 action 自身的 wake 谓词负责重新唤醒。
	return s.PlanIncomplete && !s.PlanStalled && !s.planRequestInFlight
}

// transcriptFinalizedPrefixFence fingerprints every finalized transcript cell
// (Phase != CellMutable). The scene-wide Revision/ContentVersion counters
// advance on every cell mutation — including the active cell's append-only
// stream growth — so a memo keyed on them misses on every chunk and re-lays
// out the entire finalized history (the O(entire history) cost this memo
// exists to avoid). The fingerprint covers exactly what the finalized-prefix
// plan reads: each finalized cell's identity, its per-cell mutation fence
// (update/finalize/correct all require a strictly greater cell Revision, so
// Revision is a reliable content/presentation proxy), sequence/chain grouping
// (layout gap decisions read both), kind, phase, commit-block flag, boundary
// class, and source length. Mutable cells are excluded because they are the
// frontier barrier — their growth is planned by syncHistoryEffectsForActiveCell,
// never part of the finalized plan. Chain keys are folded into the fence so an
// unversioned snapshot that rewires tool-chain grouping cannot be memoized as
// identical (the SceneID == 0 guard this fence replaces).
func transcriptFinalizedPrefixFence(transcript TranscriptState) uint64 {
	const (
		fnvOffset = uint64(14695981039346656037) // FNV-1a 64-bit offset basis
		fnvPrime  = uint64(1099511628211)
	)
	h := fnvOffset
	for _, cell := range transcript.Cells {
		// The mutable cell (and any transient second mutable cell before it
		// finalizes) is the frontier barrier and never enters the finalized
		// plan; exclude it so its stream growth cannot invalidate the memo.
		if cell.Phase == scene.CellMutable {
			continue
		}
		h ^= uint64(cell.ID)
		h *= fnvPrime
		h ^= cell.Revision
		h *= fnvPrime
		h ^= cell.Sequence
		h *= fnvPrime
		h ^= uint64(cell.Kind)
		h *= fnvPrime
		h ^= uint64(cell.Phase)
		h *= fnvPrime
		if cell.HistoryCommitBlocked {
			h ^= 1
		}
		h *= fnvPrime
		h ^= uint64(cell.Boundary)
		h *= fnvPrime
		h ^= uint64(len(cell.Source))
		h *= fnvPrime
		h = transcriptFenceFoldString(h, cell.ChainKey, fnvPrime)
		h = transcriptFenceFoldString(h, cell.BoundaryGroupKey, fnvPrime)
	}
	return h
}

// transcriptFenceFoldString folds a grouping key into the finalized-prefix
// fence. Chain keys are identity-relevant layout inputs (gap decisions), so
// they must be part of the fingerprint even for unversioned snapshots.
func transcriptFenceFoldString(h uint64, s string, prime uint64) uint64 {
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime
	}
	return h
}

// transcriptPlanInputs 是 transcriptPlanMemoHit 与截断续跑游标共享的输入指纹：
// 任何一项变化都同时让 memo 与游标失效。字段与 memo 的比较项一一对应（fence 是
// 逐 cell 的 finalized 指纹，不含 mutable 前沿；themeKey 用 themeFingerprint；
// terminalEpoch 覆盖 reconcileScrollback 的整体 ledger 替换）。
type transcriptPlanInputs struct {
	sceneID       uint64
	fence         uint64
	finalized     int
	layoutGen     uint64
	width         int
	height        int
	projection    bool
	themeKey      string
	terminalEpoch uint64
	// finalizedAckVersion 是已 finalize cell 的 Active-origin 交付版本：ack 收缩
	// skipRows 后，游标必须从 0 重新规划（前缀候选集变了）。
	finalizedAckVersion uint64
}

func currentTranscriptPlanInputs(state *UIControllerState) transcriptPlanInputs {
	return transcriptPlanInputs{
		sceneID:             state.Transcript.SceneID,
		fence:               transcriptFinalizedPrefixFence(state.Transcript),
		finalized:           transcriptFinalizedCellCount(state.Transcript),
		layoutGen:           state.LayoutGeneration,
		width:               state.Geometry.Width,
		height:              state.Geometry.Height,
		projection:          state.SemanticActiveCellProjection,
		themeKey:            themeFingerprint(state.Theme),
		terminalEpoch:       state.HistoryEffects.TerminalEpoch,
		finalizedAckVersion: state.HistoryEffects.finalizedActiveAckPlanVersion,
	}
}

// transcriptPlanMemoHit reports whether the finalized-prefix plan inputs are
// identical to the ones that produced the last full transcript plan. The plan
// (planEligibleHistoryCommits) is pure over the finalized transcript fence,
// geometry, theme, layout generation, and the projection flag: the acked
// Active-origin prefix it reads via activeAckedRenderedPrefixRows is frozen
// once a cell finalizes, and the mutable cell itself is the frontier barrier —
// never part of the finalized plan. The transcript fence is the per-cell
// finalized fingerprint (not the scene-wide Revision/ContentVersion counters,
// which the active cell's stream growth advances on every chunk). The fence
// folds chain keys and sequence, so even an unversioned snapshot (SceneID ==
// 0) can be memoized safely: every layout-relevant cell mutation advances a
// covered field (cell Revision on update/finalize, ID/kind/phase/boundary/
// blocked/chain on structural rewiring, source length on stream growth).
func transcriptPlanMemoHit(state *UIControllerState) bool {
	effects := &state.HistoryEffects
	// 截断计划未完成时 memo 必须视为失效：memo 描述的是"完整规划"，命中会让
	// 续跑变成空操作，而 ledger 里只有前缀（P1.1b 的游标另走 planResume*，
	// 由 PlanIncomplete 与输入指纹共同守卫）。
	if effects.PlanIncomplete {
		return false
	}
	if !effects.lastPlannedTranscriptValid ||
		effects.lastPlannedTranscriptSceneID != state.Transcript.SceneID ||
		effects.lastPlannedTranscriptFence != transcriptFinalizedPrefixFence(state.Transcript) ||
		effects.lastPlannedTranscriptCells != transcriptFinalizedCellCount(state.Transcript) ||
		effects.lastPlannedTranscriptLayoutGen != state.LayoutGeneration ||
		effects.lastPlannedWidth != state.Geometry.Width ||
		effects.lastPlannedHeight != state.Geometry.Height ||
		effects.lastPlannedProjection != state.SemanticActiveCellProjection ||
		effects.lastPlannedThemeKey != themeFingerprint(state.Theme) ||
		effects.lastPlannedTerminalEpoch != effects.TerminalEpoch ||
		effects.lastPlannedFinalizedActiveAckVersion != effects.finalizedActiveAckPlanVersion {
		return false
	}
	// Every input above is unchanged, but the memo only fingerprints plan
	// *inputs*: it cannot see that the ledger the plan was reconciled into no
	// longer holds it. That state is reachable (reconcileScrollback replaces
	// the ledger wholesale, and an armed replacement can be reduced before any
	// plan was minted), and memoizing it is destructive: the executor is then
	// authorized to clear native scrollback while the queue has nothing to
	// replay. A plan that produced candidates must still be present in the
	// ledger to be memoizable; a plan that produced none stays memoizable, so
	// a legitimately empty queue (fresh session, everything already delivered)
	// never pays for the O(entire history) re-layout this memo exists to avoid.
	if effects.lastPlannedCandidateCount > 0 && !effects.ledger.holdsPlan() {
		return false
	}
	return true
}

func recordTranscriptPlanMemo(state *UIControllerState, candidates int) {
	effects := &state.HistoryEffects
	effects.lastPlannedTranscriptValid = true
	effects.lastPlannedTranscriptSceneID = state.Transcript.SceneID
	effects.lastPlannedTranscriptFence = transcriptFinalizedPrefixFence(state.Transcript)
	effects.lastPlannedTranscriptCells = transcriptFinalizedCellCount(state.Transcript)
	effects.lastPlannedTranscriptLayoutGen = state.LayoutGeneration
	effects.lastPlannedWidth = state.Geometry.Width
	effects.lastPlannedHeight = state.Geometry.Height
	effects.lastPlannedProjection = state.SemanticActiveCellProjection
	effects.lastPlannedThemeKey = themeFingerprint(state.Theme)
	effects.lastPlannedTerminalEpoch = effects.TerminalEpoch
	effects.lastPlannedFinalizedActiveAckVersion = effects.finalizedActiveAckPlanVersion
	effects.lastPlannedCandidateCount = candidates
}

// transcriptFinalizedCellCount counts the cells the finalized-prefix plan can
// consume. The total cell count is deliberately not used as a memo fence: a busy
// turn keeps appending *mutable* tail cells (new reasoning / tool-chain
// boundaries), and treating any count change as a plan-input change re-planned
// the entire history per appended cell — measured at 723ms/op on a 2000-cell
// transcript, and visible live as plan-last-ms 4.7-6.9s with ~9 plans/min. Only
// the finalized prefix enters planEligibleHistoryCommits; mutable cells are the
// frontier barrier and are planned by syncHistoryEffectsForActiveCell.
func transcriptFinalizedCellCount(transcript TranscriptState) int {
	count := 0
	for _, cell := range transcript.Cells {
		if cell.Phase != scene.CellMutable {
			count++
		}
	}
	return count
}

// syncHistoryEffectsForActiveCell is the hot path for append-only stream
// updates. The finalized transcript prefix and its physical rows cannot change
// while the same cell remains mutable, so rebuilding it here only burns CPU
// and allocations. Reconcile just this cell's active handoff candidates.
func syncHistoryEffectsForActiveCell(state *UIControllerState) {
	if state == nil || !state.SemanticActiveCellProjection ||
		state.Active.Phase != ActiveCellMutable || state.Active.CellID == 0 {
		return
	}
	// Fast path: an append-only stream update that neither moves a source
	// boundary (Stable/Enqueued/Acked), nor resizes (Geometry generation), nor
	// reflows (Layout generation), nor changes the source content that the
	// planner branches on (length / markdown shape / commit barrier) cannot
	// change the planned candidate set.
	// planMutableActiveCellHistoryCommitsWithTheme is pure over these inputs,
	// so skip the rebuild entirely instead of churning CPU and allocations on
	// every chunk.
	looksMarkdown := state.Active.Kind == scene.KindAssistant && markdown.LooksLikeMarkdown(state.Active.Source)
	supplementMarkdown := state.Active.Kind == scene.KindReasoning && markdown.LooksLikeMarkdown(state.Active.Source)
	if state.HistoryEffects.lastPlannedActiveEnqueuedValid &&
		state.HistoryEffects.lastPlannedActiveStable == state.Active.Stable &&
		state.HistoryEffects.lastPlannedActiveEnqueued == state.Active.Enqueued &&
		state.HistoryEffects.lastPlannedActiveAcked == state.Active.Acked &&
		state.HistoryEffects.lastPlannedLayoutGeneration == state.LayoutGeneration &&
		state.HistoryEffects.lastPlannedGeometryGeneration == state.Geometry.Generation &&
		state.HistoryEffects.lastPlannedSourceLen == len(state.Active.Source) &&
		state.HistoryEffects.lastPlannedKind == state.Active.Kind &&
		state.HistoryEffects.lastPlannedLooksMarkdown == looksMarkdown &&
		state.HistoryEffects.lastPlannedSupplementMarkdown == supplementMarkdown &&
		state.HistoryEffects.lastPlannedBlocked == state.Active.HistoryCommitBlocked {
		return
	}
	candidates := planMutableActiveCellHistoryCommitsWithTheme(
		state.Active, state.Geometry, state.LayoutGeneration, state.Theme,
	)
	syncHistoryEffectCandidates(state, candidates, state.Active.CellID)
	state.HistoryEffects.lastPlannedActiveStable = state.Active.Stable
	state.HistoryEffects.lastPlannedActiveEnqueued = state.Active.Enqueued
	state.HistoryEffects.lastPlannedActiveAcked = state.Active.Acked
	state.HistoryEffects.lastPlannedLayoutGeneration = state.LayoutGeneration
	state.HistoryEffects.lastPlannedGeometryGeneration = state.Geometry.Generation
	state.HistoryEffects.lastPlannedSourceLen = len(state.Active.Source)
	state.HistoryEffects.lastPlannedKind = state.Active.Kind
	state.HistoryEffects.lastPlannedLooksMarkdown = looksMarkdown
	state.HistoryEffects.lastPlannedSupplementMarkdown = supplementMarkdown
	state.HistoryEffects.lastPlannedBlocked = state.Active.HistoryCommitBlocked
	state.HistoryEffects.lastPlannedActiveEnqueuedValid = true
}

// syncHistoryEffectCandidates reconciles a planned candidate set with the
// reducer-owned ledger. scopeCellID is zero for a complete transcript plan;
// otherwise only active entries for that cell are touched.
func syncHistoryEffectCandidates(state *UIControllerState, candidates []HistoryCommit, scopeCellID scene.CellID) {
	valid := make(map[historyCommitSourceKey]HistoryCommit, len(candidates))
	for _, candidate := range candidates {
		valid[historyCommitSourceIdentity(candidate)] = candidate
	}
	if ledger := state.HistoryEffects.ledger; ledger != nil {
		reconcile := func(entry HistoryCommitEntry) {
			candidate, exists := valid[historyCommitSourceIdentity(entry.Commit)]
			switch entry.State {
			case HistoryCommitPending:
				if !exists {
					_ = state.HistoryEffects.invalidate(entry.Commit.Token)
					return
				}
				// A semantic snapshot can retain the same cell source while
				// changing a preceding boundary/gap. The token remains the
				// same unstarted effect, but its display payload must be rebased
				// before a presenter can write the old physical rows.
				if !historyCommitPresentationEqual(entry.Commit, candidate) {
					if err := ledger.RebasePending(entry.Commit.Token, candidate); err != nil {
						state.HistoryEffects.ProjectionUnknown = true
					}
				}
			case HistoryCommitInFlight:
				// Once a terminal transaction was claimed, a changed display
				// payload may already be partially written. Never let its old
				// acknowledgement prove delivery for the new semantic layout.
				if !exists || !historyCommitPresentationEqual(entry.Commit, candidate) {
					_ = state.HistoryEffects.invalidate(entry.Commit.Token)
				}
			}
		}
		if scopeCellID == 0 {
			for _, entry := range ledger.byToken {
				reconcile(entry)
			}
		} else {
			for _, token := range ledger.activeTokensByCell[scopeCellID] {
				if entry, exists := ledger.byToken[token]; exists {
					reconcile(entry)
				}
			}
		}
	}
	enqueueHistoryCandidates(state, candidates)
}

// syncHistoryEffectCandidatesPrefix 用于**被预算截断**的规划结果：candidates 是
// 完整规划的前缀，它缺少的只是「尚未走到」的尾部 cell，不代表那些 cell 的候选
// 失效。因此只入队、不逐出 —— 逐出留给下一次完整规划。
//
// 区分这两条路径是必需的，不是优化：syncHistoryEffectCandidates 把 candidates
// 当作**完整**有效集合，凡是 ledger 里存在、却不在集合中的 pending/in-flight
// 条目都会被 invalidate。把截断前缀直接交给它，会在每一次布局超预算时把尾部
// 已排队（甚至已交给 terminal）的提交取消掉。
func syncHistoryEffectCandidatesPrefix(state *UIControllerState, candidates []HistoryCommit) {
	enqueueHistoryCandidates(state, candidates)
}

// enqueueHistoryCandidates 把候选入队到 reducer 自有 ledger。入队本身是幂等的：
// 已经有 terminal 记录的来源直接跳过，重复区间由 ErrDuplicateCommitRange 吸收。
func enqueueHistoryCandidates(state *UIControllerState, candidates []HistoryCommit) {
	for _, candidate := range candidates {
		if state.HistoryEffects.hasTerminalRecordForSource(candidate) {
			continue
		}
		if err := state.HistoryEffects.enqueue(candidate); err != nil &&
			!errors.Is(err, ErrDuplicateCommitRange) {
			state.HistoryEffects.ProjectionUnknown = true
		}
	}
	advanceActiveCellEnqueuedFromEffects(state)
}

// historyCommitPresentationEqual compares every non-token field that can
// affect terminal bytes. Token is reducer-owned delivery identity and is
// intentionally omitted so a pending effect can retain its identity while its
// current-layout display payload is safely rebased before any write begins.
//
// For active commits, DisplayRange is the band row relative to the Acked
// frontier at planning time.  When the Acked frontier advances between the
// full-transcript replan (after a scrollback reconciliation resets
// Acked=0) and the next active-only replan (from the new Acked.End), two
// plans for the same source range + lines produce different DisplayRange
// values although the terminal bytes are identical.  Comparing DisplayRange
// here would invalidate the in-flight commit on every streaming delta,
// re-arming a full scrollback reset + O(N²) replan per chunk — the
// high-CPU loop.  Skip DisplayRange for active commits; the source range
// and rendered lines are the authority for byte equality.
func historyCommitPresentationEqual(current, candidate HistoryCommit) bool {
	sameRevision := current.Revision == candidate.Revision ||
		(current.Origin == HistoryCommitActive && candidate.Origin == HistoryCommitActive)
	if current.Origin != candidate.Origin ||
		current.CellID != candidate.CellID || !sameRevision ||
		current.SourceRange != candidate.SourceRange ||
		current.FragmentID != candidate.FragmentID ||
		current.LayoutGeneration != candidate.LayoutGeneration ||
		!render.LinesEqual(current.Lines, candidate.Lines) {
		return false
	}
	if current.Origin == HistoryCommitActive {
		return true
	}
	return current.DisplayRange == candidate.DisplayRange
}

func advanceActiveCellEnqueuedFromEffects(state *UIControllerState) {
	if state == nil || state.Active.Phase != ActiveCellMutable || state.Active.CellID == 0 ||
		state.HistoryEffects.ledger == nil {
		return
	}
	frontier := state.Active.Enqueued.End
	commits := make([]HistoryCommit, 0)
	for _, token := range state.HistoryEffects.ledger.activeTokensByCell[state.Active.CellID] {
		entry, exists := state.HistoryEffects.ledger.byToken[token]
		if !exists {
			continue
		}
		commit := entry.Commit
		if commit.Origin != HistoryCommitActive || commit.CellID != state.Active.CellID ||
			commit.SourceRange.End <= frontier || commit.SourceRange.End > state.Active.Stable.End {
			continue
		}
		switch entry.State {
		case HistoryCommitPending, HistoryCommitInFlight, HistoryCommitAcked, HistoryCommitStateFailed:
			commits = append(commits, commit)
		}
	}
	sort.Slice(commits, func(i, j int) bool {
		if commits[i].SourceRange.Start != commits[j].SourceRange.Start {
			return commits[i].SourceRange.Start < commits[j].SourceRange.Start
		}
		if commits[i].SourceRange.End != commits[j].SourceRange.End {
			return commits[i].SourceRange.End < commits[j].SourceRange.End
		}
		return commits[i].Token < commits[j].Token
	})
	for _, commit := range commits {
		if commit.SourceRange.Start > frontier {
			break
		}
		if commit.SourceRange.End > frontier {
			frontier = commit.SourceRange.End
		}
	}
	if frontier <= state.Active.Enqueued.End {
		return
	}
	if next, err := MarkActiveEnqueued(state.Active, frontier); err == nil {
		state.Active = next
	}
}

func rebasePendingHistoryEffects(state *UIControllerState) {
	if state == nil {
		return
	}
	candidates := planEligibleHistoryCommits(state.AppState)
	valid := make(map[historyCommitSourceKey]struct{}, len(candidates))
	for _, candidate := range candidates {
		valid[historyCommitSourceIdentity(candidate)] = struct{}{}
	}
	if ledger := state.HistoryEffects.ledger; ledger != nil {
		for _, entry := range ledger.byToken {
			switch entry.State {
			case HistoryCommitPending:
				if _, exists := valid[historyCommitSourceIdentity(entry.Commit)]; !exists {
					_ = state.HistoryEffects.invalidate(entry.Commit.Token)
				}
			}
		}
	}
	for _, candidate := range candidates {
		if err := state.HistoryEffects.rebasePending(candidate); err != nil &&
			!errors.Is(err, ErrCommitNotPending) {
			state.HistoryEffects.ProjectionUnknown = true
		}
		if candidate.Origin == HistoryCommitActive &&
			!state.HistoryEffects.hasTerminalRecordForSource(candidate) {
			if err := state.HistoryEffects.enqueue(candidate); err != nil &&
				!errors.Is(err, ErrDuplicateCommitRange) {
				state.HistoryEffects.ProjectionUnknown = true
			}
		}
	}
	advanceActiveCellEnqueuedFromEffects(state)
}

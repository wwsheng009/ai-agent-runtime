package ui

import (
	"errors"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// planEligibleHistoryCommits selects finalized display ranges above the retained
// primary transcript viewport. It keeps source/display identity explicit: plain
// cells are split at stable source-line boundaries while structured Markdown
// rows use a renderer fragment identity. A still-mutable active cell contributes
// its own overflow prefix so early rows cross the physical writer before the
// final event arrives.
//
// P1-1 Stage 2 起规划是**无预算单遍**：不存在截断前缀/续跑游标，本函数与生产
// 热路径调用同一个实现。
func planEligibleHistoryCommits(state AppState) []HistoryCommit {
	commits, _ := planEligibleHistoryCommitsWithin(state)
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
		generation: state.Geometry.Generation,
		theme:      state.Theme,
	}
}

// transcriptPlanSnapshotFor 在锁内构造完整快照（同步路径）：layoutRows 与
// byID/mutable 一次派生完成。
func transcriptPlanSnapshotFor(state AppState) transcriptPlanSnapshot {
	snap := transcriptPlanMintSnapshotFor(state)
	snap.mutable = mutableTranscriptCellIDs(state.Transcript)
	snap.layoutRows = state.Transcript.LayoutRows(state.Geometry.Generation)
	return snap
}

// screenTranscriptPlanWindow 是 screening 相位：纯布局，**不读 ledger、
// 不读 HistoryEffects**，输出即全量布局（无预算单遍）。
func screenTranscriptPlanWindow(snap transcriptPlanSnapshot) []AppScreenRow {
	return layoutTranscriptScreenRows(snap.layoutRows, snap.byID, snap.mutable, snap.width, snap.theme)
}

// transcriptPlanRetention 记录"整 cell 提交集可保留"的复用段：cell 内容
// 寻址键未变（物理行缓存命中且窗口完整覆盖），且段内每一行都已有终态记录
// （Queued/Delivered/已结算/压缩墓碑）且呈现代一致。完整 pass 的 reconcile
// 可以整 cell 跳过这些条目：候选集（新增工作）与呈现都未变。
type transcriptPlanRetention struct {
	cells map[transcriptPlanCellIdentity]struct{}
}

type transcriptPlanCellIdentity struct {
	id       scene.CellID
	revision uint64
}

func (r *transcriptPlanRetention) mark(id scene.CellID, revision uint64) {
	if r.cells == nil {
		r.cells = make(map[transcriptPlanCellIdentity]struct{})
	}
	r.cells[transcriptPlanCellIdentity{id: id, revision: revision}] = struct{}{}
}

// mintTranscriptPlan 是铸 commit 相位（锁内）：按快照的 byID/width/theme/
// generation 解读 rows，其余输入（frontier、settled）一律在 live 状态上求值。
// A2 第二刀：active 不再铸提交（停铸），finalize 从 source 0 全量铸；skipRows 恒 0。
func mintTranscriptPlan(state AppState, snap transcriptPlanSnapshot, rows []AppScreenRow) ([]HistoryCommit, transcriptPlanRetention) {
	frontierCells, _ := canonicalHistoryCommitFrontier(state)
	// A2 第一刀（停铸 active）：mutable 期间不再铸 active 提交；finalize 时从
	// source 0 一次性铸全量 transcript 提交。旧 active 铸路径保留在
	// planMutableActiveCellHistoryCommitsWithTheme（死代码，第二刀删除）。
	// 已结算分片（Acked/Failed/Abandoned/Invalidated）不再参与 reconcile，
	// 规划时直接跳过，避免每次 transcript 迁移都把整段历史重新物化 payload。
	settled := state.HistoryEffects.hasSettledRecordForSource
	// 复用段分拣：行级终态 + 呈现代一致性（见 retainedQueuedCommitForSource）。
	retainedQueued := func(key historyCommitSourceKey) (HistoryCommit, bool, bool, bool) {
		return state.HistoryEffects.retainedQueuedCommitForSource(key, snap.generation)
	}
	// The primary frame now owns only the mutable/bottom inline viewport.
	// Finalized transcript rows all belong to native terminal history; retaining
	// a screen-sized transcript tail here would make those rows disappear as
	// soon as the viewport-only presenter stops repainting the old whole frame.
	// 无预算单遍（P1-1 Stage 2）：rows 就是全量布局，displayStart 恒 0，
	// firstVisible 即全量行数。
	displayStart := 0
	firstVisible := len(rows)
	// Commit eligibility is a physical display decision. Using semantic source
	// lines here would hand off a CJK/wrapped/tab-expanded cell while some of
	// its physical rows are still visible in the primary viewport.
	commits := make([]HistoryCommit, 0)
	retention := transcriptPlanRetention{}
	for start := 0; start < len(rows); {
		cellID := rows[start].CellID
		end := start + 1
		for end < len(rows) && rows[end].CellID == cellID {
			end++
		}
		cell, found := snap.byID[cellID]
		_, beforeFrontier := frontierCells[cellID]
		if found && beforeFrontier && cellIsFinalizedForHistory(cell) && cell.Source != "" {
			// A2 第二刀：active 不再铸提交，finalize 从 0 全量铸；skipRows 恒 0，
			// whole-cell 兜底仅需"cell 完整包含在窗口内"。
			if cellUsesStructuredPresentation(cell) {
				commits = append(commits, planMarkdownCellHistoryCommits(cell, rows[start:end], displayStart+start, firstVisible, 0, snap.generation, snap.byID, settled)...)
			} else if segments, mapped, retained := planPlainCellHistoryCommits(cell, rows[start:end], displayStart+start, firstVisible, 0, snap.width, themeFingerprint(snap.theme), snap.generation, snap.byID, settled, retainedQueued); mapped {
				commits = append(commits, segments...)
				if retained {
					retention.mark(cell.ID, cell.Revision)
				}
			} else if end <= firstVisible {
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
	return commits, retention
}

// planEligibleHistoryCommitsWithin 是规划的唯一实现（P1-1 Stage 2：无预算单遍）。
// 返回完整候选集与复用段保留集；不存在截断前缀，调用方可以在结果上直接做
// membership reconcile 并记录 memo。
func planEligibleHistoryCommitsWithin(state AppState) ([]HistoryCommit, transcriptPlanRetention) {
	commits, retention, _ := planEligibleHistoryCommitsWithinTimed(state)
	return commits, retention
}

// planEligibleHistoryCommitsWithinTimed 与 planEligibleHistoryCommitsWithin 同义，
// 额外返回分相位耗时（P16 归因）：screen = 快照 + 全量布局（含并行首渲染墙钟），
// mint = 铸 commit。apply（reconcile + 入队）由调用方补。
func planEligibleHistoryCommitsWithinTimed(state AppState) ([]HistoryCommit, transcriptPlanRetention, transcriptPlanPhaseTiming) {
	if state.Geometry.Width < 1 || state.Geometry.Height < 1 {
		return nil, transcriptPlanRetention{}, transcriptPlanPhaseTiming{}
	}
	screenStart := time.Now()
	snap := transcriptPlanSnapshotFor(state)
	rows := screenTranscriptPlanWindow(snap)
	screenMs := time.Since(screenStart).Milliseconds()
	mintStart := time.Now()
	commits, retention := mintTranscriptPlan(state, snap, rows)
	phases := transcriptPlanPhaseTiming{
		cells:    len(snap.cells),
		screenMs: screenMs,
		mintMs:   time.Since(mintStart).Milliseconds(),
	}
	return commits, retention, phases
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
// classifyPlainSegmentRetention 在物理行缓存命中时以逐行身份分拣复用段：
// 全部行要么已有终态记录且呈现代一致（整 cell 保留），要么任一行为新来源/
// 代漂移（返回 classified=false，回退重铸路径 —— 新行必须入候选，代漂移必须
// 触发 rebase）。仅完整覆盖（所有行在可见窗口内）与 skipRows==0 时分拣，
// 保证与无预算全量 pass 等价。
func classifyPlainSegmentRetention(cell scene.TranscriptCell, physical []planPhysicalRow, leadingGaps, displayStart, firstVisible, skipRows int, generation uint64, retainedQueued func(historyCommitSourceKey) (HistoryCommit, bool, bool, bool)) (commits []HistoryCommit, classified bool, retained bool) {
	if retainedQueued == nil || skipRows != 0 {
		return nil, false, false
	}
	if displayStart+leadingGaps+len(physical) > firstVisible {
		return nil, false, false
	}
	commits = make([]HistoryCommit, 0, len(physical))
	for index, pr := range physical {
		key := historyCommitSourceIdentity(HistoryCommit{
			CellID:      cell.ID,
			Revision:    cell.Revision,
			SourceRange: pr.source,
			FragmentID:  pr.fragment,
		})
		commit, hasQueued, terminal, safe := retainedQueued(key)
		if !terminal || !safe {
			return nil, false, false
		}
		if hasQueued {
			// 1b：复用段不重跑 assemble，但 DisplayRange 按当前全局行坐标做常量
			// 偏移重写（与 assemble 的 start/end 规则一致），保持簿记精确。
			start := leadingGaps + index
			if index == 0 {
				start = 0
			}
			end := leadingGaps + index + 1
			commit.DisplayRange = DisplayRange{Start: displayStart + start, End: displayStart + end}
			commit.LayoutGeneration = generation
			commits = append(commits, commit)
		}
	}
	return commits, true, true
}

func planPlainCellHistoryCommits(cell scene.TranscriptCell, rows []AppScreenRow, displayStart, firstVisible, skipRows, width int, themeFp string, generation uint64, byID map[scene.CellID]scene.TranscriptCell, settled func(historyCommitSourceKey) bool, retainedQueued func(historyCommitSourceKey) (HistoryCommit, bool, bool, bool)) ([]HistoryCommit, bool, bool) {
	lineRanges := sourceLineRanges(cell.Source)
	if len(lineRanges) == 0 {
		return nil, false, false
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
			if retainedCommits, classified, retained := classifyPlainSegmentRetention(cell, cached, leadingGaps, displayStart, firstVisible, skipRows, generation, retainedQueued); classified {
				return retainedCommits, true, retained
			}
			return assemblePlainHistoryCommits(cell, cached, leadingGaps, displayStart, firstVisible, skipRows, generation, settled), true, false
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
			return nil, false, false
		}
		for offset, text := range wrapped {
			if rows[row+offset].TranscriptGap || rows[row+offset].Text != text || len(rows[row+offset].RenderLine.Spans) != 0 {
				return nil, false, false
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
		return nil, false, false
	}
	sharedHistoryPlan.put(key, physical)
	return assemblePlainHistoryCommits(cell, physical, leadingGaps, displayStart, firstVisible, skipRows, generation, settled), true, false
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
	if state == nil {
		return
	}
	if state.HistoryEffects.DeferHistoryDelivery {
		// 窗口化装载（较早页补齐）进行中：不铸任何提交，也不记录 memo。
		// 场景与视口照常安装/渲染（帧路径不读 ledger），但被装载的 generation
		// 必须等到收尾的授权式替换**一次性按 cell 顺序**铸出，否则最新一页
		// 先写、较早页后写，append-only 的 scrollback 会读成「新在前、旧在后」，
		// 有限缓冲下被挤掉的正是最新消息。
		return
	}
	if transcriptPlanMemoHit(state) {
		// The finalized transcript prefix and every layout input it depends on
		// are unchanged. A2 第二刀后 active 不再铸提交，因此 memo 命中即无工作
		// （旧实现需在此对账 active 候选，正是 ~190% CPU 热路径的来源）。
		return
	}
	// A memo miss means the plan inputs moved. Stage 2 起不存在"欠账"计划：下面的
	// 单遍规划要么完整覆盖，要么没有候选。
	effects := &state.HistoryEffects
	// P16：规划器本体（screening + 铸 commit，含入队）的耗时归因。这是锁内最贵
	// 的一段，必须能回答「P12 的冻结是不是花在规划上」；memo 命中（上方早退）
	// 不算一次规划，因此不记录，避免把空转计成规划。
	planStarted := time.Now()
	planPhases := transcriptPlanPhaseTiming{}
	defer func() { effects.recordTranscriptPlanTiming(time.Since(planStarted), planPhases) }()

	commits, retention, screenPhases := planEligibleHistoryCommitsWithinTimed(state.AppState)
	planPhases = screenPhases
	applyStart := time.Now()
	applyTranscriptPlan(state, commits, retention)
	planPhases.applyMs = time.Since(applyStart).Milliseconds()
}

// applyTranscriptPlan 把一次完整规划（无预算单遍）落到队列状态上：membership
// reconcile（复用段整 cell 跳过）+ memo。Stage 3 起这是唯一实现（无异步结果
// 路径）。
func applyTranscriptPlan(state *UIControllerState, commits []HistoryCommit, retained transcriptPlanRetention) {
	syncHistoryEffectCandidatesRetained(state, commits, retained)
	recordTranscriptPlanMemo(state, len(commits))
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
// frontier barrier — mutable growth is not part of the finalized plan (A2 第二刀
// 后也不再产生 active 提交). Chain keys are folded into the fence so an
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
//
// 按 8 字节字折叠（FNV 乘子链每字一次，尾字节逐字节），覆盖全部字节且
// 确定性不变：BoundaryGroupKey 可能是长键，逐字节折叠实测 90ms/4225 cell，
// 是 memo 检查里最贵的一段。
func transcriptFenceFoldString(h uint64, s string, prime uint64) uint64 {
	for len(s) >= 8 {
		h ^= uint64(s[0]) | uint64(s[1])<<8 | uint64(s[2])<<16 | uint64(s[3])<<24 |
			uint64(s[4])<<32 | uint64(s[5])<<40 | uint64(s[6])<<48 | uint64(s[7])<<56
		h *= prime
		s = s[8:]
	}
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime
	}
	return h
}

// transcriptPlanInputs 是 transcriptPlanMemoHit 与截断续跑游标共享的输入指纹：
// 任何一项变化都同时让 memo 与游标失效。字段与 memo 的比较项一一对应（fence 是
// 逐 cell 的 finalized 指纹，不含 mutable 前沿；themeKey 用 themeFingerprint；
// terminalEpoch 保留为语义代输入——当前没有生产推进点，整体 ledger 替换由
// invalidateTranscriptPlanMemo 显式失效覆盖）。
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
}

func currentTranscriptPlanInputs(state *UIControllerState) transcriptPlanInputs {
	return transcriptPlanInputs{
		sceneID:       state.Transcript.SceneID,
		fence:         transcriptFinalizedPrefixFence(state.Transcript),
		finalized:     transcriptFinalizedCellCount(state.Transcript),
		layoutGen:     state.Geometry.Generation,
		width:         state.Geometry.Width,
		height:        state.Geometry.Height,
		projection:    state.SemanticActiveCellProjection,
		themeKey:      themeFingerprint(state.Theme),
		terminalEpoch: state.HistoryEffects.TerminalEpoch,
	}
}

// transcriptPlanMemoHit reports whether the finalized-prefix plan inputs are
// identical to the ones that produced the last full transcript plan. The plan
// (planEligibleHistoryCommits) is pure over the finalized transcript fence,
// geometry, theme, layout generation, and the projection flag; the mutable cell
// itself is the frontier barrier — never part of the finalized plan (A2 第二刀后
// 也不再产生 active 提交). The transcript fence is the per-cell
// finalized fingerprint (not the scene-wide Revision/ContentVersion counters,
// which the active cell's stream growth advances on every chunk). The fence
// folds chain keys and sequence, so even an unversioned snapshot (SceneID ==
// 0) can be memoized safely: every layout-relevant cell mutation advances a
// covered field (cell Revision on update/finalize, ID/kind/phase/boundary/
// blocked/chain on structural rewiring, source length on stream growth).
func transcriptPlanMemoHit(state *UIControllerState) bool {
	effects := &state.HistoryEffects
	if !effects.lastPlannedTranscriptValid ||
		effects.lastPlannedTranscriptSceneID != state.Transcript.SceneID ||
		effects.lastPlannedTranscriptFence != transcriptFinalizedPrefixFence(state.Transcript) ||
		effects.lastPlannedTranscriptCells != transcriptFinalizedCellCount(state.Transcript) ||
		effects.lastPlannedTranscriptLayoutGen != state.Geometry.Generation ||
		effects.lastPlannedWidth != state.Geometry.Width ||
		effects.lastPlannedHeight != state.Geometry.Height ||
		effects.lastPlannedProjection != state.SemanticActiveCellProjection ||
		effects.lastPlannedThemeKey != themeFingerprint(state.Theme) ||
		effects.lastPlannedTerminalEpoch != effects.TerminalEpoch {
		return false
	}
	// Every input above is unchanged, but the memo only fingerprints plan
	// *inputs*: it cannot see that the ledger the plan was reconciled into no
	// longer holds it. That state is reachable (an external wholesale ledger
	// replacement, or a load re-proof reduced before any plan was minted), and
	// memoizing it strands the loaded generation: the queue reads "planned"
	// while there is nothing to deliver. A plan that produced candidates must
	// still be present in the
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
	effects.lastPlannedTranscriptLayoutGen = state.Geometry.Generation
	effects.lastPlannedWidth = state.Geometry.Width
	effects.lastPlannedHeight = state.Geometry.Height
	effects.lastPlannedProjection = state.SemanticActiveCellProjection
	effects.lastPlannedThemeKey = themeFingerprint(state.Theme)
	effects.lastPlannedTerminalEpoch = effects.TerminalEpoch
	effects.lastPlannedCandidateCount = candidates
}

// transcriptFinalizedCellCount counts the cells the finalized-prefix plan can
// consume. The total cell count is deliberately not used as a memo fence: a busy
// turn keeps appending *mutable* tail cells (new reasoning / tool-chain
// boundaries), and treating any count change as a plan-input change re-planned
// the entire history per appended cell — measured at 723ms/op on a 2000-cell
// transcript, and visible live as plan-last-ms 4.7-6.9s with ~9 plans/min. Only
// the finalized prefix enters planEligibleHistoryCommits; mutable cells are the
// frontier barrier (A2 第二刀后不再单独规划).
func transcriptFinalizedCellCount(transcript TranscriptState) int {
	count := 0
	for _, cell := range transcript.Cells {
		if cell.Phase != scene.CellMutable {
			count++
		}
	}
	return count
}

// syncHistoryEffectCandidates reconciles a planned candidate set with the
// reducer-owned ledger.
func syncHistoryEffectCandidates(state *UIControllerState, candidates []HistoryCommit) {
	syncHistoryEffectCandidatesRetained(state, candidates, transcriptPlanRetention{})
}

// syncHistoryEffectCandidatesRetained 在完整 pass 上消费复用段保留集：
// retained.cells 中的条目候选集与呈现都未变（分拣保证），跳过逐条
// invalidate/rebase 对账；其余条目仍按"候选列表 == 完整有效集合"处理。
func syncHistoryEffectCandidatesRetained(state *UIControllerState, candidates []HistoryCommit, retained transcriptPlanRetention) {
	retainedCell := func(commit HistoryCommit) bool {
		if len(retained.cells) == 0 {
			return false
		}
		_, ok := retained.cells[transcriptPlanCellIdentity{id: commit.CellID, revision: commit.Revision}]
		return ok
	}
	valid := make(map[historyCommitSourceKey]HistoryCommit, len(candidates))
	for _, candidate := range candidates {
		if retainedCell(candidate) {
			continue
		}
		valid[historyCommitSourceIdentity(candidate)] = candidate
	}
	if ledger := state.HistoryEffects.ledger; ledger != nil {
		reconcile := func(entry HistoryCommitEntry) {
			candidate, exists := valid[historyCommitSourceIdentity(entry.Commit)]
			switch entry.State {
			case HistoryCommitQueued:
				if !exists {
					_ = state.HistoryEffects.invalidate(entry.Commit.Token)
					return
				}
				if entry.Commit.Token == state.HistoryEffects.WriteCursor {
					// The executor holds this token as the active write claim: it may
					// already have crossed the writer. A changed display payload must
					// invalidate rather than rebase: rebasing would let old bytes be
					// acknowledged as the new semantic layout.
					if !historyCommitPresentationEqual(entry.Commit, candidate) {
						_ = state.HistoryEffects.invalidate(entry.Commit.Token)
					}
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
			}
		}
		for _, entry := range ledger.byToken {
			if len(retained.cells) > 0 {
				if _, ok := retained.cells[transcriptPlanCellIdentity{id: entry.Commit.CellID, revision: entry.Commit.Revision}]; ok {
					continue
				}
			}
			reconcile(entry)
		}
	}
	enqueueHistoryCandidatesRetained(state, candidates, retained)
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
	enqueueHistoryCandidatesRetained(state, candidates, transcriptPlanRetention{})
}

// enqueueHistoryCandidatesRetained 跳过保留段：其台账记录仍在（候选集完整
// 契约由 mint 的并集保证），无需重复的 terminal 查询与入队。
func enqueueHistoryCandidatesRetained(state *UIControllerState, candidates []HistoryCommit, retained transcriptPlanRetention) {
	for _, candidate := range candidates {
		if len(retained.cells) > 0 {
			if _, ok := retained.cells[transcriptPlanCellIdentity{id: candidate.CellID, revision: candidate.Revision}]; ok {
				continue
			}
		}
		if state.HistoryEffects.hasTerminalRecordForSource(candidate) {
			continue
		}
		if err := state.HistoryEffects.enqueue(candidate); err != nil &&
			!errors.Is(err, ErrDuplicateCommitRange) {
			state.HistoryEffects.ProjectionUnknown = true
		}
	}
}

// historyCommitPresentationEqual compares every non-token field that can
// affect terminal bytes. Token is reducer-owned delivery identity and is
// intentionally omitted so a pending effect can retain its identity while its
// current-layout display payload is safely rebased before any write begins.
//
// DisplayRange is deliberately NOT compared, for active and finalized commits
// alike. It is layout bookkeeping with no delivery consumer (P1-1 Stage 1
// design note): terminal bytes are the source range plus the rendered lines.
// Prepending older pages or inserting cells mid-transcript shifts every later
// commit's display range by a constant while the bytes stay identical;
// comparing DisplayRange would force a full-ledger RebasePending (and, for the
// claimed token, an invalidate) on every such reorder — the O(entire history)
// churn this predicate must not re-create. Range identity no longer carries
// display coordinates either (historyCommitKey), so a display-only shift can
// never collide with a new source that reuses the freed rows.
func historyCommitPresentationEqual(current, candidate HistoryCommit) bool {
	if current.CellID != candidate.CellID ||
		current.Revision != candidate.Revision ||
		current.SourceRange != candidate.SourceRange ||
		current.FragmentID != candidate.FragmentID ||
		current.LayoutGeneration != candidate.LayoutGeneration ||
		!render.LinesEqual(current.Lines, candidate.Lines) {
		return false
	}
	return true
}

func rebasePendingHistoryEffects(state *UIControllerState) {
	if state == nil {
		return
	}
	candidates := planEligibleHistoryCommits(state.AppState)
	valid := make(map[historyCommitSourceKey]HistoryCommit, len(candidates))
	for _, candidate := range candidates {
		valid[historyCommitSourceIdentity(candidate)] = candidate
	}
	if ledger := state.HistoryEffects.ledger; ledger != nil {
		for _, entry := range ledger.byToken {
			if entry.State != HistoryCommitQueued {
				continue
			}
			candidate, exists := valid[historyCommitSourceIdentity(entry.Commit)]
			if !exists {
				_ = state.HistoryEffects.invalidate(entry.Commit.Token)
				continue
			}
			// G4/C1：claimed 且 presentation 漂移 → 不 rebase（写入在途）、不
			// invalidate（避免把写中 resize 升级为 ProjectionUnknown）；计数并
			// 交由执行器 generation 闸门 Deferred 释放后收敛。
			if entry.Commit.Token == state.HistoryEffects.WriteCursor {
				state.HistoryEffects.noteClaimedPresentationDrift(candidate)
			}
		}
	}
	for _, candidate := range candidates {
		if err := state.HistoryEffects.rebasePending(candidate); err != nil &&
			!errors.Is(err, ErrCommitNotPending) {
			state.HistoryEffects.ProjectionUnknown = true
		}
	}
}

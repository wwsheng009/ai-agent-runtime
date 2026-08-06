// ChangeSet → SceneTransaction 映射器（unified plan §6.1 上游衔接）。
//
// 本文件实现路线图 P3 的 ChangeSet→SceneTransaction 映射：消费
// encoding.ChangeSet（由 EventEncoder.Encode 产出），把每个 ItemChange
// 映射为 CellMutation，打包为一次 SceneTransaction 提交。Scene 层不负责
// 顺序推断与身份分配：
//
//   - 顺序 = ChangeSet.Changes 的数组顺序（编码器已按模型数组顺序产出，
//     对齐 Codex Thread 模型：渲染顺序 = 数据结构数组位置）；
//   - 身份 = Item.ID（"item-{n}"）解析为稳定 CellID，见 CellIDFromItemID。
//
// 映射规则（对应 render-model-spec §5 / 路线图 P3）：
//
//	| ItemChange.Op | Item.Kind | 映射为 |
//	| --- | --- | --- |
//	| append | user / assistant / reasoning / command / system / user_interaction | AppendCell（top-level，ChainKey=""）|
//	| append | tool_call | AppendCell（KindToolChain，ChainKey=Item.ID，链首）|
//	| append | tool_output（CauseID 非空且链首存在）| UpdateCell 合并进链首（§7.3：tool events 在 cell 内）|
//	| append | tool_output（无 CauseID 或链首缺失）| AppendCell（KindToolChain 独立块，记 OrphanOutputs）|
//	| upsert | 任意（cell 存在，状态非终态）| UpdateCell |
//	| upsert | 任意（cell 存在，状态终态）| FinalizeCell（INV-SCENE-04：同一 cell 迁移，不新增）|
//	| upsert | tool_output（链首存在）| UpdateCell 合并进链首（链首终态由 tool_call 状态决定）|
//	| remove | 任意（mutable cell）| RemoveMutableCell |
//
// Revision 规则：cell 的 Revision 由本映射器统一递增（当前 Revision + 1），
// 不直接透传 ItemChange.Revision —— tool_output 合并与 tool_call 自身更新
// 共享同一 cell，两个独立递增序列不能直接拼接（INV-SCENE-03 要求单调）。
// 重放时相同事件序列产生相同 Revision 序列，保证 replay 一致性。
//
// 已知约束：OpRemove 只映射 mutable cell；终态 cell 的移除属于会话级
// backtrack，由 P5 重放/会话重建路径处理，本映射器显式失败（INV-FRAME-01
// 整体回滚），不做静默降级。
package scene

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/encoding"
)

// CellIDFromItemID 把编码器分配的 Item.ID（"item-{n}"，见 render-model-spec
// §4.1）解析为稳定 CellID。这是身份映射的唯一规则：本层不分配身份
// （unified plan §6.1），CellID 直接来自上游 Item.ID，因此重放时无论
// Scene 是否重建，同一 Item 始终映射到同一 CellID（INV-SCENE-02）。
// 格式不符返回错误（上游契约破坏，显式暴露而非兜底分配）。
func CellIDFromItemID(id string) (CellID, error) {
	rest, ok := strings.CutPrefix(id, "item-")
	if !ok {
		return 0, fmt.Errorf("changeset: invalid item id %q (want item-{n})", id)
	}
	n, err := strconv.ParseUint(rest, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("changeset: invalid item id %q: %v", id, err)
	}
	return CellID(n), nil
}

// ChangeSetMapper 把 encoding.ChangeSet 映射为 SceneTransaction。
//
// 映射器是有状态的（持有权威 Scene 用于查询当前 Revision/链首），但
// Map 必须在调用方串行消费 ChangeSet 的上下文中使用（SceneController
// 串行提交，unified plan §4.1）；Apply 一步完成 Map + Submit，避免
// 映射与提交之间 Scene 被其他事务推进。
type ChangeSetMapper struct {
	scene *TuiScene

	// chainHeads 记录链首 cell 的 tool_call 自身文本。按编码器事实
	// （applyToolStarted 后 Head 恒定，applyToolFinished 只改 Status），
	// tool_call 文本一般不变；本表用于 tool_call Head 演进时把链首
	// Source（callHead + "\n" + 合并输出）拆回两部分，保留已合并输出。
	chainHeads map[CellID]string

	// OrphanOutputs 统计：带 CauseID 但链首 cell 缺失而独立成块的 tool
	// 输出数（编码器乱序免疫的延续：无父时独立块）。
	OrphanOutputs uint64
	// FallbackCount 统计：未知 ItemKind 降级为诊断 cell 的次数。
	FallbackCount uint64
}

// NewChangeSetMapper 创建绑定给定 Scene 的映射器。
func NewChangeSetMapper(s *TuiScene) *ChangeSetMapper {
	return &ChangeSetMapper{scene: s, chainHeads: make(map[CellID]string)}
}

// pendingCell 是 Map 期间对单个 cell 的影子状态：同一 ChangeSet 内多个
// change 可能作用于同一 cell（如 tool_call append + tool_output 合并 +
// tool_call upsert 终态），Revision 计算必须基于"本事务内已应用的
// 最新值"，而不是 Scene 的提交前值。
type pendingCell struct {
	kind                 CellKind
	chainKey             string
	revision             uint64
	phase                CellPhase
	source               string
	presentation         TranscriptPresentation
	historyCommitBlocked bool
	// finalizeAtEnd 标记 tool_call 本批次到达终态但尚未产出 FinalizeCell：
	// 编码器 applyToolFinished 一次产出 [upsert(tool_call 终态),
	// append(tool_output)]，若立即 finalize，随后 tool_output 合并进链首
	// 会对 committed cell 执行 update 而失败。延迟到批次结束统一 finalize，
	// 保证合并（UpdateCell）先于 FinalizeCell 提交（INV-SCENE-04 不变量
	// 不变：同一 cell 仅一次 finalize，且发生在所有本批内容落定之后）。
	finalizeAtEnd bool
}

// Map 把 ChangeSet 映射为一次原子事务（不提交）。
//
// 结果基于调用时刻的 Scene 状态；调用方应立即 Submit（或使用 Apply）。
// 空 ChangeSet 返回空事务（无 mutation），Apply 会跳过提交。
func (m *ChangeSetMapper) Map(cs *encoding.ChangeSet) (SceneTransaction, error) {
	var tx SceneTransaction
	if cs == nil || len(cs.Changes) == 0 {
		tx.Cause = "changeset: 0 changes"
		tx.Flush = FlushImmediate
		return tx, nil
	}
	pending := make(map[CellID]pendingCell)
	allUpdates := true
	for _, ch := range cs.Changes {
		if ch.Item == nil {
			return SceneTransaction{}, fmt.Errorf("changeset: nil item in change %d", ch.Revision)
		}
		mu, updated, err := m.mapChange(ch, pending)
		if err != nil {
			return SceneTransaction{}, err
		}
		if mu != nil {
			tx.Mutations = append(tx.Mutations, mu)
		}
		if !updated {
			allUpdates = false
		}
	}
	// 批次收尾：对标记 finalizeAtEnd 的 cell 统一产出 FinalizeCell。
	// mutation 顺序因此是 [更新/合并…, FinalizeCell]，全部合法。
	for id, p := range pending {
		if !p.finalizeAtEnd {
			continue
		}
		// Revision 取 p.revision+1：同批合并（UpdateCell）可能已把 cell
		// revision 推进到 p.revision，finalize 必须严格大于当前
		// （INV-SCENE-03；UpdateCell 之后执行 finalize，见 INV-SCENE-04）。
		tx.Mutations = append(tx.Mutations, &FinalizeCell{
			ID: id, Revision: p.revision + 1, Source: p.source,
			Presentation: p.presentation, HistoryCommitBlocked: false,
		})
		p.phase = CellCommitted
		p.finalizeAtEnd = false
		pending[id] = p
	}
	tx.Cause = fmt.Sprintf("changeset: %d change(s)", len(cs.Changes))
	// 纯 mutable update 可延迟 flush（transaction.go FlushCoalescable）；
	// 任何结构变化（append/finalize/remove）都必须立即 flush。
	if allUpdates && len(tx.Mutations) > 0 {
		tx.Flush = FlushCoalescable
	} else {
		tx.Flush = FlushImmediate
	}
	return tx, nil
}

// Apply 映射并一次性提交（Map + SceneController.Submit）。
// 空 ChangeSet 不提交，返回当前 Scene revision。
// 任一 mutation 失败整体回滚（INV-FRAME-01），Scene 不变。
func (m *ChangeSetMapper) Apply(cs *encoding.ChangeSet) (SceneTransaction, uint64, error) {
	tx, err := m.Map(cs)
	if err != nil {
		return SceneTransaction{}, 0, err
	}
	if len(tx.Mutations) == 0 {
		if m.scene == nil {
			return tx, 0, fmt.Errorf("changeset: nil scene")
		}
		return tx, m.scene.Revision(), nil
	}
	rev, _, err := NewController(m.scene).Submit(tx)
	if err != nil {
		return SceneTransaction{}, 0, err
	}
	return tx, rev, nil
}

// mapChange 映射单个 ItemChange，返回 mutation 与"是否纯更新"。
func (m *ChangeSetMapper) mapChange(ch encoding.ItemChange, pending map[CellID]pendingCell) (CellMutation, bool, error) {
	it := ch.Item
	id, err := CellIDFromItemID(it.ID)
	if err != nil {
		return nil, false, err
	}

	switch ch.Op {
	case encoding.OpAppend:
		return m.mapAppend(id, it, ch.AfterID, ch.BeforeID, pending)

	case encoding.OpUpsert:
		return m.mapUpsert(id, it, pending)

	case encoding.OpRemove:
		cur, ok := m.current(id, pending)
		if !ok {
			return nil, false, fmt.Errorf("changeset: remove of unknown item %q (cell %d)", it.ID, id)
		}
		if cur.phase != CellMutable {
			return nil, false, fmt.Errorf(
				"changeset: remove of committed cell %d (item %q) is a session-level backtrack; not supported by mutable-only Scene remove", id, it.ID)
		}
		mu := &RemoveMutableCell{ID: id, Revision: cur.revision + 1}
		pending[id] = pendingCell{kind: cur.kind, chainKey: cur.chainKey, revision: mu.Revision, phase: CellMutable, source: cur.source}
		return mu, false, nil

	default:
		return nil, false, fmt.Errorf("changeset: unknown op %v for item %q", ch.Op, it.ID)
	}
}

// mapAppend 处理 OpAppend；afterID 非空时为 Tail 锚定插入（编码器
// SubmitUserInteraction 的锚点，见 ItemChange.AfterID）。
func (m *ChangeSetMapper) mapAppend(id CellID, it *encoding.Item, afterID, beforeID string, pending map[CellID]pendingCell) (CellMutation, bool, error) {
	// tool 输出归组：有 CauseID 且链首 cell 存在时合并进链首
	// （§7.3：tool events 在 cell 内，不创建新边界）。
	if it.Kind == encoding.KindToolOutput && it.CauseID != "" {
		if target, ok := m.chainTarget(it.CauseID, pending); ok {
			return m.mergeOutput(target, id, it, pending), true, nil
		}
		m.OrphanOutputs++ // 有 CauseID 但链首缺失：按编码器"无父时独立块"降级
	}

	kind := m.cellKind(it)
	cell := TranscriptCell{
		ID:                   id,
		Kind:                 kind,
		Source:               it.Head,
		Presentation:         presentationFromEncoding(it.Presentation),
		HistoryCommitBlocked: it.HistoryCommitBlocked,
		Revision:             1, // 编码器 append 修订号为 1（render-model-spec §4.2）
		Provenance:           "changeset:" + string(it.Kind),
	}
	if it.Kind == encoding.KindToolCall {
		// tool_call 是链首：以 Item.ID 为归组键。
		// 后续 tool_output 经 CauseID 解析回同一 CellID 合并（稠密，无新边界）。
		cell.ChainKey = it.ID
		m.chainHeads[id] = it.Head
	} else {
		// 独立 tool 输出（无 CauseID / 孤儿降级）：独立块，无链身份。
		cell.ChainKey = ""
	}
	if it.Status.Terminal() || !streamedKind(it.Kind) {
		// append 时通常非终态（编码器总是以 pending 开始）；宽容处理
		// 直接以 committed 落盘。一次性 kind（user/system/command 等）
		// 无后续 upsert，append 即终态。
		cell.Phase = CellCommitted
	}
	var mu CellMutation = &AppendCell{Cell: cell}
	if beforeID != "" {
		anchorCell, err := CellIDFromItemID(beforeID)
		if err != nil {
			return nil, false, fmt.Errorf("changeset: invalid before anchor item id %q: %v", beforeID, err)
		}
		if _, ok := m.scene.Cell(anchorCell); !ok {
			return nil, false, fmt.Errorf("changeset: before anchor cell %d (item %q) not found", anchorCell, beforeID)
		}
		mu = &InsertCellBefore{Before: anchorCell, Cell: cell}
	}
	if it.Kind != encoding.KindToolOutput && afterID != "" {
		// Tail 锚定插入（KindUserInteraction，设计文档 §1.3 行 12）：在
		// 锚点 cell 之后插入而非追加到末尾。锚点目标必须已提交（历史
		// 块）；缺失是编码器退化遗漏，显式失败（INV-FRAME-01 回滚）。
		anchorCell, err := CellIDFromItemID(afterID)
		if err != nil {
			return nil, false, fmt.Errorf("changeset: invalid anchor item id %q: %v", afterID, err)
		}
		if _, ok := m.scene.Cell(anchorCell); !ok {
			return nil, false, fmt.Errorf("changeset: anchor cell %d (item %q) not found", anchorCell, afterID)
		}
		mu = &InsertCell{After: anchorCell, Cell: cell}
	}
	pending[id] = pendingCell{
		kind: kind, chainKey: cell.ChainKey, revision: 1, phase: cell.Phase,
		source: it.Head, presentation: cell.Presentation,
		historyCommitBlocked: cell.HistoryCommitBlocked,
	}
	return mu, false, nil
}

// mapUpsert 处理 OpUpsert。
func (m *ChangeSetMapper) mapUpsert(id CellID, it *encoding.Item, pending map[CellID]pendingCell) (CellMutation, bool, error) {
	// tool 输出合并：状态由 tool_call 决定，链首终态不由 tool_output 触发。
	if it.Kind == encoding.KindToolOutput && it.CauseID != "" {
		if target, ok := m.chainTarget(it.CauseID, pending); ok {
			return m.mergeOutput(target, id, it, pending), true, nil
		}
		// 编码器保证带 CauseID 的 tool_output upsert 时父块存在；缺失说明
		// 映射器与编码器状态不同步，显式失败（原子回滚）。
		return nil, false, fmt.Errorf(
			"changeset: upsert of tool output %q references missing chain %q", it.ID, it.CauseID)
	}

	cur, ok := m.current(id, pending)
	if !ok {
		return nil, false, fmt.Errorf(
			"changeset: upsert of unknown item %q (cell %d); encoder emits append when the item is missing", it.ID, id)
	}

	next := cur.revision + 1
	// upsert 的 Item 是编码器产出的完整最新快照（render-model-spec §4.2：
	// 同一 Item 多次更新已去重合并为最新 Head），非 tool_call 直接采用。
	newSource := it.Head
	if it.Kind == encoding.KindToolCall {
		// tool_call 自身文本：Head 恒定（编码器事实）时保留 cur.source
		// （含已合并输出）；演进时（防御路径）拆分旧 call 部分、保留
		// 已合并输出块后替换为新 Head。
		prev, seen := m.chainHeads[id]
		switch {
		case !seen:
			m.chainHeads[id] = it.Head
			newSource = it.Head
		case prev != it.Head:
			tail := splitChainTail(cur.source, prev)
			newSource = it.Head
			if tail != "" {
				newSource += "\n" + tail
			}
			m.chainHeads[id] = it.Head
		default:
			newSource = cur.source
		}
	}
	if it.Status.Terminal() {
		if cur.phase != CellMutable {
			return nil, false, fmt.Errorf(
				"changeset: finalize of already-committed cell %d (item %q) (INV-SCENE-04)", id, it.ID)
		}
		if cur.finalizeAtEnd {
			// 同一批次内重复终态：编码器幂等跳过不会到达，防御性失败。
			return nil, false, fmt.Errorf(
				"changeset: duplicate terminal upsert for cell %d (item %q)", id, it.ID)
		}
		// 延迟 finalize：tool_call 终态后同批可能还有 tool_output 合并进
		// 链首（applyToolFinished 产出 [upsert, append]）。pending 保持
		// mutable 并打标记，批次结束统一产出 FinalizeCell（见 Map）。
		pending[id] = pendingCell{
			kind: cur.kind, chainKey: cur.chainKey, revision: next,
			phase: CellMutable, source: newSource,
			presentation: presentationFromEncoding(it.Presentation),
			historyCommitBlocked: false, finalizeAtEnd: true,
		}
		return nil, false, nil
	}

	presentation := presentationFromEncoding(it.Presentation)
	mu := &UpdateCell{
		ID: id, Revision: next, Source: newSource, Presentation: presentation,
		HistoryCommitBlocked: it.HistoryCommitBlocked,
	}
	pending[id] = pendingCell{
		kind: cur.kind, chainKey: cur.chainKey, revision: next,
		phase: CellMutable, source: newSource, presentation: presentation,
		historyCommitBlocked: it.HistoryCommitBlocked, finalizeAtEnd: cur.finalizeAtEnd,
	}
	return mu, true, nil
}

// mergeOutput 把 tool 输出合并进链首 cell（update 不产生新边界）。
func (m *ChangeSetMapper) mergeOutput(target *TranscriptCell, outID CellID, it *encoding.Item, pending map[CellID]pendingCell) CellMutation {
	source := target.Source // 追加式：callHead + 已合并输出 + 新输出
	if it.Head != "" {
		if source != "" {
			source += "\n"
		}
		source += it.Head
	}
	rev := target.Revision + 1
	presentation := target.Presentation
	if mapped := presentationFromEncoding(it.Presentation); mapped.Kind != PresentationPlain {
		presentation = mapped
	}
	mu := &UpdateCell{
		ID: target.ID, Revision: rev, Source: source, Presentation: presentation,
		HistoryCommitBlocked: target.HistoryCommitBlocked,
	}
	finalizeAtEnd := false
	if p, ok := pending[target.ID]; ok {
		finalizeAtEnd = p.finalizeAtEnd // 保留终态标记（合并发生在 finalize 之前）
	}
	pending[target.ID] = pendingCell{
		kind: target.Kind, chainKey: target.ChainKey, revision: rev,
		phase: target.Phase, source: source, presentation: presentation,
		historyCommitBlocked: target.HistoryCommitBlocked, finalizeAtEnd: finalizeAtEnd,
	}
	// 归组键（outID）不创建 cell；留占位以防同批后续 upsert 命中
	// （正常不会发生，编码器按 (ID, Revision) 去重合并）。
	_ = outID
	return mu
}

// splitChainTail 把链首 Source（"callHead\n<合并输出>"）拆出输出部分。
// callHead 未知/不匹配时保守返回整个 Source（不丢可见内容）。
func splitChainTail(source, callHead string) string {
	if callHead == "" {
		return source
	}
	if rest, ok := strings.CutPrefix(source, callHead); ok {
		return strings.TrimPrefix(rest, "\n")
	}
	return source
}

// chainTarget 解析 CauseID 并返回链首 cell（优先同批影子状态）。
func (m *ChangeSetMapper) chainTarget(causeID string, pending map[CellID]pendingCell) (*TranscriptCell, bool) {
	cid, err := CellIDFromItemID(causeID)
	if err != nil {
		return nil, false
	}
	if p, ok := pending[cid]; ok {
		if p.kind != KindToolChain || p.chainKey != causeID {
			return nil, false
		}
		return &TranscriptCell{
			ID: cid, Kind: p.kind, ChainKey: p.chainKey, Revision: p.revision,
			Phase: p.phase, Source: p.source, Presentation: p.presentation,
			HistoryCommitBlocked: p.historyCommitBlocked,
		}, true
	}
	cell, ok := m.scene.Cell(cid)
	if !ok || cell.Kind != KindToolChain || cell.ChainKey != causeID {
		return nil, false
	}
	return cell, true
}

// current 返回 cell 当前状态（优先同批影子状态）。
func (m *ChangeSetMapper) current(id CellID, pending map[CellID]pendingCell) (pendingCell, bool) {
	if p, ok := pending[id]; ok {
		return p, true
	}
	cell, ok := m.scene.Cell(id)
	if !ok {
		return pendingCell{}, false
	}
	return pendingCell{
		kind: cell.Kind, chainKey: cell.ChainKey, revision: cell.Revision,
		phase: cell.Phase, source: cell.Source, presentation: cell.Presentation,
		historyCommitBlocked: cell.HistoryCommitBlocked,
	}, true
}

func presentationFromEncoding(source encoding.Presentation) TranscriptPresentation {
	presentation := TranscriptPresentation{Document: source.Document.Clone()}
	switch source.Kind {
	case encoding.PresentationAssistantMarkdown:
		presentation.Kind = PresentationAssistantMarkdown
	case encoding.PresentationDiffSupplement:
		presentation.Kind = PresentationDiffSupplement
	case encoding.PresentationDocument:
		presentation.Kind = PresentationDocument
	default:
		presentation.Kind = PresentationPlain
		presentation.Document = render.Document{}
	}
	return presentation
}

// cellKind 把 ItemKind 映射为 Scene CellKind（render-model-spec §5 表格）。
//
//	KindUser → KindUser；KindAssistant → KindAssistant；KindReasoning → KindSupplement
//	KindToolCall/KindToolOutput → KindToolChain；KindCommand → KindCommand
//	KindSystem → KindSystem；KindPriorityPrompt → KindSupplement；KindUserInteraction → KindCommand
//	（/debug、/model 输出按 command cell 呈现，与既有 /debug display 实现一致）
//	未知 → KindDiagnostic（降级，内容保持可见，记 FallbackCount）
func (m *ChangeSetMapper) cellKind(it *encoding.Item) CellKind {
	switch it.Kind {
	case encoding.KindUser:
		return KindUser
	case encoding.KindAssistant:
		return KindAssistant
	case encoding.KindReasoning:
		return KindSupplement
	case encoding.KindSupplement:
		return KindSupplement
	case encoding.KindToolCall, encoding.KindToolOutput:
		return KindToolChain
	case encoding.KindCommand:
		return KindCommand
	case encoding.KindSystem:
		return KindSystem
	case encoding.KindPriorityPrompt:
		return KindSupplement
	case encoding.KindUserInteraction:
		return KindCommand
	default:
		m.FallbackCount++
		return KindDiagnostic
	}
}

// streamedKind 判断该 ItemKind 是否会被后续 upsert 流式更新（append 时
// 保持 mutable）；一次性 kind（user/system/command/user_interaction/未知
// 诊断）append 即终态，直接以 committed 落盘——编码器对它们不产后续
// upsert，保持 mutable 会滞留可更新状态（与编码器事实不符）。
func streamedKind(k encoding.ItemKind) bool {
	switch k {
	case encoding.KindAssistant,
		encoding.KindReasoning,
		encoding.KindToolCall,
		encoding.KindToolOutput:
		return true
	default:
		return false
	}
}

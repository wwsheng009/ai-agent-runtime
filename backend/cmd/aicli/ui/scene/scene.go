// Package scene 实现统一 Scene 数据层（unified plan P4–P9 Scene 终局）。
//
// 设计文档：
//   - 上位方案：docs/plan/aicli-tui-unified-render-architecture-refactor-plan.md
//     （§3 不变量、§5 Scene 数据模型、§6 RenderEvent/SceneTransaction、
//     §7 block boundary 与 gap policy）
//   - 上游衔接：渲染模型编码器（encoding.RenderModel）分配 Item.ID/Seq；
//     本层 SceneTransaction 由 ChangeSet 直接映射提交，不负责顺序推断与
//     身份分配（unified plan §6.1 上游衔接注）。
//   - gap 决策：相邻 top-level cell 的语义 gap 由 boundary.ResolveGap 纯函数
//     生成（INV-GAP-03），本层 Layout 负责派生 boundary row。
//
// 本包只做 Scene 数据模型与事务：无终端 I/O、无 ANSI、无渲染。
// 物理屏幕由 presenter 层消费 Scene 快照。
package scene

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/boundary"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
)

var sceneIDCounter atomic.Uint64

// CellID 是 transcript cell 的稳定身份（unified plan §5.2）。
// 创建时分配，整个 live -> final -> replay/persist 映射期间不变（INV-SCENE-02）。
type CellID uint64

// CellKind 区分 transcript cell 的语义类型（unified plan §5.2 Kind 至少区分）。
type CellKind uint8

const (
	KindUser         CellKind = iota // 用户消息
	KindAssistant                    // assistant 消息
	KindToolChain                    // tool chain（含并行工具归组）
	KindRuntimeEvent                 // runtime event（async 诊断/事件）
	KindSupplement                   // supplement/reasoning（属 assistant cell）
	KindSystem                       // system/notice/warning
	KindCommand                      // command result
	KindDiagnostic                   // 显式允许进入交互 transcript 的诊断
)

func (k CellKind) String() string {
	switch k {
	case KindUser:
		return "user"
	case KindAssistant:
		return "assistant"
	case KindToolChain:
		return "tool_chain"
	case KindRuntimeEvent:
		return "runtime_event"
	case KindSupplement:
		return "supplement"
	case KindSystem:
		return "system"
	case KindCommand:
		return "command"
	case KindDiagnostic:
		return "diagnostic"
	default:
		return fmt.Sprintf("cell_kind(%d)", uint8(k))
	}
}

// CellPhase 是 cell 生命周期阶段（unified plan §5.2 / §8.1）。
type CellPhase uint8

const (
	CellMutable            CellPhase = iota // 可更新（streaming/tool running）
	CellCommitted                           // 已提交（finalize 后）
	CellPartiallyHandedOff                  // 超大 cell 分片交接中
	CellHandedOff                           // 已交 native scrollback
)

func (p CellPhase) String() string {
	switch p {
	case CellMutable:
		return "mutable"
	case CellCommitted:
		return "committed"
	case CellPartiallyHandedOff:
		return "partially_handed_off"
	case CellHandedOff:
		return "handed_off"
	default:
		return fmt.Sprintf("cell_phase(%d)", uint8(p))
	}
}

// PresentationKind selects the pure projection used by layout. Source remains
// the canonical text/replay value; rich presentations never store ANSI bytes.
type PresentationKind uint8

const (
	PresentationPlain PresentationKind = iota
	PresentationAssistantMarkdown
	PresentationDiffSupplement
	PresentationDocument
)

type TranscriptPresentation struct {
	Kind     PresentationKind
	Document render.Document
}

func (p TranscriptPresentation) Clone() TranscriptPresentation {
	p.Document = p.Document.Clone()
	return p
}

// TranscriptCell 是 Scene 的最小语义单元（unified plan §5.2）。
//
// Source 保存语义内容（当前为 string 最小投影；RichDocument 落地后替换），
// 不是终端换行后的字符串数组。DisplayLines(width, theme) 是派生函数，
// 不在本包。
//
// ChainKey 是 tool-chain 归组键（本包的扩展字段，供 Layout 的 gap 决策
// 投影 boundary.CellMeta 使用）：非空表示该 cell 是某 tool-chain 的内部
// 成员，不推进 Sequence、与同链成员稠密衔接（§7.3 规则表）。
// BoundaryGroupKey 仅控制相邻顶层 section 的 gap；它不影响 tool ownership
// 或 Sequence。reasoning 与同一精确 LLM request 的 assistant 共享该键。
type TranscriptCell struct {
	ID           CellID
	Sequence     uint64 // 只在创建 top-level cell 时增加（§5.3）
	Kind         CellKind
	Source       string
	Presentation TranscriptPresentation
	// HistoryCommitBlocked is projection metadata for a mutable assistant. It
	// never creates a visible row; the history planner treats it as an ordering
	// fence until a possible reasoning predecessor has been resolved.
	HistoryCommitBlocked bool
	Revision             uint64
	Phase                CellPhase
	Boundary             boundary.BoundaryClass
	Provenance           string
	ChainKey             string // tool-chain 归组键；"" = top-level 独立 cell
	BoundaryGroupKey     string // 同一语义请求的边界归组键；"" = 独立边界
	CreatedAt            time.Time
	FinalizedAt          *time.Time
}

// BoundaryMeta 把 cell 投影为 boundary 决策所需的元数据视图。
func (c *TranscriptCell) BoundaryMeta() boundary.CellMeta {
	if c == nil {
		return boundary.CellMeta{}
	}
	kind := boundary.KindSystem
	switch c.Kind {
	case KindUser:
		kind = boundary.KindUser
	case KindAssistant, KindSupplement:
		kind = boundary.KindAssistant
	case KindToolChain:
		kind = boundary.KindTool
	case KindCommand:
		kind = boundary.KindCommand
	}
	cls := c.Boundary
	if cls == boundary.BoundaryDense && c.ChainKey == "" && c.Kind != KindToolChain {
		cls = boundary.BoundaryNormal // 非 tool-chain 的 cell 不得声明稠密
	}
	return boundary.CellMeta{
		ID:       fmt.Sprintf("%d", c.ID),
		Kind:     kind,
		TopLevel: c.ChainKey == "",
		ChainKey: c.ChainKey,
		GroupKey: c.BoundaryGroupKey,
		Boundary: cls,
		Mutable:  c.Phase == CellMutable,
	}
}

// TuiScene 是交互 UI 的唯一权威状态（INV-SCENE-01）。
//
// 本实现只包含 transcript 语义 cell 层；ActiveBand/status/prompt/popup
// 是 overlay，不参与 transcript sequence 与 boundary state（INV-SCENE-05）。
// 所有变更经 ApplyCellMutation / 事务提交发生，外部不得直接改 Cells。
type TuiScene struct {
	id             uint64
	cells          []*TranscriptCell
	nextID         uint64
	nextSeq        uint64
	revision       uint64 // Scene revision：每次事务提交 +1
	contentVersion uint64 // 每次成功 cell mutation +1，仅用于快照变更检测
	lastSnapshot   *Snapshot
	byID           map[CellID]*TranscriptCell
	byChain        map[string]*TranscriptCell // ChainKey -> 链首 cell（仅供校验）
}

// New 创建空 Scene。
func New() *TuiScene {
	return &TuiScene{
		id:      sceneIDCounter.Add(1),
		byID:    make(map[CellID]*TranscriptCell),
		byChain: make(map[string]*TranscriptCell),
	}
}

func cloneCell(cell TranscriptCell) TranscriptCell {
	if cell.FinalizedAt != nil {
		finalizedAt := *cell.FinalizedAt
		cell.FinalizedAt = &finalizedAt
	}
	cell.Presentation = cell.Presentation.Clone()
	return cell
}

// Cells 返回有序 cell 引用切片（渲染顺序 = 数组顺序，对齐 Codex Thread 模型）。
//
// scene 采用 copy-on-write（update/finalize 替换 cell 对象而非原地修改），
// 因此返回的 cell 对象对已发布快照是不可变的：调用方必须只读消费，
// 任何修改必须经 ApplyCellMutation 走事务（INV-SCENE-01）。
func (s *TuiScene) Cells() []*TranscriptCell {
	if s == nil {
		return nil
	}
	out := make([]*TranscriptCell, 0, len(s.cells))
	for _, c := range s.cells {
		if c == nil {
			continue
		}
		out = append(out, c)
	}
	return out
}

// Cell 按 ID 查询。COW 语义下返回只读引用（调用方不得修改）。
func (s *TuiScene) Cell(id CellID) (*TranscriptCell, bool) {
	if s == nil {
		return nil, false
	}
	c, ok := s.byID[id]
	if !ok || c == nil {
		return nil, false
	}
	return c, true
}

// Len 返回 cell 总数。
func (s *TuiScene) Len() int {
	if s == nil {
		return 0
	}
	return len(s.cells)
}

// Revision 返回当前 Scene revision（每次事务提交 +1）。
func (s *TuiScene) Revision() uint64 {
	if s == nil {
		return 0
	}
	return s.revision
}

// NextID 分配单调 CellID（编码器不可用时的兜底；正常路径 ID 来自上游 Item.ID）。
func (s *TuiScene) NextID() CellID {
	if s == nil {
		return 0
	}
	s.nextID++
	return CellID(s.nextID)
}

// Snapshot 返回不可变快照（presenter/layout 只读消费）。
//
// COW 不变量：快照引用的 cell 对象此后不被任何 mutation 修改
// （update/finalize 替换指针，remove 仅删除引用），因此快照可在
// 释放 scene 锁后安全消费，也保证了事务回滚（SceneController.restore）
// 看到的是事务前的 cell 状态。
func (s *TuiScene) Snapshot() *Snapshot {
	if s == nil {
		return &Snapshot{}
	}
	if s.lastSnapshot != nil && s.lastSnapshot.Revision == s.revision &&
		s.lastSnapshot.ContentVersion == s.contentVersion {
		return s.lastSnapshot
	}
	s.lastSnapshot = &Snapshot{
		SceneID:        s.id,
		Revision:       s.revision,
		ContentVersion: s.contentVersion,
		Cells:          s.Cells(),
	}
	return s.lastSnapshot
}

// Snapshot 是 Scene 的不可变视图。
type Snapshot struct {
	// SceneID is an opaque process-local source identity. It distinguishes a
	// rebuilt/replayed Scene from an older Scene that happens to have the same
	// transaction revision.
	SceneID uint64
	// Revision is the public transaction revision used by replay diagnostics.
	Revision uint64
	// ContentVersion advances for every successful cell mutation, including
	// direct single-mutation tests that intentionally do not advance Revision.
	// SceneID + Revision + ContentVersion is therefore an exact no-op fence.
	ContentVersion uint64
	Cells          []*TranscriptCell
}

// AppendCell 追加一个新 cell（unified plan §6.1）。
//
//   - ID 为空时由 Scene 分配（NextID）；非空时校验全局唯一（INV-SCENE-02）；
//   - top-level cell（ChainKey == ""）分配单调 Sequence；
//     tool-chain 内部成员不推进 Sequence（§5.3）；
//   - 若 Kind 为 tool-chain 且 ChainKey 非空，登记为链首。
type AppendCell struct {
	Cell TranscriptCell
}

// InsertCell 在指定 cell 之后插入一个新 cell（Tail 锚定插入，编码器
// ItemChange.AfterID 语义，设计文档 §1.3 行 12：/debug、/model 交互输出
// 以触发时刻模型尾部为锚点参与渲染总序）。
//
//   - After 必须存在（锚点目标）；不存在时显式失败（INV-FRAME-01 回滚），
//     锚点缺失的退化由编码器完成（SubmitUserInteraction 退化为 append）；
//   - 其余规则与 AppendCell 一致：ID 唯一（INV-SCENE-02）、top-level 分配
//     单调 Sequence、ChainKey 登记。
type InsertCell struct {
	After CellID // 锚点 cell（插入到其后）
	Cell  TranscriptCell
}

type InsertCellBefore struct {
	Before CellID
	Cell   TranscriptCell
}

// UpdateCell 按 ID 更新既有 mutable cell（unified plan §6.1）。
// Revision 必须大于当前（INV-SCENE-03），旧 revision 不得覆盖新 revision。
type UpdateCell struct {
	ID                   CellID
	Revision             uint64
	Source               string
	Presentation         TranscriptPresentation
	HistoryCommitBlocked bool
	BoundaryGroupKey     string
}

// FinalizeCell 把 mutable cell 转为 committed（unified plan §6.1）。
// finalize 是同一 cell 的状态迁移，不是 append 新 cell（INV-SCENE-04）。
type FinalizeCell struct {
	ID                   CellID
	Revision             uint64
	Source               string
	Presentation         TranscriptPresentation
	HistoryCommitBlocked bool
	BoundaryGroupKey     string
}

// RemoveMutableCell 按 ID 移除 mutable cell（unified plan §6.1）。
type RemoveMutableCell struct {
	ID       CellID
	Revision uint64
}

// ApplyCellMutation 在 Scene 上应用单个 cell 变更，返回受影响 cell 的副本。
// 失败时 Scene 不变（调用方应把失败视为事务回滚）。
func (s *TuiScene) ApplyCellMutation(m CellMutation) (*TranscriptCell, error) {
	if s == nil {
		return nil, fmt.Errorf("nil scene")
	}
	var (
		cell *TranscriptCell
		err  error
	)
	switch m := m.(type) {
	case *AppendCell:
		cell, err = s.append(m)
	case *InsertCell:
		cell, err = s.insert(m)
	case *InsertCellBefore:
		cell, err = s.insertBefore(m)
	case *UpdateCell:
		cell, err = s.update(m)
	case *FinalizeCell:
		cell, err = s.finalize(m)
	case *RemoveMutableCell:
		cell, err = s.remove(m)
	default:
		return nil, fmt.Errorf("unknown cell mutation %T", m)
	}
	if err != nil {
		return nil, err
	}
	s.contentVersion++
	s.lastSnapshot = nil
	return cell, nil
}

func (s *TuiScene) append(m *AppendCell) (*TranscriptCell, error) {
	if m == nil {
		return nil, fmt.Errorf("nil append mutation")
	}
	cell := m.Cell
	if cell.ID == 0 {
		cell.ID = s.NextID()
	}
	if _, exists := s.byID[cell.ID]; exists {
		return nil, fmt.Errorf("duplicate cell id %d (INV-SCENE-02)", cell.ID)
	}
	now := time.Now().UTC()
	if cell.CreatedAt.IsZero() {
		cell.CreatedAt = now
	}
	if cell.Sequence == 0 {
		if cell.ChainKey == "" {
			// top-level：分配单调 Sequence（§5.3）
			s.nextSeq++
			cell.Sequence = s.nextSeq
		}
		// tool-chain 内部成员不推进 Sequence
	}
	// Phase 零值即 CellMutable，无需处理。
	if cell.ChainKey != "" {
		if _, exists := s.byChain[cell.ChainKey]; exists {
			return nil, fmt.Errorf("duplicate chain key %q", cell.ChainKey)
		}
		s.byChain[cell.ChainKey] = &cell
	}
	cp := cloneCell(cell)
	s.cells = append(s.cells, &cp)
	s.byID[cp.ID] = &cp
	return &cp, nil
}

func (s *TuiScene) insert(m *InsertCell) (*TranscriptCell, error) {
	if m == nil {
		return nil, fmt.Errorf("nil insert mutation")
	}
	anchor, ok := s.byID[m.After]
	if !ok || anchor == nil {
		return nil, fmt.Errorf("insert: anchor cell %d not found", m.After)
	}
	cell := m.Cell
	if cell.ID == 0 {
		cell.ID = s.NextID()
	}
	if _, exists := s.byID[cell.ID]; exists {
		return nil, fmt.Errorf("duplicate cell id %d (INV-SCENE-02)", cell.ID)
	}
	now := time.Now().UTC()
	if cell.CreatedAt.IsZero() {
		cell.CreatedAt = now
	}
	if cell.Sequence == 0 {
		if cell.ChainKey == "" {
			// top-level：分配单调 Sequence（§5.3 创建即分配；插入中间不
			// 重编号既有 cell，渲染顺序 = Cells 数组顺序）。
			s.nextSeq++
			cell.Sequence = s.nextSeq
		}
	}
	if cell.ChainKey != "" {
		if _, exists := s.byChain[cell.ChainKey]; exists {
			return nil, fmt.Errorf("duplicate chain key %q", cell.ChainKey)
		}
		s.byChain[cell.ChainKey] = &cell
	}
	cp := cloneCell(cell)
	at := -1
	for i, c := range s.cells {
		if c.ID == m.After {
			at = i
			break
		}
	}
	if at < 0 {
		return nil, fmt.Errorf("insert: anchor cell %d missing from ordered cells", m.After)
	}
	s.cells = append(s.cells, nil)
	copy(s.cells[at+2:], s.cells[at+1:])
	s.cells[at+1] = &cp
	s.byID[cp.ID] = &cp
	return &cp, nil
}

func (s *TuiScene) insertBefore(m *InsertCellBefore) (*TranscriptCell, error) {
	if m == nil {
		return nil, fmt.Errorf("insert before: nil mutation")
	}
	if anchor, ok := s.byID[m.Before]; !ok || anchor == nil {
		return nil, fmt.Errorf("insert before: anchor cell %d not found", m.Before)
	}
	cell := m.Cell
	if cell.ID == 0 {
		cell.ID = s.NextID()
	}
	if _, exists := s.byID[cell.ID]; exists {
		return nil, fmt.Errorf("duplicate cell id %d (INV-SCENE-02)", cell.ID)
	}
	now := time.Now().UTC()
	if cell.CreatedAt.IsZero() {
		cell.CreatedAt = now
	}
	if cell.Sequence == 0 && cell.ChainKey == "" {
		s.nextSeq++
		cell.Sequence = s.nextSeq
	}
	if cell.ChainKey != "" {
		if _, exists := s.byChain[cell.ChainKey]; exists {
			return nil, fmt.Errorf("duplicate chain key %q", cell.ChainKey)
		}
		s.byChain[cell.ChainKey] = &cell
	}
	cp := cloneCell(cell)
	at := -1
	for i, candidate := range s.cells {
		if candidate.ID == m.Before {
			at = i
			break
		}
	}
	if at < 0 {
		return nil, fmt.Errorf("insert before: anchor cell %d missing from ordered cells", m.Before)
	}
	s.cells = append(s.cells, nil)
	copy(s.cells[at+1:], s.cells[at:])
	s.cells[at] = &cp
	s.byID[cp.ID] = &cp
	return &cp, nil
}

func (s *TuiScene) update(m *UpdateCell) (*TranscriptCell, error) {
	if m == nil {
		return nil, fmt.Errorf("nil update mutation")
	}
	c, ok := s.byID[m.ID]
	if !ok {
		return nil, fmt.Errorf("update: unknown cell %d", m.ID)
	}
	if c.Phase != CellMutable {
		return nil, fmt.Errorf("update: cell %d not mutable (phase %v)", m.ID, c.Phase)
	}
	if m.Revision <= c.Revision {
		return nil, fmt.Errorf("update: stale revision %d <= %d (INV-SCENE-03)", m.Revision, c.Revision)
	}
	// Copy-on-write: 不修改已发布快照引用的 cell 对象，
	// 而是替换 scene 的引用为新对象（旧对象保持不可变）。
	nc := *c
	nc.Source = m.Source
	nc.Presentation = m.Presentation.Clone()
	nc.HistoryCommitBlocked = m.HistoryCommitBlocked
	nc.BoundaryGroupKey = m.BoundaryGroupKey
	nc.Revision = m.Revision
	s.replaceCell(&nc)
	cp := cloneCell(nc)
	return &cp, nil
}

func (s *TuiScene) finalize(m *FinalizeCell) (*TranscriptCell, error) {
	if m == nil {
		return nil, fmt.Errorf("nil finalize mutation")
	}
	c, ok := s.byID[m.ID]
	if !ok {
		return nil, fmt.Errorf("finalize: unknown cell %d", m.ID)
	}
	if c.Phase != CellMutable {
		return nil, fmt.Errorf("finalize: cell %d not mutable (phase %v) (INV-SCENE-04)", m.ID, c.Phase)
	}
	if m.Revision <= c.Revision {
		return nil, fmt.Errorf("finalize: stale revision %d <= %d (INV-SCENE-03)", m.Revision, c.Revision)
	}
	// Copy-on-write: 与 update 一致，替换引用而非原地修改。
	nc := *c
	nc.Source = m.Source
	nc.Presentation = m.Presentation.Clone()
	nc.HistoryCommitBlocked = m.HistoryCommitBlocked
	nc.BoundaryGroupKey = m.BoundaryGroupKey
	nc.Revision = m.Revision
	nc.Phase = CellCommitted
	now := time.Now().UTC()
	nc.FinalizedAt = &now
	s.replaceCell(&nc)
	cp := cloneCell(nc)
	return &cp, nil
}

// replaceCell 把 scene 对某 cell 的引用原子地切换到新对象（copy-on-write）。
// 调用方必须在持有 scene 写锁（SceneController 事务内）时调用；
// 已发布快照仍引用旧对象，不受影响。
func (s *TuiScene) replaceCell(nc *TranscriptCell) {
	s.byID[nc.ID] = nc
	for i, cc := range s.cells {
		if cc.ID == nc.ID {
			s.cells[i] = nc
			return
		}
	}
}

func (s *TuiScene) remove(m *RemoveMutableCell) (*TranscriptCell, error) {
	if m == nil {
		return nil, fmt.Errorf("nil remove mutation")
	}
	c, ok := s.byID[m.ID]
	if !ok {
		return nil, fmt.Errorf("remove: unknown cell %d", m.ID)
	}
	if c.Phase != CellMutable {
		return nil, fmt.Errorf("remove: cell %d not mutable (phase %v)", m.ID, c.Phase)
	}
	if c.ChainKey != "" {
		delete(s.byChain, c.ChainKey)
	}
	delete(s.byID, m.ID)
	for i, cc := range s.cells {
		if cc.ID == m.ID {
			s.cells = append(s.cells[:i], s.cells[i+1:]...)
			break
		}
	}
	cp := cloneCell(*c)
	return &cp, nil
}

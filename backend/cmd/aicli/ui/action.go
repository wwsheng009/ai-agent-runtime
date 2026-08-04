package ui

import (
	"errors"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/renderengine"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

var ErrUIActionEffectFailed = errors.New("ui terminal effect failed")

// ActionClass 是 UIAction 的投递分类（实施指南 Phase 1 任务 1：
// durable / coalescable / barrier 三分法）。
//
//   - ClassDurable：内容性 action（RuntimeEvent、Input、facade 变更），
//     绝不丢弃；mailbox 满时生产者背压等待。
//   - ClassCoalescable：意图性 action（DrawRequested、Timer），
//     同 key 待处理时合并/替换为最新（latest-wins），可安全丢弃旧意图。
//   - ClassBarrier：顺序性 action（Resize、Lease、EffectResult），
//     按入队顺序在其之前的所有 action 之后执行，本身不参与合并。
type ActionClass uint8

const (
	ClassDurable ActionClass = iota
	ClassCoalescable
	ClassBarrier
)

func (c ActionClass) String() string {
	switch c {
	case ClassDurable:
		return "durable"
	case ClassCoalescable:
		return "coalescable"
	case ClassBarrier:
		return "barrier"
	}
	return "unknown"
}

// UIAction 是 UI actor（UIController）的单一输入单元（IR-1）：
// 所有业务 producer 只能 Post(UIAction)，不得直接 mutation surface。
type UIAction interface {
	isUIAction()
	// Class 返回投递分类。
	Class() ActionClass
	// CoalesceKey 返回 ClassCoalescable 的合并 key；空串表示不可合并。
	// Durable/Barrier 必须返回空串。
	CoalesceKey() string
}

// coalesceMerger 允许合并类 action 把新意图并入同 key 的待处理 action
// （例如 DrawRequested 的 dirty 位取并集）。未实现该接口时 latest-wins
// 直接替换。
type coalesceMerger interface {
	mergeCoalesce(pending UIAction) UIAction
}

// mergeActions 是 mailbox 的合并入口。
func mergeActions(pending, incoming UIAction) UIAction {
	if m, ok := pending.(coalesceMerger); ok {
		if merged := m.mergeCoalesce(incoming); merged != nil {
			return merged
		}
	}
	return incoming
}

// ---------------------------------------------------------------------------
// 意图类（coalescable）
// ---------------------------------------------------------------------------

// DrawRequested 请求一次 frame 绘制。同 key 待处理时合并：dirty 取并集，
// generation/reason 取最新（IR-10：repaint 可重发字节，以 token/range 判
// 定 exactly-once，文本不作为权威断言）。
type DrawRequested struct {
	Key        string
	Reason     string
	Dirty      renderengine.DirtyFlags
	Generation uint64
}

func (DrawRequested) isUIAction()           {}
func (DrawRequested) Class() ActionClass    { return ClassCoalescable }
func (a DrawRequested) CoalesceKey() string { return a.Key }

func (a DrawRequested) mergeCoalesce(pending UIAction) UIAction {
	if p, ok := pending.(DrawRequested); ok {
		a.Dirty |= p.Dirty
		if a.Generation < p.Generation {
			a.Generation = p.Generation
		}
		// latest-wins：以新 action 的 Reason 为准（空 Reason 时保留旧值）。
		if p.Reason != "" {
			a.Reason = p.Reason
		}
	}
	return a
}

// Timer 取代 FramePump 的任意定时回调：Timer action 携带调度 key 与
// generation，由 reducer 端根据 generation 判断是否过期（stale callback
// 防护，IR-11：FramePump callback 只投递 action）。
type Timer struct {
	Key        string
	Generation uint64
}

func (Timer) isUIAction()           {}
func (Timer) Class() ActionClass    { return ClassCoalescable }
func (a Timer) CoalesceKey() string { return a.Key }

// ---------------------------------------------------------------------------
// 顺序类（barrier）
// ---------------------------------------------------------------------------

// Resize 是几何 barrier：必须在其前所有 action 之后、其后 action 之前
// 执行，且不参与合并（IR-12：resize 从 semantic source 派生）。Applied
// 表示 Width/Height 来自 legacy surface 刚完成的 probe；adapter 收到该
// 回投时不得再次触发 terminal probe/reflow。
type Resize struct {
	Width      int
	Height     int
	Generation uint64
	Applied    bool
}

func (Resize) isUIAction()         {}
func (Resize) Class() ActionClass  { return ClassBarrier }
func (Resize) CoalesceKey() string { return "" }

// LeaseAcquired / LeaseReleased 是 fullscreen lease barrier
// （screen_lease.go 的所有权切换点）。
type LeaseAcquired struct {
	LeaseID uint64
}

func (LeaseAcquired) isUIAction()         {}
func (LeaseAcquired) Class() ActionClass  { return ClassBarrier }
func (LeaseAcquired) CoalesceKey() string { return "" }

type LeaseReleased struct {
	LeaseID uint64
}

func (LeaseReleased) isUIAction()         {}
func (LeaseReleased) Class() ActionClass  { return ClassBarrier }
func (LeaseReleased) CoalesceKey() string { return "" }

// EffectResult 是 terminal effect 的结果回投（实施指南 §3）：
// Err == nil 视为 Ack（Token 成功），Err != nil 视为 Failed；
// MayHavePartiallyWritten=true 时不得盲目重放同一 batch，投影进入
// Unknown 由 recovery policy 决定（Phase 3/4 使用，P1 定义类型）。
type EffectResult struct {
	Token                   uint64
	Err                     error
	MayHavePartiallyWritten bool
}

func (EffectResult) isUIAction()         {}
func (EffectResult) Class() ActionClass  { return ClassBarrier }
func (EffectResult) CoalesceKey() string { return "" }

// TerminalEffectAck is the typed success result for a terminal transaction.
// It is an alias-shaped action payload rather than an implicit nil error, so
// Phase 4 reducers can validate token/generation before advancing handoff.
type TerminalEffectAck struct {
	Token uint64
	Frame uint64
}

// TerminalEffectFailed retains whether a writer may have accepted a prefix.
// That condition requires projection recovery; it must never blindly retry the
// same native-scrollback bytes.
type TerminalEffectFailed struct {
	Token                   uint64
	Err                     error
	MayHavePartiallyWritten bool
}

func (a TerminalEffectAck) AsAction() EffectResult {
	return EffectResult{Token: a.Token}
}

func (f TerminalEffectFailed) AsAction() EffectResult {
	err := f.Err
	if err == nil {
		err = ErrUIActionEffectFailed
	}
	return EffectResult{Token: f.Token, Err: err, MayHavePartiallyWritten: f.MayHavePartiallyWritten}
}

// ---------------------------------------------------------------------------
// 内容类（durable）
// ---------------------------------------------------------------------------

// RuntimeEvent 是会话运行时事件的统一信封（Kind + Payload）。
// Payload 的权威解释在 commands 侧（chatRuntimeEventBridge），
// UI actor 只负责保序投递与 revision 记账。
type RuntimeEvent struct {
	Kind    string
	Payload any
}

func (RuntimeEvent) isUIAction()         {}
func (RuntimeEvent) Class() ActionClass  { return ClassDurable }
func (RuntimeEvent) CoalesceKey() string { return "" }

// ReplaceTranscriptAction transfers an immutable semantic Scene snapshot into
// AppState. It carries cells, never terminal rows; the reducer copies it before
// publication so producers cannot retain mutable actor memory.
//
// Phase 2 defines this transition boundary before every existing Scene producer
// is migrated. Callers must not synthesize it from ScreenModel/historyWindow.
type ReplaceTranscriptAction struct {
	Snapshot *scene.Snapshot
}

func (ReplaceTranscriptAction) isUIAction()         {}
func (ReplaceTranscriptAction) Class() ActionClass  { return ClassDurable }
func (ReplaceTranscriptAction) CoalesceKey() string { return "" }

// SetActiveCellAction replaces the semantic mutable active-cell snapshot. Its
// Source and source ranges are authoritative; ActiveBand display rows remain a
// layout projection and are intentionally absent.
type SetActiveCellAction struct {
	Active ActiveCellState
}

func (SetActiveCellAction) isUIAction()         {}
func (SetActiveCellAction) Class() ActionClass  { return ClassDurable }
func (SetActiveCellAction) CoalesceKey() string { return "" }

// ClearActiveCellAction removes the current mutable semantic cell after a
// finalized transcript action has been accepted.
type ClearActiveCellAction struct{}

func (ClearActiveCellAction) isUIAction()         {}
func (ClearActiveCellAction) Class() ActionClass  { return ClassDurable }
func (ClearActiveCellAction) CoalesceKey() string { return "" }

// InputEvent 是用户输入事件（键盘/粘贴/IME）。durable：绝不丢弃用户输入。
// Cursor/PasteActive 保留编辑器快照所需的最小语义；Render 表示该快照
// 应立即投影到已显示的 prompt，而非只更新下一次编辑器绘制的状态。
// 注意与 Input（输入框组件）区分。
type InputEvent struct {
	Text        string
	Cursor      int
	PasteActive bool
	Render      bool
}

func (InputEvent) isUIAction()         {}
func (InputEvent) Class() ActionClass  { return ClassDurable }
func (InputEvent) CoalesceKey() string { return "" }

// ---------------------------------------------------------------------------
// Facade 变更类（durable）——Phase 1 任务 4：
// 现有 SetActiveBandStyled/ClearActiveBand/SetStatusModels/prompt/popup
// 保留为 facade，但内部只投递对应 action。
// ---------------------------------------------------------------------------

// SetActiveBandAction 对应 SetActiveBandStyled/SetActiveBand。
type SetActiveBandAction struct {
	Lines      []render.Line
	RawLines   []string // 非空时对应 SetActiveBand（纯文本形态）
	Generation uint64
}

func (SetActiveBandAction) isUIAction()         {}
func (SetActiveBandAction) Class() ActionClass  { return ClassDurable }
func (SetActiveBandAction) CoalesceKey() string { return "" }

// ClearActiveBandAction 对应 ClearActiveBand。
type ClearActiveBandAction struct {
	Generation uint64
}

func (ClearActiveBandAction) isUIAction()         {}
func (ClearActiveBandAction) Class() ActionClass  { return ClassDurable }
func (ClearActiveBandAction) CoalesceKey() string { return "" }

// SetStatusModelsAction 对应 SetStatusModels。
type SetStatusModelsAction struct {
	Status  style.StatusLineModel
	Dynamic *style.StatusLineModel
}

func (SetStatusModelsAction) isUIAction()         {}
func (SetStatusModelsAction) Class() ActionClass  { return ClassDurable }
func (SetStatusModelsAction) CoalesceKey() string { return "" }

// SetStatusModelAction updates only the persistent status row. It remains
// distinct from SetStatusModelsAction because callers of the legacy facade do
// not intend to clear the current dynamic activity row as a side effect.
type SetStatusModelAction struct {
	Status style.StatusLineModel
}

func (SetStatusModelAction) isUIAction()         {}
func (SetStatusModelAction) Class() ActionClass  { return ClassDurable }
func (SetStatusModelAction) CoalesceKey() string { return "" }

// SetDynamicStatusModelAction updates only the transient activity row.
// Keeping the partial semantic intent explicit avoids a second status state
// channel while preserving the legacy SetDynamicStatusModel contract.
type SetDynamicStatusModelAction struct {
	Dynamic *style.StatusLineModel
}

func (SetDynamicStatusModelAction) isUIAction()         {}
func (SetDynamicStatusModelAction) Class() ActionClass  { return ClassDurable }
func (SetDynamicStatusModelAction) CoalesceKey() string { return "" }

// ShowPromptAction 对应 ShowPrompt。
type ShowPromptAction struct {
	Line string
}

func (ShowPromptAction) isUIAction()         {}
func (ShowPromptAction) Class() ActionClass  { return ClassDurable }
func (ShowPromptAction) CoalesceKey() string { return "" }

// ClearPromptRowsAction 对应 ClearPromptRows。ShowPrompt 与 ClearPromptRows
// 成对出现（chat loop 提交时隐藏 prompt 给 band 让位）：两者必须走同一条
// actor 队列，否则 ShowPrompt 异步渲染后同步 ClearPromptRows 已失效，
// mid-stream 会残留 prompt 行。
type ClearPromptRowsAction struct {
	Rows int
}

func (ClearPromptRowsAction) isUIAction()         {}
func (ClearPromptRowsAction) Class() ActionClass  { return ClassDurable }
func (ClearPromptRowsAction) CoalesceKey() string { return "" }

// SetPromptStateAction 对应 SetPromptInputState（line/input/rows/cursor）。
type SetPromptStateAction struct {
	Line      string
	Input     string
	Rows      int
	CursorRow int
	CursorCol int
}

func (SetPromptStateAction) isUIAction()         {}
func (SetPromptStateAction) Class() ActionClass  { return ClassDurable }
func (SetPromptStateAction) CoalesceKey() string { return "" }

// TrackPromptInputAction records editor state without requiring an immediate
// repaint when only the in-place cursor/input changes. It has the same
// semantic payload as SetPromptStateAction, but Apply preserves the legacy
// TrackPromptInputState paint behavior.
type TrackPromptInputAction struct {
	Line      string
	Input     string
	Rows      int
	CursorRow int
	CursorCol int
}

func (TrackPromptInputAction) isUIAction()         {}
func (TrackPromptInputAction) Class() ActionClass  { return ClassDurable }
func (TrackPromptInputAction) CoalesceKey() string { return "" }

// ResetPromptAction clears a previous physical prompt footprint and starts a
// fresh one-line prompt. Rows is the previous footprint to clear, rather than
// the row allocation of the new prompt.
type ResetPromptAction struct {
	Line string
	Rows int
}

func (ResetPromptAction) isUIAction()         {}
func (ResetPromptAction) Class() ActionClass  { return ClassDurable }
func (ResetPromptAction) CoalesceKey() string { return "" }

// SetPromptRowsAction changes the prompt viewport allocation while retaining
// the current prompt semantic source.
type SetPromptRowsAction struct {
	Rows int
}

func (SetPromptRowsAction) isUIAction()         {}
func (SetPromptRowsAction) Class() ActionClass  { return ClassDurable }
func (SetPromptRowsAction) CoalesceKey() string { return "" }

// SetPromptNoticeAction and SetPromptEditorStatusAction update the two
// independently-owned prompt context lines. They must not overwrite each
// other because status/timer updates and editor movement run on different
// producer paths.
type SetPromptNoticeAction struct {
	Line string
}

func (SetPromptNoticeAction) isUIAction()         {}
func (SetPromptNoticeAction) Class() ActionClass  { return ClassDurable }
func (SetPromptNoticeAction) CoalesceKey() string { return "" }

type SetPromptEditorStatusAction struct {
	Line string
}

func (SetPromptEditorStatusAction) isUIAction()         {}
func (SetPromptEditorStatusAction) Class() ActionClass  { return ClassDurable }
func (SetPromptEditorStatusAction) CoalesceKey() string { return "" }

// SetComposerPreviewAction/ClearComposerPreviewAction retain the legacy
// transitional composer API as an ordered BottomPane intent. They are not a
// second composer owner: the reducer updates the same BottomPaneState that
// later Compose will consume.
type SetComposerPreviewAction struct {
	Line string
}

func (SetComposerPreviewAction) isUIAction()         {}
func (SetComposerPreviewAction) Class() ActionClass  { return ClassDurable }
func (SetComposerPreviewAction) CoalesceKey() string { return "" }

type ClearComposerPreviewAction struct{}

func (ClearComposerPreviewAction) isUIAction()         {}
func (ClearComposerPreviewAction) Class() ActionClass  { return ClassDurable }
func (ClearComposerPreviewAction) CoalesceKey() string { return "" }

// ShowPopupAction 对应 ShowPopup 及其 preserve-cursor/owner 变体。
type ShowPopupAction struct {
	Lines          []string
	PreserveCursor bool
	Owner          string
	BelowPrompt    bool
	Prompt         string // ShowPopupInput* 的输入行
	Input          bool   // 保留空 prompt 的输入型 popup 语义
	// Handle is populated by BeginPopupInputForOwner*. The caller receives the
	// token before this durable action is reduced, so a subsequent update/clear
	// can be ordered behind the begin action without a synchronous surface
	// mutation. A nil handle denotes the legacy owner-based popup API.
	Handle   *PopupHandle
	Viewport *PopupViewportSpec
}

func (ShowPopupAction) isUIAction()         {}
func (ShowPopupAction) Class() ActionClass  { return ClassDurable }
func (ShowPopupAction) CoalesceKey() string { return "" }

// ClearPopupAction 对应 ClearPopup 及其 preserve-cursor/owner/handle 变体。
type ClearPopupAction struct {
	PreserveCursor bool
	Owner          string
	Handle         *PopupHandle
}

func (ClearPopupAction) isUIAction()         {}
func (ClearPopupAction) Class() ActionClass  { return ClassDurable }
func (ClearPopupAction) CoalesceKey() string { return "" }

// UpdatePopupAction 对应 UpdatePopupInputForHandle。
type UpdatePopupAction struct {
	Handle         PopupHandle
	Lines          []string
	Prompt         string
	PreserveCursor bool
}

func (UpdatePopupAction) isUIAction()         {}
func (UpdatePopupAction) Class() ActionClass  { return ClassDurable }
func (UpdatePopupAction) CoalesceKey() string { return "" }

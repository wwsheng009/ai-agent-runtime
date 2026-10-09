package ui

import "strings"

// PopupPort 是 L5-2 Batch B（方案 §2 D1）引入的会话级 popup 门面：
// commands 侧经此接口完成全部 popup 展示/更新/清理与 HasActive 查询，不再
// 直读 *FixedBottomSurface 的 popup 方法（门禁：
// TestChatPopupFamilyDirectReadsFrozen）。
//
// 语义与现 surface facade 完全一致（owner/viewport/belowPrompt/preserveCursor/
// 优先级/栈恢复），仅所有权与投递路径不同：
//
//   - unified（uiPoster 已接线且 actor 存活）：Begin/Show/Update/Clear 直接投递
//     Show/Update/ClearPopupAction 到 controller，由 BottomPaneState 状态机归约
//     （applyPopupShow/Begin/Update/Clear）；surface 不再持有 popup 语义状态；
//   - legacy/compat（无 poster 或 actor 拒绝）：回落 surface 本地实现
//     （showPopupInputForOwnerImpl / beginPopupInputForHandleImpl /
//     clearPopupForOwnerPreserveCursorImpl 等），legacy 路径原样保留；
//   - handle 分配上移到门面边界：Begin* 先分配 token（经 surface 共享的
//     allocatePopupInstance，保证同一 surface 上 token 单调唯一、compat 直调路径
//     不与之串号），再投递 begin action；后续 Update/Clear 携带同一 token，天然
//     FIFO 排在 begin 之后（「先分配 token 后投递」语义）；
//   - HasActivePopup：unified 查询 reducer 权威的 BottomPaneState；legacy 查询
//     surface 本地状态（stateSource 为 nil 或未接线时回落）。
type PopupPort interface {
	ShowPopupInputForOwner(lines []string, prompt string, owner string)
	ShowPopupInputPreserveCursorForOwner(lines []string, prompt string, owner string)
	ShowPopupPreserveCursorForOwner(lines []string, owner string)
	ShowPopupPreserveCursorForOwnerBelowPrompt(lines []string, owner string)
	BeginPopupInputForOwner(lines []string, prompt string, owner string) PopupHandle
	BeginPopupInputForOwnerWithViewport(lines []string, prompt string, owner string, viewport PopupViewportSpec) PopupHandle
	UpdatePopupInputForHandle(handle PopupHandle, lines []string, prompt string, preserveCursor bool) bool
	ClearPopup()
	ClearPopupPreserveCursor()
	ClearPopupForOwnerPreserveCursor(owner string)
	ClearPopupHandlePreserveCursor(handle PopupHandle)
	ShowPendingPastePreview(lines int, text string)
	ClearPendingPastePreview()
	HasActivePopup() bool
}

// popupPort 是 PopupPort 的默认实现：surface 提供本地回落与共享 token 分配，
// stateSource 提供 unified 侧的 BottomPaneState 查询（nil 或 ok=false 时回落
// surface 本地状态）。
type popupPort struct {
	surface *FixedBottomSurface
	// stateSource 返回 reducer 权威的 bottom pane 状态（unified actor 存活时
	// ok=true）。commands 侧注入 coordinator 的查询闭包；legacy/无 actor 场景为
	// nil，HasActivePopup 直接读 surface。
	stateSource func() (BottomPaneState, bool)
}

// NewPopupPort 构造 popup 门面。surface 为 nil 时返回安全 no-op 端口（方法全部
// 无副作用），便于调用方在无 surface/headless 场景直接调用而无需逐点判空。
func NewPopupPort(surface *FixedBottomSurface, stateSource func() (BottomPaneState, bool)) PopupPort {
	return &popupPort{surface: surface, stateSource: stateSource}
}

// available 报告门面是否持有可用的本地 surface（terminal 是 surface 的构造期
// 不变量，与现 facade 的判空口径一致）。
func (p *popupPort) available() bool {
	return p != nil && p.surface != nil && p.surface.terminal != nil
}

// bottomPaneStatePopupActive 是 BottomPaneState 侧的 HasActivePopup 判据，
// 与 surface.HasActivePopup 的字段集合保持一致（popupLines/owner/instance/stack；
// composerLine 单独存在不算 active，沿用既有语义）。
func bottomPaneStatePopupActive(state BottomPaneState) bool {
	return len(state.PopupLines) > 0 || state.PopupOwner != "" || state.PopupInstance != 0 || len(state.PopupStack) > 0
}

func (p *popupPort) ShowPopupInputForOwner(lines []string, prompt string, owner string) {
	if !p.available() {
		return
	}
	prompt = strings.TrimRight(SanitizeTerminalText(prompt), "\r\n")
	if p.surface.postFacadeAction(ShowPopupAction{
		Lines:  lines,
		Owner:  owner,
		Prompt: prompt,
		Input:  true,
	}) {
		return
	}
	p.surface.showPopupInputForOwnerImpl(lines, prompt, owner, false)
}

func (p *popupPort) ShowPopupInputPreserveCursorForOwner(lines []string, prompt string, owner string) {
	if !p.available() {
		return
	}
	prompt = strings.TrimRight(SanitizeTerminalText(prompt), "\r\n")
	if p.surface.postFacadeAction(ShowPopupAction{
		Lines:          lines,
		PreserveCursor: true,
		Owner:          owner,
		Prompt:         prompt,
		Input:          true,
	}) {
		return
	}
	p.surface.showPopupInputForOwnerImpl(lines, prompt, owner, true)
}

func (p *popupPort) ShowPopupPreserveCursorForOwner(lines []string, owner string) {
	if !p.available() {
		return
	}
	if p.surface.postFacadeAction(ShowPopupAction{Lines: lines, PreserveCursor: true, Owner: owner}) {
		return
	}
	p.surface.showPopupPreserveCursorForOwner(lines, owner, false)
}

func (p *popupPort) ShowPopupPreserveCursorForOwnerBelowPrompt(lines []string, owner string) {
	if !p.available() {
		return
	}
	if p.surface.postFacadeAction(ShowPopupAction{Lines: lines, PreserveCursor: true, Owner: owner, BelowPrompt: true}) {
		return
	}
	p.surface.showPopupPreserveCursorForOwner(lines, owner, true)
}

func (p *popupPort) BeginPopupInputForOwner(lines []string, prompt string, owner string) PopupHandle {
	return p.beginPopupInputForOwner(lines, prompt, owner, nil)
}

func (p *popupPort) BeginPopupInputForOwnerWithViewport(lines []string, prompt string, owner string, viewport PopupViewportSpec) PopupHandle {
	return p.beginPopupInputForOwner(lines, prompt, owner, &viewport)
}

// beginPopupInputForOwner 是 tokenized begin 的门面路径：先在门面边界分配
// token（surface 共享分配器），再投递携带该 token 的 ShowPopupAction；投递失败
// （legacy/无 poster/actor 关闭）回落 surface 的 handle 实现。两条路径都保证
// 调用方在归约前拿到身份，且后续 Update/Clear 以同一 token 保持 FIFO。
func (p *popupPort) beginPopupInputForOwner(lines []string, prompt string, owner string, viewport *PopupViewportSpec) PopupHandle {
	if !p.available() {
		return PopupHandle{}
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return PopupHandle{}
	}
	prompt = strings.TrimRight(SanitizeTerminalText(prompt), "\r\n")
	handle := PopupHandle{owner: owner, instance: p.surface.allocatePopupInstance()}
	if !handle.Valid() {
		return PopupHandle{}
	}
	if p.surface.postFacadeAction(ShowPopupAction{
		Lines:    lines,
		Owner:    owner,
		Prompt:   prompt,
		Input:    true,
		Handle:   &handle,
		Viewport: clonePopupViewportSpec(viewport),
	}) {
		return handle
	}
	_ = p.surface.beginPopupInputForHandleImpl(lines, prompt, handle, viewport)
	return handle
}

func (p *popupPort) UpdatePopupInputForHandle(handle PopupHandle, lines []string, prompt string, preserveCursor bool) bool {
	if !p.available() || !handle.Valid() {
		return false
	}
	prompt = strings.TrimRight(SanitizeTerminalText(prompt), "\r\n")
	if p.surface.postFacadeAction(UpdatePopupAction{
		Handle:         handle,
		Lines:          lines,
		Prompt:         prompt,
		PreserveCursor: preserveCursor,
	}) {
		return true
	}
	return p.surface.updatePopupInputForHandleImpl(handle, lines, prompt, preserveCursor)
}

func (p *popupPort) ClearPopup() {
	if !p.available() {
		return
	}
	if p.surface.postFacadeAction(ClearPopupAction{}) {
		return
	}
	p.surface.clearPopupImpl()
}

func (p *popupPort) ClearPopupPreserveCursor() {
	if !p.available() {
		return
	}
	if p.surface.postFacadeAction(ClearPopupAction{PreserveCursor: true}) {
		return
	}
	p.surface.clearPopupPreserveCursorImpl()
}

func (p *popupPort) ClearPopupForOwnerPreserveCursor(owner string) {
	if !p.available() {
		return
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return
	}
	if p.surface.postFacadeAction(ClearPopupAction{PreserveCursor: true, Owner: owner}) {
		return
	}
	p.surface.clearPopupForOwnerPreserveCursorImpl(owner)
}

func (p *popupPort) ClearPopupHandlePreserveCursor(handle PopupHandle) {
	if !p.available() || !handle.Valid() {
		return
	}
	if p.surface.postFacadeAction(ClearPopupAction{PreserveCursor: true, Handle: &handle}) {
		return
	}
	p.surface.clearPopupHandlePreserveCursorImpl(handle)
}

// ShowPendingPastePreview 复用 surface 的预览构造语义（NormalizePastedText +
// buildPendingPastePreviewLines），再走门面的 owner 化展示路径。
func (p *popupPort) ShowPendingPastePreview(lines int, text string) {
	if !p.available() {
		return
	}
	text = NormalizePastedText(text)
	lines = maxInt(0, lines)
	preview := buildPendingPastePreviewLines(lines, text)
	p.ShowPopupPreserveCursorForOwner(preview, "pending_paste")
}

func (p *popupPort) ClearPendingPastePreview() {
	if !p.available() {
		return
	}
	p.ClearPopupForOwnerPreserveCursor("pending_paste")
}

// HasActivePopup 是 popup 族的读侧门：unified 下查询 reducer 权威的
// BottomPaneState（surface 本地字段在 unified 下不再被归约写入）；legacy 下
// 查询 surface 本地状态。
func (p *popupPort) HasActivePopup() bool {
	if p == nil {
		return false
	}
	if p.stateSource != nil {
		if state, ok := p.stateSource(); ok {
			return bottomPaneStatePopupActive(state)
		}
	}
	if p.surface == nil {
		return false
	}
	return p.surface.HasActivePopup()
}

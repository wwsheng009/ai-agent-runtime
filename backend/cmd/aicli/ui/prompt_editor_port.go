package ui

// PromptEditorPort 是 L5-2c 引入的会话级 prompt-editor 门面：commands 侧
// composer 生命周期（状态行同步 + 可见行数预算）经此接口完成，不再直读
// *FixedBottomSurface 的对应方法（门禁：TestChatPopupFamilyDirectReadsFrozen
// 邻近族白名单清零）。
//
// 语义与 surface facade 一致，仅所有权与投影来源不同：
//
//   - unified（poster 已接线且 actor 存活）：SetStatusLine 直接投递
//     SetPromptEditorStatusAction 到 controller（reducer 写
//     Bottom.PromptEditorStatusLine）；MaxVisibleRows 用渲染器同源的
//     BottomPanePolicyForGeometry 纯投影（BottomPaneState + GeometryState）；
//   - legacy/compat（无 poster/actor 或状态源未接线）：回落 surface 本地实现
//     （setPromptEditorStatusLineImpl / promptInputMaxVisibleRowsImpl），
//     原语义保留；
//   - 无 surface：no-op（SetStatusLine=false；MaxVisibleRows=
//     ChatComposerMaxVisibleRows，与现 disabled/nil 口径一致）。
type PromptEditorPort interface {
	SetStatusLine(line string) bool
	MaxVisibleRows() int
}

// promptEditorPort 是默认实现：surface 提供 legacy 回落；stateSource 提供
// unified 侧的 (BottomPaneState, GeometryState) 权威快照（nil 或 ok=false 时
// 回落 surface 本地实现）。
type promptEditorPort struct {
	surface *FixedBottomSurface
	// stateSource 返回 reducer 权威的 bottom pane 状态与几何（unified actor
	// 存活时 ok=true）。commands 侧注入 coordinator 的查询闭包；legacy/无
	// actor 场景为 nil。
	stateSource func() (BottomPaneState, GeometryState, bool)
}

// NewPromptEditorPort 构造 prompt-editor 门面。surface 为 nil 时返回安全
// no-op 端口（方法全部无副作用），便于调用方在无 surface/headless 场景直接
// 调用而无需逐点判空。
func NewPromptEditorPort(surface *FixedBottomSurface, stateSource func() (BottomPaneState, GeometryState, bool)) PromptEditorPort {
	return &promptEditorPort{surface: surface, stateSource: stateSource}
}

// available 报告门面是否持有可用的本地 surface（terminal 是 surface 的构造期
// 不变量，与现 facade 的判空口径一致）。
func (p *promptEditorPort) available() bool {
	return p != nil && p.surface != nil && p.surface.terminal != nil
}

// SetStatusLine 与 surface.SetPromptEditorStatusLine 同投递：unified 走
// postFacadeAction（reducer 归约，surface 不再持有语义状态），投递被拒/
// 无 poster 时回落 legacy impl（sanitize + reflow 原语义）。
func (p *promptEditorPort) SetStatusLine(line string) bool {
	if !p.available() {
		return false
	}
	if p.surface.postFacadeAction(SetPromptEditorStatusAction{Line: line}) {
		return true
	}
	return p.surface.setPromptEditorStatusLineImpl(line)
}

// MaxVisibleRows 是编辑器视口预算：unified 用渲染器同源投影
// （BottomPanePolicyForGeometry → PromptMaxVisibleRows）；legacy 回落 surface
// 探针预算（promptInputMaxVisibleRowsImpl）；无 surface 返回常规 composer 上限。
func (p *promptEditorPort) MaxVisibleRows() int {
	if p == nil {
		return ChatComposerMaxVisibleRows
	}
	if p.stateSource != nil {
		if state, geometry, ok := p.stateSource(); ok {
			return BottomPanePolicyForGeometry(state, geometry).PromptMaxVisibleRows
		}
	}
	if !p.available() {
		return ChatComposerMaxVisibleRows
	}
	return p.surface.promptInputMaxVisibleRowsImpl()
}

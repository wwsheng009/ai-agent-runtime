package ui

// ActiveBandViewportPort 是 L5-2b（D2-a ②）引入的 ActiveBand 视口门面：
// commands 侧流式视口（宽+行）经此接口获取，不再直读 *FixedBottomSurface 的
// ActiveBandViewportSize（门禁：TestChatGeometryFamilyDirectReadsFrozen 白名单清零）。
//
// 语义：
//
//   - unified（渲染链几何源可用）：ViewportSize = (geometry.Width,
//     ActiveBandRows(geometry.Height))——presenter probe → Resize action →
//     AppState.Geometry 是 unified 的几何权威；Unified() 报告该源可用；
//   - legacy/compat：回落 surface 终端缓存（activeBandViewportSizeImpl，原语义，
//     含「terminal 不可用 → (0, ActiveBandMinRows)」口径）；
//   - 无 surface 且无渲染链源：no-op 口径 (0, ActiveBandMinRows)。
type ActiveBandViewportPort interface {
	ViewportSize() (width, rows int)
	Unified() bool
}

// activeBandViewportPort 是默认实现：surface 提供 legacy 回落；stateSource 提供
// 渲染链权威几何（nil 或 ok=false 时回落）。
type activeBandViewportPort struct {
	surface *FixedBottomSurface
	// stateSource 返回渲染链权威几何（unified actor 存活且已测量时 ok=true）。
	// commands 侧注入 coordinator 的查询闭包；legacy/无 actor 场景为 nil。
	stateSource func() (GeometryState, bool)
}

// NewActiveBandViewportPort 构造视口门面。surface 为 nil 时返回安全 no-op 端口。
func NewActiveBandViewportPort(surface *FixedBottomSurface, stateSource func() (GeometryState, bool)) ActiveBandViewportPort {
	return &activeBandViewportPort{surface: surface, stateSource: stateSource}
}

// unifiedGeometry 返回可用的渲染链几何（未接线/未测量时为 false）。
func (p *activeBandViewportPort) unifiedGeometry() (GeometryState, bool) {
	if p == nil || p.stateSource == nil {
		return GeometryState{}, false
	}
	geometry, ok := p.stateSource()
	if !ok || geometry.Width <= 0 || geometry.Height <= 0 {
		return GeometryState{}, false
	}
	return geometry, true
}

// Unified 报告渲染链几何源是否可用（调用点据此保留 legacy 的 enabled/探针分叉）。
func (p *activeBandViewportPort) Unified() bool {
	_, ok := p.unifiedGeometry()
	return ok
}

// ViewportSize 返回 ActiveBand 视口（宽+行）：unified 走渲染链投影；legacy 回落
// surface 终端缓存；无来源返回 (0, ActiveBandMinRows)。
func (p *activeBandViewportPort) ViewportSize() (width, rows int) {
	if geometry, ok := p.unifiedGeometry(); ok {
		return geometry.Width, ActiveBandRows(geometry.Height)
	}
	if p != nil && p.surface != nil {
		return p.surface.activeBandViewportSizeImpl()
	}
	return 0, ActiveBandMinRows
}

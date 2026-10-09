package ui

import "time"

// GeometrySyncPort 是 L5-2 Batch A（方案 §2 D2-a ①）引入的几何门面：
// commands 侧经此接口请求终端几何探针与 ActiveBand 宽度，不再直读
// *FixedBottomSurface 的几何方法。surface 内部实现保持现状（布局宽度仍归
// surface）；语义与现有方法零变化：
//
//   - RequestGeometrySync(minInterval)：复用 syncTerminalGeometry
//     （terminal.RefreshSize + applyLayoutWithSizeLocked）。minInterval > 0 且
//     距上次探针不足该间隔时返回 (false, false) 且不触碰终端；minInterval <= 0
//     强制探针；
//   - ActiveBandWidth：复用 ActiveBandViewportSize 的宽度读数（缓存宽度，
//     无终端 syscall）。
//
// 渲染器接管布局宽度（D2-a ②，L5-2b）之前，本接口是 commands 侧几何探针与
// 读宽的唯一入口（门禁：commands 生产代码零 SyncTerminalGeometry* 直读）。
type GeometrySyncPort interface {
	// RequestGeometrySync 请求一次几何同步。minInterval > 0 且距上次探针不足
	// 该间隔时跳过（sizeChanged=false, probed=false，不做任何终端访问）；
	// 否则执行探针并返回 sizeChanged（相对上次已应用布局的宽/高变化）与
	// probed=true。
	RequestGeometrySync(minInterval time.Duration) (sizeChanged, probed bool)
	// ActiveBandWidth 返回 ActiveBand 视口的缓存终端宽度（未知时 0）。
	ActiveBandWidth() int
}

// 编译期锁定：*FixedBottomSurface 是几何门面的实现者。
var _ GeometrySyncPort = (*FixedBottomSurface)(nil)

// RequestGeometrySync 实现 GeometrySyncPort：直接复用 syncTerminalGeometry
// 的节流 + RefreshSize + applyLayoutWithSizeLocked 语义。
func (s *FixedBottomSurface) RequestGeometrySync(minInterval time.Duration) (sizeChanged, probed bool) {
	return s.syncTerminalGeometry(minInterval)
}

// ActiveBandWidth 实现 GeometrySyncPort：读取 ActiveBandViewportSize 的宽度。
func (s *FixedBottomSurface) ActiveBandWidth() int {
	width, _ := s.ActiveBandViewportSize()
	return width
}

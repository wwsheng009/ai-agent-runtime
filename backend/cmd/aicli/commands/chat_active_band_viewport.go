package commands

import "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"

// currentActiveBandViewportPort 读取已注入的 ActiveBand 视口门面（L5-2b）。写点是
// SetSurface/Shutdown（c.mu 下经原子指针发布），读取点分布在流式管线（多数持
// c.mu），用原子读避免与 surface 生命周期切换竞争。
func (c *chatInteractionCoordinator) currentActiveBandViewportPort() ui.ActiveBandViewportPort {
	if c == nil {
		return nil
	}
	if slot := c.activeBandViewportPort.Load(); slot != nil {
		return *slot
	}
	return nil
}

// activeBandViewport 解析会话 ActiveBand 视口门面（L5-2b：方案 §2「渲染链接管
// 布局宽度」）。
//
// 解析顺序：
//  1. coordinator 已注入的 ui.ActiveBandViewportPort（SetSurface 生命周期绑定）：
//     unified 走渲染链几何投影（geometry.Width + ActiveBandRows）、legacy 回落
//     surface 终端缓存；
//  2. 未注入（测试/headless 直接设置 c.surface，未走 SetSurface）：按 legacy
//     语义构造 surface 本地门面；
//  3. surface 也不可用：no-op 端口（(0, ActiveBandMinRows)），调用安全。
//
// 始终返回非 nil 端口；调用点不得再直读 .Surface.ActiveBandViewportSize
// （门禁：TestChatGeometryFamilyDirectReadsFrozen）。调用方须持 c.mu 或已过
// SetSurface 发布点（注入端口为原子读，未注入时的 c.surface 读与既有 Locked
// 调用点口径一致）。
func (c *chatInteractionCoordinator) activeBandViewport() ui.ActiveBandViewportPort {
	if c == nil {
		return ui.NewActiveBandViewportPort(nil, nil)
	}
	if port := c.currentActiveBandViewportPort(); port != nil {
		return port
	}
	return ui.NewActiveBandViewportPort(c.surface, nil)
}

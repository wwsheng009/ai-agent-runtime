// presence.go 是在场策略链扩展点（docs/bots/10-presence-signals-design.md §3.2 /
// docs/bots/06-target-architecture.md §1 D1）：通知内核只依赖"是否有人机同屏的
// 查看者"这一事实，具体信号源（gateway console 登记表、runtime-server web SSE
// 订阅计数、aicli TUI 前台）由宿主适配成 Presence 实现。
//
// 语义（与现网冻结行为逐字一致）：在场 → **跳过投递**，且**不认领去重键**——
// 查看者离开后同一事件可再次投递（"切走补推一次"）。因此门禁必须发生在认领
// 去重键之前；实现本门的 Dispatcher 会据此短路（见 Dispatch）。
package notify

import "strings"

// Presence 是在场权威：报告 nodeID 上的 sessionID 当前是否存在本地前台查看者。
//
// 契约：
//   - 实现须并发安全，且**不得阻塞**（通知派发在热路径上）；
//   - 返回 false 表示"无人在看"（含无从判定），调用方按不在场放行；
//   - 不注入（nil）= 不做在场门，行为与接入前完全一致。
type Presence interface {
	Active(nodeID, sessionID string) bool
}

// PresenceFunc 把函数适配为 Presence（便于测试与轻量宿主）。
type PresenceFunc func(nodeID, sessionID string) bool

// Active 见 Presence 契约。
func (f PresenceFunc) Active(nodeID, sessionID string) bool {
	return f != nil && f(nodeID, sessionID)
}

// SetPresence 注入在场权威（nil = 关闭在场门，行为不变）。
func (d *Dispatcher) SetPresence(p Presence) { d.presence = p }

// presenceActive 是门禁判定：未注入、或事件缺少 node/session 定位时不做在场门
// ——缺定位无法判定，按"不在场"保守放行，宁可多推一次也不误吞通知。
func (d *Dispatcher) presenceActive(ev Event) bool {
	if d == nil || d.presence == nil {
		return false
	}
	node := strings.TrimSpace(ev.Node)
	session := strings.TrimSpace(ev.Session)
	if node == "" || session == "" {
		return false
	}
	return d.presence.Active(node, session)
}

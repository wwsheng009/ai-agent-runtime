package tunnel

import (
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

// ============================================================================
// 窗口动作队列（gateway 控制台 → 节点执行）
//
// 控制台要能对某个会话「拉起本地窗口 / 恢复 / 打开 / 关闭」。这里不加任何入站
// 监听：动作排队，随下一次 HEARTBEAT_ACK 的 Pending 下发，由节点自己在本机执行
// （与 P1-b 的 watch:/unwatch: 同一条通道、同一个形状）。
//
// 三条不变式：
//   - 动作字符串与解析口径都在 wire 层（wire.WindowActionName / ParseWindowAction），
//     gateway 与 agent 不可能各自演化出一套方言；
//   - 队列有界且按「动作+会话」去重：控制台被连点不会在节点上排出一串窗口；
//   - 连接断开即丢弃队列：过期的「关闭」绝不在重连后突然生效。
// ============================================================================

// windowActionQueueLimit 是每个节点最多堆积的待下发动作数。超过即拒绝（让操作者
// 知道自己点多了），而不是静默丢弃最新一条。
const windowActionQueueLimit = 16

// windowActionQueue 是「每节点一份」的待下发队列。
type windowActionQueue struct {
	mu     sync.Mutex
	byNode map[string][]string
}

func newWindowActionQueue() *windowActionQueue {
	return &windowActionQueue{byNode: map[string][]string{}}
}

// queue 追加一条动作；同样的动作已在队列里时视为重复（成功但不重复排队）。
// 队列满时返回 false。
func (q *windowActionQueue) queue(nodeID, action string) bool {
	if q == nil {
		return false
	}
	nodeID = strings.TrimSpace(nodeID)
	action = strings.TrimSpace(action)
	if nodeID == "" || action == "" {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, pending := range q.byNode[nodeID] {
		if pending == action {
			return true
		}
	}
	if len(q.byNode[nodeID]) >= windowActionQueueLimit {
		return false
	}
	q.byNode[nodeID] = append(q.byNode[nodeID], action)
	return true
}

// drain 取出并清空某节点的待下发动作（一次心跳只消费一次）。
func (q *windowActionQueue) drain(nodeID string) []string {
	if q == nil {
		return nil
	}
	nodeID = strings.TrimSpace(nodeID)
	q.mu.Lock()
	defer q.mu.Unlock()
	pending := q.byNode[nodeID]
	if len(pending) == 0 {
		return nil
	}
	delete(q.byNode, nodeID)
	return pending
}

// forget 丢弃某节点的全部待下发动作（连接关闭时调用，防止过期动作在重连后执行）。
func (q *windowActionQueue) forget(nodeID string) {
	if q == nil {
		return
	}
	nodeID = strings.TrimSpace(nodeID)
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.byNode, nodeID)
}

// pendingCount 返回某节点当前堆积量（测试与诊断用）。
func (q *windowActionQueue) pendingCount(nodeID string) int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.byNode[strings.TrimSpace(nodeID)])
}

// QueueWindowAction 把一条窗口动作排给某个节点，随下一次 HEARTBEAT_ACK 下发。
//
// 只有**活连接**才接受排队：节点离线时直接拒绝。否则一条「关闭」会在几小时后某次
// 无关的重连上突然执行——操作者早就忘了自己点过什么。
// 返回 (是否入队, 面向操作者的一句话原因)。
func (s *Server) QueueWindowAction(nodeID, kind, sessionID string) (bool, string) {
	if s == nil {
		return false, "tunnel server is not initialized"
	}
	nodeID = strings.TrimSpace(nodeID)
	kind = strings.ToLower(strings.TrimSpace(kind))
	if nodeID == "" {
		return false, "node_id is required"
	}
	if !wire.ValidWindowAction(kind) {
		return false, "unknown window action " + strings.TrimSpace(kind) + " (use open-terminal|resume|new|close)"
	}
	if _, live := s.session(nodeID); !live {
		return false, "node " + nodeID + " is offline (no live agent connection)"
	}
	if kind != wire.WindowActionNew && strings.TrimSpace(sessionID) == "" {
		return false, "session_id is required for window action " + kind
	}
	if !s.windowActions.queue(nodeID, wire.WindowActionName(kind, sessionID)) {
		return false, "too many pending window actions for node " + nodeID + "; retry later"
	}
	return true, ""
}

// TakePendingWindowActions 取出并清空某节点的待下发动作。名字里的 Take 是有副作用
// 的（消费掉队列）：它存在的意义是让宿主与测试能断言「一次心跳恰好交付一批」，
// 只读观察请用 pendingCount。
func (s *Server) TakePendingWindowActions(nodeID string) []string {
	if s == nil || s.windowActions == nil {
		return nil
	}
	return s.windowActions.drain(nodeID)
}

// turn 派发路由（P2 切片 B，docs/bots/09 §7.2 R1/R2a/R3 + §7.3 派发契约）：
//
//	poller 收到入站消息后按绑定状态决定「在哪执行」：
//	  R1  绑定存在且 NodeID 是活 mesh peer → 远程派发到该节点；
//	  R2a 绑定存在但 NodeID 掉线 → 切换 B（本切片先按"本机执行 + 回写新
//	      NodeID"的最小实现——完整接管语义（会话核对 + IM 提示）在切片 C）；
//	  R3  无绑定 → 按 runner_preference 选举（PickRunnerForUnbound）：
//	      选中本机 → 本地执行；选中远端 → 远程派发（auto-create 落绑定由
//	      既有 Handle/回写路径完成）。
//
// 本文件只做**决策**（纯逻辑，可测）：派发执行走 Deps.RemoteDispatch，
// 宿主注入（runtime-server/aicli 各自实现 HTTP 调用）；nil 即单机模式，
// 全部本地执行——存量行为零变化。
package connectors

import (
	"context"
	"strings"
	"time"
)

// DispatchCallTimeout 是单次跨节点派发的默认上限（v1 终态等待；runner 侧
// 一个 turn 可能跑数分钟——与 gateway_node invoke 的 180s 网关默认同量级，
// 宿主可按 provider 特性覆盖）。
const DispatchCallTimeout = 180 * time.Second

// DispatchRequest 是跨节点 turn 派发体（docs/bots/09 §7.3 契约，宿主内部
// 令牌鉴权；JSON 形状由宿主 HTTP 层编码，这里承载语义字段）。
type DispatchRequest struct {
	ConnectorID string
	Chat        string
	Text        string
	UserID      string
	UserName    string
	MessageID   string
	// Target 是派发目标（runner 节点寻址）。
	Target RunnerPeer
	// Session 是会话亲和引用：SessionID 非空 = runner 续用该会话（R1）；
	// 空 = runner 侧 auto-create（R3）。
	Session SessionRef
	// Command 标记命令回环：runner 走既有 CommandBackend 而非注入 prompt。
	Command bool
}

// DispatchResult 是派发终态（v1 远程只回终态，流式增量仅本机保留）。
type DispatchResult struct {
	Reply   []OutboundMessage
	Session SessionRef
}

// RunnerPlane 是本宿主作为 runner 接收派发的能力面（restapi
// POST /connectors/dispatch 的执行后端）：runtime-server 用 runtimeapi
// SubmitConnectorPrompt / CommandBackend 实现，aicli 用 localActorRegistry
// 同构实现；gateway 是纯 poller 不实现。
type RunnerPlane interface {
	// DispatchTurn 执行一轮派发：Command=false 注入 prompt（session 非空续用，
	// 空 auto-create）；Command=true 走命令回环（Text 形如 "/approve ..."）。
	// 返回 runner 侧实际会话（调用方回写绑定）与终态回复。
	DispatchTurn(ctx context.Context, req DispatchRequest) (DispatchResult, error)
}

// RunnerPeer 是一次选举的候选执行体（与 runner_preference.go 的
// runnerCandidate 同构但对外可见——宿主从 mesh peers 视图装配）。
type RunnerPeer struct {
	// HostKind：HostKindAICLI / HostKindRuntimeServer（gateway 是 poller，
	// 不作为 runner 候选）。
	HostKind string
	// NodeID 是 mesh 节点 id（派发目标寻址键；本机节点 = Deps.HostID 的
	// instance 段或裸 kind）。
	NodeID string
	// Local 报告该候选是否就是本进程（本机执行不走 RemoteDispatch）。
	Local bool
	// Alive 报告节点当前在线（stale 快照不参选）。
	Alive bool
}

// aliveRunnerPeers 把候选收敛到「活着的 runner」（gateway/掉线节点剔除）。
func aliveRunnerPeers(peers []RunnerPeer) []RunnerPeer {
	out := make([]RunnerPeer, 0, len(peers))
	for _, p := range peers {
		if p.Alive && (p.HostKind == HostKindAICLI || p.HostKind == HostKindRuntimeServer) {
			out = append(out, p)
		}
	}
	return out
}

// routeDecision 是 DecideRoute 的输出。
type routeDecision struct {
	// Remote 为 true 时派发到 Peer（R1/R3-remote）；false 本机执行。
	Remote bool
	Peer   RunnerPeer
	// Affinity 报告是否携带绑定会话（R1 亲和；R3 的 auto-create 为 false）。
	Affinity bool
	// Unbound 报告无绑定（R3）：runner 侧执行后由调用方落 auto-create 绑定。
	Unbound bool
	// Failover 报告 R2a（绑定 NodeID 掉线，本机接管继续服务该绑定会话）：
	// 执行体转移，绑定记录不变；调用方执行成功后更新绑定的 NodeID 并在 IM
	// 提示一次（D3 前半，docs/bots/09 §7.2）。
	Failover bool
}

// DecideRoute 按 09 §7.2 做路由决策（纯逻辑）：
//   - binding 非零且 Session.NodeID 指向活 peer → R1 远程亲和；
//   - binding 非零但 NodeID 不在活 peer 集（掉线/异机缺失）→ R2 最小实现：
//     本机执行（切片 C 升级为接管语义），Affinity=false；
//   - 绑定的 NodeID 就是本机 → 本机执行（Affinity=true，保持会话亲和）；
//   - 无绑定 → R3：本机在活 peer 中且偏好命中 → 本机；否则按偏好选举远端；
//     无任何活 runner → 本机兜底执行（连接器绝不因 mesh 缺席而拒收消息）。
//
// rrCursor/rrNext 是 round-robin 的游标入参/出参（调用方持久持有）。
func DecideRoute(preference string, peers []RunnerPeer, localHostID string, binding SessionRef, rrCursor int) (routeDecision, int) {
	localKind := HostKindOf(localHostID)
	alive := aliveRunnerPeers(peers)

	// 本机身份匹配：peer.NodeID == localHostID 或 NodeID 的 instance 段命中。
	isLocal := func(p RunnerPeer) bool {
		if p.Local {
			return true
		}
		if strings.EqualFold(strings.TrimSpace(p.NodeID), strings.TrimSpace(localHostID)) {
			return true
		}
		return localKind != "" && HostKindOf(p.NodeID) == localKind &&
			strings.Contains(strings.ToLower(p.NodeID), strings.ToLower(localKind))
	}

	// 有绑定：R1 / R2。
	if !binding.IsZero() {
		bindingNodeID := strings.TrimSpace(binding.NodeID)
		if bindingNodeID != "" {
			// 本机绑定快捷路径（先于 peers 查找）：绑定 NodeID 就是本机——
			// 实例唯一 HostID（aicli:node-x / runtime-server）或本机裸 kind
			// （runner 回传/单实例宿主格式）→ 直接本机亲和执行。
			// 背景（2026-10-15 真机 qq-bot1）：peers 快照可能不含 self 或使用
			// mesh 原始 node id，若不短路会把「绑定在本机」误判为「原节点
			// 掉线」，触发假 R2a（反复回写 NodeID + 向用户发接管提示）。
			if strings.EqualFold(bindingNodeID, strings.TrimSpace(localHostID)) ||
				(localKind != "" && strings.EqualFold(bindingNodeID, localKind)) {
				return routeDecision{Remote: false, Affinity: true}, rrCursor
			}
			for _, p := range alive {
				if strings.EqualFold(strings.TrimSpace(p.NodeID), strings.TrimSpace(binding.NodeID)) {
					if isLocal(p) {
						return routeDecision{Remote: false, Affinity: true}, rrCursor
					}
					// R1：亲和派发（远程继续服务该绑定会话）。
					return routeDecision{Remote: true, Peer: p, Affinity: true}, rrCursor
				}
			}
		}
		// R2a（D3 前半）：绑定目标不可达（掉线/为空）→ 本机接管继续服务该
		// 绑定会话（执行体转移，绑定记录不变）。mesh 在场（有其它活 runner）
		// 时置 Failover：调用方执行成功后回写 NodeID 并 IM 提示一次；纯单机
		// 模式（无 peers 视图）不提示——历史行为保持静默。
		return routeDecision{Remote: false, Affinity: true, Failover: len(alive) > 0}, rrCursor
	}

	// R3：无绑定。本机总是可用兜底；先按偏好选举（含本机候选）。
	candidates := make([]runnerCandidate, 0, len(alive))
	localInPeers := false
	for _, p := range alive {
		candidates = append(candidates, runnerCandidate{HostKind: p.HostKind, NodeID: p.NodeID, Alive: true})
		if isLocal(p) {
			localInPeers = true
		}
	}
	if !localInPeers {
		// 本机自己也是一个候选（peers 视图可能不含 self）。
		candidates = append(candidates, runnerCandidate{HostKind: localKind, NodeID: localHostID, Alive: true})
	}
	picked, rrNext, ok := PickRunnerForUnbound(preference, candidates, rrCursor)
	if !ok {
		// 无活 runner（理论上不可达：本机已兜底入列）——本机执行。
		return routeDecision{Remote: false, Unbound: true}, rrCursor
	}
	if isLocal(RunnerPeer{HostKind: picked.HostKind, NodeID: picked.NodeID}) {
		return routeDecision{Remote: false, Unbound: true}, rrNext
	}
	return routeDecision{
		Remote: true, Unbound: true,
		Peer: RunnerPeer{HostKind: picked.HostKind, NodeID: picked.NodeID, Alive: true},
	}, rrNext
}

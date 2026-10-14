// 宿主身份（docs/bots/09 §7.5 节点身份，2026-10-14 P2 切片 A）：
//
// 	HostID   = "<kind>" 或 "<kind>:<instance>"——实例唯一（归属账本 owners 的
//	           Owner 值、P2 NodeID 亲和的判定键）。
//	HostKind = 宿主种类（aicli | runtime-server | gateway）——与
//	           policy.runtime_owner 的词表对齐（用户配置的是"哪种宿主可认领"，
//	           不是"哪个实例"）。
//
// 为什么拆两个字段：aicli 宿主 HostID 实例化（`aicli:<mesh-node-id>`，同机多
// TUI 各自唯一）后，存量 `runtime_owner: aicli` 配置若仍按 HostID 严格相等
// 匹配会失配、连接器拒绝认领。policy 匹配改按 Kind 后，裸词与 `kind:instance`
// 两种 HostID 都命中——存量配置零改动。
package connectors

import "strings"

// HostKind 取值（宿主种类词表；与 policy.runtime_owner / HostID 前缀同词表）。
const (
	HostKindAICLI         = "aicli"
	HostKindRuntimeServer = "runtime-server"
	HostKindGateway       = "gateway"
)

// hostIDKindSeparator 分隔 HostID 的 kind 与 instance 段。
const hostIDKindSeparator = ":"

// BuildHostID 组装实例唯一 HostID：instance 为空时退回裸 kind（单实例宿主
// 如 runtime-server/gateway 的形态，与历史值完全一致）。
func BuildHostID(kind, instance string) string {
	kind = strings.TrimSpace(kind)
	instance = strings.TrimSpace(instance)
	if kind == "" {
		return ""
	}
	if instance == "" {
		return kind
	}
	return kind + hostIDKindSeparator + instance
}

// HostKindOf 从 HostID 提取宿主种类：`aicli:node-x` → `aicli`；裸词原样返回；
// 空值返回空串。
func HostKindOf(hostID string) string {
	hostID = strings.TrimSpace(hostID)
	if hostID == "" {
		return ""
	}
	if idx := strings.Index(hostID, hostIDKindSeparator); idx > 0 {
		return strings.TrimSpace(hostID[:idx])
	}
	return hostID
}

// HostMatchesPolicy 报告宿主是否满足 policy.runtime_owner 准入（service.go
// 的认领/接管两处共用；语义 docs/bots/07-config-ux.md §5.6 + 09 §7.5）：
//   - 空/auto = 任何宿主；
//   - 其它值按 HostKind 匹配（大小写不敏感）——裸 HostID 与 `kind:instance`
//     形态都命中，保证 HostID 实例化不破坏存量 runtime_owner 配置。
func HostMatchesPolicy(hostID, policyOwner string) bool {
	policyOwner = strings.TrimSpace(policyOwner)
	if policyOwner == "" || strings.EqualFold(policyOwner, "auto") {
		return true
	}
	return strings.EqualFold(HostKindOf(hostID), policyOwner)
}

// mesh peer 记录词表（docs/bots/09 §7.5 切片 A：kind + capabilities 统一，
// 与 internal/mesh NodeRecord.Kind/Capabilities 字段对齐——这些常量描述
// 连接器视角的语义，mesh 包自身不感知 connectors）。
const (
	// MeshPeerKindAICLI 是 aicli chat 节点的 mesh Kind 值（gateway REGISTER
	// 与 node record 同词）。
	MeshPeerKindAICLI = "aicli"
	// MeshPeerKindRuntimeServer 是 runtime-server 节点的 mesh Kind 值（P2 入网
	// 后写入 node record）。
	MeshPeerKindRuntimeServer = "runtime-server"
	// MeshCapabilityRunner 声明节点可作为 runner 执行 connector turn（P2 派发
	// 的候选过滤位；`runner_preference` 选举只考虑带该位的活节点）。
	MeshCapabilityRunner = "runner"
	// MeshCapabilityPoller 声明节点正在持有该通道的轮询归属（凭据与出站都在
	// poller 侧；通知收口 §7.4 按它判定）。
	MeshCapabilityPoller = "poller"
)

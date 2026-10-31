package connectors

import (
	"os"
	"strings"
	"sync"
)

// EnvNotificationOwner 是节点侧「通知出站归属」的**可选**覆盖开关：
//
//	AICLI_CONNECTOR_NOTIFY_OWNER=auto / 未设置（默认）
//	  -> 自动模式：按网关下发的 NotifySuppress 规则，只关闭与网关
//	     source=node target 重叠的本地自动发送；其余通道仍本地发送。
//	AICLI_CONNECTOR_NOTIFY_OWNER=gateway
//	  -> 强制关闭本机全部 attention 通知（排障/特殊部署用）。
//	AICLI_CONNECTOR_NOTIFY_OWNER=node
//	  -> 强制忽略网关抑制规则，全部由本地 bot 发送（独立部署/排障用）。
//
// 默认 auto 不需要任何环境变量：网关把 source=node target 推导成
// (session, kind) 规则，经 REGISTER_ACK/HEARTBEAT_ACK 下发；节点断线时
// agent 清空规则并回退本地发送，不会因为网关不可达而丢通知。
const EnvNotificationOwner = "AICLI_CONNECTOR_NOTIFY_OWNER"

// NotificationsOwnerFromEnv 归一化开关值；未知值回落 auto（自动）。
func NotificationsOwnerFromEnv() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvNotificationOwner))) {
	case "gateway":
		return "gateway"
	case "node":
		return "node"
	default:
		return "auto"
	}
}

// NotifySuppressRule 是网关下发的本地通知抑制规则（与 wire 同义，
// 这里用中立类型避免 connectors 依赖网关协议包）。
//
//   - Kinds 为空 = 所有 kind；
//   - Sessions 为空 = 全部会话；非空 = 仅列出的会话。
type NotifySuppressRule struct {
	Kinds    []string
	Sessions []string
}

var gatewayNotifySuppress struct {
	mu    sync.RWMutex
	rules []NotifySuppressRule
}

// SetGatewayNotifySuppress 替换当前网关下发的抑制规则集（进程级）。
// 传 nil/空表示网关不再接管任何本地通知（断线/无 target）。
func SetGatewayNotifySuppress(rules []NotifySuppressRule) {
	next := make([]NotifySuppressRule, 0, len(rules))
	for _, rule := range rules {
		copied := NotifySuppressRule{
			Kinds:    normalizeRuleStrings(rule.Kinds),
			Sessions: normalizeRuleStrings(rule.Sessions),
		}
		if len(copied.Kinds) == 0 && len(copied.Sessions) == 0 {
			// 空规则 = 抑制全部；这是合法且最宽的语义，必须保留一条。
			next = append(next, copied)
			continue
		}
		next = append(next, copied)
	}
	gatewayNotifySuppress.mu.Lock()
	gatewayNotifySuppress.rules = next
	gatewayNotifySuppress.mu.Unlock()
}

// GatewaySuppresses 报告某个本地自动通知是否已被网关接管。
// owner=node 强制返回 false；owner=gateway 强制返回 true；auto 按规则匹配。
func GatewaySuppresses(sessionID, kind string) bool {
	switch NotificationsOwnerFromEnv() {
	case "node":
		return false
	case "gateway":
		return true
	}
	kind = normalizeNotifyKind(kind)
	sessionID = strings.TrimSpace(sessionID)
	gatewayNotifySuppress.mu.RLock()
	defer gatewayNotifySuppress.mu.RUnlock()
	for _, rule := range gatewayNotifySuppress.rules {
		if !ruleMatchesSession(rule, sessionID) {
			continue
		}
		if len(rule.Kinds) == 0 {
			return true
		}
		for _, ruleKind := range rule.Kinds {
			if normalizeNotifyKind(ruleKind) == kind {
				return true
			}
		}
	}
	return false
}

// ResetGatewayNotifySuppressForTest 清空进程级规则（测试隔离用）。
func ResetGatewayNotifySuppressForTest() {
	gatewayNotifySuppress.mu.Lock()
	gatewayNotifySuppress.rules = nil
	gatewayNotifySuppress.mu.Unlock()
}

// normalizeNotifyKind 把本地/网关两套词表收敛到网关词表：
// 本地 completion 与网关 completed 指同一事件。
func normalizeNotifyKind(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "completion" {
		return "completed"
	}
	return kind
}

func normalizeRuleStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, dup := seen[item]; dup {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func ruleMatchesSession(rule NotifySuppressRule, sessionID string) bool {
	if len(rule.Sessions) == 0 {
		return true
	}
	for _, id := range rule.Sessions {
		if strings.EqualFold(strings.TrimSpace(id), sessionID) {
			return true
		}
	}
	return false
}

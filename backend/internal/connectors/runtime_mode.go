package connectors

import "strings"

// 本文件定义连接器的宿主运行形态与入站策略（docs/gateway/18 §7.3 C2）。
//
// Config.RuntimeMode 有三个取值：
//   - ""（默认）：宿主正常激活 + 轮询/webhook，入站经 Backend 投递给 agent；
//   - "outbound"：只出站副本——不 Activate、不轮询/建长连接/注册 webhook，
//     仅 SendOutbound 可用（网关从节点复制 bot 配置后跑的第二个实例）；
//   - "gateway"：网关自持——宿主本机激活 + 轮询入站，会话上下文
//     （微信 context_token）由**本机**产出并持久化，出站同理由本机执行。
//     「gateway」与默认值的运行行为一致，区别在语义与配套策略：自持连接器
//     不再从源节点同步上下文（见 cmd/gateway/node_connector_handoff.go），
//     因此通知/回复不依赖节点在线。
const (
	// RuntimeModeDefault 是默认运行形态（正常轮询/webhook）。
	RuntimeModeDefault = ""
	// RuntimeModeOutbound 是只出站副本形态。
	RuntimeModeOutbound = "outbound"
	// RuntimeModeGateway 是网关自持形态。
	RuntimeModeGateway = "gateway"
)

// IsOutboundOnly 报告该连接器只允许出站（不激活/不轮询/不注册 webhook）。
func (c Config) IsOutboundOnly() bool {
	return strings.EqualFold(strings.TrimSpace(c.RuntimeMode), RuntimeModeOutbound)
}

// IsSelfHosted 报告该连接器由本机自持运行（网关自持形态）：「收 + 发」都在
// 本宿主，会话上下文本地产出，不再依赖任何节点的状态同步。
func (c Config) IsSelfHosted() bool {
	return strings.EqualFold(strings.TrimSpace(c.RuntimeMode), RuntimeModeGateway)
}

// 入站策略（settings.inbound_mode）取值：自持连接器可用它把「只为刷新会话
// 上下文而收」的通道从 agent 投递中摘出来（例如纯通知 bot，没有 agent 可接）。
const (
	// InboundModeSettingKey 是策略键名（settings，未知键透传，无需改 Schema）。
	InboundModeSettingKey = "inbound_mode"
	// InboundModeAgent 是默认策略：入站经 Backend 投递给 agent。
	InboundModeAgent = "agent"
	// InboundModeNotifyOnly 是「只为通知」策略：入站只用于刷新会话上下文
	// （微信 context_token 等），不投递给 agent，也不回「处理失败」文案。
	InboundModeNotifyOnly = "notify-only"
)

// InboundMode 返回本连接器生效的入站策略（缺省 agent）。
func (c Config) InboundMode() string {
	value := strings.TrimSpace(c.Setting(InboundModeSettingKey, InboundModeAgent))
	if strings.EqualFold(value, InboundModeNotifyOnly) {
		return InboundModeNotifyOnly
	}
	return InboundModeAgent
}

// InboundNotifyOnly 报告入站是否只用于刷新会话上下文（不投递 agent）。
// 仅自持连接器允许该语义：非自持连接器设置同值不生效，避免节点侧误配后
// 静默吞掉对话消息。
func (c Config) InboundNotifyOnly() bool {
	return c.IsSelfHosted() && c.InboundMode() == InboundModeNotifyOnly
}

// NormalizeRuntimeMode 规整管理面提交的运行形态，返回规整值与是否合法。
// 未识别的值一律判为非法，由调用方回 400（不做静默降级：静默降级会让
// 「看起来配好了」与「实际仍在抢入站」并存）。
func NormalizeRuntimeMode(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case RuntimeModeDefault:
		return RuntimeModeDefault, true
	case RuntimeModeOutbound:
		return RuntimeModeOutbound, true
	case RuntimeModeGateway:
		return RuntimeModeGateway, true
	default:
		return "", false
	}
}
// Package connectors 是 IM 连接器核心：把外部 IM 消息变成宿主 agent 的一次交互，
// 并把回复发回 IM。
//
// 设计见 docs/bots/02-solution-design.md §2（两层解耦）：
//   - Provider：通道适配（收/发/鉴权/重连），按能力实现可选接口（Polling/Typing/Streaming）；
//   - Backend：宿主适配（runtime-server / aicli / gateway 各一份），把消息交给 agent；
//   - 两者通过 InboundMessage / OutboundMessage / BackendRequest / BackendResult 数据契约连接。
//
// P0 范围（docs/bots/04-migration-roadmap.md）：核心 + Telegram（polling）+ 假后端端到端；
// 命令全集、媒体、限速、webhook 入口、宿主接线在后续阶段增量补齐。
package connectors

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/api"
)

// Config 是一个通道实例的运行配置（来自宿主存储，密钥已展开）。
type Config struct {
	ID       string
	Provider string
	Name     string
	Enabled  bool
	Mode     string // polling | webhook（由 Schema.Modes 定义）
	// CreatedAt/UpdatedAt 是配置生命周期时间戳（UTC；零值 = 旧数据未打戳，
	// 视图/落盘均省略，读取方需容忍缺省）。additive：宿主创建时打戳，
	// 更新路径只动 UpdatedAt，不改写 CreatedAt。
	CreatedAt time.Time
	UpdatedAt time.Time
	Settings  map[string]string
	Secrets   map[string]string
	Policy    Policy
}

// Secret 读取密钥（大小写不敏感的空值处理）。
func (c Config) Secret(key string) string {
	if c.Secrets == nil {
		return ""
	}
	return strings.TrimSpace(c.Secrets[key])
}

// Setting 读取普通配置，缺省回退到 fallback。
func (c Config) Setting(key, fallback string) string {
	if c.Settings == nil {
		return fallback
	}
	if value := strings.TrimSpace(c.Settings[key]); value != "" {
		return value
	}
	return fallback
}

// Policy 是"用户操作逻辑"配置（docs/bots/07-config-ux.md §4.2）。
type Policy struct {
	// CommandPrefix 命令前缀，默认 "/"。
	CommandPrefix string
	// AllowedUsers 非空时仅这些平台用户可对话（空=不限制，宿主可在外层再收紧）。
	AllowedUsers []string
	// SessionMode：chat（1 IM 会话 ↔ 1 agent 会话，默认）| thread（未来）。
	SessionMode string
	// ReplyMode：stream（默认，尽量流式编辑）| final（只发终态）。
	ReplyMode string
	// RuntimeOwner：运行归属（auto|aicli|runtime-server|gateway）：空/auto = 先到先得，
	// 显式值仅身份匹配的宿主可认领（心跳超时可接管，见 docs/bots/07-config-ux.md §5.6）。
	RuntimeOwner string
	// Notify 是事件通知接收配置（docs/bots/09-multi-instance-binding.md §7.4.1 D5）。
	// 零值 = 不接收任何事件通知（与历史行为一致；零配置零打扰）。
	Notify NotifyPolicy
	// Route 是连接器级模型路由（docs/bots/09 §5.5 补充定案 D6）：配置后覆盖
	// 全局默认 provider/model/reasoning effort，作用于本 bot 的**新回合**；
	// 已绑定的既有会话不迁移（上下文保持，仅执行参数变化）。
	// 零值 = 未配置，回落宿主默认（runtimeConfig.Agent.Default*）。
	Route RoutePolicy
	// ContentMirror（S1a，docs/bots/13-event-subscription-routing-design.md §8）：
	// 本地来源回合（TUI/Web/API/系统）的终态正文是否作为副本投递到该 bot 的
	// 绑定 chats（订阅平面 content 形态；零值 = 关，与 Notify 注意力独立）。
	// IM 来源回合的正文走回复路径，不在此镜像（13 §4.2 L3）。
	ContentMirror bool
	// Events 是声明式事件订阅（S2，docs/bots/13 §3.4/§6.1）：按
	// 「事件类型 × 会话选择器 × 投递形态」描述本 bot 要接收什么。
	// 未配置（或 enabled=false）时回退 legacy（Notify + ContentMirror），
	// 双读单写（13 §4.1）。
	Events EventsPolicy
}

// RoutePolicy 是连接器级模型路由（D6）。三项均可独立省略：省略项回落
// 宿主全局默认，非空项覆盖。
type RoutePolicy struct {
	// Provider 为空 = 用全局默认 provider。
	Provider string
	// Model 为空 = 用（覆盖后 provider 的）默认模型。
	Model string
	// ReasoningEffort 为空 = 用会话/全局默认推理档位。
	ReasoningEffort string
}

// EventsPolicy 是声明式事件订阅的容器（S2，docs/bots/13 §3.4/§6.1）。
type EventsPolicy struct {
	// Enabled 是总开关；与 Subscriptions 同时满足时才走新模型。
	Enabled bool
	// Subscriptions 是订阅列表；非法项（空 types/未知模式/ids 缺失）在求值时被忽略。
	Subscriptions []EventSubscription
}

// Configured 报告是否启用声明式订阅模型（enabled 且有订阅）。
// 未配置时调用方回退 legacy（Notify/ContentMirror）——双读单写（13 §4.1）。
func (p EventsPolicy) Configured() bool {
	return p.Enabled && len(p.Subscriptions) > 0
}

// EventSubscription 是一条声明式订阅（13 §3.4）。
type EventSubscription struct {
	// ID 是稳定标识（诊断/审计用；可空，空时按序号生成）。
	ID string
	// Enabled 控制单条订阅启停（S4）：nil/true = 启用（缺省），false = 显式停用。
	Enabled *bool
	// Sessions 是会话选择器：bound（默认）| ids | all。
	Sessions SessionSelector
	// Types 是事件类型集合，支持族通配（"turn.*"）；必填（空订阅被忽略）。
	// 规范类型与别名见 subscriptions.go 的事件词表。
	Types []string
	// Deliver 是投递形态：notice（默认）| content | digest | stream。
	// digest 仅 completion 走聚合（交互类按即时 notice 处理）；stream 仅
	// assistant_delta 类事件（占位消息 + 节流编辑，13 §3.4/§7.1 S4）。
	Deliver string
}

// IsEnabled 报告单条订阅是否启用（缺省启用；显式 false 停用）。
func (s EventSubscription) IsEnabled() bool {
	return s.Enabled == nil || *s.Enabled
}

// SessionSelector 是订阅的会话范围（13 §3.4）。
type SessionSelector struct {
	// Mode：bound（默认，绑定到本 bot 的会话）| ids | all。
	Mode string
	// IDs：mode=ids 时的显式会话列表（支持"多会话 → 1 个 bot"）。
	IDs []string
}

// HasOverride 报告该路由策略是否包含任一覆盖项。
func (r RoutePolicy) HasOverride() bool {
	return strings.TrimSpace(r.Provider) != "" ||
		strings.TrimSpace(r.Model) != "" ||
		strings.TrimSpace(r.ReasoningEffort) != ""
}

// NotifyPolicy 描述 bot 的事件通知接收（D5）。
type NotifyPolicy struct {
	// Enabled 总开关；false（含缺省）时本 bot 不接收任何事件通知。
	Enabled bool
	// Kinds 是接收的事件类型白名单（approval|question|completion|error，
	// 与 AttentionKind* 同名）；空 = 全部类型。
	Kinds []string
	// Scope 限定通知来源会话：sessions（默认，仅已绑定到本 bot 的会话）|
	// all（宿主内全部会话，广播模式）。
	Scope string
}

const (
	// NotifyScopeSessions 是 D5 的默认 scope：只收绑定到本 bot 的会话事件。
	NotifyScopeSessions = "sessions"
	// NotifyScopeAll 是广播 scope：收宿主内全部会话事件（显式开启）。
	NotifyScopeAll = "all"
)

// AcceptsNotice 报告本连接器是否接收该条注意力通知（D5 过滤点，宿主在事件
// 映射后、入队前调用）。
//
// 语义分层（docs/bots/09 §7.4.1）：
//   - 事件会话**已绑定**到本连接器：approval/question（命令回环）始终接收；
//     completion（回执类提示）与 notify 总开关联动——2026-10-14 真机反馈
//     「每条回复尾随 ✅ 回合已完成」噪音后收口为默认静音（零配置零打扰）；
//   - 事件会话**未绑定**（即"其它会话"的事件）→ 需要 notify.enabled=true 且
//     kinds 命中且 scope=all（scope=sessions 天然排除未绑定会话）。
//
// boundHere 报告事件会话是否绑定到本连接器；为 nil 时按"未绑定"处理。
func (p Policy) AcceptsNotice(notice AttentionNotice, boundHere func(session SessionRef) bool) bool {
	if boundHere != nil && boundHere(notice.Session) {
		// 绑定会话：approval/question（命令回环）不受开关影响；completion 属
		// 回执类提示，与 notify 总开关联动（默认关闭 = 零配置零打扰）。
		if notice.Kind == AttentionKindCompletion {
			return p.Notify.Enabled
		}
		return true
	}
	// 其它会话的事件：D5 显式开启才接收。
	if !p.Notify.Enabled {
		return false
	}
	if len(p.Notify.Kinds) > 0 {
		allowed := false
		for _, kind := range p.Notify.Kinds {
			if strings.EqualFold(strings.TrimSpace(kind), strings.TrimSpace(notice.Kind)) {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	return strings.EqualFold(strings.TrimSpace(p.Notify.Scope), NotifyScopeAll)
}

func (p Policy) commandPrefix() string {
	prefix := strings.TrimSpace(p.CommandPrefix)
	if prefix == "" {
		return "/"
	}
	return prefix
}

func (p Policy) streamingEnabled() bool {
	return !strings.EqualFold(strings.TrimSpace(p.ReplyMode), "final")
}

// AllowsUser 报告该平台用户是否允许使用。
//
// 语义（2026-10-15 调整）：allowed_users 为空 = 不限制（放行任意平台用户）；
// 非空时仅命中清单的平台用户可对话。此前「空 = 全拒绝」的语义会把新启用的
// 连接器变成对所有人静默拒收（且平台用户标识对用户不可预知，如 QQ 的
// user_openid），运维成本高于风险，故收敛为「空 = 放行」。
func (p Policy) AllowsUser(userID string) bool {
	if len(p.AllowedUsers) == 0 {
		return true
	}
	userID = strings.TrimSpace(userID)
	for _, allowed := range p.AllowedUsers {
		if strings.TrimSpace(allowed) == userID {
			return true
		}
	}
	return false
}

// Media 是消息附件的最小描述（P0 只透传元数据，下载/转码在后续阶段）。
type Media struct {
	Kind        string // image | voice | video | file
	FileID      string
	FileName    string
	ContentType string
	Size        int64
	// URL 是远端媒体地址（provider 发送时按需下载）；与 Path 二选一。
	URL string
	// Path 是本地媒体绝对路径（file:// 前缀会被 provider 解析为本地路径）。
	Path string
}

// InboundMessage 是通道无关的入站消息。
type InboundMessage struct {
	ConnectorID string
	Provider    string
	// Chat 是 provider 内稳定的会话标识（如 telegram:<chat_id>）。
	Chat      string
	ChatTitle string
	MessageID string
	// DedupeKey 是通道私有的幂等后缀（可空）。与 MessageID 一起构成入站去重
	// 键：QQ 官方要求同 id 的重复推送结合 message_scene.ext 的 msg_idx 去重，
	// 仅靠 MessageID 会把同 id 的不同推送误判为重复。其它通道留空即沿用
	// chat|messageID 的既有口径。
	DedupeKey string
	UserID    string
	UserName  string
	Text      string
	Media     []Media
	// ProviderData 是通道私有逃生舱（如 reply 目标、原始 update）。
	ProviderData map[string]any
	ReceivedAt   time.Time
}

// OutboundMessage 是通道无关的出站消息。
type OutboundMessage struct {
	Text  string
	Media []Media
}

// SessionRef 指向宿主 agent 会话（gateway 场景额外带 NodeID）。
type SessionRef struct {
	NodeID    string
	SessionID string
}

// IsZero 报告未绑定任何会话。
func (r SessionRef) IsZero() bool { return r.NodeID == "" && r.SessionID == "" }

// Binding 是 IM 会话 → 宿主会话的映射。
type Binding struct {
	ConnectorID string
	Chat        string
	Session     SessionRef
	UpdatedAt   time.Time
}

// Activation 是 Provider 激活（校验凭据/注册回调）的结果：
// 可回填 Settings/Secrets（如飞书 webhook_url、Bot 用户名），Info 用于日志/UI。
type Activation struct {
	Settings map[string]string
	Secrets  map[string]string
	Info     string
}

// Provider 是通道适配器（第二层解耦的传输侧）。
type Provider interface {
	// Name 是通道标识（telegram/feishu/qqbot/wechat），与 Config.Provider 对应。
	Name() string
	// Schema 是该通道的配置面（三端前端渲染的唯一来源）。
	Schema() api.ProviderSchema
	// Activate 校验凭据并在需要时注册回调。
	Activate(ctx context.Context, cfg Config) (Activation, error)
	// Deactivate 注销回调/释放资源（可空实现）。
	Deactivate(ctx context.Context, cfg Config) error
	// Send 向指定 IM 会话发送消息（幂等由上层账本保证）。
	Send(ctx context.Context, cfg Config, chat string, msgs []OutboundMessage) error
}

// InboundHandler 是轮询/WS 解析出入站消息后的回调（失败可由实现重试）。
type InboundHandler func(ctx context.Context, msg InboundMessage) error

// PollingState 是轮询游标的持久化视图（Telegram offset / 微信 buf / QQ seq）。
type PollingState interface {
	Get(key string) (string, bool)
	Set(key, value string) error
}

// PollingProvider 是长轮询/长连接通道（Telegram polling、微信、QQ、飞书 WS）。
type PollingProvider interface {
	Provider
	// SupportsPolling 报告当前配置是否应启动轮询（如 mode=webhook 时返回 false）。
	SupportsPolling(cfg Config) bool
	// RunPolling 阻塞运行直至 ctx 取消；实现负责断线重连与游标续传。
	RunPolling(ctx context.Context, cfg Config, handle InboundHandler, state PollingState) error
}

// StateAwareProvider 是 Provider 的可选能力：宿主在启动轮询/出站发送前，把该
// 连接器的持久化状态视图（与 PollingState 同一 KV 命名空间）交给 provider，
// 用于跨重启记忆（如微信 context_token：重启后无需等会话下一条入站即可回复）。
//
// 实现约定：
//   - 同一 provider 实例可能服务多个连接器（按 connectorID 隔离）；
//   - 同一连接器可能被重复附着（宿主每次使用前调用）：实现应幂等覆盖；
//   - 不支持该能力的 Provider 不受影响（宿主探测后跳过）。
type StateAwareProvider interface {
	Provider
	AttachState(connectorID string, state PollingState)
}

// CredentialVerifier 是 Provider 的可选能力：宿主在**保存配置落盘前**用真实
// 请求校验凭据可用性（如 QQ 换取一次 App Access Token），把「保存成功但凭据
// 无效、重启后才发现」的静默失败前移到保存路径（调用方据此返回明确错误并
// 拒绝落盘）。
//
// 实现约定：
//   - 可能有网络调用；调用方负责超时（建议 ≤15s）；
//   - 返回 nil = 凭据可用；错误文本必须脱敏（app_secret 绝不回流到日志/API）；
//   - 只做「可用性校验」，不替代 Activate（后者负责注册回调/预热缓存/启动轮询）。
type CredentialVerifier interface {
	Provider
	// VerifyCredentials 校验 cfg 中凭据的可用性。
	VerifyCredentials(ctx context.Context, cfg Config) error
}

// FailedReply 是一次「回复投递失败」的快照（切片 2f：失败回复重放，微信
// errcode -2 的恢复路径）。宿主在实现 ReplyReplayProvider 的通道上记录快照；
// 用户随后发来重发意图时由 Service 用新一条入站带来的上下文重放。
type FailedReply struct {
	ConnectorID string
	Chat        string
	// MessageID 是触发该回复的入站消息 id（审计/日志用，可空）。
	MessageID string
	// Reply 是未能投递的回复快照（深拷贝语义由 Store 实现保证）。
	Reply []OutboundMessage
	// Error 是最近一次失败原因（截断，审计用）。
	Error string
	// FailedAt 是最近一次失败时间（UTC）。
	FailedAt time.Time
}

// ReplyReplayProvider 是 Provider 的可选能力（切片 2f）：声明该通道支持
// 「失败回复重放」——宿主在回复投递失败时记录快照（见 FailedReply），用户
// 随后发来重发意图时重放。Provider 只负责判定用户意图，账本与投递编排由
// Service 负责（实现参考微信：「重发/继续/retry」等短文本，排除带媒体消息）。
type ReplyReplayProvider interface {
	Provider
	// IsReplyReplayIntent 报告入站消息是否为「重发上次失败回复」意图。
	// 仅在该连接器存在失败快照且渠道匹配时被调用。
	IsReplyReplayIntent(msg InboundMessage) bool
}

// OutboundMediaNoteProvider 是 Provider 的可选能力：返回追加到入站文本尾部的
// 「如何回发媒体」通道提示（宿主在入站媒体物化后注入；空串 = 不注入）。
// 与 ParseOutboundMediaDirectives 配套：提示里的围栏块名必须是该通道接受的
// 名字（见 MediaDirectiveNote）。
type OutboundMediaNoteProvider interface {
	Provider
	OutboundMediaNote() string
}

// WebhookResult 是 webhook 入口的处理结果（二选一）：
//   - Message：解析出的入站消息，宿主交给 Service.HandleInbound；
//   - Response：直接应答体（如飞书 challenge 回显），宿主按 Status 返回（默认 200）。
type WebhookResult struct {
	Message  *InboundMessage
	Response any
	Status   int
}

// WebhookProvider 支持 HTTP webhook 入口（飞书事件、Telegram/Discord 回调、QQ 回调）。
type WebhookProvider interface {
	Provider
	// HandleWebhook 校验签名/挑战并解析请求；校验失败应返回 error（宿主转 4xx）。
	HandleWebhook(ctx context.Context, cfg Config, r *http.Request) (WebhookResult, error)
}

// ActionProvider 支持通道特有动作（由 ProviderSchema.Actions 声明，如微信/QQ 扫码登录）。
// 管理面 POST /connectors/{id}/actions/{action} 按 action 调用：
//   - action 来自 ProviderSchema.Actions 的 Key，宿主不额外解释；
//   - payload 是调用方透传的可选 JSON object（可为 nil）；
//   - 返回 JSON 可序列化载荷（如二维码图/链接/登录状态），宿主原样回 200；
//   - 返回 error 由宿主转 4xx（参数/凭据/上游失败）。
type ActionProvider interface {
	Provider
	RunAction(ctx context.Context, cfg Config, action string, payload map[string]any) (map[string]any, error)
}

// TypingSession 是一次"正在输入"保活。
type TypingSession interface {
	Stop(ctx context.Context) error
}

// TypingProvider 支持 typing 指示（Telegram sendChatAction / 微信 typing ticket）。
type TypingProvider interface {
	Provider
	StartTyping(ctx context.Context, cfg Config, chat string) (TypingSession, error)
}

// StreamingSession 是一次可编辑的流式回复。
//
// 契约（2026-10-17 核实）：
//   - Update 接收**累计全文**（当前回合已生成的全部可见正文），不是单个
//     token/增量。调用方（Service.replySink / StreamMirror）负责把
//     assistant_delta 累积后再传入。
//   - Telegram editMessageText、飞书卡片 PATCH、QQ stream_messages
//     （input_mode=replace）都是「以本次文本替换已发送内容」，因此 Provider
//     收到全量后直接替换；Provider 不做增量累积。
//   - Provider 必须保证已下发的可见文本不缩短（媒体指令块可能被剥离，见
//     QQ/Telegram/飞书的中间态处理），否则平台前缀约束会失败。
//   - Final/Fail 同样接收完整终态正文/提示，不是增量。
type StreamingSession interface {
	// Update 用累计全文更新已发送的占位消息（实现可自行节流/降级）。
	Update(ctx context.Context, text string) error
	// Final 以终态消息收尾（文本为完整终态正文）。
	Final(ctx context.Context, msgs []OutboundMessage) error
	// Fail 收尾为失败提示（text 为完整提示文本）。
	Fail(ctx context.Context, text string) error
}

// StreamingProvider 支持编辑式流式回复（Telegram editMessageText / 飞书卡片）。
type StreamingProvider interface {
	Provider
	StartStreaming(ctx context.Context, cfg Config, chat string) (StreamingSession, error)
}

// MessageRetractor 是可选能力：撤回机器人自己发送的消息（QQ 官方 2 分钟内）。
// 未实现该接口的通道不受影响；调用方应做能力探测。
type MessageRetractor interface {
	Provider
	// RetractMessage 撤回 chat 中 messageID 对应的机器人消息。
	RetractMessage(ctx context.Context, cfg Config, chat, messageID string) error
	// LastRecallableMessage 返回该会话最近一条仍在平台撤回窗口内的机器人
	// 消息 id；没有可用消息时 ok=false（不报错）。
	LastRecallableMessage(chat string) (messageID string, ok bool)
}

// ShareLinkProvider 是可选能力：生成机器人邀请/分享链接（QQ 官方
// POST /v2/generate_url_link）。未实现该接口的通道 `/invite` 会明确提示。
type ShareLinkProvider interface {
	Provider
	// GenerateShareLink 生成邀请链接；callbackData 可空（用户添加时回传）。
	GenerateShareLink(ctx context.Context, cfg Config, callbackData string) (string, error)
}

// BackendRequest 是交给宿主 agent 的一次交互请求。
type BackendRequest struct {
	ConnectorID string
	Chat        string
	Session     SessionRef // 已绑定会话；零值表示需要新建
	SessionMode string     // chat | thread（透传策略）
	Text        string
	UserID      string
	UserName    string
	MessageID   string
	// Policy 是本请求所属连接器的操作逻辑快照（宿主据此执行策略相关行为，
	// 如 D6 连接器级模型路由）。零值安全：宿主按空路由回落全局默认。
	Policy Policy
}

// BackendResult 是一次交互的结果。
type BackendResult struct {
	// Session 是本次交互所在的宿主会话（首次消息时由宿主创建）。
	Session SessionRef
	// Reply 是终态回复内容（可为空表示不回复）。
	Reply []OutboundMessage
}

// ReplySink 供 Backend 推送流式片段（可选；终态内容走 BackendResult.Reply）。
type ReplySink interface {
	Delta(ctx context.Context, text string) error
}

// Backend 是宿主 agent 适配（runtime-server / aicli / gateway 各实现一份）。
type Backend interface {
	Name() string
	Handle(ctx context.Context, req BackendRequest, sink ReplySink) (BackendResult, error)
}

// CommandRequest 是一条 IM 命令交给宿主的执行请求（可选能力，见 CommandBackend）。
//
// 职责边界：核心只负责解析命令名/参数、补齐当前绑定会话，并把请求转给宿主；
// request_id / 序号等参数的格式与含义完全由宿主解释（核心不校验、不解释）。
type CommandRequest struct {
	ConnectorID string
	Chat        string
	// Session 是当前 IM 会话绑定的宿主会话（未绑定时为零值）。
	Session   SessionRef
	UserID    string
	UserName  string
	MessageID string
	// Command 是规范化命令名（不含前缀）：approve | deny | answer。
	Command string
	// Args 是命令参数的空白分词，可能为空：
	//   - approve/deny：Args[0] 是可选 request_id；空 = “当前挂起项”语义；
	//   - answer：Args[0] 是序号或 request_id，其余是回答内容分词。
	Args []string
	// Text 是命令名之后的原始参数文本（保留内部空格，Args 的分词源）。
	Text string
}

// CommandResult 是宿主对一条命令的处理结果。
type CommandResult struct {
	// Handled 表示宿主已处理该命令；false = 没有可处理的挂起项或参数无法识别，
	// 核心会回一条明确提示（不静默、不落入普通 agent turn）。
	Handled bool
	// Reply 是回给 IM 的消息（可选；Handled 且为空时核心不回复）。
	Reply []OutboundMessage
}

// CommandBackend 是 Backend 的可选能力扩展：宿主实现后 /approve /deny /answer
// 才能在 IM 内闭环（docs/bots/01-requirements.md FR-7、03-host-integration.md §6）。
//
// 未实现时核心对这三条命令回明确提示，且不会把命令文本当作普通消息交给
// Backend.Handle。命令路径不计入普通 turn 计数（inbound/outbound stats）。
//
// 三宿主待接清单（03 §6）：
//   - runtime-server：映射到 session runtime 命令 approve_tool / answer_question；
//   - aicli：走 actor 的审批/提问响应通道（与 TUI 同一入口）；
//   - gateway：经隧道把命令转给节点会话（与入站 submit_prompt 同链路）。
type CommandBackend interface {
	Backend
	// DecideApproval 处理 /approve 与 /deny（Command 区分两者）。
	DecideApproval(ctx context.Context, req CommandRequest) (CommandResult, error)
	// AnswerQuestion 处理 /answer（Args[0] 为序号或 request_id，其余为回答内容）。
	AnswerQuestion(ctx context.Context, req CommandRequest) (CommandResult, error)
}

// BacktrackBackend 是可选能力：把 IM 会话回退到上一轮（对应 TUI /backtrack）。
//
// 语义（与 runtimechat.BacktrackRequest 对齐）：
//   - 默认回退「最近一轮」：删除最后一轮用户提问及其后的助手回复；
//   - TurnsBefore=1 表示保留最近一轮、回退到再上一轮（即删除最近两轮）；
//   - 只做 conversation 模式（IM 用户看不到代码 diff，不默认恢复代码）。
//
// 未实现该接口的宿主（当前 gateway）由核心回明确提示，不静默。
type BacktrackBackend interface {
	Backend
	Backtrack(ctx context.Context, req BacktrackRequest) (BacktrackResult, error)
}

// BacktrackRequest 是 IM 侧的回退请求（通道无关）。
type BacktrackRequest struct {
	ConnectorID string
	Chat        string
	// Session 是当前 IM 会话绑定的宿主会话（零值表示尚未绑定）。
	Session SessionRef
	// TurnsBefore 是要保留的最近轮数：0 = 回退最近一轮（默认）。
	TurnsBefore int
}

// BacktrackResult 是回退结果摘要。
type BacktrackResult struct {
	// Handled 表示宿主已执行回退；false = 没有可回退的轮次。
	Handled bool
	// RemovedTurns 是被删除的用户轮数。
	RemovedTurns int
	// AnchorPreview 是被回退那一轮的用户提问预览（用于回执）。
	AnchorPreview string
	// Reply 是给 IM 的回执文案（宿主可定制；空则由核心生成）。
	Reply string
}

// 注意力通知类型（AttentionNotice.Kind）。
const (
	AttentionKindApproval   = "approval"
	AttentionKindQuestion   = "question"
	AttentionKindCompletion = "completion"
	AttentionKindError      = "error"
	// 工作类聚合 kind（S4，13 §3.3）：工具/子代理事件只走 digest 形态投递
	//（逐条 notice 会刷屏；notice 订阅对工作类不生效，见 subscriptions.go）。
	AttentionKindTool     = "tool"
	AttentionKindSubagent = "subagent"
)

// AttentionNotice 是一条“注意力出站”事件（轻量结构，宿主构造后交给
// Service.NotifyAttention）。
//
// 职责边界：核心只做「按 Session 反查绑定 → 出站到各 IM」，不做优先级/免打扰/
// 合并/去重——同一事件是否已投递、是否与 Web Push/ntfy/收件箱重复，由调用方
// （或后续 notify 切片）负责（docs/bots/01-requirements.md FR-8）。
type AttentionNotice struct {
	// Kind 是通知类型：approval | question | completion | error
	// （仅用于日志与文案归属，核心不按类型分支）。
	Kind string
	// Session 是目标宿主会话（必填；核心按它反查全部 IM 绑定）。
	Session SessionRef
	// Text 是推送给 IM 的文本（必填）。
	Text string
}

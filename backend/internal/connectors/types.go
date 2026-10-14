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

// AllowsUser 报告该平台用户是否允许使用（白名单为空 = 不限制）。
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
type StreamingSession interface {
	// Update 用累积文本编辑已发送的占位消息（实现可自行节流/降级）。
	Update(ctx context.Context, text string) error
	// Final 以终态消息收尾（文内可追加最终内容）。
	Final(ctx context.Context, msgs []OutboundMessage) error
	// Fail 收尾为失败提示。
	Fail(ctx context.Context, text string) error
}

// StreamingProvider 支持编辑式流式回复（Telegram editMessageText / 飞书卡片）。
type StreamingProvider interface {
	Provider
	StartStreaming(ctx context.Context, cfg Config, chat string) (StreamingSession, error)
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

// 注意力通知类型（AttentionNotice.Kind）。
const (
	AttentionKindApproval   = "approval"
	AttentionKindQuestion   = "question"
	AttentionKindCompletion = "completion"
	AttentionKindError      = "error"
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

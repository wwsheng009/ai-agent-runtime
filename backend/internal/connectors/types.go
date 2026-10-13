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
	Settings map[string]string
	Secrets  map[string]string
	Policy   Policy
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
	// RuntimeOwner：运行归属（auto|aicli|runtime-server|gateway），P1 启用抢占。
	RuntimeOwner string
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

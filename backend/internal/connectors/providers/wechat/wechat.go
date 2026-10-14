// Package wechat 实现微信（iLink bot）通道适配器：长轮询 getupdates + 文本收发 +
// 扫码登录（ActionProvider）+ typing ticket。
//
// 设计见 docs/bots/04-migration-roadmap.md P3「微信 + 媒体/主动推送完善」。
// 切片 2b 范围：文本收发（getupdates 长轮询、sync_buf 游标持久化、sendmessage）、
// 扫码登录（start_login / login_status 两个 action，成功后以 settings/secrets
// 合并补丁回填）、typing（TypingProvider）、入站媒体降级为文本占位；
// 真媒体（CDN 上传/下载/转码）与 errcode -2 的失败重放留待 P3 后续切片。
//
// 协议细节镜像上游 codex-server/backend/internal/bots/wechat.go 与
// wechat_auth.go（getupdates/getconfig/sendmessage/sendtyping、get_bot_qrcode/
// get_qrcode_status、markdown 清洗），契约适配为 connectors.Provider /
// PollingProvider / ActionProvider / TypingProvider（无新增接口）。
package wechat

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/api"
)

// Name 是通道标识（与 connectors.Config.Provider 对应）。
const Name = "wechat"

const (
	// defaultLongPollTimeout 是 getupdates 的长轮询超时（服务端可通过响应里的
	// longpolling_timeout_ms 覆盖）。
	defaultLongPollTimeout = 35 * time.Second
	// longPollTimeoutBuf 是 HTTP 超时相对长轮询超时的额外余量。
	longPollTimeoutBuf = 5 * time.Second

	defaultHTTPTimeout = 15 * time.Second
	configHTTPTimeout  = 10 * time.Second

	// defaultBaseURL 是 iLink 官方默认基址：用户无需知道/填写，扫码登录直接可用
	// （上游 codex-server 的二维码登录同样默认此地址）；登录成功后按上游返回值回填。
	defaultBaseURL = "https://ilinkai.weixin.qq.com"

	defaultRetryDelay = time.Second
	maxRetryDelay     = 30 * time.Second

	// syncBufKey 是 PollingState 游标键：重启后从 get_updates_buf 续传。
	syncBufKey = "sync_buf"

	// channelVersion 透传到 base_info.channel_version 与 iLink-App-ClientVersion。
	channelVersion = "0.3.0"

	messageTypeUser      = 1
	messageStateComplete = 2
	messageTypeBot       = 2

	itemTypeText  = 1
	itemTypeImage = 2
	itemTypeVoice = 3
	itemTypeFile  = 4
	itemTypeVideo = 5

	typingStatusTyping   = 1
	typingStatusCancel   = 2
	typingKeepaliveDelay = 5 * time.Second
	typingConfigTTL      = 24 * time.Hour

	// sessionExpiredErrCode 是上游「会话过期，需要重新扫码」错误码（errcode/ret）。
	sessionExpiredErrCode = -14
	// replyContextInvalidRet 是 sendmessage 回复上下文失效错误码（P3 失败重放）。
	replyContextInvalidRet = -2

	// maxTrackedChats 是进程内 context_token 记忆的防御上限。
	maxTrackedChats = 4096

	contextTokenKey = "context_token"
	sessionIDKey    = "session_id"
	createdAtMSKey  = "created_at_ms"
)

// clientVersionHeader 是 iLink-App-ClientVersion（版本号按 0xMMmmpp 编码的十进制）。
var clientVersionHeader = strconv.FormatUint(uint64(buildClientVersion(channelVersion)), 10)

// ── 通道报文类型（镜像上游字段） ────────────────────────────────────────────

// apiEnvelope 是所有 iLink API 响应的公共错误头。
type apiEnvelope struct {
	Ret     int    `json:"ret"`
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

type getUpdatesResponse struct {
	apiEnvelope
	Msgs          []wechatMessage `json:"msgs"`
	GetUpdatesBuf string          `json:"get_updates_buf"`
	LongPollMS    int             `json:"longpolling_timeout_ms"`
}

type getConfigResponse struct {
	apiEnvelope
	TypingTicket string `json:"typing_ticket"`
}

// flexibleString 兼容 create_time_ms / len 等字段的数字或字符串形态。
type flexibleString string

func (v *flexibleString) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	switch trimmed {
	case "", "null":
		*v = ""
		return nil
	}
	if strings.HasPrefix(trimmed, `"`) {
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		*v = flexibleString(value)
		return nil
	}
	*v = flexibleString(trimmed)
	return nil
}

func (v flexibleString) String() string { return string(v) }

type wechatMessage struct {
	FromUserID   string              `json:"from_user_id"`
	ToUserID     string              `json:"to_user_id"`
	ClientID     string              `json:"client_id"`
	SessionID    string              `json:"session_id"`
	MessageType  int                 `json:"message_type"`
	MessageState int                 `json:"message_state"`
	ItemList     []wechatMessageItem `json:"item_list"`
	ContextToken string              `json:"context_token"`
	CreateTimeMS flexibleString      `json:"create_time_ms"`
}

type wechatMessageItem struct {
	Type      int                 `json:"type"`
	TextItem  *wechatTextItem     `json:"text_item"`
	ImageItem *wechatImageItem    `json:"image_item"`
	VoiceItem *wechatVoiceItem    `json:"voice_item"`
	FileItem  *wechatFileItem     `json:"file_item"`
	VideoItem *wechatVideoItem    `json:"video_item"`
	RefMsg    *wechatReferenceMsg `json:"ref_msg"`
}

type wechatTextItem struct {
	Text   string              `json:"text"`
	RefMsg *wechatReferenceMsg `json:"ref_msg"`
}

type wechatCDNMedia struct {
	EncryptQueryParam string `json:"encrypt_query_param"`
	AESKey            string `json:"aes_key"`
	EncryptType       int    `json:"encrypt_type"`
	FullURL           string `json:"full_url"`
}

type wechatImageItem struct {
	Media      *wechatCDNMedia `json:"media"`
	ThumbMedia *wechatCDNMedia `json:"thumb_media"`
	AESKeyHex  string          `json:"aeskey"`
	URL        string          `json:"url"`
	MidSize    int64           `json:"mid_size"`
}

type wechatVoiceItem struct {
	Media      *wechatCDNMedia `json:"media"`
	Text       string          `json:"text"`
	EncodeType int             `json:"encode_type"`
}

type wechatFileItem struct {
	Media    *wechatCDNMedia `json:"media"`
	FileName string          `json:"file_name"`
	Len      string          `json:"len"`
}

type wechatVideoItem struct {
	Media      *wechatCDNMedia `json:"media"`
	ThumbMedia *wechatCDNMedia `json:"thumb_media"`
	VideoSize  int64           `json:"video_size"`
}

type wechatReferenceMsg struct {
	Text        string             `json:"text"`
	Title       string             `json:"title"`
	MessageItem *wechatMessageItem `json:"message_item"`
}

type wechatSendMessageRequest struct {
	Msg      wechatOutboundMessage `json:"msg"`
	BaseInfo wechatBaseInfo        `json:"base_info"`
}

type wechatOutboundMessage struct {
	FromUserID   string              `json:"from_user_id"`
	ToUserID     string              `json:"to_user_id"`
	ClientID     string              `json:"client_id"`
	MessageType  int                 `json:"message_type"`
	MessageState int                 `json:"message_state"`
	ItemList     []wechatMessageItem `json:"item_list"`
	ContextToken string              `json:"context_token"`
}

type wechatBaseInfo struct {
	ChannelVersion string `json:"channel_version"`
}

// requestError 是微信 API 请求错误（描述文本已脱敏，绝不回传 bot_token）。
type requestError struct {
	method      string
	statusCode  int
	status      string
	description string
	cause       error
	apiRet      int
}

func (e *requestError) Error() string {
	if e == nil {
		return ""
	}
	if e.cause != nil {
		return fmt.Sprintf("wechat %s request failed: %v", e.method, e.cause)
	}

	detail := strings.TrimSpace(e.description)
	code := e.codeLabel()
	switch {
	case e.status != "" && detail != "" && code != "":
		return fmt.Sprintf("wechat %s returned %s (%s): %s", e.method, e.status, code, detail)
	case e.status != "" && detail != "":
		return fmt.Sprintf("wechat %s returned %s: %s", e.method, e.status, detail)
	case e.status != "" && code != "":
		return fmt.Sprintf("wechat %s returned %s (%s)", e.method, e.status, code)
	case e.status != "":
		return fmt.Sprintf("wechat %s returned %s", e.method, e.status)
	case detail != "" && code != "":
		return fmt.Sprintf("wechat %s api error (%s): %s", e.method, code, detail)
	case detail != "":
		return fmt.Sprintf("wechat %s api error: %s", e.method, detail)
	case code != "":
		return fmt.Sprintf("wechat %s request failed (%s)", e.method, code)
	default:
		return fmt.Sprintf("wechat %s request failed", e.method)
	}
}

func (e *requestError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *requestError) codeLabel() string {
	if e == nil {
		return ""
	}
	if strings.EqualFold(strings.TrimSpace(e.status), "api error") {
		if e.apiRet != 0 || e.statusCode != 0 {
			return fmt.Sprintf("ret=%d errcode=%d", e.apiRet, e.statusCode)
		}
		return ""
	}
	if e.statusCode != 0 {
		return fmt.Sprintf("code=%d", e.statusCode)
	}
	return ""
}

// ── Provider ────────────────────────────────────────────────────────────────

// Provider 是微信通道适配器；HTTP 细节可注入（测试用 httptest 假 iLink 服务）。
type Provider struct {
	// apiBase 是构造参数覆盖（测试替身/反向代理），优先于 Settings["base_url"]；
	// Settings["api_base"] 又优先于本字段（与 qqbot/telegram 的约定一致）。
	apiBase    string
	client     *http.Client
	retryDelay time.Duration
	logf       func(format string, args ...any)
	now        func() time.Time

	// mu 保护三类进程内状态：扫码登录态（按 connectorID）、最近 context_token
	// （按 connectorID+chat，回复必需）、typing ticket 缓存。
	mu       sync.Mutex
	logins   map[string]*loginSession
	contexts map[string]string
	typing   map[string]typingCacheEntry
}

type typingCacheEntry struct {
	ticket    string
	expiresAt time.Time
}

// Option 配置 Provider。
type Option func(*Provider)

// WithAPIBase 覆盖 iLink 基址（测试服务器/反向代理；优先于 Settings["base_url"]）。
func WithAPIBase(base string) Option {
	return func(p *Provider) { p.apiBase = strings.TrimRight(strings.TrimSpace(base), "/") }
}

// WithHTTPClient 注入 HTTP 客户端（测试/代理）。
func WithHTTPClient(client *http.Client) Option {
	return func(p *Provider) {
		if client != nil {
			p.client = client
		}
	}
}

// WithRetryDelay 覆盖断线重连退避基数（测试用）。
func WithRetryDelay(d time.Duration) Option {
	return func(p *Provider) {
		if d > 0 {
			p.retryDelay = d
		}
	}
}

// WithLogger 注入日志函数。
func WithLogger(logf func(format string, args ...any)) Option {
	return func(p *Provider) {
		if logf != nil {
			p.logf = logf
		}
	}
}

// New 构造微信 Provider。
func New(opts ...Option) *Provider {
	provider := &Provider{
		client:     &http.Client{},
		retryDelay: defaultRetryDelay,
		logf:       log.Printf,
		now:        time.Now,
		logins:     make(map[string]*loginSession),
		contexts:   make(map[string]string),
		typing:     make(map[string]typingCacheEntry),
	}
	for _, opt := range opts {
		opt(provider)
	}
	return provider
}

// Name 实现 connectors.Provider。
func (p *Provider) Name() string { return Name }

// Schema 是微信的配置面（三端前端据此渲染）。
//
// 字段镜像上游 wechat 配置（bot_token / base_url / account_id / owner_user_id /
// route_tag）并按本仓约定加 api_base 覆盖项；`bot_token`/`base_url` 在扫码登录
// 成功前可以为空（Schema 不标必填，Activate/RunPolling 时校验），登录成功由
// login_status 的返回值回填。
func (p *Provider) Schema() api.ProviderSchema {
	return api.ProviderSchema{
		Provider: Name,
		Label:    "微信",
		// 只声明已实现的能力：长轮询（getupdates）。微信无 webhook 入口。
		Modes: []string{"polling"},
		Fields: []api.FieldSchema{
			{
				Key: "bot_token", Label: "Bot Token", Type: api.FieldSecret,
				Placeholder: "扫码登录成功后自动回填",
				Help:        "iLink bot_token（只写不读）；也可在扫码登录前手动填写",
			},
			{
				Key: "base_url", Label: "iLink 基址", Type: api.FieldURL,
				Placeholder: defaultBaseURL,
				Default:     defaultBaseURL,
				Help:        "留空即用官方默认地址；扫码登录成功后按上游返回值回填",
			},
			{
				Key: "api_base", Label: "API 基址覆盖", Type: api.FieldURL,
				Help: "可留空；用于测试替身/反向代理，优先于 base_url",
			},
			{
				Key: "account_id", Label: "账号 ID", Type: api.FieldText,
				Help: "扫码登录成功后自动回填（ilink_bot_id）",
			},
			{
				Key: "owner_user_id", Label: "所有者用户 ID", Type: api.FieldText,
				Help: "扫码登录成功后自动回填（ilink_user_id）；配置后 Activate 会调用 getconfig 做轻量凭据校验",
			},
			{
				Key: "route_tag", Label: "路由标签", Type: api.FieldText,
				Help: "可留空；透传 SKRouteTag",
			},
			{
				Key: "mode", Label: "接入模式", Type: api.FieldEnum, Default: "polling",
				Options: []api.Option{{Value: "polling", Label: "长轮询"}},
			},
		},
		Actions: []api.ActionSpec{
			{Key: loginActionStart, Label: "扫码登录", Kind: "qr"},
			{Key: loginActionStatus, Label: "登录状态", Kind: "button"},
		},
	}
}

// ── 凭据与基址解析 ──────────────────────────────────────────────────────────

// tokenFor 解析 bot_token：连接器配置优先；配置尚未回填时回退到该连接器最近
// 一次已确认扫码登录的凭据（进程内缓存，24h 有效）。
func (p *Provider) tokenFor(cfg connectors.Config) string {
	if token := cfg.Secret("bot_token"); token != "" {
		return token
	}
	if session := p.confirmedSession(cfg.ID); session != nil {
		return session.botToken
	}
	return ""
}

// baseFor 解析 iLink 基址：Settings["api_base"] > 构造参数覆盖 > Settings["base_url"]
// > 已确认登录的 baseurl > 官方默认地址（defaultBaseURL）。因此用户无需填写基址，
// 扫码登录开箱即用；测试替身仍可用 api_base/构造参数覆盖。
func (p *Provider) baseFor(cfg connectors.Config) string {
	if base := strings.TrimSpace(cfg.Setting("api_base", "")); base != "" {
		return strings.TrimRight(base, "/")
	}
	if p.apiBase != "" {
		return p.apiBase
	}
	if base := strings.TrimSpace(cfg.Setting("base_url", "")); base != "" {
		return strings.TrimRight(base, "/")
	}
	if session := p.confirmedSession(cfg.ID); session != nil {
		return strings.TrimRight(session.baseURL, "/")
	}
	return defaultBaseURL
}

// routeTag 读取路由标签（SKRouteTag）。
func (p *Provider) routeTag(cfg connectors.Config) string {
	return strings.TrimSpace(cfg.Setting("route_tag", ""))
}

// connectorKey 是进程内登录态/游标记忆的连接器身份键（缺 ID 时退化为基址）。
func connectorKey(cfg connectors.Config) string {
	if id := strings.TrimSpace(cfg.ID); id != "" {
		return id
	}
	return "base:" + strings.TrimRight(strings.TrimSpace(cfg.Setting("base_url", "")), "/")
}

// ── Provider 接口 ───────────────────────────────────────────────────────────

// Activate 校验凭据并预热登录态缓存：
//   - bot_token 与基址齐备（未扫码登录时可先调用 start_login）；
//   - 配置了 owner_user_id 时调用 getconfig 做一次轻量校验（typing ticket 结果
//     按连接器缓存 24h，重复 Activate 不重复请求）；
//   - 错误文本已脱敏（bot_token 不回传）。
func (p *Provider) Activate(ctx context.Context, cfg connectors.Config) (connectors.Activation, error) {
	token := p.tokenFor(cfg)
	if token == "" {
		return connectors.Activation{}, errors.New("wechat: bot_token is required（未扫码登录时先执行 start_login）")
	}
	base := p.baseFor(cfg)
	if err := validateBaseURL(base); err != nil {
		return connectors.Activation{}, err
	}

	info := "base_url=" + base
	if account := strings.TrimSpace(cfg.Setting("account_id", "")); account != "" {
		info += " account_id=" + account
	}
	if owner := strings.TrimSpace(cfg.Setting("owner_user_id", "")); owner != "" {
		if _, err := p.typingTicket(ctx, cfg, owner); err != nil {
			return connectors.Activation{}, err
		}
		info += "（getconfig 校验通过）"
	} else {
		info += "（未配置 owner_user_id，跳过 getconfig 探测）"
	}
	return connectors.Activation{Info: info}, nil
}

// Deactivate 无远端状态需要清理（轮询随 ctx 退出；登录态缓存自行过期）。
func (p *Provider) Deactivate(context.Context, connectors.Config) error { return nil }

// SupportsPolling 报告是否应启动长轮询（凭据与基址齐备）。Schema 只声明
// polling，历史配置里的其它 mode 与上游同语义（按轮询处理）。
func (p *Provider) SupportsPolling(cfg connectors.Config) bool {
	return p.tokenFor(cfg) != "" && p.baseFor(cfg) != ""
}

// ── 发送 ────────────────────────────────────────────────────────────────────

// Send 逐条发送文本消息（POST /ilink/bot/sendmessage）。
//
// 回复必须携带该会话最近一次入站消息的 context_token：轮询解析入站时按
// connector+chat 记忆（进程内，防御上限 maxTrackedChats），重启后需等该会话
// 下一次入站才能回复——上游把 token 存进会话 ProviderState 持久化，本仓
// ProviderData 暂未落库，标注为 P3 改进点（随真媒体切片一起处理）。
// OutboundMessage.Media 本期忽略（P3 做 CDN 上传），markdown 按上游策略清洗。
func (p *Provider) Send(ctx context.Context, cfg connectors.Config, chat string, msgs []connectors.OutboundMessage) error {
	token := p.tokenFor(cfg)
	if token == "" {
		return errors.New("wechat: bot_token is required")
	}
	base := p.baseFor(cfg)
	if err := validateBaseURL(base); err != nil {
		return err
	}
	toUserID := strings.TrimSpace(chat)
	if toUserID == "" {
		return errors.New("wechat: chat is required")
	}
	contextToken := p.contextToken(connectorKey(cfg), toUserID)
	if contextToken == "" {
		return fmt.Errorf("wechat: chat %q 缺少 context_token（回复需要该会话最近一次入站消息）", toUserID)
	}

	for _, msg := range msgs {
		text := cleanMarkdown(msg.Text)
		if text == "" {
			// P3：OutboundMessage.Media（CDN 上传）与纯媒体消息暂不支持。
			continue
		}
		if err := p.sendTextMessage(ctx, base, token, p.routeTag(cfg), toUserID, contextToken, text); err != nil {
			return err
		}
	}
	return nil
}

func (p *Provider) sendTextMessage(ctx context.Context, base, token, routeTag, toUserID, contextToken, text string) error {
	var response apiEnvelope
	payload := wechatSendMessageRequest{
		Msg: wechatOutboundMessage{
			ToUserID:     strings.TrimSpace(toUserID),
			ClientID:     randomClientID(),
			MessageType:  messageTypeBot,
			MessageState: messageStateComplete,
			ItemList: []wechatMessageItem{
				{Type: itemTypeText, TextItem: &wechatTextItem{Text: text}},
			},
			ContextToken: strings.TrimSpace(contextToken),
		},
		BaseInfo: wechatBaseInfo{ChannelVersion: channelVersion},
	}
	if err := p.callJSON(ctx, defaultHTTPTimeout, base, token, routeTag, http.MethodPost, "/ilink/bot/sendmessage", payload, &response); err != nil {
		return wrapSendError("text", textSendSummary(text), err)
	}
	return nil
}

func wrapSendError(mode, summary string, err error) error {
	if err == nil {
		return nil
	}
	mode = strings.TrimSpace(mode)
	summary = strings.TrimSpace(summary)
	switch {
	case mode != "" && summary != "":
		return fmt.Errorf("wechat sendmessage %s failed (%s): %w", mode, summary, err)
	case mode != "":
		return fmt.Errorf("wechat sendmessage %s failed: %w", mode, err)
	default:
		return fmt.Errorf("wechat sendmessage failed: %w", err)
	}
}

func textSendSummary(text string) string {
	trimmed := strings.TrimSpace(text)
	return fmt.Sprintf("chars=%d preview=%q", len([]rune(trimmed)), truncateRunes(trimmed, 40))
}

// ── 长轮询 ──────────────────────────────────────────────────────────────────

// RunPolling 长轮询 getupdates：
//   - 游标 get_updates_buf 经 PollingState 持久化（键 sync_buf），重启续传；
//   - 解析文本/引用文本/语音转写；图片/文件/视频降级为文本占位（P3 真媒体）；
//   - 断线（网络/5xx/429/上游错误）指数退避重连；errcode -14（会话过期）
//     仅告警并放慢重试，等待重新扫码登录后自动恢复；
//   - ctx 取消返回 nil（宿主 Stop 时静默退出）。
func (p *Provider) RunPolling(ctx context.Context, cfg connectors.Config, handle connectors.InboundHandler, state connectors.PollingState) error {
	token := p.tokenFor(cfg)
	if token == "" {
		return errors.New("wechat: bot_token is required")
	}
	base := p.baseFor(cfg)
	if err := validateBaseURL(base); err != nil {
		return err
	}
	routeTag := p.routeTag(cfg)

	syncBuf := ""
	if state != nil {
		if raw, ok := state.Get(syncBufKey); ok {
			syncBuf = strings.TrimSpace(raw)
		}
	}

	pollTimeout := defaultLongPollTimeout
	backoff := p.retryDelay
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}

		response, err := p.getUpdates(ctx, base, token, routeTag, syncBuf, pollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			delay := backoff
			if isSessionExpiredError(err) {
				// 会话过期（errcode -14）：无法自动恢复，按指数退避重试
				// （上限 maxRetryDelay），等待用户重新扫码登录后自动恢复。
				p.logf("wechat: 会话已过期（errcode=%d），请重新扫码登录；%s 后重试", sessionExpiredErrCode, delay)
			} else {
				p.logf("wechat: getupdates failed: %v (retry in %s)", err, delay)
			}
			if err := sleepCtx(ctx, delay); err != nil {
				return nil
			}
			backoff = nextBackoff(backoff)
			continue
		}
		backoff = p.retryDelay

		if response.LongPollMS > 0 {
			pollTimeout = time.Duration(response.LongPollMS) * time.Millisecond
		}
		if next := strings.TrimSpace(response.GetUpdatesBuf); next != "" && next != syncBuf {
			// 先持久化游标再处理消息（与上游一致）：本批消息视为已认领，
			// 重启后从新游标续传；重复投递由宿主的去重账本兜底。
			if state != nil {
				if err := state.Set(syncBufKey, next); err != nil {
					p.logf("wechat: persist sync_buf failed: %v", err)
				}
			}
			syncBuf = next
		}

		for _, message := range response.Msgs {
			inbound, ok := p.inboundMessage(cfg.ID, message)
			if !ok {
				continue
			}
			if contextToken, _ := inbound.ProviderData[contextTokenKey].(string); strings.TrimSpace(contextToken) != "" {
				p.rememberContextToken(connectorKey(cfg), inbound.Chat, contextToken)
			}
			if err := handle(ctx, inbound); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				// 单条消息失败（队列满/后端错误）不阻塞轮询：消息已认领，
				// 平台不会重投，记日志后继续。
				p.logf("wechat: handle message %s failed: %v", inbound.MessageID, err)
			}
		}
	}
}

func (p *Provider) getUpdates(ctx context.Context, base, token, routeTag, syncBuf string, timeout time.Duration) (getUpdatesResponse, error) {
	var response getUpdatesResponse
	err := p.callJSON(ctx, timeout+longPollTimeoutBuf, base, token, routeTag, http.MethodPost, "/ilink/bot/getupdates", map[string]any{
		"get_updates_buf": strings.TrimSpace(syncBuf),
		"base_info":       wechatBaseInfo{ChannelVersion: channelVersion},
	}, &response)
	if err != nil {
		return getUpdatesResponse{}, err
	}
	return response, nil
}

// inboundMessage 解析一条入站消息：仅用户消息（message_type=1）；文本/语音
// 转写/引用文本拼成正文，媒体降级为文本占位并保留元数据（P3 下载）。
func (p *Provider) inboundMessage(connectorID string, message wechatMessage) (connectors.InboundMessage, bool) {
	if message.MessageType != messageTypeUser {
		return connectors.InboundMessage{}, false
	}
	fromUserID := strings.TrimSpace(message.FromUserID)
	if fromUserID == "" {
		return connectors.InboundMessage{}, false
	}

	text := extractText(message.ItemList)
	media := extractMedia(message.ItemList)
	summary := summaryText(text, media)
	if strings.TrimSpace(summary) == "" {
		return connectors.InboundMessage{}, false
	}

	title := firstNonEmpty(strings.TrimSpace(message.SessionID), fromUserID)
	return connectors.InboundMessage{
		ConnectorID: connectorID,
		Provider:    Name,
		Chat:        fromUserID,
		ChatTitle:   title,
		MessageID:   stableMessageID(message),
		UserID:      fromUserID,
		Text:        summary,
		Media:       media,
		ProviderData: map[string]any{
			contextTokenKey: strings.TrimSpace(message.ContextToken),
			sessionIDKey:    strings.TrimSpace(message.SessionID),
			createdAtMSKey:  strings.TrimSpace(message.CreateTimeMS.String()),
		},
		ReceivedAt: p.now().UTC(),
	}, true
}

// extractText 抽取正文：引用文本（含被引消息的递归文本）以 "Quoted: " 前缀
// 成行（镜像上游），语音消息取其转写文本。
func extractText(items []wechatMessageItem) string {
	if len(items) == 0 {
		return ""
	}
	lines := make([]string, 0, len(items)*2)
	for _, item := range items {
		if quote := extractQuotedText(item); quote != "" {
			lines = append(lines, "Quoted: "+quote)
		}
		switch item.Type {
		case itemTypeText:
			if item.TextItem != nil && strings.TrimSpace(item.TextItem.Text) != "" {
				lines = append(lines, strings.TrimSpace(item.TextItem.Text))
			}
		case itemTypeVoice:
			if item.VoiceItem != nil && strings.TrimSpace(item.VoiceItem.Text) != "" {
				lines = append(lines, strings.TrimSpace(item.VoiceItem.Text))
			}
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func extractQuotedText(item wechatMessageItem) string {
	references := []*wechatReferenceMsg{item.RefMsg}
	if item.TextItem != nil {
		references = append(references, item.TextItem.RefMsg)
	}
	for _, reference := range references {
		if reference == nil {
			continue
		}
		if text := strings.TrimSpace(reference.Text); text != "" {
			return text
		}
		if reference.MessageItem != nil {
			if quoted := extractText([]wechatMessageItem{*reference.MessageItem}); quoted != "" {
				if title := strings.TrimSpace(reference.Title); title != "" {
					return title + " | " + quoted
				}
				return quoted
			}
		}
		if title := strings.TrimSpace(reference.Title); title != "" {
			return title
		}
	}
	return ""
}

// extractMedia 把媒体项转为元数据（P3 下载用）与文本占位行。
func extractMedia(items []wechatMessageItem) []connectors.Media {
	var media []connectors.Media
	for _, item := range items {
		switch item.Type {
		case itemTypeImage:
			entry := connectors.Media{Kind: "image"}
			if item.ImageItem != nil {
				entry.FileID = mediaFileID(item.ImageItem.Media)
				entry.Size = item.ImageItem.MidSize
			}
			media = append(media, entry)
		case itemTypeVoice:
			// 有转写文本的语音已并入正文，不再产生「[语音]」占位；只有
			// 无转写时才降级为占位（P3 真媒体/转码）。
			if item.VoiceItem != nil && strings.TrimSpace(item.VoiceItem.Text) == "" {
				media = append(media, connectors.Media{Kind: "voice", FileID: mediaFileID(item.VoiceItem.Media)})
			}
		case itemTypeFile:
			entry := connectors.Media{Kind: "file"}
			if item.FileItem != nil {
				entry.FileID = mediaFileID(item.FileItem.Media)
				entry.FileName = strings.TrimSpace(item.FileItem.FileName)
				entry.Size = parseSize(item.FileItem.Len)
			}
			media = append(media, entry)
		case itemTypeVideo:
			entry := connectors.Media{Kind: "video"}
			if item.VideoItem != nil {
				entry.FileID = mediaFileID(item.VideoItem.Media)
				entry.Size = item.VideoItem.VideoSize
			}
			media = append(media, entry)
		}
	}
	return media
}

// mediaFileID 取媒体的可下载句柄（P3 真媒体用）：优先 full_url，退回加密查询参数。
func mediaFileID(media *wechatCDNMedia) string {
	if media == nil {
		return ""
	}
	if fullURL := strings.TrimSpace(media.FullURL); fullURL != "" {
		return fullURL
	}
	return strings.TrimSpace(media.EncryptQueryParam)
}

// summaryText 把正文与媒体占位行拼成给 agent 的文本（本轮媒体降级策略：
// 图片「[图片]」、语音「[语音]」（有转写则用转写）、文件「[文件: name]」、
// 视频「[视频]」；P3 再做真媒体下载/转码）。
func summaryText(text string, media []connectors.Media) string {
	text = strings.TrimSpace(text)
	placeholder := mediaPlaceholders(media)
	switch {
	case text == "" && placeholder == "":
		return ""
	case placeholder == "":
		return text
	case text == "":
		return placeholder
	default:
		return text + "\n" + placeholder
	}
}

// mediaPlaceholders 按序生成占位行；图片/视频占位不重复出现（多图合并为一行）。
func mediaPlaceholders(media []connectors.Media) string {
	if len(media) == 0 {
		return ""
	}
	lines := make([]string, 0, len(media))
	for _, item := range media {
		switch item.Kind {
		case "image":
			lines = append(lines, "[图片]")
		case "voice":
			lines = append(lines, "[语音]")
		case "video":
			lines = append(lines, "[视频]")
		case "file":
			if name := strings.TrimSpace(item.FileName); name != "" {
				lines = append(lines, "[文件: "+name+"]")
			} else {
				lines = append(lines, "[文件]")
			}
		}
	}
	return strings.Join(dedupeLines(lines), "\n")
}

// dedupeLines 相邻重复占位合并（同一消息里多张图片只留一行「[图片]」）。
func dedupeLines(lines []string) []string {
	deduped := make([]string, 0, len(lines))
	for _, line := range lines {
		if len(deduped) > 0 && deduped[len(deduped)-1] == line {
			continue
		}
		deduped = append(deduped, line)
	}
	return deduped
}

func parseSize(raw string) int64 {
	if parsed, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil && parsed > 0 {
		return parsed
	}
	return 0
}

// stableMessageID 生成稳定的入站消息 id（去重账本键的一部分；字段全空返回空串，
// 由宿主按 chat 兜底）。镜像上游：sha1(from|client_id|create_time_ms|context_token)。
func stableMessageID(message wechatMessage) string {
	joined := strings.Join([]string{
		strings.TrimSpace(message.FromUserID),
		strings.TrimSpace(message.ClientID),
		strings.TrimSpace(message.CreateTimeMS.String()),
		strings.TrimSpace(message.ContextToken),
	}, "\x00")
	if strings.TrimSpace(strings.ReplaceAll(joined, "\x00", "")) == "" {
		return ""
	}
	sum := sha1.Sum([]byte(joined))
	return "wechat:" + hex.EncodeToString(sum[:])
}

// ── context_token 记忆（回复必需） ──────────────────────────────────────────

func (p *Provider) rememberContextToken(connectorID, chat, token string) {
	connectorID = strings.TrimSpace(connectorID)
	chat = strings.TrimSpace(chat)
	token = strings.TrimSpace(token)
	if connectorID == "" || chat == "" || token == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.contexts == nil {
		p.contexts = make(map[string]string)
	}
	key := connectorID + "\n" + chat
	if len(p.contexts) >= maxTrackedChats {
		if _, exists := p.contexts[key]; !exists {
			p.contexts = make(map[string]string)
		}
	}
	p.contexts[key] = token
}

func (p *Provider) contextToken(connectorID, chat string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.contexts[strings.TrimSpace(connectorID)+"\n"+strings.TrimSpace(chat)]
}

// ── typing（TypingProvider） ────────────────────────────────────────────────

// StartTyping 通过 typing ticket 发送「正在输入」并每 5s 保活，Stop 时发送取消。
// typing ticket 来自 getconfig（按连接器+会话缓存 24h）；上游未下发 ticket 时
// 静默返回 nil（无 typing 能力，不影响消息往来）。
func (p *Provider) StartTyping(ctx context.Context, cfg connectors.Config, chat string) (connectors.TypingSession, error) {
	token := p.tokenFor(cfg)
	if token == "" {
		return nil, errors.New("wechat: bot_token is required")
	}
	base := p.baseFor(cfg)
	if err := validateBaseURL(base); err != nil {
		return nil, err
	}
	toUserID := strings.TrimSpace(chat)
	if toUserID == "" {
		return nil, errors.New("wechat: chat is required before typing")
	}
	if p.contextToken(connectorKey(cfg), toUserID) == "" {
		return nil, fmt.Errorf("wechat: chat %q 缺少 context_token（typing 需要该会话最近一次入站消息）", toUserID)
	}

	ticket, err := p.typingTicket(ctx, cfg, toUserID)
	if err != nil {
		return nil, err
	}
	if ticket == "" {
		return nil, nil
	}
	if err := p.sendTyping(ctx, base, token, p.routeTag(cfg), toUserID, ticket, typingStatusTyping); err != nil {
		return nil, err
	}

	sessionCtx, cancel := context.WithCancel(context.Background())
	session := &typingSession{
		provider: p,
		base:     base,
		token:    token,
		routeTag: p.routeTag(cfg),
		toUserID: toUserID,
		ticket:   ticket,
		cancel:   cancel,
		done:     make(chan struct{}),
	}
	go session.keepalive(sessionCtx)
	return session, nil
}

type typingSession struct {
	provider *Provider
	base     string
	token    string
	routeTag string
	toUserID string
	ticket   string
	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once
}

func (s *typingSession) Stop(ctx context.Context) error {
	if s == nil {
		return nil
	}
	var stopErr error
	s.stopOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		if s.done != nil {
			<-s.done
		}
		if ctx == nil {
			ctx = context.Background()
		}
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), configHTTPTimeout)
		defer cancel()
		stopErr = s.provider.sendTyping(stopCtx, s.base, s.token, s.routeTag, s.toUserID, s.ticket, typingStatusCancel)
	})
	return stopErr
}

func (s *typingSession) keepalive(ctx context.Context) {
	ticker := time.NewTicker(typingKeepaliveDelay)
	defer ticker.Stop()
	defer close(s.done)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			requestCtx, cancel := context.WithTimeout(context.Background(), configHTTPTimeout)
			_ = s.provider.sendTyping(requestCtx, s.base, s.token, s.routeTag, s.toUserID, s.ticket, typingStatusTyping)
			cancel()
		}
	}
}

// typingTicket 取会话的 typing ticket：命中缓存直接返回，否则调用 getconfig
// （上游轻量配置接口）并按连接器+会话+令牌缓存 24h。
func (p *Provider) typingTicket(ctx context.Context, cfg connectors.Config, toUserID string) (string, error) {
	token := p.tokenFor(cfg)
	if token == "" {
		return "", errors.New("wechat: bot_token is required")
	}
	base := p.baseFor(cfg)
	if err := validateBaseURL(base); err != nil {
		return "", err
	}
	cacheKey := strings.Join([]string{connectorKey(cfg), base, strings.TrimSpace(toUserID), token}, "\n")

	p.mu.Lock()
	entry, ok := p.typing[cacheKey]
	p.mu.Unlock()
	if ok && strings.TrimSpace(entry.ticket) != "" && p.now().Before(entry.expiresAt) {
		return entry.ticket, nil
	}

	response, err := p.getConfig(ctx, base, token, p.routeTag(cfg), toUserID, p.contextToken(connectorKey(cfg), toUserID))
	if err != nil {
		return "", err
	}
	ticket := strings.TrimSpace(response.TypingTicket)
	if ticket == "" {
		return "", nil
	}
	p.mu.Lock()
	if p.typing == nil {
		p.typing = make(map[string]typingCacheEntry)
	}
	p.typing[cacheKey] = typingCacheEntry{ticket: ticket, expiresAt: p.now().Add(typingConfigTTL)}
	p.mu.Unlock()
	return ticket, nil
}

func (p *Provider) getConfig(ctx context.Context, base, token, routeTag, toUserID, contextToken string) (getConfigResponse, error) {
	var response getConfigResponse
	err := p.callJSON(ctx, configHTTPTimeout, base, token, routeTag, http.MethodPost, "/ilink/bot/getconfig", map[string]any{
		"ilink_user_id": strings.TrimSpace(toUserID),
		"context_token": strings.TrimSpace(contextToken),
		"base_info":     wechatBaseInfo{ChannelVersion: channelVersion},
	}, &response)
	if err != nil {
		return getConfigResponse{}, err
	}
	return response, nil
}

func (p *Provider) sendTyping(ctx context.Context, base, token, routeTag, toUserID, ticket string, status int) error {
	var response apiEnvelope
	return p.callJSON(ctx, configHTTPTimeout, base, token, routeTag, http.MethodPost, "/ilink/bot/sendtyping", map[string]any{
		"ilink_user_id": strings.TrimSpace(toUserID),
		"typing_ticket": strings.TrimSpace(ticket),
		"status":        status,
		"base_info":     wechatBaseInfo{ChannelVersion: channelVersion},
	}, &response)
}

// ── HTTP ────────────────────────────────────────────────────────────────────

// callJSON 发起一次带 bot_token 的 iLink API 调用；错误文本抹除令牌。
func (p *Provider) callJSON(ctx context.Context, timeout time.Duration, base, token, routeTag, method, path string, payload, out any) error {
	return p.doJSON(ctx, timeout, base, token, routeTag, method, path, payload, out, true)
}

// getJSON 发起一次无鉴权的 GET（扫码登录的 get_bot_qrcode/get_qrcode_status）。
func (p *Provider) getJSON(ctx context.Context, timeout time.Duration, base, routeTag, path string, query url.Values, out any) error {
	return p.doJSON(ctx, timeout, base, "", routeTag, http.MethodGet, path, query, out, false)
}

func (p *Provider) doJSON(ctx context.Context, timeout time.Duration, base, token, routeTag, method, path string, payload any, out any, authorized bool) error {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	endpoint, err := buildURL(base, path)
	if err != nil {
		return err
	}

	var body io.Reader
	if query, ok := payload.(url.Values); ok {
		if encoded := query.Encode(); encoded != "" {
			endpoint += "?" + encoded
		}
	} else if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode wechat %s payload: %w", path, err)
		}
		body = bytes.NewReader(encoded)
	}

	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("build wechat %s request: %s", path, redact(err.Error(), token))
	}
	if authorized {
		request.Header.Set("AuthorizationType", "ilink_bot_token")
		request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	}
	if payload != nil && body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	applyCommonHeaders(request.Header, routeTag)

	response, err := p.client.Do(request)
	if err != nil {
		// *url.Error 的文本可能带出请求 URL/头；不做 %w 包装，避免上层解出原始错误。
		return &requestError{method: path, cause: errors.New(redact(err.Error(), token))}
	}
	defer response.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read wechat %s response: %w", path, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &requestError{
			method:      path,
			statusCode:  response.StatusCode,
			status:      response.Status,
			description: truncateRunes(redact(string(raw), token), 200),
		}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode wechat %s response: %w", path, err)
	}
	switch typed := out.(type) {
	case *apiEnvelope:
		return apiError(path, *typed)
	case *getUpdatesResponse:
		return apiError(path, typed.apiEnvelope)
	case *getConfigResponse:
		return apiError(path, typed.apiEnvelope)
	case *qrCodeResponse:
		return apiError(path, typed.apiEnvelope)
	case *qrCodeStatusResponse:
		return apiError(path, typed.apiEnvelope)
	}
	return nil
}

// apiError 把 iLink 业务错误转成脱敏的 requestError（ret/errcode 双字段）。
func apiError(path string, envelope apiEnvelope) error {
	if envelope.Ret == 0 && envelope.ErrCode == 0 {
		return nil
	}
	description := strings.TrimSpace(envelope.ErrMsg)
	if description == "" {
		switch {
		case path == "/ilink/bot/sendmessage" &&
			(envelope.Ret == replyContextInvalidRet || envelope.ErrCode == replyContextInvalidRet):
			description = "reply context unavailable"
		case envelope.Ret == sessionExpiredErrCode || envelope.ErrCode == sessionExpiredErrCode:
			description = "session expired"
		}
	}
	if description == "" {
		description = "wechat api request failed"
	}
	return &requestError{
		method:      path,
		apiRet:      envelope.Ret,
		statusCode:  envelope.ErrCode,
		status:      "api error",
		description: description,
	}
}

// isSessionExpiredError 报告上游是否返回 errcode/ret=-14（会话过期，需重新扫码）。
func isSessionExpiredError(err error) bool {
	var requestErr *requestError
	if !errors.As(err, &requestErr) || requestErr == nil {
		return false
	}
	return requestErr.apiRet == sessionExpiredErrCode || requestErr.statusCode == sessionExpiredErrCode
}

// isTransientError 分类可重试错误：网络/超时/408/429/5xx（供未来重试策略使用）。
func isTransientError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var requestErr *requestError
	if errors.As(err, &requestErr) {
		switch {
		case requestErr.cause != nil:
			return isTransientError(requestErr.cause)
		case requestErr.statusCode == http.StatusRequestTimeout:
			return true
		case requestErr.statusCode == http.StatusTooManyRequests:
			return true
		case requestErr.statusCode >= 500:
			return true
		}
	}
	return false
}

func applyCommonHeaders(headers http.Header, routeTag string) {
	if headers == nil {
		return
	}
	headers.Set("iLink-App-Id", "bot")
	headers.Set("iLink-App-ClientVersion", clientVersionHeader)
	headers.Set("X-WECHAT-UIN", randomUIN())
	if routeTag = strings.TrimSpace(routeTag); routeTag != "" {
		headers.Set("SKRouteTag", routeTag)
	}
}

// buildURL 把 base 与 API 路径拼成绝对 URL（保留 base 的路径前缀）。
func buildURL(base, path string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("wechat: base url must be absolute")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + path
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func validateBaseURL(base string) error {
	if strings.TrimSpace(base) == "" {
		return errors.New("wechat: base_url is required（或用 api_base 指定测试基址；扫码登录成功后会自动回填）")
	}
	if _, err := buildURL(base, "/"); err != nil {
		return err
	}
	return nil
}

// redact 从错误/响应文本中抹除密钥。
func redact(message string, secrets ...string) string {
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "***")
		}
	}
	return message
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// ── 退避与随机 ──────────────────────────────────────────────────────────────

func nextBackoff(current time.Duration) time.Duration {
	if current <= 0 {
		return defaultRetryDelay
	}
	next := current * 2
	if next > maxRetryDelay {
		return maxRetryDelay
	}
	return next
}

func sleepCtx(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// buildClientVersion 把 "0.3.0" 编码为 0x000300 的十进制值（上游同算法）。
func buildClientVersion(version string) uint32 {
	parts := strings.Split(strings.TrimSpace(version), ".")
	values := [3]uint64{}
	for index := 0; index < len(values) && index < len(parts); index++ {
		parsed, err := strconv.ParseUint(strings.TrimSpace(parts[index]), 10, 8)
		if err != nil {
			continue
		}
		values[index] = parsed
	}
	return uint32((values[0]&0xff)<<16 | (values[1]&0xff)<<8 | (values[2] & 0xff))
}

func randomUIN() string {
	buffer := make([]byte, 4)
	if _, err := rand.Read(buffer); err != nil {
		return base64.StdEncoding.EncodeToString([]byte(strconv.FormatInt(time.Now().UnixNano(), 10)))
	}
	value := binary.BigEndian.Uint32(buffer)
	return base64.StdEncoding.EncodeToString([]byte(strconv.FormatUint(uint64(value), 10)))
}

func randomClientID() string {
	buffer := make([]byte, 8)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("wechat:%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("wechat:%d-%s", time.Now().UnixMilli(), hex.EncodeToString(buffer))
}

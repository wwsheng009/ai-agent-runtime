// Package feishu 实现飞书（自建应用/Lark）通道适配器的 webhook 模式：
// url_verification challenge 应答、事件 v2 校验与 AES 解密、im.message.receive_v1
// 文本入站、tenant_access_token 缓存与文本回复。
//
// 设计见 docs/bots/04-migration-roadmap.md P1「飞书 + gateway 前置 + 三端配置页」与
// docs/bots/03-host-integration.md §6；协议细节镜像上游
// codex-server/backend/internal/bots/feishu.go（challenge / 事件解析 / 回复路径）。
//
// 切片 3 范围（webhook 文本收发）：
//   - Schema 只声明 webhook（WS 长连接未实现前不列出，避免"诚实声明"失真）；
//   - Activate：tenant_access_token 获取与缓存（按 connectorID、提前刷新）+
//     bot info 轻量探测（失败仅告警，凭据有效性已由 token 接口验证）；
//   - HandleWebhook：challenge、header.token 校验、encrypt_key AES-256-CBC 事件解密、
//     im.message.receive_v1 → InboundMessage（幂等键=header.event_id）、其它事件静默 202；
//   - Send：优先 reply 原消息（quote，进程内按 connector+chat 记忆 msg_id），
//     无 msg_id 时走 chat 直发；错误脱敏 app_secret/token。
//
// 待续（P1 后续切片）：WS 长连接（/callback/ws/endpoint + 二进制帧重连）、
// 卡片式流式回复（StreamingProvider）、typing 表情、媒体上传/下载、审批事件路由。
package feishu

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/api"
)

// Name 是通道标识（与 connectors.Config.Provider 对应）。
const Name = "feishu"

// 配置字段键（Schema 与实现共用的唯一来源）：
// app_id/api_base 是普通设置，app_secret/verification_token/encrypt_key 是密钥。
const (
	settingAppID            = "app_id"
	secretAppSecret         = "app_secret"
	secretVerificationToken = "verification_token"
	secretEncryptKey        = "encrypt_key"
	settingAPIBase          = "api_base"
)

const (
	defaultAPIBase     = "https://open.feishu.cn"
	defaultHTTPTimeout = 15 * time.Second

	tenantAccessTokenPath = "/open-apis/auth/v3/tenant_access_token/internal"
	botInfoPath           = "/open-apis/bot/v3/info"
	messageReplyPathFmt   = "/open-apis/im/v1/messages/%s/reply"
	messageCreatePath     = "/open-apis/im/v1/messages"

	// maxBodyBytes 是 webhook/API 响应的读取上限（防御超大 body）。
	maxBodyBytes = 1 << 20
	// defaultTokenTTL 是 tenant_access_token 的兜底有效期（飞书返回 expire=7200）。
	defaultTokenTTL = 2 * time.Hour
	// tokenRefreshSkew 是提前刷新窗口：剩余寿命小于该值时重新获取。
	tokenRefreshSkew = 5 * time.Minute
	// maxTrackedChats 是进程内「会话 → 最近入站 msg_id」记忆的防御上限。
	maxTrackedChats = 4096

	eventMessageReceive = "im.message.receive_v1"

	// ProviderData 键（逃生舱：回复目标与事件元数据）。
	providerDataMessageID = "feishu_message_id"
	providerDataChatType  = "feishu_chat_type"
	providerDataEventType = "feishu_event_type"
)

// ── 报文类型（镜像上游字段） ────────────────────────────────────────────────

type tenantAccessTokenResponse struct {
	Code              int    `json:"code"`
	Msg               string `json:"msg"`
	TenantAccessToken string `json:"tenant_access_token"`
	Expire            int    `json:"expire"`
}

type botInfoResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Bot  struct {
		OpenID      string `json:"open_id"`
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
	} `json:"bot"`
	Data struct {
		OpenID      string `json:"open_id"`
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
	} `json:"data"`
}

func (r botInfoResponse) openID() string { return firstNonEmpty(r.Bot.OpenID, r.Data.OpenID) }

func (r botInfoResponse) displayName() string {
	return firstNonEmpty(r.Bot.DisplayName, r.Bot.Name, r.Data.DisplayName, r.Data.Name)
}

// webhookEnvelope 同时覆盖明文/密文两种 webhook 体：
// 密文模式只有 encrypt 字段，解密后才是完整事件体。
type webhookEnvelope struct {
	Encrypt   string          `json:"encrypt"`
	Type      string          `json:"type"`
	Challenge string          `json:"challenge"`
	Token     string          `json:"token"`
	Schema    string          `json:"schema"`
	Header    eventHeader     `json:"header"`
	Event     json.RawMessage `json:"event"`
}

type eventHeader struct {
	EventType       string `json:"event_type"`
	EventID         string `json:"event_id"`
	Token           string `json:"token"`
	AppID           string `json:"app_id"`
	TenantKey       string `json:"tenant_key"`
	EventCreateTime string `json:"event_create_time"`
}

type messageEventEnvelope struct {
	Sender  messageSender `json:"sender"`
	Message messageBody   `json:"message"`
	Chat    struct {
		Name string `json:"name"`
	} `json:"chat"`
}

type messageSender struct {
	SenderID   openIDRef `json:"sender_id"`
	SenderType string    `json:"sender_type"`
	Name       string    `json:"name"`
	SenderName string    `json:"sender_name"`
}

type openIDRef struct {
	OpenID string `json:"open_id"`
}

type messageBody struct {
	MessageID   string           `json:"message_id"`
	RootID      string           `json:"root_id"`
	ParentID    string           `json:"parent_id"`
	ThreadID    string           `json:"thread_id"`
	ChatID      string           `json:"chat_id"`
	ChatType    string           `json:"chat_type"`
	MessageType string           `json:"message_type"`
	Content     string           `json:"content"`
	Mentions    []messageMention `json:"mentions"`
}

type messageMention struct {
	Key  string    `json:"key"`
	Name string    `json:"name"`
	ID   openIDRef `json:"id"`
}

type sendMessageResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		MessageID string `json:"message_id"`
	} `json:"data"`
}

// ── 错误（已脱敏：绝不回传 app_secret/token 原文） ─────────────────────────

type requestError struct {
	operation  string
	statusCode int
	status     string
	apiCode    int
	apiMessage string
	cause      error
}

func (e *requestError) Error() string {
	if e == nil {
		return ""
	}
	if e.cause != nil {
		return fmt.Sprintf("feishu %s request failed: %v", e.operation, e.cause)
	}
	detail := truncateRunes(strings.TrimSpace(e.apiMessage), 200)
	switch {
	case e.status != "" && detail != "" && e.apiCode != 0:
		return fmt.Sprintf("feishu %s returned %s (code=%d): %s", e.operation, e.status, e.apiCode, detail)
	case e.status != "" && detail != "":
		return fmt.Sprintf("feishu %s returned %s: %s", e.operation, e.status, detail)
	case e.status != "" && e.apiCode != 0:
		return fmt.Sprintf("feishu %s returned %s (code=%d)", e.operation, e.status, e.apiCode)
	case e.status != "":
		return fmt.Sprintf("feishu %s returned %s", e.operation, e.status)
	case detail != "" && e.apiCode != 0:
		return fmt.Sprintf("feishu %s api error (code=%d): %s", e.operation, e.apiCode, detail)
	case detail != "":
		return fmt.Sprintf("feishu %s api error: %s", e.operation, detail)
	case e.apiCode != 0:
		return fmt.Sprintf("feishu %s api error (code=%d)", e.operation, e.apiCode)
	default:
		return fmt.Sprintf("feishu %s request failed", e.operation)
	}
}

func (e *requestError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// newAPIError 构造业务错误（apiMessage 先脱敏再截断）。
func newAPIError(operation string, apiCode int, apiMessage string, secrets ...string) error {
	message := strings.TrimSpace(redact(apiMessage, secrets...))
	if message == "" {
		message = "feishu api request failed"
	}
	return &requestError{operation: operation, status: "api error", apiCode: apiCode, apiMessage: message}
}

// ── Provider ────────────────────────────────────────────────────────────────

type tokenEntry struct {
	token     string
	expiresAt time.Time
}

// Provider 是飞书通道适配器（webhook 模式）；HTTP 细节可注入（测试用 httptest 假飞书 API）。
type Provider struct {
	// apiBase 是构造参数覆盖（测试替身/反向代理）；Settings["api_base"] 优先于本字段。
	apiBase string
	client  *http.Client
	logf    func(format string, args ...any)
	now     func() time.Time

	// mu 保护 tenant_access_token 缓存与「会话 → 最近入站 msg_id」记忆。
	mu           sync.Mutex
	tokens       map[string]tokenEntry
	replyTargets map[string]string
}

// Option 配置 Provider。
type Option func(*Provider)

// WithAPIBase 覆盖飞书开放平台基址（测试服务器/反向代理；优先于 Settings["api_base"]）。
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

// WithLogger 注入日志函数。
func WithLogger(logf func(format string, args ...any)) Option {
	return func(p *Provider) {
		if logf != nil {
			p.logf = logf
		}
	}
}

// New 构造飞书 Provider。
func New(opts ...Option) *Provider {
	provider := &Provider{
		client:       &http.Client{},
		logf:         log.Printf,
		now:          time.Now,
		tokens:       make(map[string]tokenEntry),
		replyTargets: make(map[string]string),
	}
	for _, opt := range opts {
		opt(provider)
	}
	return provider
}

// Name 实现 connectors.Provider。
func (p *Provider) Name() string { return Name }

// Schema 是飞书的配置面（三端前端据此渲染）。
//
// 只声明已实现的 webhook 模式：WS 长连接（/callback/ws/endpoint + 二进制帧）未实现前
// 不列入 Modes，避免前端出现不可用的接入方式；无 actions。
func (p *Provider) Schema() api.ProviderSchema {
	return api.ProviderSchema{
		Provider: Name,
		Label:    "飞书",
		Modes:    []string{"webhook"},
		Fields: []api.FieldSchema{
			{
				Key: settingAppID, Label: "App ID", Type: api.FieldText, Required: true,
				Placeholder: "cli_xxxxxxxxxxxx",
				Help:        "飞书开放平台自建应用凭据（事件订阅用；与 App Secret 配套）",
			},
			{
				Key: secretAppSecret, Label: "App Secret", Type: api.FieldSecret, Required: true,
				Help: "只写不读；用于获取 tenant_access_token（错误文本已脱敏）",
			},
			{
				Key: secretVerificationToken, Label: "Verification Token", Type: api.FieldSecret,
				Help: "可选；配置后校验 challenge 与事件 header.token（未配置时跳过校验并告警）",
			},
			{
				Key: secretEncryptKey, Label: "Encrypt Key", Type: api.FieldSecret,
				Help: "可选；配置后按 AES-256-CBC（sha256(encrypt_key)）解密事件密文",
			},
			{
				Key: settingAPIBase, Label: "API 基址", Type: api.FieldURL,
				Default: defaultAPIBase,
				Help:    "可选；默认 https://open.feishu.cn（Lark 国际版改为 https://open.larksuite.com）",
			},
		},
	}
}

// ── 凭据与基址解析 ──────────────────────────────────────────────────────────

// baseFor 解析飞书基址：Settings["api_base"] > 构造参数覆盖 > 官方默认值。
func (p *Provider) baseFor(cfg connectors.Config) string {
	if base := strings.TrimSpace(cfg.Setting(settingAPIBase, "")); base != "" {
		return strings.TrimRight(base, "/")
	}
	if p.apiBase != "" {
		return p.apiBase
	}
	return defaultAPIBase
}

// connectorKey 是进程内 token/回复目标记忆的连接器身份键（缺 ID 时退化为 App ID）。
func connectorKey(cfg connectors.Config) string {
	if id := strings.TrimSpace(cfg.ID); id != "" {
		return id
	}
	return "app:" + strings.TrimSpace(cfg.Setting(settingAppID, ""))
}

// ── Provider 接口 ───────────────────────────────────────────────────────────

// Activate 校验凭据并预热 token 缓存：
//   - app_id/app_secret 必填，api_base 必须是绝对 URL；
//   - 调用 tenant_access_token 接口验证凭据（结果按连接器缓存，提前 5 分钟刷新）；
//   - bot info 是轻量补充探测：失败只告警（token 接口已证明凭据有效），
//     成功则回填 bot 名称到 Info 便于管理面展示；
//   - 错误文本已脱敏（app_secret 不回传）。
func (p *Provider) Activate(ctx context.Context, cfg connectors.Config) (connectors.Activation, error) {
	appID := strings.TrimSpace(cfg.Setting(settingAppID, ""))
	if appID == "" {
		return connectors.Activation{}, errors.New("feishu: app_id is required")
	}
	appSecret := cfg.Secret(secretAppSecret)
	if appSecret == "" {
		return connectors.Activation{}, errors.New("feishu: app_secret is required")
	}
	base := p.baseFor(cfg)
	if err := validateBaseURL(base); err != nil {
		return connectors.Activation{}, err
	}

	token, err := p.tenantToken(ctx, cfg)
	if err != nil {
		return connectors.Activation{}, err
	}

	info := "api_base=" + base
	if bot, err := p.fetchBotInfo(ctx, base, token); err != nil {
		p.logf("feishu: bot info 探测失败（凭据已通过 tenant_access_token 校验）: %v", err)
	} else if name := bot.displayName(); name != "" {
		info += " bot=" + name
	}
	return connectors.Activation{Info: info}, nil
}

// Deactivate 无远端状态需要清理（token 缓存自行过期，webhook 由宿主路由卸载）。
func (p *Provider) Deactivate(context.Context, connectors.Config) error { return nil }

// ── tenant_access_token 获取与缓存 ─────────────────────────────────────────

// tenantToken 返回缓存的 tenant_access_token；缺失或剩余寿命小于
// tokenRefreshSkew（提前刷新）时重新获取并按 connectorID 缓存。
func (p *Provider) tenantToken(ctx context.Context, cfg connectors.Config) (string, error) {
	key := connectorKey(cfg)
	now := p.now().UTC()

	p.mu.Lock()
	if entry, ok := p.tokens[key]; ok && entry.token != "" && now.Add(tokenRefreshSkew).Before(entry.expiresAt) {
		p.mu.Unlock()
		return entry.token, nil
	}
	p.mu.Unlock()

	appID := strings.TrimSpace(cfg.Setting(settingAppID, ""))
	if appID == "" {
		return "", errors.New("feishu: app_id is required")
	}
	appSecret := cfg.Secret(secretAppSecret)
	if appSecret == "" {
		return "", errors.New("feishu: app_secret is required")
	}
	base := p.baseFor(cfg)
	if err := validateBaseURL(base); err != nil {
		return "", err
	}

	response, err := p.fetchTenantToken(ctx, base, appID, appSecret)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(response.TenantAccessToken)
	if token == "" {
		return "", errors.New("feishu: tenant access token response did not include a token")
	}
	ttl := time.Duration(response.Expire) * time.Second
	if ttl <= 0 {
		ttl = defaultTokenTTL
	}

	p.mu.Lock()
	if p.tokens == nil {
		p.tokens = make(map[string]tokenEntry)
	}
	p.tokens[key] = tokenEntry{token: token, expiresAt: p.now().UTC().Add(ttl)}
	p.mu.Unlock()
	return token, nil
}

func (p *Provider) fetchTenantToken(ctx context.Context, base, appID, appSecret string) (tenantAccessTokenResponse, error) {
	var response tenantAccessTokenResponse
	request := map[string]string{
		"app_id":     strings.TrimSpace(appID),
		"app_secret": strings.TrimSpace(appSecret),
	}
	if err := p.doJSON(ctx, http.MethodPost, base, tenantAccessTokenPath, "", request, &response, appSecret); err != nil {
		return tenantAccessTokenResponse{}, err
	}
	if response.Code != 0 {
		return tenantAccessTokenResponse{}, newAPIError(tenantAccessTokenPath, response.Code, response.Msg, appSecret)
	}
	return response, nil
}

func (p *Provider) fetchBotInfo(ctx context.Context, base, token string) (botInfoResponse, error) {
	var response botInfoResponse
	if err := p.doJSON(ctx, http.MethodGet, base, botInfoPath, token, nil, &response, token); err != nil {
		return botInfoResponse{}, err
	}
	if response.Code != 0 {
		return botInfoResponse{}, newAPIError(botInfoPath, response.Code, response.Msg, token)
	}
	return response, nil
}

// ── 发送 ────────────────────────────────────────────────────────────────────

// Send 逐条发送文本：优先 reply 原消息（quote 效果，需要该会话最近一次入站的
// msg_id——由 HandleWebhook 进程内记忆，重启后需该会话下次入站才能 quote），
// 无记忆时按 receive_id_type=chat_id 直发。
//
// 注意：Service 的 Send 只带 chat，不带入站 msg_id，因此回复目标在 Provider
// 进程内维护（与微信 context_token 同模式）；跨重启持久化留待 P1 后续切片。
// OutboundMessage.Media 本期忽略（媒体上传留待 P3）。
func (p *Provider) Send(ctx context.Context, cfg connectors.Config, chat string, msgs []connectors.OutboundMessage) error {
	chatID := strings.TrimSpace(chat)
	if chatID == "" {
		return errors.New("feishu: chat is required")
	}
	base := p.baseFor(cfg)
	if err := validateBaseURL(base); err != nil {
		return err
	}
	token, err := p.tenantToken(ctx, cfg)
	if err != nil {
		return err
	}

	replyTo := p.replyTarget(connectorKey(cfg), chatID)
	for _, msg := range msgs {
		text := strings.TrimSpace(msg.Text)
		if text == "" {
			// P3：OutboundMessage.Media（图片/文件上传）暂不支持。
			continue
		}
		if err := p.sendText(ctx, cfg, base, token, chatID, replyTo, text); err != nil {
			return err
		}
	}
	return nil
}

func (p *Provider) sendText(ctx context.Context, cfg connectors.Config, base, token, chatID, replyTo, text string) error {
	appSecret := cfg.Secret(secretAppSecret)
	content, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return fmt.Errorf("encode feishu text content: %w", err)
	}

	if replyTo != "" {
		var response sendMessageResponse
		sendErr := p.doJSON(ctx, http.MethodPost, base, fmt.Sprintf(messageReplyPathFmt, url.PathEscape(replyTo)), token, map[string]any{
			"content":  string(content),
			"msg_type": "text",
		}, &response, appSecret, token)
		if sendErr == nil {
			if response.Code != 0 {
				return newAPIError("im/v1/messages/reply", response.Code, response.Msg, appSecret, token)
			}
			return nil
		}
		if !shouldFallbackToCreate(sendErr) {
			return sendErr
		}
		// 原消息已被撤回/过期等：降级为 chat 直发（镜像上游 shouldFeishuFallbackToCreate）。
		p.logf("feishu: reply 到消息 %s 失败（%v），降级为 chat 直发", replyTo, sendErr)
	}

	var response sendMessageResponse
	if err := p.doJSON(ctx, http.MethodPost, base, messageCreatePath+"?receive_id_type=chat_id", token, map[string]any{
		"receive_id": chatID,
		"content":    string(content),
		"msg_type":   "text",
	}, &response, appSecret, token); err != nil {
		return err
	}
	if response.Code != 0 {
		return newAPIError("im/v1/messages", response.Code, response.Msg, appSecret, token)
	}
	return nil
}

// shouldFallbackToCreate 判定 reply 失败后是否降级为 chat 直发：
// 网络层错误与鉴权失败不降级（重试/修正凭据更合适），其余上游拒绝按原消息不可用处理。
func shouldFallbackToCreate(err error) bool {
	var requestErr *requestError
	if !errors.As(err, &requestErr) || requestErr == nil {
		return false
	}
	if requestErr.cause != nil {
		return false
	}
	if requestErr.statusCode == http.StatusUnauthorized || requestErr.statusCode == http.StatusForbidden {
		return false
	}
	return true
}

// ── 回复目标记忆（进程内，按 connector+chat） ───────────────────────────────

func (p *Provider) rememberReplyTarget(connectorID, chat, messageID string) {
	connectorID = strings.TrimSpace(connectorID)
	chat = strings.TrimSpace(chat)
	messageID = strings.TrimSpace(messageID)
	if connectorID == "" || chat == "" || messageID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.replyTargets == nil {
		p.replyTargets = make(map[string]string)
	}
	if len(p.replyTargets) >= maxTrackedChats {
		for key := range p.replyTargets {
			delete(p.replyTargets, key)
			if len(p.replyTargets) < maxTrackedChats {
				break
			}
		}
	}
	p.replyTargets[connectorID+"|"+chat] = messageID
}

func (p *Provider) replyTarget(connectorID, chat string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.TrimSpace(p.replyTargets[strings.TrimSpace(connectorID)+"|"+strings.TrimSpace(chat)])
}

// ── Webhook 入口 ────────────────────────────────────────────────────────────

// HandleWebhook 实现 connectors.WebhookProvider：
//
//	challenge（url_verification）→ 校验 token 后返回 {"challenge":...}（直应答）；
//	事件 v2 → header.token 校验（+ 可选 AES 解密）→ 仅解析
//	im.message.receive_v1；其它事件返回空结果（宿主回 202）。
//
// 校验失败一律返回 error（宿主转 400），不在响应体里暴露细节。
func (p *Provider) HandleWebhook(ctx context.Context, cfg connectors.Config, r *http.Request) (connectors.WebhookResult, error) {
	if r == nil || r.Body == nil {
		return connectors.WebhookResult{}, errors.New("feishu: webhook request body is required")
	}
	defer r.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		return connectors.WebhookResult{}, fmt.Errorf("read feishu webhook body: %s", err.Error())
	}
	var envelope webhookEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return connectors.WebhookResult{}, fmt.Errorf("decode feishu webhook: %s", err.Error())
	}

	// 加密模式：外层只有 {"encrypt":"..."}，解密后才是完整事件体。
	if encrypted := strings.TrimSpace(envelope.Encrypt); encrypted != "" {
		plaintext, err := p.decryptEnvelope(cfg, encrypted)
		if err != nil {
			return connectors.WebhookResult{}, err
		}
		if err := json.Unmarshal(plaintext, &envelope); err != nil {
			return connectors.WebhookResult{}, fmt.Errorf("decode feishu webhook (decrypted): %s", err.Error())
		}
	}

	// url_verification challenge：按配置校验 token（若配置）后原样回显。
	if strings.EqualFold(strings.TrimSpace(envelope.Type), "url_verification") {
		if err := p.checkToken(cfg, envelope.Token); err != nil {
			return connectors.WebhookResult{}, err
		}
		challenge := strings.TrimSpace(envelope.Challenge)
		if challenge == "" {
			return connectors.WebhookResult{}, errors.New("feishu: webhook challenge is required")
		}
		return connectors.WebhookResult{Response: map[string]string{"challenge": challenge}}, nil
	}

	// 事件 v2：header.token 即 verification_token。
	if err := p.checkToken(cfg, envelope.Header.Token); err != nil {
		return connectors.WebhookResult{}, err
	}

	switch strings.TrimSpace(envelope.Header.EventType) {
	case eventMessageReceive:
		message, ok, err := p.inboundMessage(cfg, envelope.Header, envelope.Event)
		if err != nil {
			return connectors.WebhookResult{}, err
		}
		if !ok {
			return connectors.WebhookResult{}, nil // 自身消息/空文本：静默 202
		}
		if replyTo, _ := message.ProviderData[providerDataMessageID].(string); strings.TrimSpace(replyTo) != "" {
			p.rememberReplyTarget(connectorKey(cfg), message.Chat, replyTo)
		}
		return connectors.WebhookResult{Message: &message}, nil
	default:
		// 其它事件类型（审批、表情回复、加群等）本期不处理：返回空结果，
		// 宿主回 202，不产生 InboundMessage 也不触发宿主回复。
		return connectors.WebhookResult{}, nil
	}
}

// checkToken 校验 challenge/事件 header.token：
//   - 配置了 verification_token：常量时间比较，不匹配 → error（宿主 400）；
//   - 未配置（兼容策略）：无法验签，放行但每次告警。飞书控制台在开启事件订阅时
//     会同时下发 verification_token 与 encrypt_key，建议至少配置 encrypt_key
//     （密文可保证完整性与保密性），生产环境两者都配。
func (p *Provider) checkToken(cfg connectors.Config, token string) error {
	expected := cfg.Secret(secretVerificationToken)
	if expected == "" {
		p.logf("feishu: verification_token 未配置，跳过 webhook token 校验（建议在连接器配置中补充）")
		return nil
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(token)), []byte(expected)) != 1 {
		return errors.New("feishu: webhook verification token mismatch")
	}
	return nil
}

// decryptEnvelope 用连接器配置的 encrypt_key 解密事件载荷（缺配置/解密失败 → error）。
func (p *Provider) decryptEnvelope(cfg connectors.Config, encrypted string) ([]byte, error) {
	key := cfg.Secret(secretEncryptKey)
	if key == "" {
		return nil, errors.New("feishu: received encrypted payload but encrypt_key is not configured")
	}
	plaintext, err := decryptEventPayload(encrypted, key)
	if err != nil {
		return nil, fmt.Errorf("feishu: decrypt webhook payload failed: %s", redact(err.Error(), key))
	}
	return plaintext, nil
}

// ── 入站事件解析 ────────────────────────────────────────────────────────────

// inboundMessage 把 im.message.receive_v1 事件转成 InboundMessage：
//   - Chat=chat_id，UserID=sender open_id，MessageID=header.event_id（平台重试
//     携带同一 event_id，作为宿主去重账本的幂等键；缺失时回退 message_id）；
//   - 文本从 message.content JSON 取 text；非文本降级为文本占位（P3 补媒体下载）；
//   - bot 自身消息（sender_type=app）与空内容返回 ok=false（静默忽略）。
func (p *Provider) inboundMessage(cfg connectors.Config, header eventHeader, raw json.RawMessage) (connectors.InboundMessage, bool, error) {
	if len(raw) == 0 {
		return connectors.InboundMessage{}, false, nil
	}
	var event messageEventEnvelope
	if err := json.Unmarshal(raw, &event); err != nil {
		return connectors.InboundMessage{}, false, fmt.Errorf("decode feishu message event: %s", err.Error())
	}
	if strings.EqualFold(strings.TrimSpace(event.Sender.SenderType), "app") {
		return connectors.InboundMessage{}, false, nil
	}

	message := event.Message
	chatID := strings.TrimSpace(message.ChatID)
	userID := strings.TrimSpace(event.Sender.SenderID.OpenID)
	messageID := firstNonEmpty(strings.TrimSpace(header.EventID), strings.TrimSpace(message.MessageID))
	if chatID == "" || userID == "" || messageID == "" {
		return connectors.InboundMessage{}, false, nil
	}

	text, media, err := extractContent(message)
	if err != nil {
		return connectors.InboundMessage{}, false, err
	}
	if strings.TrimSpace(text) == "" {
		return connectors.InboundMessage{}, false, nil
	}

	username := firstNonEmpty(strings.TrimSpace(event.Sender.Name), strings.TrimSpace(event.Sender.SenderName), userID)
	title := firstNonEmpty(strings.TrimSpace(event.Chat.Name), chatID)
	return connectors.InboundMessage{
		ConnectorID: cfg.ID,
		Provider:    Name,
		Chat:        chatID,
		ChatTitle:   title,
		MessageID:   messageID,
		UserID:      userID,
		UserName:    username,
		Text:        text,
		Media:       media,
		ProviderData: map[string]any{
			providerDataMessageID: strings.TrimSpace(message.MessageID),
			providerDataChatType:  strings.TrimSpace(message.ChatType),
			providerDataEventType: strings.TrimSpace(header.EventType),
		},
		ReceivedAt: p.now().UTC(),
	}, true, nil
}

// extractContent 解析 message.content（JSON 字符串）：
//   - text：取 text 字段，剔除 @提及占位符（@_user_N）后做 HTML 反转义；
//   - 非文本：降级为文本占位（[图片]/[语音]/…），并尽量附带媒体元数据
//     （FileID/FileName），真正的下载/转码留待 P3 媒体切片。
func extractContent(message messageBody) (string, []connectors.Media, error) {
	messageType := strings.TrimSpace(message.MessageType)
	if messageType == "text" {
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(message.Content)), &payload); err != nil {
			return "", nil, fmt.Errorf("decode feishu text content: %s", err.Error())
		}
		return strings.TrimSpace(html.UnescapeString(stripMentions(payload.Text, message.Mentions))), nil, nil
	}
	placeholder, media := placeholderFor(messageType, message.Content)
	return placeholder, media, nil
}

// placeholderFor 为非文本消息生成占位文本与媒体元数据。
func placeholderFor(messageType, content string) (string, []connectors.Media) {
	var fields map[string]any
	if strings.TrimSpace(content) != "" {
		_ = json.Unmarshal([]byte(content), &fields)
	}
	field := func(name string) string {
		if value, ok := fields[name].(string); ok {
			return strings.TrimSpace(value)
		}
		return ""
	}
	switch strings.TrimSpace(messageType) {
	case "image":
		return "[图片]", mediaEntry("image", field("image_key"), "")
	case "audio":
		return "[语音]", mediaEntry("voice", field("file_key"), "")
	case "file":
		return "[文件]", mediaEntry("file", field("file_key"), field("file_name"))
	case "video", "media":
		return "[视频]", mediaEntry("video", field("file_key"), "")
	case "sticker":
		return "[表情包]", nil
	case "post":
		return "[富文本]", nil
	case "merge_forward":
		return "[合并转发消息]", nil
	case "share_chat", "share_user":
		return "[分享名片]", nil
	default:
		return "[暂不支持的消息类型]", nil
	}
}

func mediaEntry(kind, fileID, fileName string) []connectors.Media {
	if strings.TrimSpace(fileID) == "" && strings.TrimSpace(fileName) == "" {
		return nil
	}
	return []connectors.Media{{Kind: kind, FileID: strings.TrimSpace(fileID), FileName: strings.TrimSpace(fileName)}}
}

// stripMentions 移除群聊文本里的 @提及占位符（@_user_1 形式）。
// 上游会再按 bot open_id 精确剔除，本切片只保留原文本、去掉占位符即可。
func stripMentions(text string, mentions []messageMention) string {
	for _, mention := range mentions {
		if key := strings.TrimSpace(mention.Key); key != "" {
			text = strings.ReplaceAll(text, key, "")
		}
	}
	return strings.TrimSpace(text)
}

// ── HTTP 细节 ───────────────────────────────────────────────────────────────

func (p *Provider) doJSON(ctx context.Context, method, base, path, bearer string, payload, out any, secrets ...string) error {
	ctx, cancel := context.WithTimeout(ctx, defaultHTTPTimeout)
	defer cancel()

	endpoint, err := buildURL(base, path)
	if err != nil {
		return err
	}
	redactAll := func(message string) string {
		return redact(message, append(append([]string{}, secrets...), bearer)...)
	}

	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode feishu %s payload: %w", path, err)
		}
		body = bytes.NewReader(encoded)
	}

	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("build feishu %s request: %s", path, redactAll(err.Error()))
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	if strings.TrimSpace(bearer) != "" {
		request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(bearer))
	}

	response, err := p.client.Do(request)
	if err != nil {
		// *url.Error 文本可能带出请求 URL/头：不 %w 包装，脱敏后作为 cause。
		return &requestError{operation: path, cause: errors.New(redactAll(err.Error()))}
	}
	defer response.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes))
	if err != nil {
		return fmt.Errorf("read feishu %s response: %w", path, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &requestError{
			operation:  path,
			statusCode: response.StatusCode,
			status:     response.Status,
			apiMessage: redactAll(string(raw)),
		}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode feishu %s response: %s", path, redactAll(err.Error()))
	}
	return nil
}

// buildURL 把 base 与 API 路径拼成绝对 URL（保留 base 的路径前缀；
// path 可携带 "?query"，如 /open-apis/im/v1/messages?receive_id_type=chat_id）。
func buildURL(base, path string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("feishu: api_base must be an absolute URL")
	}
	requestPath, rawQuery, _ := strings.Cut(path, "?")
	parsed.Path = strings.TrimRight(parsed.Path, "/") + requestPath
	parsed.RawQuery = rawQuery
	parsed.Fragment = ""
	return parsed.String(), nil
}

func validateBaseURL(base string) error {
	if strings.TrimSpace(base) == "" {
		return errors.New("feishu: api_base is required（默认 https://open.feishu.cn）")
	}
	_, err := buildURL(base, "/")
	return err
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

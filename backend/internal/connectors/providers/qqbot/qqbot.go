// Package qqbot 实现 QQ 官方机器人通道适配器（WS gateway 长连接）。
//
// 设计见 docs/bots/04-migration-roadmap.md P2「QQ 适配器（WS gateway、断线 resume）」。
// 切片 1 范围：App Access Token 缓存、gateway 握手（Identify/Resume/心跳）、
// 群@/C2C（含频道 @ 的防御性解析）文本入站、被动回复（msg_id + msg_seq）与
// 游标持久化（gateway_session_id / gateway_seq）；媒体与主动消息能力待续。
//
// 协议细节镜像上游 codex-server/backend/internal/bots/qqbot.go，契约适配为
// connectors.Provider / PollingProvider（无新增接口）。
package qqbot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/api"
)

// Name 是通道标识（与 connectors.Config.Provider 对应）。
const Name = "qqbot"

const (
	defaultAPIBase   = "https://api.sgroup.qq.com"
	sandboxAPIBase   = "https://sandbox.api.sgroup.qq.com"
	defaultTokenBase = "https://bots.qq.com"

	defaultRetryDelay = time.Second
	maxRetryDelay     = 30 * time.Second
	httpTimeout       = 15 * time.Second

	// tokenRefreshMargin 提前刷新令牌，避免边界过期导致请求 401。
	tokenRefreshMargin = 5 * time.Minute
	// defaultTokenTTL 是响应缺省 expires_in 时的兜底有效期。
	defaultTokenTTL = 7200 * time.Second

	// gatewayIntents 至少覆盖群@（GROUP_AT_MESSAGE_CREATE）与 C2C 私信
	// （C2C_MESSAGE_CREATE）：QQ 的 1<<25 = GROUP_AND_C2C_EVENT。
	gatewayIntents = 1 << 25

	// PollingState 游标键（docs/bots/02-solution-design.md §3 Conversation.cursor）。
	gatewaySessionKey = "gateway_session_id"
	gatewaySeqKey     = "gateway_seq"

	// maxTrackedChats 是进程内被动回复状态（最近 msg_id / msg_seq）的防御上限。
	maxTrackedChats = 4096
)

// 聊天键前缀（InboundMessage.Chat 的稳定格式；Send 据此选择回复端点）。
const (
	chatKindGroup   = "group"
	chatKindUser    = "user"
	chatKindChannel = "channel"
)

// Provider 是 QQ 机器人通道适配器；HTTP/WS 细节可注入（测试用 httptest 假网关）。
type Provider struct {
	apiBase    string
	client     *http.Client
	dialer     *websocket.Dialer
	retryDelay time.Duration
	logf       func(format string, args ...any)
	now        func() time.Time

	// tokenMu 串行化令牌获取（持锁请求，天然单飞：并发/连续调用只发一次）。
	tokenMu    sync.Mutex
	tokenCache map[string]tokenCacheEntry

	// msgMu 保护被动回复所需的进程内状态：每 chat 的最新入站 msg_id 与
	// msg_seq 计数器（QQ 要求同一 msg_id 的回复 msg_seq 唯一且递增）。
	msgMu     sync.Mutex
	lastMsgID map[string]string
	msgSeq    map[string]int
}

type tokenCacheEntry struct {
	accessToken string
	expiresAt   time.Time
	appID       string
	appSecret   string
}

// Option 配置 Provider。
type Option func(*Provider)

// WithAPIBase 覆盖 API 基址（反向代理或测试服务器；显式覆盖优先于 sandbox 标记）。
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

// WithDialer 注入 WebSocket 拨号器（测试/代理）。
func WithDialer(dialer *websocket.Dialer) Option {
	return func(p *Provider) {
		if dialer != nil {
			p.dialer = dialer
		}
	}
}

// WithRetryDelay 覆盖重连退避基数（测试用）。
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

// New 构造 QQ 机器人 Provider。
func New(opts ...Option) *Provider {
	provider := &Provider{
		apiBase:    defaultAPIBase,
		client:     &http.Client{Timeout: httpTimeout},
		dialer:     websocket.DefaultDialer,
		retryDelay: defaultRetryDelay,
		logf:       log.Printf,
		now:        time.Now,
	}
	for _, opt := range opts {
		opt(provider)
	}
	return provider
}

// Name 实现 connectors.Provider。
func (p *Provider) Name() string { return Name }

// Schema 是 QQ 机器人的配置面（三端前端据此渲染）。
func (p *Provider) Schema() api.ProviderSchema {
	return api.ProviderSchema{
		Provider: Name,
		Label:    "QQ 机器人",
		// 只声明已实现的能力：切片 1 为 WS gateway 长连接（无 webhook 入口）。
		Modes: []string{"gateway"},
		Fields: []api.FieldSchema{
			{
				Key: "app_id", Label: "AppID", Type: api.FieldText, Required: true,
				Placeholder: "1020xxxx", Help: "QQ 开放平台机器人 AppID",
			},
			{
				Key: "app_secret", Label: "AppSecret", Type: api.FieldSecret, Required: true,
				Help: "QQ 开放平台机器人 AppSecret（只写不读）",
			},
			{
				Key: "sandbox", Label: "沙箱环境", Type: api.FieldBool, Default: "false",
				Help: "开启后使用沙箱 API 基址（sandbox.api.sgroup.qq.com）",
			},
			{
				Key: "api_base", Label: "API 基址", Type: api.FieldURL,
				Help: "可留空（默认 https://api.sgroup.qq.com）；可指向测试替身或反向代理",
			},
		},
	}
}

// Activate 获取 App Access Token（校验凭据并预热令牌缓存）。
func (p *Provider) Activate(ctx context.Context, cfg connectors.Config) (connectors.Activation, error) {
	appID := p.appID(cfg)
	if appID == "" {
		return connectors.Activation{}, errors.New("qqbot: app_id is required")
	}
	if p.appSecret(cfg) == "" {
		return connectors.Activation{}, errors.New("qqbot: app_secret is required")
	}
	if _, err := p.accessToken(ctx, cfg, false); err != nil {
		return connectors.Activation{}, err
	}
	info := "app_id=" + appID
	if p.sandbox(cfg) {
		info += "（沙箱）"
	}
	return connectors.Activation{Info: info}, nil
}

// Deactivate 无远端状态需要清理（gateway 连接随轮询 ctx 退出）。
func (p *Provider) Deactivate(context.Context, connectors.Config) error { return nil }

// SupportsPolling 报告是否应启动 gateway 长连接（app_id + app_secret 齐备）。
func (p *Provider) SupportsPolling(cfg connectors.Config) bool {
	return p.appID(cfg) != "" && p.appSecret(cfg) != ""
}

// Send 发送文本消息（QQ 官方 v2 消息接口）。
//
// 被动回复：若该 chat 最近收到过入站消息，带上其事件 id 作为 msg_id，并按 chat
// 递增 msg_seq（QQ 对同一 msg_id 的回复按 msg_seq 去重，必须唯一且递增）。
// 没有可用 msg_id 时走主动消息路径（与上游实现一致：不带 msg_id/msg_seq 直接
// POST），配额/权限由平台裁决，失败错误文本已脱敏。
func (p *Provider) Send(ctx context.Context, cfg connectors.Config, chat string, msgs []connectors.OutboundMessage) error {
	if p.appID(cfg) == "" || p.appSecret(cfg) == "" {
		return errors.New("qqbot: app_id and app_secret are required")
	}
	target, err := parseChat(chat)
	if err != nil {
		return err
	}
	if target.kind == chatKindChannel {
		return fmt.Errorf("qqbot: 频道（guild）消息发送未实现（切片 1 仅支持群/C2C）：%s", target.chat)
	}
	msgID := p.lastInboundMsgID(target.chat)
	for _, msg := range msgs {
		text := strings.TrimSpace(msg.Text)
		if text == "" {
			continue
		}
		if err := p.sendText(ctx, cfg, target, text, msgID); err != nil {
			return err
		}
	}
	return nil
}

// chatTarget 是 Send 的解析结果（聊天键 → 回复端点）。
type chatTarget struct {
	kind string
	// chat 是规范化后的会话键（group:<openid> / user:<openid> / channel:<id>）。
	chat string
	// openID 是群 openid 或用户 openid（回复端点路径参数）。
	openID string
}

// parseChat 解析稳定聊天键：group:<group_openid> / user:<openid> /
// channel:<channel_id>；无前缀时按 C2C openid 处理（与上游默认一致）。
func parseChat(chat string) (chatTarget, error) {
	trimmed := strings.TrimSpace(chat)
	if trimmed == "" {
		return chatTarget{}, errors.New("qqbot: chat is required")
	}
	for _, kind := range []string{chatKindGroup, chatKindChannel, chatKindUser} {
		if !strings.HasPrefix(trimmed, kind+":") {
			continue
		}
		openID := strings.TrimSpace(strings.TrimPrefix(trimmed, kind+":"))
		if openID == "" {
			return chatTarget{}, fmt.Errorf("qqbot: chat key %q is missing openid", trimmed)
		}
		return chatTarget{kind: kind, chat: trimmed, openID: openID}, nil
	}
	return chatTarget{kind: chatKindUser, chat: trimmed, openID: trimmed}, nil
}

// messagesEndpoint 返回消息发送端点（群 /v2/groups/{id}/messages，
// C2C /v2/users/{id}/messages）。
func messagesEndpoint(base string, target chatTarget) string {
	if target.kind == chatKindGroup {
		return base + "/v2/groups/" + url.PathEscape(target.openID) + "/messages"
	}
	return base + "/v2/users/" + url.PathEscape(target.openID) + "/messages"
}

func (p *Provider) sendText(ctx context.Context, cfg connectors.Config, target chatTarget, text, msgID string) error {
	payload := map[string]any{
		"content":  text,
		"msg_type": 0,
	}
	if trimmed := strings.TrimSpace(msgID); trimmed != "" {
		payload["msg_id"] = trimmed
		payload["msg_seq"] = p.nextMsgSeq(target.chat)
	}
	return p.apiJSON(ctx, cfg, http.MethodPost, messagesEndpoint(p.baseFor(cfg), target), payload, nil)
}

// nextMsgSeq 按 chat 递增 msg_seq：同一入站 msg_id 的多段被动回复必须携带
// 唯一且递增的 msg_seq（相同值会被 QQ 去重丢弃）；按 chat 单调递增同时覆盖
// 「同一 msg_id 连续多段」与「不同 msg_id 交替回复」两种情况。
func (p *Provider) nextMsgSeq(chat string) int {
	p.msgMu.Lock()
	defer p.msgMu.Unlock()
	if p.msgSeq == nil {
		p.msgSeq = make(map[string]int)
	}
	p.msgSeq[chat]++
	return p.msgSeq[chat]
}

// rememberInboundMsgID 记录 chat 最近一次入站事件 id（被动回复的 msg_id）。
func (p *Provider) rememberInboundMsgID(chat, msgID string) {
	chat = strings.TrimSpace(chat)
	msgID = strings.TrimSpace(msgID)
	if chat == "" || msgID == "" {
		return
	}
	p.msgMu.Lock()
	defer p.msgMu.Unlock()
	if p.lastMsgID == nil {
		p.lastMsgID = make(map[string]string)
	}
	if len(p.lastMsgID) >= maxTrackedChats {
		if _, exists := p.lastMsgID[chat]; !exists {
			// 防御性上限：极端多会话场景下丢弃整表（被动回复降级为主动消息）。
			p.lastMsgID = make(map[string]string)
		}
	}
	p.lastMsgID[chat] = msgID
}

func (p *Provider) lastInboundMsgID(chat string) string {
	p.msgMu.Lock()
	defer p.msgMu.Unlock()
	return p.lastMsgID[strings.TrimSpace(chat)]
}

// ── 配置读取 ────────────────────────────────────────────────────────────────

func (p *Provider) appID(cfg connectors.Config) string {
	return strings.TrimSpace(cfg.Setting("app_id", ""))
}

func (p *Provider) appSecret(cfg connectors.Config) string {
	return cfg.Secret("app_secret")
}

func (p *Provider) sandbox(cfg connectors.Config) bool {
	parsed, err := strconv.ParseBool(strings.TrimSpace(cfg.Setting("sandbox", "")))
	return err == nil && parsed
}

// baseFor 解析连接器的 API 基址：Settings["api_base"] > 构造参数覆盖 > sandbox
// 基址 > 默认基址。空值/尾斜杠已归一。
func (p *Provider) baseFor(cfg connectors.Config) string {
	if base := strings.TrimSpace(cfg.Setting("api_base", "")); base != "" {
		return strings.TrimRight(base, "/")
	}
	if p.apiBase != defaultAPIBase {
		// 构造参数显式覆盖（测试替身/反向代理）优先于 sandbox 标记。
		return p.apiBase
	}
	if p.sandbox(cfg) {
		return sandboxAPIBase
	}
	return p.apiBase
}

// ── 令牌 ────────────────────────────────────────────────────────────────────

// accessToken 返回 App Access Token：按连接器 ID 缓存，带过期时间并提前
// tokenRefreshMargin 刷新；forceRefresh 用于 401 后的强制重取。
func (p *Provider) accessToken(ctx context.Context, cfg connectors.Config, forceRefresh bool) (string, error) {
	appID := p.appID(cfg)
	appSecret := p.appSecret(cfg)
	if appID == "" || appSecret == "" {
		return "", errors.New("qqbot: app_id and app_secret are required")
	}
	cacheKey := p.tokenCacheKey(cfg, appID)

	p.tokenMu.Lock()
	defer p.tokenMu.Unlock()
	if !forceRefresh {
		if entry, ok := p.tokenCache[cacheKey]; ok && strings.TrimSpace(entry.accessToken) != "" &&
			entry.appID == appID && entry.appSecret == appSecret &&
			p.now().Before(entry.expiresAt.Add(-tokenRefreshMargin)) {
			return entry.accessToken, nil
		}
	}
	accessToken, ttl, err := p.fetchAccessToken(ctx, p.baseFor(cfg), appID, appSecret)
	if err != nil {
		return "", err
	}
	if p.tokenCache == nil {
		p.tokenCache = make(map[string]tokenCacheEntry)
	}
	p.tokenCache[cacheKey] = tokenCacheEntry{
		accessToken: accessToken,
		expiresAt:   p.now().Add(ttl),
		appID:       appID,
		appSecret:   appSecret,
	}
	return accessToken, nil
}

// tokenCacheKey 按 connectorID 缓存（无 ID 时回落 appID）；条目同时校验
// appID/appSecret，配置换凭据后不会复用旧令牌。
func (p *Provider) tokenCacheKey(cfg connectors.Config, appID string) string {
	if id := strings.TrimSpace(cfg.ID); id != "" {
		return id
	}
	return appID
}

// fetchAccessToken POST {api_base}/app/getAppAccessToken。
// 官方令牌端点在 bots.qq.com：默认基址（未显式覆盖）下主端点 404/405 时回退，
// 测试替身/自建反代（api_base 显式指定）不会触发回退。错误文本已脱敏。
func (p *Provider) fetchAccessToken(ctx context.Context, base, appID, appSecret string) (string, time.Duration, error) {
	payload, err := json.Marshal(map[string]string{"appId": appID, "clientSecret": appSecret})
	if err != nil {
		return "", 0, fmt.Errorf("qqbot: encode token request: %w", err)
	}
	endpoints := []string{base + "/app/getAppAccessToken"}
	if base == defaultAPIBase || base == sandboxAPIBase {
		endpoints = append(endpoints, defaultTokenBase+"/app/getAppAccessToken")
	}
	var lastErr error
	for _, endpoint := range endpoints {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return "", 0, fmt.Errorf("qqbot: build token request: %s", redact(err.Error(), appSecret))
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := p.client.Do(request)
		if err != nil {
			return "", 0, fmt.Errorf("qqbot: token request failed: %s", redact(err.Error(), appSecret))
		}
		raw, readErr := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		response.Body.Close()
		if readErr != nil {
			return "", 0, fmt.Errorf("qqbot: read token response: %w", readErr)
		}
		if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed {
			lastErr = &apiError{
				method: http.MethodPost, endpoint: endpoint,
				statusCode: response.StatusCode, status: response.Status,
				body: truncate(redact(string(raw), appSecret), 200),
			}
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return "", 0, &apiError{
				method: http.MethodPost, endpoint: endpoint,
				statusCode: response.StatusCode, status: response.Status,
				body: truncate(redact(string(raw), appSecret), 200),
			}
		}
		var envelope struct {
			AccessToken string          `json:"access_token"`
			ExpiresIn   json.RawMessage `json:"expires_in"`
			Code        int             `json:"code"`
			Message     string          `json:"message"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return "", 0, fmt.Errorf("qqbot: invalid token response (HTTP %d): %s",
				response.StatusCode, truncate(redact(string(raw), appSecret), 200))
		}
		accessToken := strings.TrimSpace(envelope.AccessToken)
		if accessToken == "" {
			detail := strings.TrimSpace(envelope.Message)
			if detail == "" {
				detail = truncate(string(raw), 200)
			}
			return "", 0, fmt.Errorf("qqbot: token request failed: %s", truncate(redact(detail, appSecret), 200))
		}
		ttl := parseExpiresIn(envelope.ExpiresIn)
		if ttl <= 0 {
			ttl = defaultTokenTTL
		}
		return accessToken, ttl, nil
	}
	if lastErr != nil {
		return "", 0, lastErr
	}
	return "", 0, errors.New("qqbot: token request failed")
}

// parseExpiresIn 解析 expires_in：QQ 在不同版本返回数字或字符串，缺省返回 0
// （由调用方回落 defaultTokenTTL）。
func parseExpiresIn(raw json.RawMessage) time.Duration {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return 0
	}
	if strings.HasPrefix(trimmed, `"`) {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return 0
		}
		seconds, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	var seconds int
	if err := json.Unmarshal(raw, &seconds); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	var floatSeconds float64
	if err := json.Unmarshal(raw, &floatSeconds); err == nil && floatSeconds > 0 {
		return time.Duration(floatSeconds) * time.Second
	}
	return 0
}

// ── HTTP ────────────────────────────────────────────────────────────────────

// apiError 是带状态码的 QQ 开放平台请求错误（错误文本已脱敏）。
type apiError struct {
	method     string
	endpoint   string
	statusCode int
	status     string
	body       string
	cause      error
}

func (e *apiError) Error() string {
	if e == nil {
		return ""
	}
	if e.cause != nil {
		return fmt.Sprintf("qqbot: %s %s request failed: %v", e.method, e.endpoint, e.cause)
	}
	detail := strings.TrimSpace(e.body)
	switch {
	case e.status != "" && detail != "":
		return fmt.Sprintf("qqbot: %s %s returned %s: %s", e.method, e.endpoint, e.status, detail)
	case e.status != "":
		return fmt.Sprintf("qqbot: %s %s returned %s", e.method, e.endpoint, e.status)
	case detail != "":
		return fmt.Sprintf("qqbot: %s %s request failed: %s", e.method, e.endpoint, detail)
	default:
		return fmt.Sprintf("qqbot: %s %s request failed", e.method, e.endpoint)
	}
}

func (e *apiError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// apiJSON 调用 QQ 开放平台 API：自动附带 QQBot 令牌，401 时强制刷新令牌重试一次。
func (p *Provider) apiJSON(ctx context.Context, cfg connectors.Config, method, endpoint string, payload, out any) error {
	secrets := []string{p.appSecret(cfg)}
	token, err := p.accessToken(ctx, cfg, false)
	if err != nil {
		return err
	}
	err = p.doJSON(ctx, method, endpoint, token, secrets, payload, out)
	var apiErr *apiError
	if errors.As(err, &apiErr) && apiErr.statusCode == http.StatusUnauthorized {
		refreshed, refreshErr := p.accessToken(ctx, cfg, true)
		if refreshErr != nil {
			return refreshErr
		}
		return p.doJSON(ctx, method, endpoint, refreshed, secrets, payload, out)
	}
	return err
}

// doJSON 发起一次带 QQBot 令牌的 JSON 请求；错误文本抹除令牌与 secrets。
func (p *Provider) doJSON(ctx context.Context, method, endpoint, token string, secrets []string, payload, out any) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("qqbot: encode %s payload: %w", method, err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("qqbot: build %s request: %s", method, redactAll(err.Error(), token, secrets...))
	}
	request.Header.Set("Authorization", "QQBot "+token)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := p.client.Do(request)
	if err != nil {
		// *url.Error 的文本包含完整请求 URL；不做 %w 包装，避免上层解出原始错误。
		return &apiError{method: method, endpoint: endpoint, cause: errors.New(redactAll(err.Error(), token, secrets...))}
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("qqbot: read %s response: %w", method, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &apiError{
			method: method, endpoint: endpoint,
			statusCode: response.StatusCode, status: response.Status,
			body: truncate(redactAll(string(raw), token, secrets...), 300),
		}
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("qqbot: decode %s response: %w", method, err)
	}
	return nil
}

// redact 从错误/响应文本中抹除密钥与访问令牌（app_secret 绝不回流到日志/API）。
func redact(message string, secrets ...string) string {
	for _, secret := range secrets {
		secret = strings.TrimSpace(secret)
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "***")
		}
	}
	return message
}

// redactAll 抹除访问令牌与调用方提供的全部密钥（app_secret 绝不回流）。
func redactAll(message, token string, secrets ...string) string {
	all := make([]string, 0, len(secrets)+1)
	all = append(all, token)
	all = append(all, secrets...)
	return redact(message, all...)
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}

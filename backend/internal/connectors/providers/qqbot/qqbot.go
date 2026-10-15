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
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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
	// defaultAPIBase 是官方 2026-08 起统一的接口域名（变更记录：所有接口
	// 调用域名统一为 api.bot.qq.com）。legacy 域名作为 404/405 回退保留，
	// 兼容存量反代/内网部署。
	defaultAPIBase   = "https://api.bot.qq.com"
	legacyAPIBase    = "https://api.sgroup.qq.com"
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

	// maxMessageRunes 是单条文本消息的分片上限：QQ 文本内容上限 2000 字符，
	// 按 rune 切分（与上游 codex-server 的 qqbotMessageTextLimitRunes 一致），
	// 被动回复的多个分片按 msg_seq 递增去重。
	maxMessageRunes = 2000
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

	// mediaHostGuard 是入站/出站远端媒体的出口守卫（nil = 默认公网守卫，
	// 见 media.go）；测试/内网部署可覆盖。
	mediaHostGuard mediaHostGuardFunc
	// mediaLimitBytes 覆盖入站媒体下载上限（0 = 默认 maxInboundMediaBytes）。
	mediaLimitBytes int64
	// chunkedThreshold 覆盖分片上传阈值（0 = 默认 chunkedUploadThreshold）。
	chunkedThreshold int64
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

// WithMediaHostGuard 覆盖远端媒体的主机安全守卫（测试/内网部署用；
// nil 保持默认公网守卫：拒绝回环/私网/链路本地/组播/CGNAT）。
func WithMediaHostGuard(guard func(host string) error) Option {
	return func(p *Provider) {
		if guard != nil {
			p.mediaHostGuard = mediaHostGuardFunc(guard)
		}
	}
}

// WithInboundMediaLimit 覆盖入站媒体下载上限（字节；测试用，<=0 忽略）。
func WithInboundMediaLimit(limit int64) Option {
	return func(p *Provider) {
		if limit > 0 {
			p.mediaLimitBytes = limit
		}
	}
}

// WithChunkedUploadThreshold 覆盖分片上传阈值（字节；测试用，<=0 忽略）。
func WithChunkedUploadThreshold(limit int64) Option {
	return func(p *Provider) {
		if limit > 0 {
			p.chunkedThreshold = limit
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
			{
				Key: "markdown_support", Label: "Markdown 消息", Type: api.FieldBool, Default: "false",
				Help: "开启后回复以 markdown（msg_type=2）发送；默认纯文本（msg_type=0）",
			},
			{
				Key: "share_session_in_channel", Label: "群聊共享会话", Type: api.FieldBool, Default: "true",
				Help: "开启时同一群共用一个 agent 会话；关闭后群内按成员隔离上下文（每人独立会话）",
			},
			{
				Key: "intents", Label: "Gateway Intents", Type: api.FieldNumber,
				Help: "高级：网关事件订阅位掩码；留空=默认（仅群@与 C2C 事件）",
			},
			{
				Key: "group_message_mode", Label: "群消息接收模式", Type: api.FieldEnum, Default: "mention",
				Options: []api.Option{
					{Value: "mention", Label: "仅@机器人（默认）"},
					{Value: "all", Label: "全部群消息"},
				},
				Help: "平台开启「接收所有消息」后生效：mention=仅响应@机器人的消息；all=每条群消息都交给 agent",
			},
		},
		// QQ 官方开放平台没有扫码登录协议（凭据来自 bot.q.qq.com 后台），
		// 「授权校验」= 等价便捷流：校验 app_id/app_secret 并拉取机器人信息。
		Actions: []api.ActionSpec{
			{Key: actionCheckCredentials, Label: "授权校验", Kind: "button"},
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

// Send 发送消息（QQ 官方 v2 消息接口：文本 + rich media）。
//
// 出站策略（对齐上游 codex-server）：
//   - 文本按 maxMessageRunes 分片（多段被动回复 msg_seq 递增去重）；
//   - markdown_support=true 时以 msg_type=2 + markdown.content 发送；
//   - 被动回复（带最近入站的 msg_id）失败且属可降级错误时，自动退化为主动发送；
//   - Media 先 POST /files 上传取 file_info，再以 msg_type=7 发送。
//
// 没有可用 msg_id 时直接走主动消息路径：配额/权限由平台裁决，错误文本已脱敏。
func (p *Provider) Send(ctx context.Context, cfg connectors.Config, chat string, msgs []connectors.OutboundMessage) error {
	if p.appID(cfg) == "" || p.appSecret(cfg) == "" {
		return errors.New("qqbot: app_id and app_secret are required")
	}
	target, err := parseChat(chat)
	if err != nil {
		return err
	}
	if target.kind == chatKindChannel {
		return fmt.Errorf("qqbot: 频道（guild）消息发送未实现（仅支持群/C2C）：%s", target.chat)
	}
	// usePassive 在首个被动回复被平台拒绝（可降级错误）后翻转为 false，
	// 后续消息与媒体直接走主动路径（与上游一致，避免重复失败）。
	usePassive := strings.TrimSpace(p.lastInboundMsgID(target.chat)) != ""
	for _, msg := range msgs {
		text := strings.TrimSpace(msg.Text)
		if text != "" {
			for _, chunk := range splitMessageText(text, maxMessageRunes) {
				if err := p.sendTextWithFallback(ctx, cfg, target, chunk, &usePassive); err != nil {
					return err
				}
			}
		}
		for _, media := range msg.Media {
			if err := p.sendMediaWithFallback(ctx, cfg, target, media, &usePassive); err != nil {
				return err
			}
		}
	}
	return nil
}

// sendTextWithFallback 发送一段文本；被动失败且可降级时翻转为主动重试。
func (p *Provider) sendTextWithFallback(ctx context.Context, cfg connectors.Config, target chatTarget, text string, usePassive *bool) error {
	passive := usePassive != nil && *usePassive
	err := p.sendText(ctx, cfg, target, text, passive)
	if err != nil && passive && shouldFallbackToProactive(err) {
		if usePassive != nil {
			*usePassive = false
		}
		err = p.sendText(ctx, cfg, target, text, false)
	}
	return err
}

// sendMediaWithFallback 发送一条媒体；语义同 sendTextWithFallback。
func (p *Provider) sendMediaWithFallback(ctx context.Context, cfg connectors.Config, target chatTarget, media connectors.Media, usePassive *bool) error {
	passive := usePassive != nil && *usePassive
	err := p.sendMedia(ctx, cfg, target, media, passive)
	if err != nil && passive && shouldFallbackToProactive(err) {
		if usePassive != nil {
			*usePassive = false
		}
		err = p.sendMedia(ctx, cfg, target, media, false)
	}
	return err
}

// chatTarget 是 Send 的解析结果（聊天键 → 回复端点）。
type chatTarget struct {
	kind string
	// chat 是规范化后的会话键（group:<openid> / user:<openid> / channel:<id>）。
	chat string
	// openID 是群 openid 或用户 openid（回复端点路径参数）。
	openID string
}

// parseChat 解析稳定聊天键：group:<group_openid>[:user:<member_openid>] /
// user:<openid> / channel:<channel_id>；无前缀时按 C2C openid 处理。
// 群键的 :user: 后缀（share_session_in_channel=false 的按人隔离会话）只影响
// 会话身份，回复端点始终用群 openid——此处剥离后缀得到 target.openID。
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
		if kind == chatKindGroup {
			if index := strings.Index(openID, ":user:"); index > 0 {
				openID = strings.TrimSpace(openID[:index])
			}
		}
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

func (p *Provider) sendText(ctx context.Context, cfg connectors.Config, target chatTarget, text string, passive bool) error {
	payload := map[string]any{
		"content":  text,
		"msg_type": 0,
	}
	if p.markdownSupport(cfg) {
		payload = map[string]any{
			"markdown": map[string]any{"content": text},
			"msg_type": 2,
		}
	}
	p.applyPassiveReply(payload, target, passive)
	return p.apiJSON(ctx, cfg, http.MethodPost, messagesEndpoint(p.baseFor(cfg), target), payload, nil)
}

// sendMedia 发送一条 rich media（msg_type=7）：先上传取 file_info 再发消息。
func (p *Provider) sendMedia(ctx context.Context, cfg connectors.Config, target chatTarget, media connectors.Media, passive bool) error {
	fileInfo, err := p.uploadRichMedia(ctx, cfg, target, media)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"msg_type": 7,
		"media":    map[string]any{"file_info": fileInfo},
	}
	p.applyPassiveReply(payload, target, passive)
	return p.apiJSON(ctx, cfg, http.MethodPost, messagesEndpoint(p.baseFor(cfg), target), payload, nil)
}

// uploadRichMedia 上传媒体并返回 file_info（POST /v2/{groups|users}/{id}/files，
// srv_send_msg=false 表示只上传、不自动发送）。
//
// 上传策略（对齐官方文档「URL 上传 / 整文件上传」）：
//   - 有远端 URL 且能声明 file_type：优先官方 URL 直传（平台代下转存，省一次
//     本机下载与 base64 膨胀）；直传失败回落下载 + file_data；
//   - 仅有本地路径（或无类型信息）：读取字节走 file_data（整文件上传）。
//     大文件分片上传为后续切片（docs/bots/04 缺口矩阵）。
func (p *Provider) uploadRichMedia(ctx context.Context, cfg connectors.Config, target chatTarget, media connectors.Media) (string, error) {
	endpoint := filesEndpoint(p.baseFor(cfg), target)
	if mediaURL := strings.TrimSpace(media.URL); mediaURL != "" {
		if fileType, ok := declaredMediaFileType(media); ok {
			fileInfo, err := p.uploadMediaByURL(ctx, cfg, endpoint, mediaURL, fileType)
			if err == nil && fileInfo != "" {
				return fileInfo, nil
			}
			if err != nil {
				p.logf("qqbot: URL 直传失败，回落下载上传：%v", err)
			}
		}
	}
	data, contentType, err := p.resolveOutboundMedia(ctx, media)
	if err != nil {
		return "", err
	}
	fileType := mediaFileType(media, contentType)
	// 大文件走官方分片上传（避免 base64 膨胀与单请求体量限制）；失败回落
	// 整文件上传（file_data）。
	if int64(len(data)) > p.chunkedUploadThresholdFor() {
		fileInfo, chunkErr := p.uploadMediaChunked(ctx, cfg, target, media, fileType, data)
		if chunkErr == nil && fileInfo != "" {
			return fileInfo, nil
		}
		if chunkErr != nil {
			p.logf("qqbot: 分片上传失败，回落整文件上传：%v", chunkErr)
		}
	}
	payload := map[string]any{
		"file_type":    fileType,
		"file_data":    base64.StdEncoding.EncodeToString(data),
		"srv_send_msg": false,
	}
	var response struct {
		FileInfo string `json:"file_info"`
	}
	if err := p.apiJSON(ctx, cfg, http.MethodPost, endpoint, payload, &response); err != nil {
		return "", err
	}
	fileInfo := strings.TrimSpace(response.FileInfo)
	if fileInfo == "" {
		return "", errors.New("qqbot: upload rich media returned empty file_info")
	}
	return fileInfo, nil
}

// declaredMediaFileType 在发送方已声明类型信息时给出 file_type（URL 直传
// 前置条件）：Kind 或 Content-Type 至少一个非空即可声明；都为空时返回 false
// （走下载嗅探）。
func declaredMediaFileType(media connectors.Media) (int, bool) {
	if strings.TrimSpace(media.Kind) == "" && strings.TrimSpace(media.ContentType) == "" {
		return 0, false
	}
	return mediaFileType(media, media.ContentType), true
}

// uploadMediaByURL 走官方 URL 直传（平台下载转存），返回 file_info。
// URL 先过出口守卫（公网校验），避免把内网地址交给平台触发不可预期失败。
func (p *Provider) uploadMediaByURL(ctx context.Context, cfg connectors.Config, endpoint, mediaURL string, fileType int) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(mediaURL))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || strings.TrimSpace(parsed.Host) == "" {
		return "", fmt.Errorf("qqbot: invalid media url %q", mediaURL)
	}
	guard := p.mediaHostGuard
	if guard == nil {
		guard = defaultMediaHostGuard
	}
	if err := guard(parsed.Hostname()); err != nil {
		return "", err
	}
	payload := map[string]any{
		"file_type":    fileType,
		"url":          strings.TrimSpace(mediaURL),
		"srv_send_msg": false,
	}
	var response struct {
		FileInfo string `json:"file_info"`
	}
	if err := p.apiJSON(ctx, cfg, http.MethodPost, endpoint, payload, &response); err != nil {
		return "", err
	}
	return strings.TrimSpace(response.FileInfo), nil
}

// resolveOutboundMedia 解析出站媒体字节（URL 下载 / 本地绝对路径；
// file:// 前缀按本地路径处理），返回内容与推导出的 Content-Type。
func (p *Provider) resolveOutboundMedia(ctx context.Context, media connectors.Media) ([]byte, string, error) {
	if mediaURL := strings.TrimSpace(media.URL); mediaURL != "" {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, mediaURL, nil)
		if err != nil {
			return nil, "", fmt.Errorf("qqbot: invalid media url %q", mediaURL)
		}
		guard := p.mediaHostGuard
		if guard == nil {
			guard = defaultMediaHostGuard
		}
		response, err := p.mediaDownloadClient(guard).Do(request)
		if err != nil {
			return nil, "", fmt.Errorf("qqbot: download media url %q failed: %w", mediaURL, err)
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, "", fmt.Errorf("qqbot: media url %q returned %s", mediaURL, response.Status)
		}
		limit := p.mediaTransferLimit()
		if response.ContentLength > limit {
			return nil, "", fmt.Errorf("qqbot: media too large (%d bytes > %d)", response.ContentLength, limit)
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
		if err != nil {
			return nil, "", fmt.Errorf("qqbot: read media url %q failed: %w", mediaURL, err)
		}
		if int64(len(data)) > limit {
			return nil, "", fmt.Errorf("qqbot: media too large (> %d bytes)", limit)
		}
		contentType := strings.TrimSpace(media.ContentType)
		if contentType == "" {
			contentType = strings.TrimSpace(response.Header.Get("Content-Type"))
		}
		if contentType == "" && len(data) > 0 {
			contentType = http.DetectContentType(data)
		}
		return data, contentType, nil
	}

	mediaPath := strings.TrimSpace(media.Path)
	if mediaPath == "" {
		return nil, "", errors.New("qqbot: media requires a local path or remote url")
	}
	if strings.HasPrefix(strings.ToLower(mediaPath), "file://") {
		parsed, err := url.Parse(mediaPath)
		if err != nil {
			return nil, "", fmt.Errorf("qqbot: invalid media file url %q", mediaPath)
		}
		mediaPath = filepath.FromSlash(parsed.Path)
	}
	if !filepath.IsAbs(mediaPath) {
		return nil, "", fmt.Errorf("qqbot: media file path must be absolute: %s", mediaPath)
	}
	info, err := os.Stat(mediaPath)
	if err != nil {
		return nil, "", fmt.Errorf("qqbot: stat media file %q failed: %w", mediaPath, err)
	}
	if info.IsDir() {
		return nil, "", fmt.Errorf("qqbot: media file path must be a file: %s", mediaPath)
	}
	if limit := p.mediaTransferLimit(); info.Size() > limit {
		return nil, "", fmt.Errorf("qqbot: media too large (%d bytes > %d)", info.Size(), limit)
	}
	data, err := os.ReadFile(mediaPath)
	if err != nil {
		return nil, "", fmt.Errorf("qqbot: read media file %q failed: %w", mediaPath, err)
	}
	contentType := strings.TrimSpace(media.ContentType)
	if contentType == "" && len(data) > 0 {
		contentType = http.DetectContentType(data)
	}
	return data, contentType, nil
}

// mediaFileType 解析上传 file_type：1 图片 / 2 视频 / 3 语音 / 4 文件；
// Kind 缺省时按 Content-Type 前缀推导（与上游一致）。
func mediaFileType(media connectors.Media, contentType string) int {
	switch strings.ToLower(strings.TrimSpace(media.Kind)) {
	case "image":
		return 1
	case "video":
		return 2
	case "audio", "voice":
		return 3
	case "", "file":
		lower := strings.ToLower(strings.TrimSpace(contentType))
		switch {
		case strings.HasPrefix(lower, "image/"):
			return 1
		case strings.HasPrefix(lower, "video/"):
			return 2
		case strings.HasPrefix(lower, "audio/"):
			return 3
		default:
			return 4
		}
	default:
		return 4
	}
}

// filesEndpoint 返回媒体上传端点（群 /v2/groups/{id}/files，
// C2C /v2/users/{id}/files）。
func filesEndpoint(base string, target chatTarget) string {
	if target.kind == chatKindGroup {
		return base + "/v2/groups/" + url.PathEscape(target.openID) + "/files"
	}
	return base + "/v2/users/" + url.PathEscape(target.openID) + "/files"
}

// applyPassiveReply 给被动回复补齐 msg_id/msg_seq；无最近入站 msg_id 时保持
// 主动消息语义（不带两字段直发，由平台裁决配额/权限）。
func (p *Provider) applyPassiveReply(payload map[string]any, target chatTarget, passive bool) {
	if !passive {
		return
	}
	msgID := strings.TrimSpace(p.lastInboundMsgID(target.chat))
	if msgID == "" {
		return
	}
	payload["msg_id"] = msgID
	payload["msg_seq"] = p.nextMsgSeq(target.chat)
}

// shouldFallbackToProactive 报告被动回复失败是否可降级为主动发送：
// 4xx 中排除 401（令牌问题，需刷新而非降级）与 429（限频，降级也会失败）。
func shouldFallbackToProactive(err error) bool {
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.statusCode == http.StatusUnauthorized || apiErr.statusCode == http.StatusTooManyRequests {
		return false
	}
	return apiErr.statusCode >= http.StatusBadRequest && apiErr.statusCode < http.StatusInternalServerError
}

// splitMessageText 按 rune 数把文本切成不超过 maxRunes 的分片；空串返回 nil。
func splitMessageText(value string, maxRunes int) []string {
	if value == "" {
		return nil
	}
	if maxRunes <= 0 {
		return []string{value}
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return []string{value}
	}
	chunks := make([]string, 0, (len(runes)+maxRunes-1)/maxRunes)
	for len(runes) > 0 {
		size := maxRunes
		if size > len(runes) {
			size = len(runes)
		}
		chunks = append(chunks, string(runes[:size]))
		runes = runes[size:]
	}
	return chunks
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

// markdownSupport 报告是否以 markdown 消息发送回复（缺省 false=纯文本）。
func (p *Provider) markdownSupport(cfg connectors.Config) bool {
	parsed, err := strconv.ParseBool(strings.TrimSpace(cfg.Setting("markdown_support", "")))
	return err == nil && parsed
}

// intentsFor 解析 Gateway 事件订阅位掩码：缺省/非法值回退缩小可用集
// （gatewayIntents=群@与 C2C）；settings["intents"] 可覆盖（对齐上游
// codex-server 的 qqbot_intents），用于订阅更多事件类型。
func (p *Provider) intentsFor(cfg connectors.Config) int {
	value := strings.TrimSpace(cfg.Setting("intents", ""))
	if value == "" {
		return gatewayIntents
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return gatewayIntents
	}
	return parsed
}

// shareSessionInChannelSetting 报告群聊是否共享会话：缺省 true=同一群共用一个
// agent 会话（保持历史行为）；false=群内按成员隔离（每人独立上下文）。
// 包级函数供事件解析使用（与 Send 的 chat 键解析同口径）。
func shareSessionInChannelSetting(cfg connectors.Config) bool {
	value := strings.TrimSpace(cfg.Setting("share_session_in_channel", ""))
	if value == "" {
		return true
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return true
	}
	return parsed
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
	if base == defaultAPIBase || base == legacyAPIBase || base == sandboxAPIBase {
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

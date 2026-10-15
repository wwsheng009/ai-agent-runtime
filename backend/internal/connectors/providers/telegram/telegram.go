// Package telegram 实现 Telegram Bot 通道适配器（长轮询 + webhook 双模式）。
//
// 设计见 docs/bots/04-migration-roadmap.md P0：长轮询 + 文本收发 + typing +
// 编辑式流式回复；webhook（setWebhook 注册 + secret_token 校验 + HandleWebhook
// 入站解析）已实现；媒体、命令全集在后续阶段增量补齐。
package telegram

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/api"
)

// Name 是通道标识（与 connectors.Config.Provider 对应）。
const Name = "telegram"

const (
	defaultAPIBase     = "https://api.telegram.org"
	defaultPollTimeout = 25 * time.Second
	defaultRetryDelay  = time.Second
	cursorKey          = "update_offset"
	typingInterval     = 4 * time.Second

	modeWebhook         = "webhook"
	settingWebhookURL   = "webhook_url"
	secretWebhookSecret = "webhook_secret"
	// webhookSecretHeader 是 setWebhook 配置 secret_token 后 Telegram 随每次
	// 回调下发的校验头（固定名称，见 Bot API 文档）。
	webhookSecretHeader = "X-Telegram-Bot-Api-Secret-Token"
	// maxWebhookBodyBytes 是 webhook body 读取上限（与飞书一致，防御超大请求）。
	maxWebhookBodyBytes = 1 << 20
)

// Provider 是 Telegram 通道适配器；HTTP 细节可注入（测试用 httptest）。
type Provider struct {
	apiBase     string
	client      *http.Client
	pollTimeout time.Duration
	retryDelay  time.Duration
	logf        func(format string, args ...any)
	now         func() time.Time
	// mediaLimitBytes 覆盖入站媒体下载上限（0 = 默认 defaultMediaLimitBytes）。
	mediaLimitBytes int64
}

// Option 配置 Provider。
type Option func(*Provider)

// WithAPIBase 覆盖 API 基址（如反向代理或测试服务器）。
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

// WithPollTimeout 覆盖长轮询超时。
func WithPollTimeout(d time.Duration) Option {
	return func(p *Provider) {
		if d > 0 {
			p.pollTimeout = d
		}
	}
}

// WithRetryDelay 覆盖轮询失败后的退避基数（测试用）。
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

// WithInboundMediaLimit 覆盖入站媒体下载上限（字节；测试/高级用，<=0 忽略）。
func WithInboundMediaLimit(limit int64) Option {
	return func(p *Provider) {
		if limit > 0 {
			p.mediaLimitBytes = limit
		}
	}
}

// New 构造 Telegram Provider。
func New(opts ...Option) *Provider {
	provider := &Provider{
		apiBase:     defaultAPIBase,
		client:      &http.Client{Timeout: defaultPollTimeout + 20*time.Second},
		pollTimeout: defaultPollTimeout,
		retryDelay:  defaultRetryDelay,
		logf:        log.Printf,
		now:         time.Now,
	}
	for _, opt := range opts {
		opt(provider)
	}
	return provider
}

// Name 实现 connectors.Provider。
func (p *Provider) Name() string { return Name }

// Schema 是 Telegram 的配置面（三端前端据此渲染）。
func (p *Provider) Schema() api.ProviderSchema {
	return api.ProviderSchema{
		Provider: Name,
		Label:    "Telegram",
		// 双模式均已实现：polling（RunPolling）与 webhook（Activate 按配置调用
		// setWebhook 注册 + HandleWebhook 入站解析），宿主按 mode 择一。
		Modes: []string{"polling", modeWebhook},
		Fields: []api.FieldSchema{
			{
				Key: "bot_token", Label: "Bot Token", Type: api.FieldSecret, Required: true,
				Placeholder: "123456:ABC-DEF…", Help: "在 @BotFather 创建 bot 后获取",
			},
			{
				Key: "api_base", Label: "API 基址", Type: api.FieldURL,
				Help: "可留空（默认 https://api.telegram.org）；可指向自建 Bot API 服务器或测试替身",
			},
			{
				Key: "mode", Label: "接入模式", Type: api.FieldEnum, Default: "polling",
				Options: []api.Option{{Value: "polling", Label: "长轮询（推荐）"}, {Value: "webhook", Label: "Webhook"}},
			},
			{Key: "proxy_url", Label: "代理地址", Type: api.FieldURL, Help: "可留空；仅此连接器生效"},
			{Key: "poll_timeout_seconds", Label: "长轮询超时（秒）", Type: api.FieldNumber, Default: "25"},
			{
				Key: settingWebhookURL, Label: "Webhook 地址", Type: api.FieldURL,
				Help: "公网可达的 https 地址（如 https://<public-base-url>/hooks/connectors/<id>）；填写后 Activate 会自动调用 setWebhook 注册",
			},
			{
				Key: secretWebhookSecret, Label: "Webhook Secret Token", Type: api.FieldSecret,
				Help: "Telegram setWebhook 的 secret_token，用于校验入站请求头 X-Telegram-Bot-Api-Secret-Token；留空则不校验（不安全，生产环境务必配置）",
			},
		},
	}
}

// baseFor 解析连接器的 API 基址（Settings["api_base"] > 构造参数 > 默认），
// 空值/尾斜杠已归一，供 call/getUpdates 拼接 /bot<token>/<method>。
func (p *Provider) baseFor(cfg connectors.Config) string {
	return strings.TrimRight(cfg.Setting("api_base", p.apiBase), "/")
}

// Activation 校验 token 并返回 bot 信息。
func (p *Provider) Activation(ctx context.Context, cfg connectors.Config) (connectors.Activation, error) {
	return p.Activate(ctx, cfg)
}

// Activate 调用 getMe 校验 bot_token；webhook 模式下若配置了 webhook_url，
// 再调用 setWebhook 注册回调（allowed_updates 仅 message，与轮询对齐）。
func (p *Provider) Activate(ctx context.Context, cfg connectors.Config) (connectors.Activation, error) {
	token := cfg.Secret("bot_token")
	if token == "" {
		return connectors.Activation{}, errors.New("telegram: bot_token is required")
	}
	base := p.baseFor(cfg)
	var result struct {
		ID        int64  `json:"id"`
		Username  string `json:"username"`
		FirstName string `json:"first_name"`
	}
	if err := p.call(ctx, base, token, "getMe", nil, &result); err != nil {
		return connectors.Activation{}, err
	}
	// 仅 webhook 模式且配置了公网 URL 才注册：轮询/未填 URL 时保持原有
	// getMe 行为不变；注册失败返回 error（宿主提示改回 polling 或修正 URL）。
	if modeOf(cfg) == modeWebhook {
		if webhookURL := cfg.Setting(settingWebhookURL, ""); webhookURL != "" {
			params := map[string]any{
				"url":             webhookURL,
				"allowed_updates": []string{"message"},
			}
			// secret_token 仅在配置时下发：Telegram 要求 1–256 字符，空串会被拒。
			if secret := cfg.Secret(secretWebhookSecret); secret != "" {
				params["secret_token"] = secret
			}
			if err := p.call(ctx, base, token, "setWebhook", params, nil); err != nil {
				return connectors.Activation{}, err
			}
		}
	}
	info := strings.TrimSpace(result.Username)
	if info != "" {
		info = "@" + info
	}
	return connectors.Activation{Info: info}, nil
}

// Deactivate 尽力注销 webhook：仅 webhook 模式且配置了 token 时调
// deleteWebhook；失败只记日志、不阻断本地停用（远端错误不应卡住宿主）。
func (p *Provider) Deactivate(ctx context.Context, cfg connectors.Config) error {
	if modeOf(cfg) != modeWebhook {
		return nil
	}
	token := cfg.Secret("bot_token")
	if token == "" {
		return nil
	}
	if err := p.call(ctx, p.baseFor(cfg), token, "deleteWebhook", nil, nil); err != nil {
		p.logf("telegram: deleteWebhook failed: %v", err)
	}
	return nil
}

// Send 逐条发送文本 + 媒体消息。
//
// 媒体来源两类：OutboundMessage.Media，以及回复文本里的媒体指令
// （```telegram-attachments``` 围栏块、`IMAGE:/VIDEO:/FILE:/VOICE:` 指令行，
// 见 connectors.ParseOutboundMediaDirectives）；指令行从可见文本剥离。可见文本
// 先作为普通消息发出，随后逐条发送媒体（本地路径走 multipart 上传；URL 交给
// Telegram 拉取；已有 file_id 直接复用）。单条媒体失败降级为文本提示。
func (p *Provider) Send(ctx context.Context, cfg connectors.Config, chat string, msgs []connectors.OutboundMessage) error {
	token := cfg.Secret("bot_token")
	if token == "" {
		return errors.New("telegram: bot_token is required")
	}
	base := p.baseFor(cfg)
	for _, msg := range msgs {
		visibleText, attachments := connectors.ParseOutboundMediaDirectives(msg.Text, telegramAttachmentFenceNames...)
		text := strings.TrimSpace(visibleText)
		if text != "" {
			payload := map[string]any{"chat_id": chat, "text": text, "disable_web_page_preview": true}
			var sent struct {
				MessageID int64 `json:"message_id"`
			}
			if err := p.call(ctx, base, token, "sendMessage", payload, &sent); err != nil {
				return err
			}
		}
		outboundMedia := make([]connectors.Media, 0, len(msg.Media)+len(attachments))
		outboundMedia = append(outboundMedia, msg.Media...)
		outboundMedia = append(outboundMedia, attachments...)
		if err := p.sendMediaMessages(ctx, base, token, chat, outboundMedia); err != nil {
			return err
		}
	}
	return nil
}

// sendMediaMessages 逐条发送媒体；单条失败降级为文本提示（与 Send 同口径）。
// 流式 Final 复用本函数，保证富媒体在流式/非流式两条路径行为一致（13 §7.1）。
func (p *Provider) sendMediaMessages(ctx context.Context, base, token, chat string, media []connectors.Media) error {
	for _, item := range media {
		if err := p.sendOutboundMedia(ctx, base, token, chat, item); err != nil {
			p.logf("telegram: 发送媒体失败（%s %s）：%v；降级为文本提示",
				item.Kind, telegramMediaLabel(item), err)
			notice := "[附件发送失败: " + telegramMediaLabel(item) + "]"
			if noticeErr := p.call(ctx, base, token, "sendMessage", map[string]any{"chat_id": chat, "text": notice}, nil); noticeErr != nil {
				return noticeErr
			}
		}
	}
	return nil
}

// OutboundMediaNote 实现 connectors.OutboundMediaNoteProvider：把出站媒体指令
// 协议提示注入入站文本，agent 据此在回复里声明要回发的图片/视频/文件。
func (p *Provider) OutboundMediaNote() string {
	return connectors.MediaDirectiveNote(telegramAttachmentFence)
}

// ── Webhook 入口 ────────────────────────────────────────────────────────────

// HandleWebhook 实现 connectors.WebhookProvider：
//   - 配置了 webhook_secret 时校验 X-Telegram-Bot-Api-Secret-Token（常量时间
//     比较），不符返回 error（宿主转 400）；
//   - 读取 body（上限 1MB）解析 update；非消息/无文本返回空结果（宿主回 202，
//     不产生回复）。
//
// ConnectorID/Provider 留空由宿主按路由回填（restapi 会补 cfg.ID/Provider）。
func (p *Provider) HandleWebhook(_ context.Context, cfg connectors.Config, r *http.Request) (connectors.WebhookResult, error) {
	if r == nil || r.Body == nil {
		return connectors.WebhookResult{}, errors.New("telegram: webhook request body is required")
	}
	defer r.Body.Close()

	// secret_token 校验：未配置 webhook_secret 时跳过——此时任何人只要知道
	// 回调 URL 就能伪造 update，生产环境务必在连接器配置中补上（保存后
	// Activate 会通过 setWebhook 把 secret_token 同步给 Telegram）。
	if expected := cfg.Secret(secretWebhookSecret); expected != "" {
		got := strings.TrimSpace(r.Header.Get(webhookSecretHeader))
		if subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
			return connectors.WebhookResult{}, errors.New("telegram: webhook secret token mismatch")
		}
	}

	// 多读 1 字节用于判定超限：只截断不报错可能让超大 body 的合法前缀被静默接受。
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBodyBytes+1))
	if err != nil {
		return connectors.WebhookResult{}, fmt.Errorf("read telegram webhook body: %s", err.Error())
	}
	if len(raw) > maxWebhookBodyBytes {
		return connectors.WebhookResult{}, errors.New("telegram: webhook body too large")
	}
	var upd update
	if err := json.Unmarshal(raw, &upd); err != nil {
		return connectors.WebhookResult{}, fmt.Errorf("decode telegram webhook: %s", err.Error())
	}
	msg, ok := parseUpdate("", upd)
	if !ok {
		return connectors.WebhookResult{}, nil // 非消息/无文本：静默 202
	}
	return connectors.WebhookResult{Message: &msg}, nil
}

// SupportsPolling 报告是否应启动长轮询（webhook 模式下由 HandleWebhook 入口接管）。
func (p *Provider) SupportsPolling(cfg connectors.Config) bool {
	if cfg.Secret("bot_token") == "" {
		return false
	}
	return modeOf(cfg) != modeWebhook
}

// modeOf 解析接入模式：Settings["mode"]（前端表单）> Config.Mode（结构化字段）；
// 未配置视为 polling。
func modeOf(cfg connectors.Config) string {
	if mode := cfg.Setting("mode", ""); mode != "" {
		return strings.ToLower(mode)
	}
	return strings.ToLower(strings.TrimSpace(cfg.Mode))
}

// RunPolling 长轮询 getUpdates：游标持久化（state），断线退避重连。
func (p *Provider) RunPolling(ctx context.Context, cfg connectors.Config, handle connectors.InboundHandler, state connectors.PollingState) error {
	token := cfg.Secret("bot_token")
	if token == "" {
		return errors.New("telegram: bot_token is required")
	}
	timeout := p.pollTimeout
	if raw := cfg.Setting("poll_timeout_seconds", ""); raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil && seconds > 0 && seconds <= 120 {
			timeout = time.Duration(seconds) * time.Second
		}
	}
	offset := int64(0)
	if raw, ok := state.Get(cursorKey); ok {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			offset = parsed
		}
	}

	backoff := p.retryDelay
	base := p.baseFor(cfg)
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		updates, err := p.getUpdates(ctx, base, token, offset, timeout)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			p.logf("telegram: getUpdates failed: %v (retry in %s)", err, backoff)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = p.retryDelay
		for _, update := range updates {
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}
			msg, ok := parseUpdate(cfg.ID, update)
			if !ok {
				continue
			}
			if err := handle(ctx, msg); err != nil {
				// 队列满/瞬时失败：不回滚游标（消息已认领，平台不会重投），
				// 记日志继续，避免阻塞整个轮询。
				p.logf("telegram: handle message %s failed: %v", msg.MessageID, err)
			}
		}
		if len(updates) > 0 {
			if err := state.Set(cursorKey, strconv.FormatInt(offset, 10)); err != nil {
				p.logf("telegram: persist cursor failed: %v", err)
			}
		}
	}
}

// StartTyping 每 4s 发送一次 sendChatAction:typing，直到 Stop。
func (p *Provider) StartTyping(ctx context.Context, cfg connectors.Config, chat string) (connectors.TypingSession, error) {
	token := cfg.Secret("bot_token")
	if token == "" {
		return nil, errors.New("telegram: bot_token is required")
	}
	sessionCtx, cancel := context.WithCancel(ctx)
	session := &typingSession{cancel: cancel, done: make(chan struct{})}
	base := p.baseFor(cfg)
	go func() {
		defer close(session.done)
		p.sendChatAction(sessionCtx, base, token, chat)
		ticker := time.NewTicker(typingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-sessionCtx.Done():
				return
			case <-ticker.C:
				p.sendChatAction(sessionCtx, base, token, chat)
			}
		}
	}()
	return session, nil
}

func (p *Provider) sendChatAction(ctx context.Context, base, token, chat string) {
	payload := map[string]any{"chat_id": chat, "action": "typing"}
	if err := p.call(ctx, base, token, "sendChatAction", payload, nil); err != nil && ctx.Err() == nil {
		p.logf("telegram: sendChatAction failed: %v", err)
	}
}

// StartStreaming 先发一条占位消息，后续用 editMessageText 增量更新。
func (p *Provider) StartStreaming(ctx context.Context, cfg connectors.Config, chat string) (connectors.StreamingSession, error) {
	token := cfg.Secret("bot_token")
	if token == "" {
		return nil, errors.New("telegram: bot_token is required")
	}
	payload := map[string]any{"chat_id": chat, "text": "思考中…", "disable_web_page_preview": true}
	var sent struct {
		MessageID int64 `json:"message_id"`
	}
	base := p.baseFor(cfg)
	if err := p.call(ctx, base, token, "sendMessage", payload, &sent); err != nil {
		return nil, err
	}
	return &streamingSession{provider: p, base: base, token: token, chat: chat, messageID: sent.MessageID, last: "思考中…"}, nil
}

type typingSession struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (s *typingSession) Stop(context.Context) error {
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(time.Second):
	}
	return nil
}

type streamingSession struct {
	provider  *Provider
	base      string
	token     string
	chat      string
	messageID int64
	last      string
}

func (s *streamingSession) Update(ctx context.Context, text string) error {
	// 出站媒体指令（```telegram-attachments``` 围栏 / IMAGE: 等指令行）是协议
	// 而非用户正文：流式中间态先截断未闭合块、再剥离已闭合块与指令行（与 QQ
	// 流式同口径，2026-10-15），避免指令原文泄漏给用户；终态由 Final 解析并
	// 补发附件。
	visible := connectors.TruncateOpenAttachmentBlocks(text, telegramAttachmentFenceNames...)
	visible, _ = connectors.ParseOutboundMediaDirectives(visible, telegramAttachmentFenceNames...)
	text = strings.TrimSpace(visible)
	if text == "" || text == s.last {
		return nil
	}
	payload := map[string]any{"chat_id": s.chat, "message_id": s.messageID, "text": text}
	if err := s.provider.call(ctx, s.base, s.token, "editMessageText", payload, nil); err != nil {
		return err
	}
	s.last = text
	return nil
}

// Final 用可见正文收尾并发送附件（msg.Media + 文本媒体指令，与普通 Send 同
// 口径）。纯媒体回复（无可见正文）保留占位消息并直发媒体。
func (s *streamingSession) Final(ctx context.Context, msgs []connectors.OutboundMessage) error {
	visibleParts := make([]string, 0, len(msgs))
	outboundMedia := make([]connectors.Media, 0, len(msgs))
	for _, msg := range msgs {
		visible, attachments := connectors.ParseOutboundMediaDirectives(msg.Text, telegramAttachmentFenceNames...)
		if text := strings.TrimSpace(visible); text != "" {
			visibleParts = append(visibleParts, text)
		}
		outboundMedia = append(outboundMedia, msg.Media...)
		outboundMedia = append(outboundMedia, attachments...)
	}
	if text := strings.TrimSpace(strings.Join(visibleParts, "\n\n")); text != "" {
		if err := s.Update(ctx, text); err != nil {
			return err
		}
	}
	return s.provider.sendMediaMessages(ctx, s.base, s.token, s.chat, outboundMedia)
}

func (s *streamingSession) Fail(ctx context.Context, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		text = "处理失败，请稍后重试。"
	}
	return s.Update(ctx, text)
}

type update struct {
	UpdateID int64            `json:"update_id"`
	Message  *telegramMessage `json:"message"`
}

// telegramMessage 是 Bot API message 的最小子集（文本 + 媒体 + 说明文字）。
type telegramMessage struct {
	MessageID int64 `json:"message_id"`
	From      *struct {
		ID        int64  `json:"id"`
		Username  string `json:"username"`
		FirstName string `json:"first_name"`
	} `json:"from"`
	Chat struct {
		ID    int64  `json:"id"`
		Type  string `json:"type"`
		Title string `json:"title"`
	} `json:"chat"`
	Text    string `json:"text"`
	Caption string `json:"caption"`
	// Photo 是同一张图的多档尺寸（末档最大）；取末档。
	Photo    []telegramPhotoSize `json:"photo"`
	Document *telegramFileRef    `json:"document"`
	Voice    *telegramFileRef    `json:"voice"`
	Audio    *telegramFileRef    `json:"audio"`
	Video    *telegramFileRef    `json:"video"`
}

// telegramPhotoSize 是 photo 数组中的一档尺寸。
type telegramPhotoSize struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

// telegramFileRef 是 document/voice/audio/video 的公共文件引用。
type telegramFileRef struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
}

func parseUpdate(connectorID string, upd update) (connectors.InboundMessage, bool) {
	if upd.Message == nil {
		return connectors.InboundMessage{}, false
	}
	text := strings.TrimSpace(upd.Message.Text)
	media, placeholders := extractInboundMedia(upd.Message)
	if text == "" {
		text = strings.TrimSpace(upd.Message.Caption)
	}
	if text == "" && len(placeholders) > 0 {
		text = strings.Join(placeholders, "\n")
	}
	if text == "" && len(media) == 0 {
		return connectors.InboundMessage{}, false // 非文本/媒体消息（如编辑、回调查询）
	}
	msg := connectors.InboundMessage{
		ConnectorID: connectorID,
		Provider:    Name,
		Chat:        strconv.FormatInt(upd.Message.Chat.ID, 10),
		ChatTitle:   strings.TrimSpace(upd.Message.Chat.Title),
		MessageID:   strconv.FormatInt(upd.Message.MessageID, 10),
		Text:        text,
		Media:       media,
		ReceivedAt:  time.Now().UTC(),
	}
	if upd.Message.From != nil {
		msg.UserID = strconv.FormatInt(upd.Message.From.ID, 10)
		msg.UserName = strings.TrimSpace(upd.Message.From.Username)
		if msg.UserName == "" {
			msg.UserName = strings.TrimSpace(upd.Message.From.FirstName)
		}
	}
	return msg, true
}

func (p *Provider) getUpdates(ctx context.Context, base, token string, offset int64, timeout time.Duration) ([]update, error) {
	params := map[string]any{
		"offset":          offset,
		"timeout":         int(timeout.Seconds()),
		"allowed_updates": []string{"message"},
	}
	var updates []update
	if err := p.call(ctx, base, token, "getUpdates", params, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

// call 调用 Bot API：{"ok":bool,"result":…,"description":…} 统一解包。
func (p *Provider) call(ctx context.Context, base, token, method string, payload any, out any) error {
	endpoint := base + "/bot" + token + "/" + method
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		// 请求构造失败的错误文本带完整 URL（内嵌 bot_token），必须脱敏。
		return fmt.Errorf("telegram: %s request build failed: %s", method, redactToken(err.Error(), token))
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return p.doAPIRequest(req, token, method, out)
}

// doAPIRequest 执行请求并解包 {"ok":…,"result":…} 信封（JSON 调用与 multipart
// 上传共用）；错误文本统一脱敏 bot_token。
func (p *Provider) doAPIRequest(req *http.Request, token, method string, out any) error {
	resp, err := p.client.Do(req)
	if err != nil {
		// *url.Error 的文本包含完整请求 URL（内嵌 bot_token）；这里不做 %w
		// 包装，避免上层 errors.Unwrap 拿到含 token 的原始错误。
		return fmt.Errorf("telegram: %s request failed: %s", method, redactToken(err.Error(), token))
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var envelope struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
		ErrorCode   int             `json:"error_code"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("telegram: invalid response (HTTP %d): %s", resp.StatusCode, truncate(redactToken(string(raw), token), 200))
	}
	if !envelope.OK {
		description := strings.TrimSpace(envelope.Description)
		if description == "" {
			description = strings.TrimSpace(string(raw))
		}
		return fmt.Errorf("telegram: %s failed: %s", method, truncate(redactToken(description, token), 200))
	}
	if out != nil && len(envelope.Result) > 0 {
		if err := json.Unmarshal(envelope.Result, out); err != nil {
			return fmt.Errorf("telegram: decode %s result: %w", method, err)
		}
	}
	return nil
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}

// redactToken 从错误/响应文本中抹除 bot_token：client.Do 的 *url.Error 与
// NewRequestWithContext 的解析错误都会带出内嵌 token 的完整 URL，上层日志与
// restapi /test 响应直接转发这些错误文本。
func redactToken(message, token string) string {
	if token == "" {
		return message
	}
	return strings.ReplaceAll(message, token, "***")
}

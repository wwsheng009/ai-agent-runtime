// Package telegram 实现 Telegram Bot 通道适配器（长轮询为主，webhook 由宿主入口接入）。
//
// 设计见 docs/bots/04-migration-roadmap.md P0：长轮询 + 文本收发 + typing +
// 编辑式流式回复；媒体、命令全集、webhook 注册在后续阶段增量补齐。
package telegram

import (
	"bytes"
	"context"
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
)

// Provider 是 Telegram 通道适配器；HTTP 细节可注入（测试用 httptest）。
type Provider struct {
	apiBase     string
	client      *http.Client
	pollTimeout time.Duration
	retryDelay  time.Duration
	logf        func(format string, args ...any)
	now         func() time.Time
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
		Modes:    []string{"polling", "webhook"},
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

// Activate 调用 getMe 校验 bot_token。
func (p *Provider) Activate(ctx context.Context, cfg connectors.Config) (connectors.Activation, error) {
	token := cfg.Secret("bot_token")
	if token == "" {
		return connectors.Activation{}, errors.New("telegram: bot_token is required")
	}
	var result struct {
		ID        int64  `json:"id"`
		Username  string `json:"username"`
		FirstName string `json:"first_name"`
	}
	if err := p.call(ctx, p.baseFor(cfg), token, "getMe", nil, &result); err != nil {
		return connectors.Activation{}, err
	}
	info := strings.TrimSpace(result.Username)
	if info != "" {
		info = "@" + info
	}
	return connectors.Activation{Info: info}, nil
}

// Deactivate 无远端状态需要清理。
func (p *Provider) Deactivate(context.Context, connectors.Config) error { return nil }

// Send 逐条发送文本消息。
func (p *Provider) Send(ctx context.Context, cfg connectors.Config, chat string, msgs []connectors.OutboundMessage) error {
	token := cfg.Secret("bot_token")
	if token == "" {
		return errors.New("telegram: bot_token is required")
	}
	for _, msg := range msgs {
		text := strings.TrimSpace(msg.Text)
		if text == "" {
			continue
		}
		payload := map[string]any{"chat_id": chat, "text": text, "disable_web_page_preview": true}
		var sent struct {
			MessageID int64 `json:"message_id"`
		}
		if err := p.call(ctx, p.baseFor(cfg), token, "sendMessage", payload, &sent); err != nil {
			return err
		}
	}
	return nil
}

// SupportsPolling 报告是否应启动长轮询（webhook 模式由宿主入口接管）。
func (p *Provider) SupportsPolling(cfg connectors.Config) bool {
	if cfg.Secret("bot_token") == "" {
		return false
	}
	// 模式读取优先级：Settings["mode"]（前端表单）> Config.Mode（结构化字段）。
	mode := cfg.Setting("mode", "")
	if mode == "" {
		mode = cfg.Mode
	}
	return !strings.EqualFold(strings.TrimSpace(mode), "webhook")
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
	text = strings.TrimSpace(text)
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

func (s *streamingSession) Final(ctx context.Context, msgs []connectors.OutboundMessage) error {
	text := joinText(msgs)
	if text == "" {
		return nil // 占位消息即最终内容（例如提示类回复）
	}
	return s.Update(ctx, text)
}

func (s *streamingSession) Fail(ctx context.Context, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		text = "处理失败，请稍后重试。"
	}
	return s.Update(ctx, text)
}

func joinText(msgs []connectors.OutboundMessage) string {
	parts := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		if text := strings.TrimSpace(msg.Text); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

type update struct {
	UpdateID int64 `json:"update_id"`
	Message  *struct {
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
		Text string `json:"text"`
	} `json:"message"`
}

func parseUpdate(connectorID string, upd update) (connectors.InboundMessage, bool) {
	if upd.Message == nil {
		return connectors.InboundMessage{}, false
	}
	text := strings.TrimSpace(upd.Message.Text)
	if text == "" {
		return connectors.InboundMessage{}, false // P0 仅文本
	}
	msg := connectors.InboundMessage{
		ConnectorID: connectorID,
		Provider:    Name,
		Chat:        strconv.FormatInt(upd.Message.Chat.ID, 10),
		ChatTitle:   strings.TrimSpace(upd.Message.Chat.Title),
		MessageID:   strconv.FormatInt(upd.Message.MessageID, 10),
		Text:        text,
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
		return err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
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
		return fmt.Errorf("telegram: invalid response (HTTP %d): %s", resp.StatusCode, truncate(string(raw), 200))
	}
	if !envelope.OK {
		description := strings.TrimSpace(envelope.Description)
		if description == "" {
			description = strings.TrimSpace(string(raw))
		}
		return fmt.Errorf("telegram: %s failed: %s", method, truncate(description, 200))
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

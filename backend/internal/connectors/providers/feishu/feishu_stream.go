// 卡片式流式回复（connectors.StreamingProvider，切片 3c）。
//
// 协议（移植自 codex-server feishu.go 流式会话，按本仓契约简化）：
//   - Start：发一条「可更新」卡片（schema 2.0 + update_multi=true）占位；
//   - Update：PATCH /im/v1/messages/{message_id} 用累积文本整体替换卡片正文
//     （飞书 PATCH 语义为内容替换，天然支持 Markdown 增量渲染）；
//   - Final：卡片 PATCH 到终态文本；msg.Media 走普通富媒体发送（卡片不承载媒体）；
//   - Fail：PATCH 为失败提示；
//   - 任何一步失败：卡片路径不可用时回退普通发送（不丢回复）。
//
// 开关（双层）：Service 侧 Policy.streamingEnabled()（ReplyMode != final）+
// Provider 侧 Settings["streaming"]="true"（默认关闭：卡片 PATCH 协议需真机
// 验证后再放开）；任一层关闭即走普通发送。卡片路径任何一步失败均由 Service
// 回退普通发送（不丢回复）。
package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

const (
	// streamingSetting 是流式回复开关（默认关闭；true 才启用卡片流式）。
	streamingSetting = "streaming"
	// streamPlaceholderText 是流式占位卡的初始正文。
	streamPlaceholderText = "思考中…"
	// streamFailureText 是 Fail 兜底文案。
	streamFailureText = "处理失败，请稍后重试。"
	// messagePatchPathFmt 是卡片内容替换端点（PATCH interactive）。
	messagePatchPathFmt = "/open-apis/im/v1/messages/%s"
)

// buildUpdatableCardJSON 构造可更新卡片载荷（schema 2.0 + update_multi，
// 单 markdown 元素承载累积正文；与上游 buildFeishuUpdatableCardJSON 对齐）。
func buildUpdatableCardJSON(content string) string {
	card := map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"wide_screen_mode": true,
			"update_multi":     true,
		},
		"body": map[string]any{
			"elements": []map[string]any{
				{
					"tag":     "markdown",
					"content": content,
				},
			},
		},
	}
	data, _ := json.Marshal(card)
	return string(data)
}

// streamingEnabled 报告该连接器是否显式开启流式回复（Provider 层开关，
// Settings["streaming"]="true"；默认关闭，需真机验证后放开。实际还受
// Service 侧 Policy.streamingEnabled() 门控）。
func streamingEnabled(cfg connectors.Config) bool {
	return strings.EqualFold(strings.TrimSpace(cfg.Settings[streamingSetting]), "true")
}

// StartStreaming 实现 connectors.StreamingProvider：发一张可更新卡片占位。
// 未开启 streaming 开关时返回 (nil, nil)（Service 视为不支持，走普通发送）。
func (p *Provider) StartStreaming(ctx context.Context, cfg connectors.Config, chat string) (connectors.StreamingSession, error) {
	if !streamingEnabled(cfg) {
		return nil, nil
	}
	chatID := strings.TrimSpace(chat)
	if chatID == "" {
		return nil, errors.New("feishu: chat is required")
	}
	base := p.baseFor(cfg)
	if err := validateBaseURL(base); err != nil {
		return nil, err
	}
	token, err := p.tenantToken(ctx, cfg)
	if err != nil {
		return nil, err
	}
	appSecret := cfg.Secret(secretAppSecret)

	content := buildUpdatableCardJSON(streamPlaceholderText)
	var response sendMessageResponse
	if err := p.doJSON(ctx, http.MethodPost, base, messageCreatePath+"?receive_id_type=chat_id", token, map[string]any{
		"receive_id": chatID,
		"content":    content,
		"msg_type":   "interactive",
	}, &response, appSecret, token); err != nil {
		return nil, err
	}
	if response.Code != 0 {
		return nil, newAPIError("im/v1/messages", response.Code, response.Msg, appSecret, token)
	}
	messageID := strings.TrimSpace(response.Data.MessageID)
	if messageID == "" {
		return nil, errors.New("feishu: streaming card create returned empty message_id")
	}
	return &streamSession{
		provider: p,
		cfg:      cfg,
		base:     base,
		token:    token,
		chat:     chatID,
		msgID:    messageID,
		last:     streamPlaceholderText,
	}, nil
}

// streamSession 实现 connectors.StreamingSession。
type streamSession struct {
	provider *Provider
	cfg      connectors.Config
	base     string
	token    string
	chat     string

	mu    sync.Mutex
	msgID string
	last  string
}

// patchCard 用 PATCH（interactive）替换卡片正文。
func (s *streamSession) patchCard(ctx context.Context, content string) error {
	s.mu.Lock()
	messageID := s.msgID
	s.mu.Unlock()
	if messageID == "" {
		return errors.New("feishu: streaming card message id is empty")
	}
	appSecret := s.cfg.Secret(secretAppSecret)
	var response sendMessageResponse
	err := s.provider.doJSON(ctx, http.MethodPatch, s.base,
		fmt.Sprintf(messagePatchPathFmt, url.PathEscape(messageID)), s.token,
		map[string]any{"content": buildUpdatableCardJSON(content)}, &response,
		appSecret, s.token)
	if err != nil {
		return err
	}
	if response.Code != 0 {
		return newAPIError("im/v1/messages/patch", response.Code, response.Msg, appSecret, s.token)
	}
	return nil
}

// Update 以累积文本替换卡片正文（Service 已节流；与前次相同则跳过）。
func (s *streamSession) Update(ctx context.Context, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	s.mu.Lock()
	if text == s.last {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	if err := s.patchCard(ctx, text); err != nil {
		return err
	}
	s.mu.Lock()
	s.last = text
	s.mu.Unlock()
	return nil
}

// Final 收尾：文本 PATCH 到卡片终态；媒体（含指令行解析出的附件）走普通富
// 媒体发送——先发媒体、再终态收尾卡片，保证时间线顺序可读。混合消息按
// 「媒体 + 文本」拆分，媒体不会因消息带文本而被丢弃。
func (s *streamSession) Final(ctx context.Context, msgs []connectors.OutboundMessage) error {
	remaining := outboundMessages(msgs)
	var mediaMessages []connectors.OutboundMessage
	texts := make([]string, 0, len(remaining))
	for _, msg := range remaining {
		if len(msg.Media) > 0 {
			mediaMessages = append(mediaMessages, connectors.OutboundMessage{Media: msg.Media})
		}
		if text := strings.TrimSpace(msg.Text); text != "" {
			texts = append(texts, text)
		}
	}
	if len(mediaMessages) > 0 {
		if err := s.provider.Send(ctx, s.cfg, s.chat, mediaMessages); err != nil {
			return err
		}
	}
	final := strings.TrimSpace(strings.Join(texts, "\n\n"))
	if final == "" {
		final = s.last
	}
	if final == "" {
		return nil
	}
	return s.Update(ctx, final)
}

// Fail 收尾为失败提示（PATCH 失败时不抛错——占位卡已尽力，回复本体另有失败路径）。
func (s *streamSession) Fail(ctx context.Context, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		text = streamFailureText
	}
	if err := s.Update(ctx, text); err != nil {
		s.provider.logf("feishu: 流式失败提示 PATCH 失败（%s）：%v", text, err)
	}
	return nil
}

// outboundMessages 规整出站消息：媒体指令行剥离并合并进 Media。
func outboundMessages(msgs []connectors.OutboundMessage) []connectors.OutboundMessage {
	out := make([]connectors.OutboundMessage, 0, len(msgs))
	for _, msg := range msgs {
		visible, attachments := connectors.ParseOutboundMediaDirectives(msg.Text, feishuAttachmentFenceNames...)
		media := append(append([]connectors.Media{}, msg.Media...), attachments...)
		out = append(out, connectors.OutboundMessage{Text: strings.TrimSpace(visible), Media: media})
	}
	return out
}

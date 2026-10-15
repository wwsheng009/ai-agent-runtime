package qqbot

// 流式消息（官方「流式发送单聊消息」，**仅 C2C**）：
//
//	POST /v2/users/{user_openid}/stream_messages
//	  input_mode=replace：content_raw 为当前全量正文（须以上游已下发前缀开头；
//	    本会话文本只增不减，天然满足前缀一致约束）；
//	  input_state：1=生成中 / 10=生成结束；
//	  index 从 0 递增；首片由服务端生成并返回 stream_msg_id，后续分片携带；
//	  msg_seq 去重（同一流式消息全部片段使用同一序号，与官方示例一致）。
//
// 群/频道不支持流式：StartStreaming 返回**委托会话**（Update 空操作，
// Final/Fail 走普通发送）——行为与未实现 StreamingProvider 一致，且不会在
// 每回合触发 Service 的「start streaming failed」告警。无被动上下文（无最近
// 入站 msg_id）的单聊同样退化为委托会话（避免主动消息语义下流式失败）。

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// 流式消息 input_state 取值（官方：1=生成中，10=生成结束）。
const (
	streamInputStateGenerating = 1
	streamInputStateFinished   = 10
)

// streamSession 实现 connectors.StreamingSession。
type streamSession struct {
	provider *Provider
	cfg      connectors.Config
	target   chatTarget
	chat     string

	// streaming=false 时为委托会话（群/频道/无被动上下文）。
	streaming bool
	msgID     string
	msgSeq    int
	streamID  string
	index     int
	lastText  string
	closed    bool
}

// StartStreaming 实现 connectors.StreamingProvider。
func (p *Provider) StartStreaming(_ context.Context, cfg connectors.Config, chat string) (connectors.StreamingSession, error) {
	target, err := parseChat(chat)
	if err != nil {
		return nil, err
	}
	session := &streamSession{provider: p, cfg: cfg, target: target, chat: target.chat}
	if target.kind == chatKindUser {
		if msgID := strings.TrimSpace(p.lastInboundMsgID(target.chat)); msgID != "" {
			session.streaming = true
			session.msgID = msgID
			session.msgSeq = p.nextMsgSeq(target.chat)
		}
	}
	return session, nil
}

// Update 以 replace 模式推送当前全量正文（Service 已做节流，传参为累积文本）。
func (s *streamSession) Update(ctx context.Context, text string) error {
	if !s.streaming || s.closed {
		return nil
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || trimmed == s.lastText {
		return nil
	}
	// 前缀只增不减（replace 模式约束 40007）：异常缩短时跳过本次，保持已下发前缀。
	if s.lastText != "" && !strings.HasPrefix(trimmed, s.lastText) {
		return nil
	}
	if err := s.sendStreamFragment(ctx, trimmed, streamInputStateGenerating); err != nil {
		return err
	}
	s.lastText = trimmed
	return nil
}

// Final 结束流式并投递媒体（文本已由流式承载，媒体走普通富媒体发送）。
func (s *streamSession) Final(ctx context.Context, msgs []connectors.OutboundMessage) error {
	if !s.streaming {
		return s.provider.Send(ctx, s.cfg, s.chat, msgs)
	}
	if !s.closed {
		final := outboundText(msgs)
		switch {
		case final == "":
			final = s.lastText
		case s.lastText != "" && !strings.HasPrefix(final, s.lastText):
			// 终态文本与前缀不一致（异常）：以已下发内容收尾，保证前缀不变。
			final = s.lastText
		}
		if final == "" {
			// 全程无文本（如仅媒体）：无法用流式收尾，退化为普通发送。
			s.closed = true
			return s.provider.Send(ctx, s.cfg, s.chat, msgs)
		}
		if err := s.sendStreamFragment(ctx, final, streamInputStateFinished); err != nil {
			return err
		}
		s.lastText = final
		s.closed = true
	}
	for _, msg := range msgs {
		if len(msg.Media) == 0 {
			continue
		}
		if err := s.provider.Send(ctx, s.cfg, s.chat, []connectors.OutboundMessage{{Media: msg.Media}}); err != nil {
			return err
		}
	}
	return nil
}

// Fail 以已下发内容（可追加失败说明）结束流式；委托会话走普通发送。
func (s *streamSession) Fail(ctx context.Context, text string) error {
	note := strings.TrimSpace(text)
	if !s.streaming {
		if note == "" {
			return nil
		}
		return s.provider.Send(ctx, s.cfg, s.chat, []connectors.OutboundMessage{{Text: note}})
	}
	if s.closed {
		return nil
	}
	final := s.lastText
	switch {
	case note == "":
	case final == "":
		final = note
	default:
		final = final + "\n\n" + note
	}
	if final != "" {
		if err := s.sendStreamFragment(ctx, final, streamInputStateFinished); err != nil {
			return err
		}
	}
	s.closed = true
	return nil
}

// sendStreamFragment 发送一个流式分片；首片响应 id 作为后续 stream_msg_id。
func (s *streamSession) sendStreamFragment(ctx context.Context, text string, state int) error {
	payload := map[string]any{
		"input_mode":   "replace",
		"input_state":  state,
		"index":        s.index,
		"content_type": s.contentType(),
		"content_raw":  text,
		"msg_seq":      s.msgSeq,
	}
	if s.msgID != "" {
		payload["msg_id"] = s.msgID
	}
	if s.streamID != "" {
		payload["stream_msg_id"] = s.streamID
	}
	endpoint := s.provider.baseFor(s.cfg) + "/v2/users/" + url.PathEscape(s.target.openID) + "/stream_messages"
	var response struct {
		ID string `json:"id"`
	}
	if err := s.provider.apiJSON(ctx, s.cfg, http.MethodPost, endpoint, payload, &response); err != nil {
		return err
	}
	if s.streamID == "" {
		s.streamID = strings.TrimSpace(response.ID)
	}
	s.index++
	return nil
}

// contentType 按连接器配置选择流式内容格式（markdown_support=true → markdown）。
func (s *streamSession) contentType() string {
	if s.provider.markdownSupport(s.cfg) {
		return "markdown"
	}
	return "text"
}

// outboundText 合并出站文本（与终态回复同口径：非空文本以空行连接）。
func outboundText(msgs []connectors.OutboundMessage) string {
	parts := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		if text := strings.TrimSpace(msg.Text); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

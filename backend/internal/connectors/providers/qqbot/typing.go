// 输入状态（connectors.TypingProvider）：QQ 单聊支持 msg_type=6 + input_notify
// （input_type=1「正在输入」/2「取消」；input_second 为展示时长，见官方 SDK
// dto.InputNotify）。
//
// 设计取舍：
//   - 仅单聊：群聊/频道不支持该消息类型，StartTyping 返回 nil（无 typing 能力）；
//   - 走主动消息（不带 msg_id/msg_seq）：被动回复窗口很窄（单聊 60min/4 次），
//     输入状态若占被动配额会把真正的回复挤掉；主动被拒时静默降级；
//   - 体验项：发送失败只记日志，不影响回合与消息往来。
package qqbot

import (
	"context"
	"net/http"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

const (
	// qqTypingMessageType 是输入状态消息类型（msg_type=6）。
	qqTypingMessageType = 6
	// qqTypingInputTyping / qqTypingInputCancel 是 input_notify.input_type 取值。
	qqTypingInputTyping = 1
	qqTypingInputCancel = 2
	// qqTypingInputSeconds 是单次「正在输入」的展示时长（秒）。
	qqTypingInputSeconds = 60
)

// StartTyping 实现 connectors.TypingProvider：向单聊会话发送「正在输入」；
// 群聊/频道或发送失败时返回 nil（无 typing 能力，不影响消息往来）。
func (p *Provider) StartTyping(ctx context.Context, cfg connectors.Config, chat string) (connectors.TypingSession, error) {
	target, err := parseChat(chat)
	if err != nil {
		return nil, err
	}
	if target.kind != chatKindUser {
		return nil, nil
	}
	if p.appID(cfg) == "" || p.appSecret(cfg) == "" {
		return nil, nil
	}
	if err := p.sendInputNotify(ctx, cfg, target, qqTypingInputTyping, qqTypingInputSeconds); err != nil {
		p.logf("qqbot: 发送输入状态失败（降级为无 typing）：%v", err)
		return nil, nil
	}
	return &qqTypingSession{provider: p, cfg: cfg, target: target}, nil
}

// qqTypingSession 是一次「正在输入」保活（Stop 发送取消状态；幂等）。
type qqTypingSession struct {
	provider *Provider
	cfg      connectors.Config
	target   chatTarget
	stopOnce sync.Once
}

// Stop 发送取消输入状态（best-effort；重复调用幂等）。
func (s *qqTypingSession) Stop(ctx context.Context) error {
	if s == nil {
		return nil
	}
	var stopErr error
	s.stopOnce.Do(func() {
		if ctx == nil {
			ctx = context.Background()
		}
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), httpTimeout)
		defer cancel()
		stopErr = s.provider.sendInputNotify(stopCtx, s.cfg, s.target, qqTypingInputCancel, 0)
	})
	return stopErr
}

// sendInputNotify 发送 msg_type=6 输入状态（主动消息语义，不占被动回复配额）。
func (p *Provider) sendInputNotify(ctx context.Context, cfg connectors.Config, target chatTarget, inputType, seconds int) error {
	notify := map[string]any{"input_type": inputType}
	if seconds > 0 {
		notify["input_second"] = seconds
	}
	payload := map[string]any{
		"msg_type":     qqTypingMessageType,
		"input_notify": notify,
	}
	return p.apiJSON(ctx, cfg, http.MethodPost, messagesEndpoint(p.baseFor(cfg), target), payload, nil)
}

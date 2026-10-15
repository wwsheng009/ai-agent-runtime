// 打字表情（connectors.TypingProvider，切片 3e）：飞书没有「正在输入」状态
// 接口，上游用消息表情回应替代——处理中给用户最近一条入站消息加 `Typing`
// 表情、回合结束时移除（codex-server feishu.go 的移植，按本仓契约简化）。
//
// 设计取舍：
//   - 回复目标来自进程内记忆（connectorKey+chat → 最近入站 msg_id，含跨重启
//     持久化回填，见 feishu_state.go）；无目标时返回 (nil, nil)（无 typing 能力）；
//   - 加/删表情失败只记日志并返回 nil（体验项，不影响回合与消息往来）；
//   - Stop 幂等（stopOnce），且用 WithoutCancel 保证宿主取消回合后仍尽力摘除
//     表情（避免「正在输入」残留在用户消息上）。
package feishu

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

const (
	// typingReactionEmoji 是「正在处理」表情（官方 emoji_type；上游同款）。
	typingReactionEmoji = "Typing"
	// messageReactionsPathFmt 是消息表情回应端点（POST 创建 / DELETE 摘除）。
	messageReactionsPathFmt = "/open-apis/im/v1/messages/%s/reactions"
	// typingStopTimeout 是 Stop 摘除表情的单次请求超时。
	typingStopTimeout = 10 * time.Second
)

// StartTyping 实现 connectors.TypingProvider：给该会话最近一条入站消息加
// Typing 表情；无回复目标或失败时返回 (nil, nil)（无 typing 能力，不影响消息往来）。
func (p *Provider) StartTyping(ctx context.Context, cfg connectors.Config, chat string) (connectors.TypingSession, error) {
	chatID := strings.TrimSpace(chat)
	if chatID == "" {
		return nil, nil
	}
	replyTo := p.replyTarget(connectorKey(cfg), chatID)
	if replyTo == "" {
		return nil, nil
	}
	base := p.baseFor(cfg)
	if err := validateBaseURL(base); err != nil {
		return nil, nil
	}
	token, err := p.tenantToken(ctx, cfg)
	if err != nil {
		p.logf("feishu: 获取 tenant_access_token 失败（降级为无 typing）：%v", err)
		return nil, nil
	}
	reactionID, err := p.addTypingReaction(ctx, cfg, base, token, chatID, replyTo)
	if err != nil {
		p.logf("feishu: 加 Typing 表情失败（降级为无 typing）：%v", err)
		return nil, nil
	}
	return &typingSession{
		provider:   p,
		cfg:        cfg,
		base:       base,
		token:      token,
		chat:       chatID,
		messageID:  replyTo,
		reactionID: reactionID,
	}, nil
}

// addTypingReaction 给一条消息加 Typing 表情，返回 reaction_id（摘除时需要）。
func (p *Provider) addTypingReaction(ctx context.Context, cfg connectors.Config, base, token, chatID, messageID string) (string, error) {
	appSecret := cfg.Secret(secretAppSecret)
	var response messageReactionResponse
	if err := p.doJSON(ctx, http.MethodPost, base,
		fmt.Sprintf(messageReactionsPathFmt, url.PathEscape(messageID)), token,
		map[string]any{"reaction_type": map[string]any{"emoji_type": typingReactionEmoji}},
		&response, appSecret, token); err != nil {
		return "", err
	}
	if response.Code != 0 {
		return "", newAPIError("im/v1/messages/reactions", response.Code, response.Msg, appSecret, token)
	}
	reactionID := strings.TrimSpace(response.Data.ReactionID)
	if reactionID == "" {
		return "", errors.New("feishu: reaction create returned empty reaction_id")
	}
	return reactionID, nil
}

// messageReactionResponse 是表情回应接口的响应体。
type messageReactionResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		ReactionID string `json:"reaction_id"`
	} `json:"data"`
}

// typingSession 是一次「正在处理」表情回应（Stop 摘除；幂等）。
type typingSession struct {
	provider   *Provider
	cfg        connectors.Config
	base       string
	token      string
	chat       string
	messageID  string
	reactionID string

	stopOnce sync.Once
}

// Stop 摘除 Typing 表情（best-effort；重复调用幂等；宿主取消回合后仍执行；
// 摘除失败只记日志并返回 nil——表情残留不影响功能，不应让回合收尾报错）。
func (s *typingSession) Stop(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.stopOnce.Do(func() {
		if ctx == nil {
			ctx = context.Background()
		}
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), typingStopTimeout)
		defer cancel()
		if err := s.provider.removeTypingReaction(stopCtx, s.cfg, s.base, s.token, s.messageID, s.reactionID); err != nil {
			s.provider.logf("feishu: 摘除 Typing 表情失败（message %s reaction %s）：%v",
				s.messageID, s.reactionID, err)
		}
	})
	return nil
}

// removeTypingReaction 摘除一条消息上的表情回应。
func (p *Provider) removeTypingReaction(ctx context.Context, cfg connectors.Config, base, token, messageID, reactionID string) error {
	appSecret := cfg.Secret(secretAppSecret)
	path := fmt.Sprintf(messageReactionsPathFmt, url.PathEscape(messageID)) + "/" + url.PathEscape(reactionID)
	var response messageReactionResponse
	if err := p.doJSON(ctx, http.MethodDelete, base, path, token, nil, &response, appSecret, token); err != nil {
		return err
	}
	if response.Code != 0 {
		return newAPIError("im/v1/messages/reactions/delete", response.Code, response.Msg, appSecret, token)
	}
	return nil
}

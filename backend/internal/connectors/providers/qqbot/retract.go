// 消息撤回（官方 2026-07 契约）：
//
//	DELETE /v2/users/{user_openid}/messages/{message_id}
//	DELETE /v2/groups/{group_openid}/messages/{message_id}
//
// 仅能撤回机器人自己发送的消息；发送超过 2 分钟不可撤回（40064004）。
// 群管理员可额外撤回普通群成员的消息（本实现只撤回机器人自己的消息，
// 保持最小权限）。
package qqbot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// RetractMessage 实现 connectors.MessageRetractor。
func (p *Provider) RetractMessage(ctx context.Context, cfg connectors.Config, chat, messageID string) error {
	target, err := parseChat(chat)
	if err != nil {
		return err
	}
	if target.kind == chatKindChannel {
		return fmt.Errorf("qqbot: 频道消息撤回未实现（仅支持群/C2C）：%s", target.chat)
	}
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return errors.New("qqbot: message id is required")
	}
	if p.appID(cfg) == "" || p.appSecret(cfg) == "" {
		return errors.New("qqbot: app_id and app_secret are required")
	}

	endpoint := p.baseFor(cfg) + "/v2/users/" + url.PathEscape(target.openID) +
		"/messages/" + url.PathEscape(messageID)
	if target.kind == chatKindGroup {
		endpoint = p.baseFor(cfg) + "/v2/groups/" + url.PathEscape(target.openID) +
			"/messages/" + url.PathEscape(messageID)
	}
	if err := p.apiJSON(ctx, cfg, http.MethodDelete, endpoint, nil, nil); err != nil {
		return err
	}
	// 撤回成功后从账本移除，避免重复撤回同一条。
	p.sent.remove(target.chat, messageID)
	return nil
}

// LastRecallableMessage 实现 connectors.MessageRetractor：返回该会话最近
// 一条仍在 2 分钟窗口内的机器人消息 id。
func (p *Provider) LastRecallableMessage(chat string) (string, bool) {
	entry, ok := p.sent.lastRecallable(chat, p.now())
	if !ok {
		return "", false
	}
	return entry.MessageID, true
}

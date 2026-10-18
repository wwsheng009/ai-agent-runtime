// 互动事件（INTERACTION_CREATE，Intent 1<<26）与好友添加（FRIEND_ADD）。
//
// 官方契约（2026-07-23）：
//   - INTERACTION_CREATE 覆盖消息按钮(11)/快捷菜单(12)/消息反馈(13)/清空会话(14)/
//     故事集(15)/切换模型(16)/用户授权(18)/群授权(19)/群授权状态(20)；
//   - **仅 type=11/12 需要调用 PUT /interactions/{interaction_id} 回应**，否则
//     客户端一直 loading；同一 id 只能回应一次；
//   - 按钮回调的 data 在 data.resolved.button_data，用户点击即一次「输入」，
//     可复用普通入站管线交给 agent。
//
// 本实现把 11/12 的 button_data 作为入站文本（带 [按钮]/[菜单] 前缀），
// 其余类型只记录/忽略，避免把反馈、授权等非对话事件误送 agent。
package qqbot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// 官方互动类型（仅列本实现使用的值）。
const (
	interactionTypeButton          = 11 // 消息按钮回调（需回应）
	interactionTypeMenu            = 12 // 单聊快捷菜单回调（需回应）
	interactionTypeFeedback        = 13 // 消息反馈（无需回应）
	interactionTypeClear           = 14 // 清空会话（无需回应）
	interactionTypeStory           = 15 // 进出故事集（无需回应）
	interactionTypeModel           = 16 // 切换模型（无需回应）
	interactionTypeUserAuth        = 18 // 用户授权（无需回应）
	interactionTypeGroupAuth       = 19 // 群授权（无需回应）
	interactionTypeGroupAuthStatus = 20
)

// interactionEvent 是 INTERACTION_CREATE 的事件体（官方字段子集）。
type interactionEvent struct {
	ID                string `json:"id"`
	Type              int    `json:"type"`
	Scene             string `json:"scene"`
	ChatType          int    `json:"chat_type"`
	UserOpenID        string `json:"user_openid"`
	GroupOpenID       string `json:"group_openid"`
	GroupMemberOpenID string `json:"group_member_openid"`
	ChannelID         string `json:"channel_id"`
	GuildID           string `json:"guild_id"`
	Data              struct {
		Type     int `json:"type"`
		Resolved struct {
			ButtonData string `json:"button_data"`
			ButtonID   string `json:"button_id"`
			FeatureID  string `json:"feature_id"`
			MessageID  string `json:"message_id"`
			Action     string `json:"action"`
		} `json:"resolved"`
	} `json:"data"`
}

// interactionNeedsAck 报告该互动类型是否必须调用 PUT /interactions/{id}。
func interactionNeedsAck(kind int) bool {
	return kind == interactionTypeButton || kind == interactionTypeMenu
}

// parseInteractionMessage 把按钮/快捷菜单回调归一为入站消息；其它类型返回
// false（由调用方决定是否仅记日志）。
func parseInteractionMessage(cfg connectors.Config, data json.RawMessage) (connectors.InboundMessage, bool) {
	var event interactionEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return connectors.InboundMessage{}, false
	}
	if !interactionNeedsAck(event.Type) {
		return connectors.InboundMessage{}, false
	}
	payload := strings.TrimSpace(event.Data.Resolved.ButtonData)
	if payload == "" {
		// 快捷菜单可能只有 feature_id（管理端配置的功能标识）。
		payload = strings.TrimSpace(event.Data.Resolved.FeatureID)
	}
	if payload == "" {
		return connectors.InboundMessage{}, false
	}
	label := "[按钮]"
	if event.Type == interactionTypeMenu {
		label = "[菜单]"
	}

	chat, userID, userName := "", "", ""
	switch strings.ToLower(strings.TrimSpace(event.Scene)) {
	case "group":
		groupOpenID := strings.TrimSpace(event.GroupOpenID)
		memberOpenID := strings.TrimSpace(event.GroupMemberOpenID)
		if groupOpenID == "" || memberOpenID == "" {
			return connectors.InboundMessage{}, false
		}
		chat = chatKindGroup + ":" + groupOpenID
		if !shareSessionInChannelSetting(cfg) {
			chat += ":user:" + memberOpenID
		}
		userID, userName = memberOpenID, memberOpenID
	case "guild":
		channelID := strings.TrimSpace(event.ChannelID)
		if channelID == "" {
			return connectors.InboundMessage{}, false
		}
		chat = chatKindChannel + ":" + channelID
		userID = strings.TrimSpace(event.UserOpenID)
		if userID == "" {
			userID = strings.TrimSpace(event.Data.Resolved.MessageID)
		}
		userName = userID
	default: // c2c
		userOpenID := strings.TrimSpace(event.UserOpenID)
		if userOpenID == "" {
			return connectors.InboundMessage{}, false
		}
		chat = chatKindUser + ":" + userOpenID
		userID, userName = userOpenID, userOpenID
	}

	return connectors.InboundMessage{
		ConnectorID: cfg.ID,
		Provider:    Name,
		Chat:        chat,
		MessageID:   strings.TrimSpace(event.ID),
		DedupeKey:   "interaction",
		UserID:      userID,
		UserName:    userName,
		Text:        label + " " + payload,
		ProviderData: map[string]any{
			"qqbot_message_type":     "interaction",
			"qqbot_interaction_id":   event.ID,
			"qqbot_interaction_type": event.Type,
			"qqbot_button_id":        event.Data.Resolved.ButtonID,
			"qqbot_button_data":      event.Data.Resolved.ButtonData,
		},
	}, true
}

// ackInteraction 调用 PUT /interactions/{interaction_id} 回应按钮/快捷菜单。
// 官方要求同一 id 只能回应一次；失败返回错误（调用方可只记日志）。
func (p *Provider) ackInteraction(ctx context.Context, cfg connectors.Config, interactionID string) error {
	interactionID = strings.TrimSpace(interactionID)
	if interactionID == "" {
		return fmt.Errorf("qqbot: interaction id is required")
	}
	endpoint := p.baseFor(cfg) + "/interactions/" + interactionID
	return p.apiJSON(ctx, cfg, http.MethodPut, endpoint, map[string]any{}, nil)
}

// friendAddEvent 是 FRIEND_ADD 的事件体（官方字段）。
type friendAddEvent struct {
	Timestamp  int64  `json:"timestamp"`
	OpenID     string `json:"openid"`
	Scene      int    `json:"scene"`
	SceneParam string `json:"scene_param"`
	ShortCode  string `json:"short_code"`
	Author     struct {
		UnionOpenID string `json:"union_openid"`
	} `json:"author"`
}

// parseFriendAddMessage 把「用户添加好友」归一为入站消息。
//
// MessageID 用 openid + 时间戳构造：FRIEND_ADD 没有独立消息 id，但同一
// 好友事件可能重投，时间戳可区分不同次添加。文本含场景与自定义 callback_data，
// agent 可据此发送欢迎语/来源归因。
func parseFriendAddMessage(cfg connectors.Config, data json.RawMessage) (connectors.InboundMessage, bool) {
	var event friendAddEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return connectors.InboundMessage{}, false
	}
	openID := strings.TrimSpace(event.OpenID)
	if openID == "" {
		return connectors.InboundMessage{}, false
	}
	messageID := fmt.Sprintf("friend-add:%s:%d", openID, event.Timestamp)
	text := fmt.Sprintf("[好友添加] scene=%d", event.Scene)
	if param := strings.TrimSpace(event.SceneParam); param != "" {
		text += " callback_data=" + param
	}
	return connectors.InboundMessage{
		ConnectorID: cfg.ID,
		Provider:    Name,
		Chat:        chatKindUser + ":" + openID,
		MessageID:   messageID,
		UserID:      openID,
		UserName:    openID,
		Text:        text,
		ProviderData: map[string]any{
			"qqbot_message_type": "friend_add",
			"qqbot_scene":        event.Scene,
			"qqbot_scene_param":  event.SceneParam,
			"qqbot_union_openid": event.Author.UnionOpenID,
			"qqbot_short_code":   event.ShortCode,
		},
		ReceivedAt: time.Now().UTC(),
	}, true
}

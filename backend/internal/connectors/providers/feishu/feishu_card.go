// 卡片按钮回调路由（card.action.trigger，切片 3f）。
//
// 背景：审批/提问的回复依赖文本命令（/approve、/deny、/answer，core
// CommandBackend 闭环）。卡片按钮回调此前与其它未知事件一样被静默 202
// 丢弃——本切片把按钮 value 里的命令文本转成普通入站消息，复用既有命令
// 闭环与白名单/去重/日志，不新增平台语义。
//
// 协议（飞书「卡片回调」v2 事件，schema 2.0）：
//
//	{"schema":"2.0","header":{"event_type":"card.action.trigger",
//	  "event_id":"...","token":"...","app_id":"..."},
//	 "event":{"operator":{"open_id":"..."},"token":"...",
//	          "action":{"value":{"command":"/approve req-1"}}}}
//
// 约定：
//   - agent 发卡时把命令放进按钮 action.value.command（约定键，见下）；
//     value 缺 command 时静默 202（非命令按钮不进 agent）；
//   - operator.open_id 作为 UserID（白名单照常生效）；
//   - 会话锚定按钮所在卡片的消息 chat_id（event.context / action.value.chat
//     均缺失时静默丢弃——无处投递回复）；
//   - MessageID=header.event_id（平台重试同 id，交宿主账本去重）；
//   - 校验与解密复用主入口（challenge/header.token/encrypt_key）。
package feishu

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

const (
	// cardActionCommandKey 是按钮 action.value 里承载命令文本的约定键。
	cardActionCommandKey = "command"
	// cardActionChatKey 是按钮 action.value 里可选的会话键（弱锚点，主锚点
	// 是 event.context.open_chat_id）。
	cardActionChatKey = "chat"
)

// cardActionEnvelope 是卡片回调的 event 载荷。
type cardActionEnvelope struct {
	Operator struct {
		OpenID string `json:"open_id"`
		UserID string `json:"user_id"`
	} `json:"operator"`
	Context struct {
		OpenChatID  string `json:"open_chat_id"`
		OpenMessage string `json:"open_message_id"`
	} `json:"context"`
	Action struct {
		Value map[string]any `json:"value"`
		Name  string         `json:"name"`
		Tag   string         `json:"tag"`
	} `json:"action"`
}

// cardActionMessage 把卡片按钮回调转成命令入站消息；返回 ok=false 表示静默
// 202（非命令按钮 / 缺 operator / 缺会话锚点）。
func cardActionMessage(cfg connectors.Config, header eventHeader, raw json.RawMessage, now func() timeTime) (connectors.InboundMessage, bool, error) {
	if len(raw) == 0 {
		return connectors.InboundMessage{}, false, nil
	}
	var event cardActionEnvelope
	if err := json.Unmarshal(raw, &event); err != nil {
		return connectors.InboundMessage{}, false, fmt.Errorf("decode feishu card action event: %s", err.Error())
	}
	command := strings.TrimSpace(cardActionValueString(event.Action.Value, cardActionCommandKey))
	if command == "" {
		return connectors.InboundMessage{}, false, nil // 非命令按钮：静默
	}
	userID := strings.TrimSpace(firstNonEmpty(event.Operator.OpenID, event.Operator.UserID))
	chatID := strings.TrimSpace(firstNonEmpty(event.Context.OpenChatID, cardActionValueString(event.Action.Value, cardActionChatKey)))
	if userID == "" || chatID == "" {
		return connectors.InboundMessage{}, false, nil
	}
	messageID := strings.TrimSpace(header.EventID)
	if messageID == "" {
		messageID = strings.TrimSpace(event.Context.OpenMessage)
	}
	if messageID == "" {
		return connectors.InboundMessage{}, false, nil // 无幂等键：丢弃（可重试事件应由平台带 event_id 重发）
	}
	return connectors.InboundMessage{
		ConnectorID: cfg.ID,
		Provider:    Name,
		Chat:        chatID,
		ChatTitle:   chatID,
		MessageID:   messageID,
		UserID:      userID,
		UserName:    userID,
		Text:        command,
		ProviderData: map[string]any{
			providerDataEventType: eventCardAction,
			providerDataMessageID: strings.TrimSpace(event.Context.OpenMessage),
		},
		ReceivedAt: now().UTC(),
	}, true, nil
}

// cardActionValueString 从按钮 value 取字符串字段（非字符串类型忽略）。
func cardActionValueString(value map[string]any, key string) string {
	if value == nil {
		return ""
	}
	text, _ := value[key].(string)
	return strings.TrimSpace(text)
}

// timeTime 是 time.Time 的本地别名（保持签名可读，避免额外 import 别名）。
type timeTime = time.Time

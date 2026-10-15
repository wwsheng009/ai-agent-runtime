// 失败回复重放（切片 2f）：识别用户的「重发上次失败回复」意图——微信
// errcode -2 导致回复投递失败后，用户发「重发/继续/retry」等短文本触发
// Service 重放快照。判定表移植上游 isWeChatFailedReplyReplayIntent。
package wechat

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// IsReplyReplayIntent 实现 connectors.ReplyReplayProvider：仅当消息不带媒体、
// 且文本（剥离通道提示语后）是短重发意图时返回 true。
func (p *Provider) IsReplyReplayIntent(msg connectors.InboundMessage) bool {
	if len(msg.Media) > 0 {
		return false
	}
	return isReplyReplayIntentText(stripOutboundMediaNote(msg.Text))
}

// stripOutboundMediaNote 剥离入站文本尾部的通道提示语（意图判定只看用户正文）。
func stripOutboundMediaNote(text string) string {
	if index := strings.Index(text, outboundMediaNote); index >= 0 {
		text = text[:index]
	}
	return strings.TrimSpace(text)
}

// isReplyReplayIntentText 判定短重发意图（上游同款词表；≤24 rune、忽略标点）。
func isReplyReplayIntentText(text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n")))
	if normalized == "" {
		return false
	}
	if len([]rune(normalized)) > 24 {
		return false
	}
	normalized = strings.Trim(normalized, " \t\n\r。.!！？?，,、；;：:")
	collapsed := strings.Join(strings.Fields(normalized), "")
	switch collapsed {
	case "继续", "继续发", "继续发送", "重发", "重新发", "重新发送",
		"再发一次", "再发一遍", "再试一次", "retry", "resend":
		return true
	}
	for _, prefix := range []string{"再发一次", "再发一遍", "重发", "重新发", "重新发送"} {
		if strings.HasPrefix(collapsed, prefix) {
			return true
		}
	}
	return false
}

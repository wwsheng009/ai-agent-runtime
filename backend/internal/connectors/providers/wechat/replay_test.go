package wechat

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// TestIsReplyReplayIntent 锁定重发意图判定（切片 2f；含通道提示语剥离与媒体排除）。
func TestIsReplyReplayIntent(t *testing.T) {
	provider := New(WithLogger(discardLogger))
	cases := []struct {
		name string
		msg  connectors.InboundMessage
		want bool
	}{
		{"exact 重发", connectors.InboundMessage{Text: "重发"}, true},
		{"note stripped", connectors.InboundMessage{Text: "继续\n\n" + outboundMediaNote}, true},
		{"punctuation trimmed", connectors.InboundMessage{Text: "重发！"}, true},
		{"prefix with extra", connectors.InboundMessage{Text: "重发一下刚才那条"}, true},
		{"english retry", connectors.InboundMessage{Text: "Retry"}, true},
		{"normal message", connectors.InboundMessage{Text: "你好"}, false},
		{"long text", connectors.InboundMessage{Text: "重发" + strings.Repeat("很长的内容", 10)}, false},
		{"with media excluded", connectors.InboundMessage{Text: "重发", Media: []connectors.Media{{Kind: "image"}}}, false},
		{"empty", connectors.InboundMessage{}, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := provider.IsReplyReplayIntent(testCase.msg); got != testCase.want {
				t.Fatalf("IsReplyReplayIntent(%+v) = %v, want %v", testCase.msg, got, testCase.want)
			}
		})
	}
}

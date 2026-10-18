package qqbot

// INTERACTION_CREATE / FRIEND_ADD 官方契约回归（2026-07-23 文档）：
//   - 按钮(11)/快捷菜单(12) 必须 PUT /interactions/{id} 回应；
//   - button_data 作为一次用户输入进入 agent；
//   - 反馈(13)/授权(18/19/20) 等非对话事件不产生入站；
//   - FRIEND_ADD 归一为单聊入站，携带 scene/callback_data。

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

func TestParseInteractionButtonC2C(t *testing.T) {
	data := `{"id":"it-1","type":11,"scene":"c2c","chat_type":2,` +
		`"user_openid":"u1","data":{"type":11,"resolved":{"button_data":"confirm:once","button_id":"allow-once"}}}`
	msg, ok := parseInteractionMessage(connectors.Config{ID: "qq-1"}, json.RawMessage(data))
	if !ok {
		t.Fatal("button interaction must parse")
	}
	if msg.Chat != "user:u1" || msg.Text != "[按钮] confirm:once" {
		t.Fatalf("msg = %+v", msg)
	}
	if msg.DedupeKey != "interaction" || msg.MessageID != "it-1" {
		t.Fatalf("dedupe = %q / id = %q", msg.DedupeKey, msg.MessageID)
	}
}

func TestParseInteractionGroupAndMenu(t *testing.T) {
	group := `{"id":"it-2","type":11,"scene":"group","chat_type":1,` +
		`"group_openid":"g1","group_member_openid":"m1",` +
		`"data":{"type":11,"resolved":{"button_data":"{\"command\":\"sample\"}"}}}`
	msg, ok := parseInteractionMessage(connectors.Config{ID: "qq-1"}, json.RawMessage(group))
	if !ok || msg.Chat != "group:g1" || msg.UserID != "m1" {
		t.Fatalf("group msg = %+v (ok=%v)", msg, ok)
	}

	menu := `{"id":"it-3","type":12,"scene":"c2c","chat_type":2,` +
		`"user_openid":"u2","data":{"type":12,"resolved":{"feature_id":"feature-1"}}}`
	msg, ok = parseInteractionMessage(connectors.Config{ID: "qq-1"}, json.RawMessage(menu))
	if !ok || msg.Text != "[菜单] feature-1" {
		t.Fatalf("menu msg = %+v (ok=%v)", msg, ok)
	}
}

func TestParseInteractionNonDialogIgnored(t *testing.T) {
	for _, kind := range []int{13, 14, 15, 16, 18, 19, 20} {
		data := `{"id":"it-x","type":` + itoa(kind) + `,"scene":"c2c","user_openid":"u1",` +
			`"data":{"type":` + itoa(kind) + `,"resolved":{"button_data":"x"}}}`
		if _, ok := parseInteractionMessage(connectors.Config{ID: "qq-1"}, json.RawMessage(data)); ok {
			t.Fatalf("interaction type %d must be ignored", kind)
		}
	}
}

func TestAckInteractionEndpoint(t *testing.T) {
	fake := newFakeQQ("secret-1", "token-1")
	server := newFakeQQServer(t, fake)
	provider := New(WithAPIBase(server.URL))
	cfg := testConfig("qq-1", "app-1", "secret-1")

	if err := provider.ackInteraction(context.Background(), cfg, "it-1"); err != nil {
		t.Fatalf("ack: %v", err)
	}
	sent := fake.sentMessages()
	if len(sent) != 1 || sent[0].path != "/interactions/it-1" {
		t.Fatalf("sent = %+v", sent)
	}
	if err := provider.ackInteraction(context.Background(), cfg, " "); err == nil {
		t.Fatal("empty interaction id must be rejected")
	}
}

func TestParseFriendAddMessage(t *testing.T) {
	data := `{"openid":"u1","timestamp":1784570523,"scene":2003,` +
		`"scene_param":"callback_abc123","author":{"union_openid":"union-1"}}`
	msg, ok := parseFriendAddMessage(connectors.Config{ID: "qq-1"}, json.RawMessage(data))
	if !ok {
		t.Fatal("friend add must parse")
	}
	if msg.Chat != "user:u1" || msg.UserID != "u1" {
		t.Fatalf("msg = %+v", msg)
	}
	if msg.Text != "[好友添加] scene=2003 callback_data=callback_abc123" {
		t.Fatalf("text = %q", msg.Text)
	}
	if msg.ProviderData["qqbot_union_openid"] != "union-1" {
		t.Fatalf("provider data = %+v", msg.ProviderData)
	}
}

func TestParseFriendAddRequiresOpenID(t *testing.T) {
	if _, ok := parseFriendAddMessage(connectors.Config{ID: "qq-1"}, json.RawMessage(`{"scene":1001}`)); ok {
		t.Fatal("missing openid must be rejected")
	}
}

// itoa 避免为测试引入 strconv 依赖噪音。
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [4]byte
	index := len(digits)
	for value > 0 {
		index--
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[index:])
}

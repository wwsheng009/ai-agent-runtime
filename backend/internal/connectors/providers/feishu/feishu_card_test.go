package feishu

// 卡片按钮回调路由测试（feishu_card.go）：
//   - value.command 命令按钮 → 命令入站消息（UserID=operator、Chat=context、
//     MessageID=event_id）；
//   - 非命令按钮 / 缺 operator / 缺会话锚点 / 无 event_id → 静默 202；
//   - header.token 与 encrypt_key 校验复用主入口（既有用例覆盖，此处锁
//     路由路径带 token 校验）。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// cardActionPayload 构造卡片按钮回调事件（schema 2.0）。
func cardActionPayload(eventID, openID, chatID, command string) map[string]any {
	value := map[string]any{}
	if command != "" {
		value["command"] = command
	}
	return map[string]any{
		"schema": "2.0",
		"header": map[string]any{
			"event_type": eventCardAction,
			"event_id":   eventID,
			"token":      "vt-123",
			"app_id":     "cli_test",
		},
		"event": map[string]any{
			"operator": map[string]any{"open_id": openID},
			"context":  map[string]any{"open_chat_id": chatID, "open_message_id": "om_card_1"},
			"action":   map[string]any{"value": value, "tag": "button", "name": "approve"},
		},
	}
}

func TestCardActionCommandRoutesToInboundMessage(t *testing.T) {
	provider := New()
	result, err := provider.HandleWebhook(context.Background(), testConfig(),
		webhookRequest(t, cardActionPayload("evt-card-1", "ou_user", "oc_chat_9", "/approve req-1")))
	if err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	if result.Message == nil {
		t.Fatalf("expected inbound message, got %+v", result)
	}
	message := result.Message
	if message.Chat != "oc_chat_9" || message.UserID != "ou_user" {
		t.Fatalf("chat/user = %q/%q, want oc_chat_9/ou_user", message.Chat, message.UserID)
	}
	if message.MessageID != "evt-card-1" {
		t.Fatalf("message id = %q, want evt-card-1 (event_id for dedup)", message.MessageID)
	}
	if strings.TrimSpace(message.Text) != "/approve req-1" {
		t.Fatalf("text = %q, want command passthrough", message.Text)
	}
}

func TestCardActionNonCommandAndIncompleteSilentlyIgnored(t *testing.T) {
	provider := New()
	cases := []struct {
		name    string
		payload map[string]any
	}{
		{"non-command button", cardActionPayload("evt-c1", "ou_user", "oc_chat_1", "")},
		{"missing operator", map[string]any{
			"schema": "2.0",
			"header": map[string]any{"event_type": eventCardAction, "event_id": "evt-c2", "token": "vt-123"},
			"event": map[string]any{
				"context": map[string]any{"open_chat_id": "oc_chat_1"},
				"action":  map[string]any{"value": map[string]any{"command": "/approve r"}},
			},
		}},
		{"missing chat anchor", map[string]any{
			"schema": "2.0",
			"header": map[string]any{"event_type": eventCardAction, "event_id": "evt-c3", "token": "vt-123"},
			"event": map[string]any{
				"operator": map[string]any{"open_id": "ou_user"},
				"action":   map[string]any{"value": map[string]any{"command": "/approve r"}},
			},
		}},
	}
	for _, testCase := range cases {
		result, err := provider.HandleWebhook(context.Background(), testConfig(),
			webhookRequest(t, testCase.payload))
		if err != nil {
			t.Fatalf("%s: HandleWebhook error = %v", testCase.name, err)
		}
		if result.Message != nil {
			t.Fatalf("%s: expected silent 202, got message %+v", testCase.name, result.Message)
		}
	}
}

// TestCardActionWrongTokenRejected 锁定卡片回调走同一 token 校验（错配 400）。
func TestCardActionWrongTokenRejected(t *testing.T) {
	provider := New()
	payload := cardActionPayload("evt-bad", "ou_user", "oc_chat_1", "/approve req-1")
	payload["header"].(map[string]any)["token"] = "wrong-token"
	if _, err := provider.HandleWebhook(context.Background(), testConfig(),
		webhookRequest(t, payload)); err == nil {
		t.Fatal("expected token mismatch error")
	}
}

// TestCardActionDecryptedEnvelope 锁定加密模式下的卡片回调可解密路由。
func TestCardActionDecryptedEnvelope(t *testing.T) {
	provider := New()
	plaintext, _ := json.Marshal(cardActionPayload("evt-enc-1", "ou_user", "oc_chat_5", "/deny req-2"))
	result, err := provider.HandleWebhook(context.Background(), testConfig(), webhookRequest(t, map[string]any{
		"encrypt": encryptPayload(t, plaintext, "ek-456"),
	}))
	if err != nil {
		t.Fatalf("HandleWebhook: %v", err)
	}
	if result.Message == nil || result.Message.Text != "/deny req-2" || result.Message.Chat != "oc_chat_5" {
		t.Fatalf("unexpected message: %+v", result.Message)
	}
}

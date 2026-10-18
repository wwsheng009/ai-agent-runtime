package qqbot

// message_scene 官方契约（2026-10-17 核查）：
//   - ext 为 key=value 列表，含 msg_idx（去重依据）、ref_msg_idx（被引用
//     消息索引）、auth_token；
//   - 单聊引用消息 message_type=103 的引用索引只在此处，不在 message_reference。

import (
	"encoding/json"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

func TestParseC2CMessageSceneDedupeAndRef(t *testing.T) {
	data := `{"id":"evt-1","content":"继续","author":{"user_openid":"u1"},` +
		`"message_scene":{"source":"default","ext":["msg_idx=REFIDX_a==","ref_msg_idx=REFIDX_b==","auth_token=tok"]}}`
	msg, ok := parseGatewayMessage(connectors.Config{ID: "qq-1"}, "C2C_MESSAGE_CREATE", json.RawMessage(data))
	if !ok {
		t.Fatal("message must parse")
	}
	if msg.DedupeKey != "REFIDX_a==" {
		t.Fatalf("DedupeKey = %q, want msg_idx", msg.DedupeKey)
	}
	if msg.ProviderData["qqbot_msg_idx"] != "REFIDX_a==" ||
		msg.ProviderData["qqbot_ref_msg_idx"] != "REFIDX_b==" {
		t.Fatalf("provider data = %+v", msg.ProviderData)
	}
	// 引用索引降级：没有 message_reference 时用 ref_msg_idx 作为引用摘要。
	if msg.Text != "Quoted: REFIDX_b==\n继续" {
		t.Fatalf("text = %q", msg.Text)
	}
}

func TestParseGroupMessageSceneDedupe(t *testing.T) {
	data := `{"id":"evt-2","group_openid":"g1","content":"<@!bot> hi","author":{"member_openid":"m1"},` +
		`"message_scene":{"ext":["msg_idx=REFIDX_g=="]}}`
	msg, ok := parseGatewayMessage(connectors.Config{ID: "qq-1"}, "GROUP_AT_MESSAGE_CREATE", json.RawMessage(data))
	if !ok {
		t.Fatal("message must parse")
	}
	if msg.DedupeKey != "REFIDX_g==" || msg.ProviderData["qqbot_msg_idx"] != "REFIDX_g==" {
		t.Fatalf("msg = %+v", msg)
	}
}

func TestParseMessageSceneMissingIsEmpty(t *testing.T) {
	data := `{"id":"evt-3","content":"hi","author":{"user_openid":"u1"}}`
	msg, ok := parseGatewayMessage(connectors.Config{ID: "qq-1"}, "C2C_MESSAGE_CREATE", json.RawMessage(data))
	if !ok {
		t.Fatal("message must parse")
	}
	if msg.DedupeKey != "" {
		t.Fatalf("DedupeKey = %q, want empty", msg.DedupeKey)
	}
}

func TestSceneExtParsing(t *testing.T) {
	scene := &gatewayMessageScene{Ext: []string{
		"msg_idx=REFIDX_a==", "broken", "empty=", " auth_token = tok ",
	}}
	got := scene.sceneExt()
	if got["msg_idx"] != "REFIDX_a==" || got["auth_token"] != "tok" {
		t.Fatalf("scene ext = %+v", got)
	}
	if _, ok := got["broken"]; ok {
		t.Fatalf("malformed item must be ignored: %+v", got)
	}
}

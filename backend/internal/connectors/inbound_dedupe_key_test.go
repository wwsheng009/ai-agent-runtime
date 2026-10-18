package connectors

// inboundDedupeKey 的通道私有后缀：QQ 官方要求同 id 的重复推送结合
// message_scene.ext 的 msg_idx 去重。其它通道不填 DedupeKey，键保持原样。

import "testing"

func TestInboundDedupeKeyWithoutSuffix(t *testing.T) {
	msg := InboundMessage{Chat: "user:u1", MessageID: "evt-1"}
	if got := inboundDedupeKey(msg); got != "user:u1|evt-1" {
		t.Fatalf("key = %q", got)
	}
}

func TestInboundDedupeKeyWithSuffix(t *testing.T) {
	msg := InboundMessage{Chat: "user:u1", MessageID: "evt-1", DedupeKey: "REFIDX_a=="}
	if got := inboundDedupeKey(msg); got != "user:u1|evt-1|REFIDX_a==" {
		t.Fatalf("key = %q", got)
	}
	// 同 id、不同 msg_idx 必须得到不同键（不被误判重复）。
	other := msg
	other.DedupeKey = "REFIDX_b=="
	if inboundDedupeKey(msg) == inboundDedupeKey(other) {
		t.Fatal("different msg_idx must not collide")
	}
}

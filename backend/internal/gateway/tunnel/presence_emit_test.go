package tunnel

import (
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

// TestApplySessionUpdateEmitsPresence：Viewing 会话经 OnSessionPresence 续期
// host 源在场；非 Viewing 不触发（TTL 到期自然失效，docs/bots/10 §3.2）。
func TestApplySessionUpdateEmitsPresence(t *testing.T) {
	reg := NewRegistry()
	reg.Upsert(&Node{NodeID: "n1"})
	var got []string
	s := &Server{
		cfg: Config{
			OnSessionChanged: func(string, string) {},
			OnSessionPresence: func(nodeID, sessionID string) {
				got = append(got, nodeID+"|"+sessionID)
			},
		},
		registry: reg,
	}
	s.applySessionUpdate("n1", wire.SessionUpdate{
		Op:      wire.SessionOpUpsert,
		Session: &wire.SessionSummary{SessionID: "s1", State: wire.SessionStateRunning, Viewing: true},
	})
	if len(got) != 1 || got[0] != "n1|s1" {
		t.Fatalf("presence emits = %v", got)
	}
	s.applySessionUpdate("n1", wire.SessionUpdate{
		Op:      wire.SessionOpUpsert,
		Session: &wire.SessionSummary{SessionID: "s1", State: wire.SessionStateIdle},
	})
	if len(got) != 1 {
		t.Fatalf("非 Viewing 不得续期：%v", got)
	}
}

// TestHeartbeatDigestMergesViewing：心跳摘要合并 Viewing 且不触发广播/持久化
// （docs/bots/10 §3.1：心跳是高频续期通道）。
func TestHeartbeatDigestMergesViewing(t *testing.T) {
	node := &Node{NodeID: "n1", Sessions: []wire.SessionSummary{{SessionID: "s1"}}}
	changed, _ := applyHeartbeatDigests(node, []wire.SessionDigest{{SessionID: "s1", Viewing: true}}, time.Now())
	if changed {
		t.Fatal("Viewing 翻转不得触发广播/持久化")
	}
	if !node.Sessions[0].Viewing {
		t.Fatal("digest 应合并 Viewing")
	}
	changed, _ = applyHeartbeatDigests(node, []wire.SessionDigest{{SessionID: "s1", Viewing: false}}, time.Now())
	if changed || node.Sessions[0].Viewing {
		t.Fatal("Viewing 回落应静默合并")
	}
}

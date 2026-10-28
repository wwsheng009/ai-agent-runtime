package tunnel

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

func TestWindowActionNameAndParseRoundTrip(t *testing.T) {
	cases := []struct {
		kind        string
		sessionID   string
		wantPending string
	}{
		{"open-terminal", "session_1", "window:open-terminal:session_1"},
		{"resume", "session_2", "window:resume:session_2"},
		{"close", "session_3", "window:close:session_3"},
		{"new", "", "window:new"},
		// 新会话动作永远不带会话 ID：队列里出现 ID 只可能是调用方拼错了。
		{"NEW", "ignored", "window:new"},
	}
	for _, testCase := range cases {
		pending := wire.WindowActionName(testCase.kind, testCase.sessionID)
		if pending != testCase.wantPending {
			t.Fatalf("name(%q,%q) = %q, want %q", testCase.kind, testCase.sessionID, pending, testCase.wantPending)
		}
		kind, sessionID, ok := wire.ParseWindowAction(pending)
		if !ok {
			t.Fatalf("parse(%q) refused a well-formed action", pending)
		}
		if kind != strings.ToLower(testCase.kind) {
			t.Fatalf("kind = %q, want %q", kind, testCase.kind)
		}
		if strings.ToLower(testCase.kind) != "new" && testCase.sessionID != "" && sessionID != testCase.sessionID {
			t.Fatalf("session = %q, want %q", sessionID, testCase.sessionID)
		}
	}
}

func TestParseWindowActionRejectsMalformed(t *testing.T) {
	rejected := []string{
		"watch:session_1",          // 不是窗口动作
		"window:",                  // 缺 kind
		"window:explode:session_1", // kind 不在词表
		"window:close",             // 会话级动作缺 ID
		"window:close:   ",         // 会话 ID 只有空白
		"WINDOW:close:session_1",   // 前缀大小写敏感（Pending 由网关生成，不做宽松匹配）
	}
	for _, pending := range rejected {
		if kind, sessionID, ok := wire.ParseWindowAction(pending); ok {
			t.Fatalf("parse(%q) accepted kind=%q session=%q", pending, kind, sessionID)
		}
	}
}

func TestWindowActionQueueDedupesAndBounds(t *testing.T) {
	queue := newWindowActionQueue()
	action := wire.WindowActionName(wire.WindowActionOpenTerminal, "session_1")
	if !queue.queue("node-a", action) {
		t.Fatal("first queue must succeed")
	}
	if !queue.queue("node-a", action) {
		t.Fatal("duplicate must succeed without stacking")
	}
	if got := queue.pendingCount("node-a"); got != 1 {
		t.Fatalf("pendingCount = %d, want 1 (deduped)", got)
	}

	for index := 0; index < windowActionQueueLimit+4; index++ {
		queue.queue("node-a", wire.WindowActionName(wire.WindowActionClose, "session_"+string(rune('a'+index%26))+strings.Repeat("x", index)))
	}
	if got := queue.pendingCount("node-a"); got > windowActionQueueLimit {
		t.Fatalf("pendingCount = %d, want <= %d", got, windowActionQueueLimit)
	}

	drained := queue.drain("node-a")
	if len(drained) == 0 {
		t.Fatal("drain must hand out the queued actions")
	}
	if again := queue.drain("node-a"); len(again) != 0 {
		t.Fatalf("second drain = %v, want empty (one heartbeat consumes once)", again)
	}
}

func TestWindowActionQueueForgetIsolatesNodes(t *testing.T) {
	queue := newWindowActionQueue()
	queue.queue("node-a", "window:resume:s1")
	queue.queue("node-b", "window:resume:s2")
	queue.forget("node-a")
	if queue.pendingCount("node-a") != 0 {
		t.Fatal("node-a queue must be dropped")
	}
	if queue.pendingCount("node-b") != 1 {
		t.Fatal("node-b queue must be untouched")
	}
}

func TestQueueWindowActionRequiresLiveConnection(t *testing.T) {
	server := NewServer(Config{})
	if ok, reason := server.QueueWindowAction("node-a", wire.WindowActionClose, "session_1"); ok || !strings.Contains(reason, "offline") {
		t.Fatalf("offline queue = (%v, %q), want refusal mentioning offline", ok, reason)
	}

	sess := &session{}
	server.registerSession("node-a", sess)
	if ok, reason := server.QueueWindowAction("node-a", wire.WindowActionOpenTerminal, "session_1"); !ok {
		t.Fatalf("live queue refused: %q", reason)
	}
	taken := server.TakePendingWindowActions("node-a")
	if strings.Join(taken, ",") != "window:open-terminal:session_1" {
		t.Fatalf("taken = %v", taken)
	}

	// 断连即丢弃：不能让一条过期动作在某次无关的重连后突然执行。
	server.QueueWindowAction("node-a", wire.WindowActionClose, "session_1")
	server.unregisterSession("node-a", sess)
	if left := server.TakePendingWindowActions("node-a"); len(left) != 0 {
		t.Fatalf("after unregister pending = %v, want empty", left)
	}
}

func TestQueueWindowActionValidatesVocabulary(t *testing.T) {
	server := NewServer(Config{})
	sess := &session{}
	server.registerSession("node-a", sess)
	defer server.unregisterSession("node-a", sess)

	if ok, reason := server.QueueWindowAction("node-a", "explode", "s1"); ok || !strings.Contains(reason, "unknown window action") {
		t.Fatalf("unknown kind = (%v,%q)", ok, reason)
	}
	if ok, reason := server.QueueWindowAction("node-a", wire.WindowActionResume, "  "); ok || !strings.Contains(reason, "session_id is required") {
		t.Fatalf("missing session = (%v,%q)", ok, reason)
	}
	if ok, _ := server.QueueWindowAction("node-a", wire.WindowActionNew, ""); !ok {
		t.Fatal("action=new must be accepted without a session id")
	}
	if ok, reason := server.QueueWindowAction("", wire.WindowActionNew, ""); ok || !strings.Contains(reason, "node_id") {
		t.Fatalf("empty node = (%v,%q)", ok, reason)
	}
}

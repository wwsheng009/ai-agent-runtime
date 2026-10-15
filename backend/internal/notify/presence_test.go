package notify

import (
	"context"
	"testing"
)

// TestDispatcherPresenceGateSuppressesAndKeepsDedupKey 锁定 S4 内核在场门
// （docs/bots/10 §3.3 / §5 S4）：在场 → 跳过投递、回执标记 Suppressed，且
// **不认领去重键**——查看者离开后同一事件可再次投递（"切走补推一次"）。
func TestDispatcherPresenceGateSuppressesAndKeepsDedupKey(t *testing.T) {
	ch := &notifyTestChannel{name: "webpush"}
	ledger := NewMemoryLedger(8)
	d := NewDispatcher(ledger, ch)

	watching := true
	d.SetPresence(PresenceFunc(func(nodeID, sessionID string) bool {
		return watching && nodeID == "n1" && sessionID == "s1"
	}))

	ev := EventFor("error", "n1|s1", notifyTestNote("boom"))
	ev.Node, ev.Session = "n1", "s1"

	r, err := d.Dispatch(context.Background(), ev)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Suppressed {
		t.Fatalf("在场应标记 Suppressed：%+v", r)
	}
	if len(ch.calls) != 0 {
		t.Fatal("在场不得投递任何通道")
	}
	if _, claimed := ledger.Receipt("n1|s1"); claimed {
		t.Fatal("在场跳过不得认领去重键（否则补推被吞）")
	}

	// 查看者离开 → 同一事件再次投递应真正送达（补推一次）。
	watching = false
	r2, err := d.Dispatch(context.Background(), ev)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Suppressed {
		t.Fatalf("离开后不应再抑制：%+v", r2)
	}
	if len(ch.calls) != 1 {
		t.Fatalf("补推应恰好投递一次，实际 %d", len(ch.calls))
	}
	if _, ok := ledger.Receipt("n1|s1"); !ok {
		t.Fatal("投递后应留下回执")
	}
}

// TestDispatcherPresenceGateNilIsNoop：未注入在场权威 = 行为不变（向后兼容）。
func TestDispatcherPresenceGateNilIsNoop(t *testing.T) {
	ch := &notifyTestChannel{name: "webpush"}
	d := NewDispatcher(nil, ch)
	ev := EventFor("error", "k", notifyTestNote("x"))
	ev.Node, ev.Session = "n1", "s1"

	r, err := d.Dispatch(context.Background(), ev)
	if err != nil {
		t.Fatal(err)
	}
	if r.Suppressed || len(ch.calls) != 1 {
		t.Fatalf("nil presence 应照常投递：suppressed=%v calls=%d", r.Suppressed, len(ch.calls))
	}
}

// TestDispatcherPresenceGateRequiresNodeAndSession：缺定位（node 或 session 为空）
// 时无法判定，按不在场放行——宁可多推一次，也不误吞通知。
func TestDispatcherPresenceGateRequiresNodeAndSession(t *testing.T) {
	ch := &notifyTestChannel{name: "webpush"}
	d := NewDispatcher(nil, ch)
	d.SetPresence(PresenceFunc(func(string, string) bool { return true }))

	for _, ev := range []Event{
		EventFor("error", "k1", notifyTestNote("no-node")),
		EventFor("error", "k2", notifyTestNote("no-session")),
	} {
		r, err := d.Dispatch(context.Background(), ev)
		if err != nil {
			t.Fatal(err)
		}
		if r.Suppressed {
			t.Fatalf("缺定位不应触发在场门：%+v", ev)
		}
	}
	if len(ch.calls) != 2 {
		t.Fatalf("缺定位应照常投递，calls=%d", len(ch.calls))
	}
}

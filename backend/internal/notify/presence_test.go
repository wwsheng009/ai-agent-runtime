package notify

import (
	"context"
	"strings"
	"testing"
	"time"
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

// —— G3 静默/合并下沉内核（docs/bots/12-notify-consolidation-plan.md §4 G3 / §3 D3）——

// TestDispatcherQuietHoursMutesNormal 锁定静默门语义：窗口内 normal 被丢弃
// （回执 Muted、不认领键——与 Suppressed 的"回滚补推"相反，静默是永久性
// 丢弃）；high 类穿透；键未认领（Muted 不是投递记账）。
func TestDispatcherQuietHoursMutesNormal(t *testing.T) {
	ch := &notifyTestChannel{name: "webpush"}
	ledger := NewMemoryLedger(8)
	d := NewDispatcher(ledger, ch)
	d.SetQuietHours([]QuietRange{{Start: 0, End: 24 * 60}}) // all day
	fixed := time.Date(2026, 10, 15, 23, 0, 0, 0, time.Local)
	d.SetCoalesceClock(func() time.Time { return fixed })

	// normal（completed）在静默窗口内 → Muted，不投递、不认领。
	ev := EventFor("completed", "k-normal", notifyTestNote("done"))
	r, err := d.Dispatch(context.Background(), ev)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Muted || r.Suppressed {
		t.Fatalf("静默窗口内 normal 应 Muted：%+v", r)
	}
	if len(ch.calls) != 0 {
		t.Fatal("静默窗口内 normal 不得投递")
	}
	if _, ok := ledger.Receipt("k-normal"); ok {
		t.Fatal("Muted 不得认领去重键（静默是时刻性判定）")
	}

	// high（error）穿透静默窗口。
	evHigh := EventFor("error", "k-high", notifyTestNote("boom"))
	if _, err := d.Dispatch(context.Background(), evHigh); err != nil {
		t.Fatal(err)
	}
	if len(ch.calls) != 1 {
		t.Fatalf("high 应穿透静默窗口，calls=%d", len(ch.calls))
	}
}

// TestDispatcherCoalescesNormalInWindow 锁定合并门语义：normal 在窗口内攒批，
// flush 后合并投递一条；键在 flush 时认领（攒批期间重复 Dispatch 同键事件
// 会再次入批——flush 合并去重由 Coalesce 处理，重复打扰由账本在 flush 认领
// 时兜底，语义与宿主侧原实现一致）。
func TestDispatcherCoalescesNormalInWindow(t *testing.T) {
	ch := &notifyTestChannel{name: "webpush"}
	d := NewDispatcher(nil, ch)
	d.SetPrimary("webpush")
	d.SetCoalesceWindow(40 * time.Millisecond)

	// 两个 normal 事件入批；flush 合并为一条。
	if _, err := d.Dispatch(context.Background(), EventFor("completed", "k1", notifyTestNote("s1"))); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Dispatch(context.Background(), EventFor("completed", "k2", notifyTestNote("s2"))); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && ch.callCount() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if ch.callCount() != 1 {
		t.Fatalf("合并窗口应恰好投递 1 条，实际 %d", ch.callCount())
	}
	if !strings.Contains(ch.calls[0].Title, "2") {
		t.Fatalf("合并标题应含会话计数 2，got %q", ch.calls[0].Title)
	}
	// 窗口后再等一段确认无第二次投递。
	time.Sleep(100 * time.Millisecond)
	if len(ch.calls) != 1 {
		t.Fatalf("合并后不得重复投递，实际 %d", len(ch.calls))
	}
}

// TestDispatcherLowDeliveredWhenInboxMissing 锁定 G3 统一路径后的 low/normal
// 语义：normal（waiting）走主通道照常投递（D6）；low/零值事件走 targetsFor
// 的 inbox 分支——无 inbox 通道时不投递、仅返回空回执（不 Muted 不 Suppressed），
// 不再依赖宿主侧批处理分支的存在性（原 flush 只处理 batched，low 恒静默）。
func TestDispatcherLowDeliveredWhenInboxMissing(t *testing.T) {
	ch := &notifyTestChannel{name: "webpush"}
	d := NewDispatcher(nil, ch)
	d.SetPrimary("webpush")

	// normal（waiting 登记为 normal）：主通道投递（D6 行为不变）。
	evNormal := EventFor("waiting", "k-normal", notifyTestNote("w"))
	if _, err := d.Dispatch(context.Background(), evNormal); err != nil {
		t.Fatal(err)
	}
	if len(ch.calls) != 1 {
		t.Fatalf("normal（waiting）应走主通道，calls=%d", len(ch.calls))
	}

	// 人造 low 事件（登记表无此 kind → PriorityOf 回落 low）：无 inbox 通道
	// → 不投递、空回执。
	evLow := Event{Kind: "unknown-kind", DedupKey: "k-low", Notification: notifyTestNote("x")}
	r, err := d.Dispatch(context.Background(), evLow)
	if err != nil {
		t.Fatal(err)
	}
	if r.Muted || r.Suppressed || len(r.Outcomes) != 0 {
		t.Fatalf("low 无 inbox 通道应不投递仅记账：%+v", r)
	}
	if len(ch.calls) != 1 {
		t.Fatalf("low 不得投到 webpush，calls=%d", len(ch.calls))
	}
}

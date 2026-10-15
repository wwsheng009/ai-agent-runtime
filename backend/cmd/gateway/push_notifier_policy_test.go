package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/push"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/tunnel"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
	"github.com/wwsheng009/ai-agent-runtime/internal/notify"
)

// 本文件是宿主级行为护栏（docs/bots/12 §4 G3 退役 notify_policy.go 时保留）：
// 合并/免打扰/投递提示/种子去重的端到端语义锁定。别名单元测试（解析/窗口/
// 合并原语）已由内核 policy_test.go 同名测试接管。

// testNotifier builds a notifier with one subscription pointing at endpoint.
func testNotifier(t *testing.T, endpoint string) *pushNotifier {
	t.Helper()
	vapid, err := push.LoadOrCreateVAPID("")
	if err != nil {
		t.Fatal(err)
	}
	store, _ := push.OpenStore("")
	if err := store.Add(testSubscription(t, endpoint)); err != nil {
		t.Fatal(err)
	}
	return newPushNotifier(push.NewManager(store, vapid, "mailto:a@b.c"), "https://gw.example.com", nil)
}
func TestPushNotifierCoalescesLowPriority(t *testing.T) {
	hits := make(chan struct{}, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits <- struct{}{}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	n := testNotifier(t, srv.URL+"/sub")
	n.coalesceWindow = 40 * time.Millisecond

	node := &tunnel.Node{NodeID: "n1", Name: "laptop", Sessions: []wire.SessionSummary{
		{SessionID: "s1", Title: "A", Attention: "completed"},
		{SessionID: "s2", Title: "B", Attention: "completed"},
	}}
	n.observe([]*tunnel.Node{node})
	if !waitHit(hits, 2*time.Second) {
		t.Fatal("expected a coalesced push")
	}
	if waitHit(hits, 250*time.Millisecond) {
		t.Fatal("expected a single coalesced push, got a second")
	}
}

func TestPushNotifierHighPriorityIsImmediate(t *testing.T) {
	hits := make(chan struct{}, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits <- struct{}{}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	n := testNotifier(t, srv.URL+"/sub")
	n.coalesceWindow = time.Hour // high priority must not wait for this

	node := &tunnel.Node{NodeID: "n1", Name: "laptop", Sessions: []wire.SessionSummary{
		{SessionID: "s1", Title: "A", Attention: "question"},
	}}
	n.observe([]*tunnel.Node{node})
	if !waitHit(hits, 2*time.Second) {
		t.Fatal("high-priority attention should be delivered immediately")
	}
}

// TestPushNotifierDeliveryHints asserts high-priority attention is sent with
// RFC 8030 Urgency: high (so mobile push services do not defer it to a Doze
// maintenance window) and a short TTL, while low-priority keeps normal urgency.
func TestPushNotifierDeliveryHints(t *testing.T) {
	type delivery struct{ urgency, ttl string }
	got := make(chan delivery, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- delivery{urgency: r.Header.Get("Urgency"), ttl: r.Header.Get("TTL")}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	n := testNotifier(t, srv.URL+"/sub")
	n.coalesceWindow = time.Hour // low priority must not be flushed in this test

	n.observe([]*tunnel.Node{{
		NodeID:   "n1",
		Sessions: []wire.SessionSummary{{SessionID: "s1", Title: "A", Attention: "approval"}},
	}})
	select {
	case d := <-got:
		if d.urgency != "high" || d.ttl != "600" {
			t.Fatalf("high-priority headers = %q/%q, want high/600", d.urgency, d.ttl)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no high-priority push received")
	}
}

func TestPushNotifierQuietHours(t *testing.T) {
	hits := make(chan struct{}, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits <- struct{}{}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	n := testNotifier(t, srv.URL+"/sub")
	n.coalesceWindow = 30 * time.Millisecond
	n.quietHours = []notify.QuietRange{{Start: 0, End: 24 * 60}} // all day
	// G3：静默判定时钟随策略下沉内核（SetCoalesceClock）；改完装配字段后
	// 置空 dispatcher 缓存使其在下一次 dispatch 重建时生效。
	n.SetCoalesceClockForTest(func() time.Time { return time.Date(2026, 10, 11, 23, 0, 0, 0, time.Local) })

	low := &tunnel.Node{NodeID: "n1", Sessions: []wire.SessionSummary{{SessionID: "s1", Attention: "completed"}}}
	n.observe([]*tunnel.Node{low})
	if waitHit(hits, 300*time.Millisecond) {
		t.Fatal("low-priority push must be muted during quiet hours")
	}

	high := &tunnel.Node{NodeID: "n1", Sessions: []wire.SessionSummary{{SessionID: "s2", Attention: "approval"}}}
	n.observe([]*tunnel.Node{high})
	if !waitHit(hits, 2*time.Second) {
		t.Fatal("high-priority push must pierce quiet hours")
	}
}

// TestPushNotifierSeedPreventsRestartRepush mirrors a gateway restart: the
// restored (stale) registry state seeds the dedup snapshot, so the first
// observe after startup does not re-push the pre-restart attention.
func TestPushNotifierSeedPreventsRestartRepush(t *testing.T) {
	hits := make(chan struct{}, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits <- struct{}{}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	n := testNotifier(t, srv.URL+"/sub")
	n.Seed([]*tunnel.Node{{
		NodeID: "n1",
		Stale:  true,
		Sessions: []wire.SessionSummary{
			{SessionID: "s1", Attention: "approval"},
		},
	}})

	// The agent reconnects and reports the same attention state: no push.
	n.observe([]*tunnel.Node{{
		NodeID: "n1",
		Sessions: []wire.SessionSummary{
			{SessionID: "s1", Attention: "approval"},
		},
	}})
	if waitHit(hits, 300*time.Millisecond) {
		t.Fatal("seeded attention must not re-push after restart")
	}

	// A different attention state for the same session still notifies.
	n.observe([]*tunnel.Node{{
		NodeID: "n1",
		Sessions: []wire.SessionSummary{
			{SessionID: "s1", Attention: "question"},
		},
	}})
	if !waitHit(hits, 2*time.Second) {
		t.Fatal("attention change after seed must push")
	}
}

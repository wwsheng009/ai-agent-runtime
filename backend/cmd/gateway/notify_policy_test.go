package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/push"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/tunnel"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

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

func TestParseQuietHours(t *testing.T) {
	ranges, err := parseQuietHours("22:00-08:00, 12:30-13:00")
	if err != nil {
		t.Fatalf("parseQuietHours: %v", err)
	}
	if len(ranges) != 2 {
		t.Fatalf("want 2 windows, got %d", len(ranges))
	}
	if ranges[0].start != 22*60 || ranges[0].end != 8*60 {
		t.Fatalf("first window = %+v", ranges[0])
	}
	if ranges[1].start != 12*60+30 || ranges[1].end != 13*60 {
		t.Fatalf("second window = %+v", ranges[1])
	}

	if got, err := parseQuietHours(""); err != nil || got != nil {
		t.Fatalf("empty spec = %v, %v", got, err)
	}
	for _, bad := range []string{"25:00-08:00", "nope", "10:00", "10:70-11:00"} {
		if _, err := parseQuietHours(bad); err == nil {
			t.Fatalf("parseQuietHours(%q) = nil error, want error", bad)
		}
	}
}

func TestQuietRangeContains(t *testing.T) {
	at := func(h, m int) time.Time {
		return time.Date(2026, 1, 1, h, m, 0, 0, time.Local)
	}
	overnight := quietRange{start: 22 * 60, end: 8 * 60}
	for _, tc := range []struct {
		t    time.Time
		want bool
	}{
		{at(23, 0), true},
		{at(2, 30), true},
		{at(7, 59), true},
		{at(8, 0), false},
		{at(12, 0), false},
		{at(21, 59), false},
	} {
		if got := overnight.contains(tc.t); got != tc.want {
			t.Fatalf("overnight.contains(%s) = %v, want %v", tc.t, got, tc.want)
		}
	}
	daytime := quietRange{start: 12 * 60, end: 13 * 60}
	if !daytime.contains(at(12, 30)) || daytime.contains(at(13, 0)) || daytime.contains(at(11, 59)) {
		t.Fatal("daytime window bounds are not half-open")
	}
	if (quietRange{start: 0, end: 0}).contains(at(0, 0)) {
		t.Fatal("zero-width window must not contain any time")
	}
}

func TestCoalesceNotifications(t *testing.T) {
	single := coalesceNotifications("completed", []push.Notification{{Title: "A"}})
	if single.Title != "A" {
		t.Fatalf("single item should pass through, got %+v", single)
	}

	merged := coalesceNotifications("completed", []push.Notification{
		{Title: "A"}, {Title: "B"}, {Title: "A"},
	})
	if merged.Title != "3 sessions completed" {
		t.Fatalf("merged title = %q", merged.Title)
	}
	if merged.Body != "A, B" {
		t.Fatalf("merged body = %q (want deduped titles)", merged.Body)
	}
	if merged.Tag != "batch|completed" {
		t.Fatalf("merged tag = %q", merged.Tag)
	}
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

func TestPushNotifierQuietHours(t *testing.T) {
	hits := make(chan struct{}, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits <- struct{}{}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	n := testNotifier(t, srv.URL+"/sub")
	n.coalesceWindow = 30 * time.Millisecond
	n.quietHours = []quietRange{{start: 0, end: 24 * 60}} // all day
	n.now = func() time.Time { return time.Date(2026, 10, 11, 23, 0, 0, 0, time.Local) }

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

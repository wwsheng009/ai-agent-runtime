package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/ntfy"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/tunnel"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

func waitNtfy(ch chan map[string]any, d time.Duration) bool {
	select {
	case <-ch:
		return true
	case <-time.After(d):
		return false
	}
}

// TestPushNotifierRoutingD6：切片二切流后的 D6 路由——normal（completed）只走
// 主通道（webpush），不再附带 ntfy；high（approval）保持全通道扇出。
func TestPushNotifierRoutingD6(t *testing.T) {
	webHits := make(chan struct{}, 8)
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		webHits <- struct{}{}
		w.WriteHeader(http.StatusCreated)
	}))
	defer web.Close()

	ntfyHits := make(chan map[string]any, 4)
	nsrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		ntfyHits <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer nsrv.Close()

	n := testNotifier(t, web.URL+"/sub")
	n.ntfy = ntfy.NewSender(nsrv.URL, "topic-r", "")

	// normal：completed → 仅 webpush（D6 单一主通道）。
	n.observe([]*tunnel.Node{{
		NodeID:   "n1",
		Sessions: []wire.SessionSummary{{SessionID: "s1", Title: "A", Attention: "completed"}},
	}})
	if !waitHit(webHits, 2*time.Second) {
		t.Fatal("normal 应走 webpush 主通道")
	}
	if waitNtfy(ntfyHits, 400*time.Millisecond) {
		t.Fatal("normal 不得附带 ntfy（D6：单一主通道）")
	}

	// high：approval → 全通道扇出（webpush + ntfy）。
	n.observe([]*tunnel.Node{{
		NodeID:   "n1",
		Sessions: []wire.SessionSummary{{SessionID: "s2", Title: "B", Attention: "approval"}},
	}})
	if !waitHit(webHits, 2*time.Second) {
		t.Fatal("high 应走 webpush")
	}
	if !waitNtfy(ntfyHits, 2*time.Second) {
		t.Fatal("high 应扇出到 ntfy")
	}
}

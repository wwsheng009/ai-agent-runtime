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

// TestPushNotifierNormalRoutedToIMWhenBound：D6 主通道——normal 类在会话已
// 绑定（IM 将投递）时 push 侧静默；未绑定会话仍走 webpush；high 不受影响。
func TestPushNotifierNormalRoutedToIMWhenBound(t *testing.T) {
	webHits := make(chan struct{}, 8)
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		webHits <- struct{}{}
		w.WriteHeader(http.StatusCreated)
	}))
	defer web.Close()

	n := testNotifier(t, web.URL+"/sub")
	n.imMainChannel = func(_ string, sessionID, kind string) bool {
		return sessionID == "s-im" && kind == "completed"
	}

	// 已绑定（IM 主通道）：normal 不落 webpush。
	n.observe([]*tunnel.Node{{
		NodeID:   "n1",
		Sessions: []wire.SessionSummary{{SessionID: "s-im", Title: "A", Attention: "completed"}},
	}})
	if waitHit(webHits, 400*time.Millisecond) {
		t.Fatal("IM 主通道场景 normal 不得再落 webpush")
	}

	// 未绑定会话：normal 仍走 webpush。
	n.observe([]*tunnel.Node{{
		NodeID:   "n1",
		Sessions: []wire.SessionSummary{{SessionID: "s-plain", Title: "B", Attention: "completed"}},
	}})
	if !waitHit(webHits, 2*time.Second) {
		t.Fatal("未绑定会话的 normal 应走 webpush")
	}

	// high：不受主通道判定影响（扇出）。
	n.observe([]*tunnel.Node{{
		NodeID:   "n1",
		Sessions: []wire.SessionSummary{{SessionID: "s-im", Title: "A", Attention: "approval"}},
	}})
	if !waitHit(webHits, 2*time.Second) {
		t.Fatal("high 应照常扇出")
	}
}

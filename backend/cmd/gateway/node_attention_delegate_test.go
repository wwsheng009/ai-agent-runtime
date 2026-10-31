package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// fakeAttentionProxy 捕获 pinned 转发的目标与请求体（不经过真实隧道）。
type fakeAttentionProxy struct {
	status int
	body   string
	nodeID string
	path   string
	req    map[string]any
}

func (f *fakeAttentionProxy) ServeProxyPinned(w http.ResponseWriter, r *http.Request, nodeID, path string) {
	f.nodeID = nodeID
	f.path = path
	var payload map[string]any
	_ = json.NewDecoder(r.Body).Decode(&payload)
	f.req = payload
	if f.status == 0 {
		f.status = http.StatusOK
	}
	w.WriteHeader(f.status)
	_, _ = w.Write([]byte(f.body))
}

func TestNodeAttentionDelegatePostsAttention(t *testing.T) {
	fp := &fakeAttentionProxy{body: `{"ok":true}`}
	delegate := newNodeAttentionDelegate(fp)
	if delegate == nil {
		t.Fatal("delegate is nil")
	}
	err := delegate(context.Background(), "node-a", connectors.AttentionNotice{
		Kind:    "error",
		Session: connectors.SessionRef{NodeID: "node-a", SessionID: "s1"},
		Text:    "boom",
	})
	if err != nil {
		t.Fatalf("delegate: %v", err)
	}
	if fp.nodeID != "node-a" || fp.path != nodeAttentionPath {
		t.Fatalf("proxy got node=%q path=%q", fp.nodeID, fp.path)
	}
	if fp.req["kind"] != "error" || fp.req["text"] != "boom" {
		t.Fatalf("payload = %+v", fp.req)
	}
	session, _ := fp.req["session"].(map[string]any)
	if session["node_id"] != "node-a" || session["session_id"] != "s1" {
		t.Fatalf("session = %+v", session)
	}
}

func TestNodeAttentionDelegateRejectsBadInputAndStatus(t *testing.T) {
	fp := &fakeAttentionProxy{status: http.StatusBadGateway, body: "node offline"}
	delegate := newNodeAttentionDelegate(fp)
	if delegate == nil {
		t.Fatal("delegate is nil")
	}
	if err := delegate(context.Background(), "", connectors.AttentionNotice{Session: connectors.SessionRef{SessionID: "s1"}, Text: "x"}); err == nil {
		t.Fatal("empty node must fail")
	}
	if err := delegate(context.Background(), "node-a", connectors.AttentionNotice{Text: "x"}); err == nil {
		t.Fatal("missing session must fail")
	}
	if err := delegate(context.Background(), "node-a", connectors.AttentionNotice{Session: connectors.SessionRef{SessionID: "s1"}}); err == nil {
		t.Fatal("missing text must fail")
	}
	err := delegate(context.Background(), "node-a", connectors.AttentionNotice{Session: connectors.SessionRef{SessionID: "s1"}, Text: "x"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("status error = %v", err)
	}
	if newNodeAttentionDelegate(nil) != nil {
		t.Fatal("nil proxy must produce nil delegate")
	}
}

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/push"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/tunnel"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

func TestPresenceRegistry(t *testing.T) {
	p := newPresenceRegistry(40 * time.Millisecond)
	if p.Active("n1", "s1") {
		t.Fatal("unknown session must not be active")
	}
	p.Touch("n1", "s1")
	if !p.Active("n1", "s1") {
		t.Fatal("touched session should be active")
	}
	if p.Active("n1", "s2") {
		t.Fatal("other session must not be active")
	}
	if p.Count() != 1 {
		t.Fatalf("count = %d, want 1", p.Count())
	}
	time.Sleep(60 * time.Millisecond)
	if p.Active("n1", "s1") {
		t.Fatal("entry should expire")
	}
	if p.Count() != 0 {
		t.Fatalf("count after expiry = %d, want 0", p.Count())
	}
}

func TestPushNotifierSuppressesWhileWatching(t *testing.T) {
	hits := make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits <- struct{}{}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	vapid, _ := push.LoadOrCreateVAPID("")
	store, _ := push.OpenStore("")
	if err := store.Add(testSubscription(t, srv.URL+"/sub")); err != nil {
		t.Fatal(err)
	}
	manager := push.NewManager(store, vapid, "mailto:a@b.c")
	presence := newPresenceRegistry(50 * time.Millisecond)
	n := newPushNotifier(manager, "", presence)

	node := &tunnel.Node{
		NodeID:   "n1",
		Name:     "laptop",
		Sessions: []wire.SessionSummary{{SessionID: "s1", Title: "T", Attention: "approval"}},
	}

	// While the console is watching, no push is delivered.
	presence.Touch("n1", "s1")
	n.observe([]*tunnel.Node{node})
	if waitHit(hits, 300*time.Millisecond) {
		t.Fatal("push must be suppressed while the session is being watched")
	}

	// Once presence expires, the next observation delivers the notification.
	time.Sleep(80 * time.Millisecond)
	n.observe([]*tunnel.Node{node})
	if !waitHit(hits, 3*time.Second) {
		t.Fatal("expected a push after the viewer looked away")
	}
}

func TestPresenceEndpoint(t *testing.T) {
	handler := handlePresence(newPresenceRegistry(time.Minute))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/presence", strings.NewReader(`{"node_id":"n1","session_id":"s1"}`))
	handler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["watching"] != true {
		t.Fatalf("body = %v", body)
	}
}

func TestPresenceEndpointRequiresNode(t *testing.T) {
	handler := handlePresence(newPresenceRegistry(time.Minute))
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodPost, "/api/presence", strings.NewReader(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

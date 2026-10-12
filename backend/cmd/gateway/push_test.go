package main

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/ntfy"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/push"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/tunnel"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

func testSubscription(t *testing.T, endpoint string) push.Subscription {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authBuf := make([]byte, 16)
	if _, err := rand.Read(authBuf); err != nil {
		t.Fatal(err)
	}
	return push.Subscription{
		Endpoint: endpoint,
		P256dh:   base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
		Auth:     base64.RawURLEncoding.EncodeToString(authBuf),
	}
}

func TestPushNotifierObserve(t *testing.T) {
	hits := make(chan struct{}, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits <- struct{}{}
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	vapid, err := push.LoadOrCreateVAPID("")
	if err != nil {
		t.Fatal(err)
	}
	store, _ := push.OpenStore("")
	if err := store.Add(testSubscription(t, srv.URL+"/sub")); err != nil {
		t.Fatal(err)
	}
	manager := push.NewManager(store, vapid, "mailto:a@b.c")
	n := newPushNotifier(manager, "https://gw.example.com", nil)

	node := &tunnel.Node{
		NodeID: "n1",
		Name:   "laptop",
		Sessions: []wire.SessionSummary{
			{SessionID: "s1", Title: "Design", Attention: "approval"},
		},
	}

	n.observe([]*tunnel.Node{node})
	if !waitHit(hits, 3*time.Second) {
		t.Fatal("expected a push for the new attention state")
	}

	// Unchanged attention must not re-notify.
	n.observe([]*tunnel.Node{node})
	if waitHit(hits, 300*time.Millisecond) {
		t.Fatal("duplicate push for unchanged attention")
	}

	// Clearing then re-arming attention notifies again.
	n.observe(nil)
	n.observe([]*tunnel.Node{node})
	if !waitHit(hits, 3*time.Second) {
		t.Fatal("expected a push after attention was re-armed")
	}
}

func TestPushConfigHandler(t *testing.T) {
	rr := httptest.NewRecorder()
	handlePushConfig(nil, nil)(rr, httptest.NewRequest(http.MethodGet, "/api/push/config", nil))
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"enabled":false`) {
		t.Fatalf("disabled config = %d %s", rr.Code, rr.Body.String())
	}

	vapid, _ := push.LoadOrCreateVAPID("")
	store, _ := push.OpenStore("")
	manager := push.NewManager(store, vapid, "mailto:a@b.c")
	rr2 := httptest.NewRecorder()
	handlePushConfig(manager, nil)(rr2, httptest.NewRequest(http.MethodGet, "/api/push/config", nil))
	if rr2.Code != http.StatusOK || !strings.Contains(rr2.Body.String(), `"public_key":"`) {
		t.Fatalf("enabled config = %d %s", rr2.Code, rr2.Body.String())
	}
}

func TestPushSubscriptionsHandler(t *testing.T) {
	vapid, _ := push.LoadOrCreateVAPID("")
	store, _ := push.OpenStore("")
	manager := push.NewManager(store, vapid, "")

	// Missing keys → 400.
	rr := httptest.NewRecorder()
	handlePushSubscriptions(manager)(rr, httptest.NewRequest(http.MethodPost, "/api/push/subscriptions", strings.NewReader(`{"endpoint":"https://x"}`)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid subscribe = %d", rr.Code)
	}

	body := `{"endpoint":"https://push.example.com/x","keys":{"p256dh":"p","auth":"a"},"label":"phone"}`
	rr2 := httptest.NewRecorder()
	handlePushSubscriptions(manager)(rr2, httptest.NewRequest(http.MethodPost, "/api/push/subscriptions", strings.NewReader(body)))
	if rr2.Code != http.StatusCreated {
		t.Fatalf("subscribe = %d %s", rr2.Code, rr2.Body.String())
	}
	if store.Count() != 1 {
		t.Fatalf("store count = %d", store.Count())
	}

	rr3 := httptest.NewRecorder()
	handlePushSubscriptions(manager)(rr3, httptest.NewRequest(http.MethodGet, "/api/push/subscriptions", nil))
	if !strings.Contains(rr3.Body.String(), "push.example.com") {
		t.Fatalf("list missing subscription: %s", rr3.Body.String())
	}
}

func TestPushUnsubscribeHandler(t *testing.T) {
	vapid, _ := push.LoadOrCreateVAPID("")
	store, _ := push.OpenStore("")
	manager := push.NewManager(store, vapid, "")
	if err := store.Add(push.Subscription{Endpoint: "https://push.example.com/x", P256dh: "p", Auth: "a"}); err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handlePushUnsubscribe(manager)(rr, httptest.NewRequest(http.MethodPost, "/api/push/unsubscribe", strings.NewReader(`{"endpoint":"https://push.example.com/x"}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("unsubscribe = %d %s", rr.Code, rr.Body.String())
	}
	if store.Count() != 0 {
		t.Fatalf("subscription not removed")
	}

	rr2 := httptest.NewRecorder()
	handlePushUnsubscribe(manager)(rr2, httptest.NewRequest(http.MethodPost, "/api/push/unsubscribe", strings.NewReader(`{"endpoint":"https://push.example.com/x"}`)))
	if rr2.Code != http.StatusNotFound {
		t.Fatalf("second unsubscribe = %d", rr2.Code)
	}
}

func waitHit(ch chan struct{}, d time.Duration) bool {
	select {
	case <-ch:
		return true
	case <-time.After(d):
		return false
	}
}

func TestNtfyMessagePriorityMapping(t *testing.T) {
	high := ntfyMessage(push.Notification{Title: "t", Body: "b", Urgency: "high", URL: "https://gw/x"})
	if high.Priority != ntfy.PriorityMax || len(high.Tags) != 1 || high.Tags[0] != "warning" {
		t.Fatalf("high priority mapping = %+v", high)
	}
	normal := ntfyMessage(push.Notification{Title: "t", Body: "b", Urgency: "normal"})
	if normal.Priority != ntfy.PriorityDefault {
		t.Fatalf("normal priority mapping = %+v", normal)
	}
}

// ntfy works even when Web Push is disabled: the notifier must still observe
// attention changes and publish to the secondary channel.
func TestPushNotifierFansOutToNtfyWithoutWebPush(t *testing.T) {
	deliveries := make(chan map[string]any, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		deliveries <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := newPushNotifier(nil, "https://gw.example.com", nil)
	n.ntfy = ntfy.NewSender(srv.URL, "aicli-gateway", "")
	if !n.ntfyEnabled() {
		t.Fatal("ntfy should be enabled")
	}
	n.observe([]*tunnel.Node{{
		NodeID:   "n1",
		Name:     "laptop",
		Sessions: []wire.SessionSummary{{SessionID: "s1", Title: "A", Attention: "approval"}},
	}})
	select {
	case got := <-deliveries:
		if got["topic"] != "aicli-gateway" || got["priority"] != float64(5) {
			t.Fatalf("ntfy payload = %v", got)
		}
		if got["click"] != "https://gw.example.com/n/n1/?session=s1" {
			t.Fatalf("click = %v", got["click"])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("expected an ntfy delivery")
	}
}

func TestPushTestHandlerReportsChannels(t *testing.T) {
	deliveries := make(chan map[string]any, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		deliveries <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rr := httptest.NewRecorder()
	handlePushTest(nil, ntfy.NewSender(srv.URL, "topic-a", ""))(rr, httptest.NewRequest(http.MethodPost, "/api/push/test", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rr.Code, rr.Body.String())
	}
	var body struct {
		Delivered int `json:"delivered"`
		Channels  struct {
			Webpush map[string]any `json:"webpush"`
			Ntfy    map[string]any `json:"ntfy"`
		} `json:"channels"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Channels.Webpush["enabled"] != false {
		t.Fatalf("webpush should be disabled: %v", body.Channels.Webpush)
	}
	if body.Channels.Ntfy["delivered"] != float64(1) || body.Channels.Ntfy["topic"] != "topic-a" {
		t.Fatalf("ntfy result = %v", body.Channels.Ntfy)
	}
	select {
	case got := <-deliveries:
		if got["title"] != "aicli-gateway" || got["priority"] != float64(5) {
			t.Fatalf("test payload = %v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("expected the test notification on ntfy")
	}

	// Fully disabled: still 503 for backwards compatibility.
	rr2 := httptest.NewRecorder()
	handlePushTest(nil, nil)(rr2, httptest.NewRequest(http.MethodPost, "/api/push/test", nil))
	if rr2.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled status = %d", rr2.Code)
	}
}

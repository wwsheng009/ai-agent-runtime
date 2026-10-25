package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/appdevice"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/tunnel"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
	"github.com/wwsheng009/ai-agent-runtime/internal/notify"
	"github.com/wwsheng009/ai-agent-runtime/internal/notify/channels"
)

type appPushCapture struct {
	calls atomic.Int64
}

func (s *appPushCapture) Send(_ context.Context, _ appdevice.Device, _ notify.Event) error {
	s.calls.Add(1)
	return nil
}

func TestPushNotifierAppPushChannel(t *testing.T) {
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer web.Close()

	devices := appdevice.NewStore()
	if _, _, err := devices.Register("windows", "wns-token", "desktop"); err != nil {
		t.Fatal(err)
	}
	capture := &appPushCapture{}
	n := testNotifier(t, web.URL+"/sub")
	n.appPush = &channels.AppPush{Store: devices, Sender: capture}
	n.normalChannels = []string{"apppush"}

	n.observe([]*tunnel.Node{{
		NodeID:   "n1",
		Sessions: []wire.SessionSummary{{SessionID: "s1", Title: "A", Attention: "completed"}},
	}})
	if !waitAppPush(capture, 1, 2*time.Second) {
		t.Fatal("configured apppush channel did not receive normal notification")
	}

	n.observe([]*tunnel.Node{{
		NodeID:   "n1",
		Sessions: []wire.SessionSummary{{SessionID: "s2", Title: "B", Attention: "approval"}},
	}})
	if !waitAppPush(capture, 2, 2*time.Second) {
		t.Fatal("high notification did not fan out to apppush")
	}
}

// TestPushNotifierAppPushOnly covers an App-first gateway where Web Push is
// disabled and ntfy is absent. The notifier must not return early just because
// p.manager is nil; apppush is a complete delivery path on its own.
func TestPushNotifierAppPushOnly(t *testing.T) {
	devices := appdevice.NewStore()
	if _, _, err := devices.Register("ios", "apns-token", "phone"); err != nil {
		t.Fatal(err)
	}
	capture := &appPushCapture{}
	n := newPushNotifier(nil, "https://gw.example.com", nil)
	n.appPush = &channels.AppPush{Store: devices, Sender: capture}
	n.normalChannels = []string{"apppush"}

	n.observe([]*tunnel.Node{{
		NodeID:   "n1",
		Sessions: []wire.SessionSummary{{SessionID: "s1", Title: "A", Attention: "approval"}},
	}})
	if !waitAppPush(capture, 1, 2*time.Second) {
		t.Fatal("app-only notifier dropped the notification")
	}
}

func waitAppPush(capture *appPushCapture, want int64, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if capture.calls.Load() >= want {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

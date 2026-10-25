package channels

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/appdevice"
	"github.com/wwsheng009/ai-agent-runtime/internal/notify"
)

type fakeAppSender struct {
	calls  int
	goneID string
	err    error
}

func (f *fakeAppSender) Send(_ context.Context, device appdevice.Device, _ notify.Event) error {
	f.calls++
	if device.ID == f.goneID {
		return ErrDeviceGone
	}
	return f.err
}

func TestAppPushFansOutAndPrunesGoneDevices(t *testing.T) {
	store := appdevice.NewStore()
	first, _, err := store.Register("android", "token-one", "phone")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := store.Register("ios", "token-two", "iphone")
	if err != nil {
		t.Fatal(err)
	}
	sender := &fakeAppSender{goneID: second.ID}
	channel := &AppPush{Store: store, Sender: sender}

	ev := notify.EventFor("approval", "device-test", notify.Notification{
		Title: "approval", Body: "waiting", Urgency: "high", TTL: 10 * time.Minute,
	})
	if err := channel.Deliver(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if sender.calls != 2 {
		t.Fatalf("sender calls = %d, want 2", sender.calls)
	}
	if store.Count() != 1 {
		t.Fatalf("store count = %d, want 1 after pruning gone token", store.Count())
	}
	if store.List()[0].ID != first.ID {
		t.Fatalf("pruned wrong device: %+v", store.List())
	}
}

func TestAppPushAggregatesProviderErrors(t *testing.T) {
	store := appdevice.NewStore()
	if _, _, err := store.Register("windows", "token-one", "desktop"); err != nil {
		t.Fatal(err)
	}
	channel := &AppPush{
		Store:  store,
		Sender: &fakeAppSender{err: errors.New("relay down")},
	}
	err := channel.Deliver(context.Background(), notify.EventFor("error", "k", notify.Notification{Title: "e"}))
	if err == nil || !strings.Contains(err.Error(), "relay down") {
		t.Fatalf("err = %v", err)
	}
	if store.Count() != 1 {
		t.Fatal("transient failure must not prune the device")
	}
}

func TestWebhookSenderPayloadAndAuth(t *testing.T) {
	var got webhookRequest
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	sender := NewWebhookSender(srv.URL, "relay-secret")
	device := appdevice.Device{
		ID: "dev-1", Platform: "android", Token: "push-token", Label: "phone",
	}
	ev := notify.EventFor("approval", "dedup-1", notify.Notification{
		Title: "Session", Body: "approval", Tag: "n1|s1", URL: "https://gw/n/1/",
		Urgency: "high", TTL: 10 * time.Minute,
	})
	ev.Node, ev.Session = "n1", "s1"
	if err := sender.Send(context.Background(), device, ev); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer relay-secret" {
		t.Fatalf("authorization = %q", auth)
	}
	if got.Device.Token != "push-token" || got.Device.Platform != "android" {
		t.Fatalf("device payload = %+v", got.Device)
	}
	if got.Notification.Kind != "approval" || got.Notification.Priority != "high" {
		t.Fatalf("notification payload = %+v", got.Notification)
	}
	if got.Notification.Node != "n1" || got.Notification.Session != "s1" {
		t.Fatalf("routing payload = %+v", got.Notification)
	}
	if got.Notification.TTLSeconds != 600 {
		t.Fatalf("ttl = %d", got.Notification.TTLSeconds)
	}
}

func TestWebhookSenderMapsGoneAndRejected(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{
		{status: http.StatusGone, want: ErrDeviceGone},
		{status: http.StatusNotFound, want: ErrDeviceGone},
		{status: http.StatusUnauthorized, want: nil},
		{status: http.StatusInternalServerError, want: nil},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
		}))
		err := NewWebhookSender(srv.URL, "").Send(context.Background(), appdevice.Device{ID: "d"}, notify.Event{})
		srv.Close()
		if tc.want != nil {
			if !errors.Is(err, tc.want) {
				t.Fatalf("status %d err = %v, want %v", tc.status, err, tc.want)
			}
		} else if err == nil {
			t.Fatalf("status %d expected an error", tc.status)
		}
	}
}

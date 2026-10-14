package channels

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/ntfy"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/push"
	"github.com/wwsheng009/ai-agent-runtime/internal/notify"
)

type fakeBroadcaster struct{ got []push.Notification }

func (f *fakeBroadcaster) Broadcast(_ context.Context, n push.Notification) int {
	f.got = append(f.got, n)
	return len(f.got)
}

func TestWebPushDeliver(t *testing.T) {
	fake := &fakeBroadcaster{}
	w := &WebPush{Manager: fake}
	if err := w.Deliver(context.Background(), notify.Event{Notification: push.Notification{Title: "t"}}); err != nil {
		t.Fatal(err)
	}
	if len(fake.got) != 1 || fake.got[0].Title != "t" {
		t.Fatalf("broadcast = %+v", fake.got)
	}
	// 未配置：静默跳过（与旧 dispatch 一致）。
	if err := (&WebPush{}).Deliver(context.Background(), notify.Event{}); err != nil {
		t.Fatal(err)
	}
}

func TestNtfyDeliverMapsAndSkipsWhenDisabled(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := &Ntfy{Sender: ntfy.NewSender(srv.URL, "topic-x", "")}
	ev := notify.Event{Notification: push.Notification{Title: "t", Body: "b", Urgency: "high", URL: "https://gw/x"}}
	if err := n.Deliver(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if got["topic"] != "topic-x" || got["priority"] != float64(5) {
		t.Fatalf("payload = %v", got)
	}
	if got["click"] != "https://gw/x" {
		t.Fatalf("click = %v", got["click"])
	}
	// 未启用：静默跳过（不发请求）。
	disabled := &Ntfy{Sender: ntfy.NewSender("", "", "")}
	if err := disabled.Deliver(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
}

func TestIMDeliverCollectsAcceptedConnectors(t *testing.T) {
	store := connectors.NewMemoryStore(connectors.Config{ID: "c1", Provider: "fake", Enabled: true})
	if err := store.PutBinding(connectors.Binding{ConnectorID: "c1", Chat: "chat-1", Session: connectors.SessionRef{SessionID: "s1"}}); err != nil {
		t.Fatal(err)
	}
	var gotIDs []string
	var gotNotice connectors.AttentionNotice
	im := &IM{Store: store, NotifyScoped: func(_ context.Context, ids []string, notice connectors.AttentionNotice) error {
		gotIDs = append([]string(nil), ids...)
		gotNotice = notice
		return nil
	}}
	// 未绑定会话 + notify 关闭 → 无接受连接器 → 静默跳过。
	if err := im.Deliver(context.Background(), notify.Event{Kind: "approval", Target: "s-other"}); err != nil {
		t.Fatal(err)
	}
	if gotIDs != nil {
		t.Fatalf("unbound+disabled must be skipped, got %v", gotIDs)
	}
	// 绑定会话 approval（命令回环始终收）→ 命中 c1。
	if err := im.Deliver(context.Background(), notify.Event{Kind: "approval", Target: "s1"}); err != nil {
		t.Fatal(err)
	}
	if len(gotIDs) != 1 || gotIDs[0] != "c1" || !strings.Contains(gotNotice.Text, "待审批") {
		t.Fatalf("delivery = %v notice=%+v", gotIDs, gotNotice)
	}
	// waiting 不在投影词表（AttentionNoticeForProjection 跳过）→ 不投递。
	gotIDs = nil
	if err := im.Deliver(context.Background(), notify.Event{Kind: "waiting", Target: "s1"}); err != nil {
		t.Fatal(err)
	}
	if gotIDs != nil {
		t.Fatalf("waiting kind must be skipped, got %v", gotIDs)
	}
}

func TestNtfyMessageMapping(t *testing.T) {
	high := NtfyMessage(push.Notification{Urgency: "high"})
	if high.Priority != ntfy.PriorityMax || len(high.Tags) != 1 || high.Tags[0] != "warning" {
		t.Fatalf("high mapping = %+v", high)
	}
	normal := NtfyMessage(push.Notification{Urgency: "normal"})
	if normal.Priority != ntfy.PriorityDefault {
		t.Fatalf("normal mapping = %+v", normal)
	}
}

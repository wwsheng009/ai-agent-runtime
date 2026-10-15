package storefile

import (
	"os"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// S2（docs/bots/13 §6.1）：policy.events 的落盘与回读（含 ids 选择器/族通配）。
func TestConfigStoreEventsPolicyRoundTrip(t *testing.T) {
	store, configPath, _ := newTestStore(t)
	cfg := sampleConfig()
	cfg.Policy.Events = connectors.EventsPolicy{Enabled: true, Subscriptions: []connectors.EventSubscription{
		{ID: "ops", Sessions: connectors.SessionSelector{Mode: "ids", IDs: []string{"sess-a", "sess-b"}}, Types: []string{"turn.*", "approval"}, Deliver: "notice"},
		{ID: "main", Sessions: connectors.SessionSelector{Mode: "bound"}, Types: []string{"assistant_message"}, Deliver: "content"},
	}}
	if err := store.PutConfig(cfg); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	for _, want := range []string{"events:", "subscriptions:", "turn.*", "deliver: content", "mode: ids"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("config.yaml missing %q:\n%s", want, raw)
		}
	}

	got, ok := store.GetConfig(cfg.ID)
	if !ok {
		t.Fatal("GetConfig: not found")
	}
	if !got.Policy.Events.Enabled || len(got.Policy.Events.Subscriptions) != 2 {
		t.Fatalf("events lost: %+v", got.Policy.Events)
	}
	ops := got.Policy.Events.Subscriptions[0]
	if ops.ID != "ops" || ops.Sessions.Mode != "ids" || len(ops.Sessions.IDs) != 2 ||
		len(ops.Types) != 2 || ops.Types[0] != "turn.*" || ops.Deliver != "notice" {
		t.Fatalf("subscription mismatch: %+v", ops)
	}
	main := got.Policy.Events.Subscriptions[1]
	if main.ID != "main" || main.Sessions.Mode != "bound" || main.Deliver != "content" {
		t.Fatalf("subscription mismatch: %+v", main)
	}
}

// S4：单条订阅显式停用（enabled:false）落盘与回读。
func TestConfigStoreEventsPolicyDisabledSubscription(t *testing.T) {
	store, configPath, _ := newTestStore(t)
	disabled := false
	cfg := sampleConfig()
	cfg.Policy.Events = connectors.EventsPolicy{Enabled: true, Subscriptions: []connectors.EventSubscription{
		{ID: "paused", Enabled: &disabled, Sessions: connectors.SessionSelector{Mode: "bound"}, Types: []string{"turn.*"}, Deliver: "notice"},
	}}
	if err := store.PutConfig(cfg); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(raw), "enabled: false") {
		t.Fatalf("config.yaml missing disabled subscription marker:\n%s", raw)
	}
	got, ok := store.GetConfig(cfg.ID)
	if !ok {
		t.Fatal("GetConfig: not found")
	}
	sub := got.Policy.Events.Subscriptions[0]
	if sub.Enabled == nil || *sub.Enabled {
		t.Fatalf("disabled flag lost: %+v", sub.Enabled)
	}
}

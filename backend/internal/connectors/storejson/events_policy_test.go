package storejson

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// S2（docs/bots/13 §6.1）：storejson 的 policy.events 回环。
func TestStoreEventsPolicyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := New(dir)
	cfg := testConfig("tg-events")
	cfg.Policy.Events = connectors.EventsPolicy{Enabled: true, Subscriptions: []connectors.EventSubscription{
		{ID: "ops", Sessions: connectors.SessionSelector{Mode: "ids", IDs: []string{"s1"}}, Types: []string{"turn.*"}, Deliver: "notice"},
	}}
	if err := store.PutConfig(cfg); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}
	got, ok := store.GetConfig("tg-events")
	if !ok {
		t.Fatal("GetConfig: not found")
	}
	if !got.Policy.Events.Enabled || len(got.Policy.Events.Subscriptions) != 1 {
		t.Fatalf("events lost: %+v", got.Policy.Events)
	}
	sub := got.Policy.Events.Subscriptions[0]
	if sub.ID != "ops" || sub.Sessions.Mode != "ids" || len(sub.Sessions.IDs) != 1 || sub.Deliver != "notice" {
		t.Fatalf("subscription mismatch: %+v", sub)
	}
}

// S4：单条订阅显式停用（enabled:false）回环。
func TestStoreEventsPolicyDisabledSubscription(t *testing.T) {
	dir := t.TempDir()
	store := New(dir)
	disabled := false
	cfg := testConfig("tg-paused")
	cfg.Policy.Events = connectors.EventsPolicy{Enabled: true, Subscriptions: []connectors.EventSubscription{
		{ID: "paused", Enabled: &disabled, Types: []string{"turn.*"}, Deliver: "notice"},
	}}
	if err := store.PutConfig(cfg); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}
	got, ok := store.GetConfig("tg-paused")
	if !ok {
		t.Fatal("GetConfig: not found")
	}
	sub := got.Policy.Events.Subscriptions[0]
	if sub.Enabled == nil || *sub.Enabled {
		t.Fatalf("disabled flag lost: %+v", sub.Enabled)
	}
}

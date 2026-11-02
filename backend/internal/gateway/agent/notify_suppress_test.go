package agent

import (
	"context"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

func TestApplyNotifySuppress(t *testing.T) {
	c := New(enabledConfig(), nil)
	var observed [][]wire.NotifySuppressRule
	c.SetNotifySuppressObserver(func(rules []wire.NotifySuppressRule) {
		observed = append(observed, cloneNotifySuppressRules(rules))
	})
	c.ApplyNotifySuppress([]wire.NotifySuppressRule{
		{Kinds: []string{"approval"}, Sessions: []string{"s1"}},
		{Kinds: []string{"completed"}},
	})

	if !c.NotifySuppressed("s1", "approval") {
		t.Fatal("s1 approval should be suppressed")
	}
	if c.NotifySuppressed("s2", "approval") {
		t.Fatal("s2 approval should not be suppressed")
	}
	if !c.NotifySuppressed("s2", "completion") {
		t.Fatal("local completion must match gateway completed")
	}
	if len(observed) != 2 { // initial empty snapshot + applied set
		t.Fatalf("observer calls = %d, want 2", len(observed))
	}
}

func TestSetNotifySuppressObserverSeedsCurrentRules(t *testing.T) {
	c := New(enabledConfig(), nil)
	c.ApplyNotifySuppress([]wire.NotifySuppressRule{{Kinds: []string{"question"}}})

	var got []wire.NotifySuppressRule
	c.SetNotifySuppressObserver(func(rules []wire.NotifySuppressRule) {
		got = cloneNotifySuppressRules(rules)
	})
	if len(got) != 1 || len(got[0].Kinds) != 1 || got[0].Kinds[0] != "question" {
		t.Fatalf("observer did not receive current rules: %+v", got)
	}
}

func TestDisconnectClearsNotifySuppress(t *testing.T) {
	rec := &dialRecorder{}
	c := New(enabledConfig(), rec.dial)
	if err := c.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	c.ApplyNotifySuppress([]wire.NotifySuppressRule{{Kinds: []string{"approval"}}})
	if !c.NotifySuppressed("s1", "approval") {
		t.Fatal("precondition: rule should be active")
	}
	if err := c.Disconnect(context.Background()); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	if c.NotifySuppressed("s1", "approval") {
		t.Fatal("disconnect must clear suppression rules (local sends resume)")
	}
}

func TestHandleConnectionLostClearsNotifySuppress(t *testing.T) {
	c := New(enabledConfig(), nil)
	c.ApplyNotifySuppress([]wire.NotifySuppressRule{{Kinds: []string{"approval"}}})
	c.HandleConnectionLost(ErrCodeNetwork, "lost", true)
	if c.NotifySuppressed("s1", "approval") {
		t.Fatal("connection loss must clear suppression rules")
	}
}

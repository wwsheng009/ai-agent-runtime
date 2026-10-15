package restapi

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// S2（docs/bots/13 §6.1）：policy.events 的视图映射与回写。
func TestPolicyViewEventsRoundTrip(t *testing.T) {
	policy := connectors.Policy{Events: connectors.EventsPolicy{Enabled: true, Subscriptions: []connectors.EventSubscription{
		{ID: "ops", Sessions: connectors.SessionSelector{Mode: "ids", IDs: []string{"a", "b"}}, Types: []string{"turn.*", "approval"}, Deliver: "notice"},
		{ID: "main", Sessions: connectors.SessionSelector{Mode: "bound"}, Types: []string{"assistant_message"}, Deliver: "content"},
	}}}

	view := policyViewFrom(policy)
	require.NotNil(t, view.Events)
	require.True(t, view.Events.Enabled)
	require.Len(t, view.Events.Subscriptions, 2)
	require.Equal(t, "ops", view.Events.Subscriptions[0].ID)
	require.Equal(t, "ids", view.Events.Subscriptions[0].Sessions.Mode)
	require.Equal(t, []string{"a", "b"}, view.Events.Subscriptions[0].Sessions.IDs)
	require.Equal(t, []string{"turn.*", "approval"}, view.Events.Subscriptions[0].Types)
	require.Equal(t, "content", view.Events.Subscriptions[1].Deliver)

	back := policyFrom(&view)
	require.True(t, back.Events.Enabled)
	require.Len(t, back.Events.Subscriptions, 2)
	require.Equal(t, policy.Events.Subscriptions[0], back.Events.Subscriptions[0])
	require.Equal(t, policy.Events.Subscriptions[1], back.Events.Subscriptions[1])

	// 缺省：零值不产出视图字段（omitempty → 线上形状不变）。
	if empty := policyViewFrom(connectors.Policy{}); empty.Events != nil {
		t.Fatalf("zero policy must not emit events view: %+v", empty.Events)
	}

	// enabled=false 的视图不激活新模型（回退 legacy）。
	disabled := policyFrom(&PolicyView{Events: &EventsPolicyView{Enabled: false, Subscriptions: []EventSubscriptionView{{Types: []string{"turn.*"}}}}})
	require.False(t, disabled.Events.Configured())
}

// S4：单条订阅 enabled 的视图语义——false 显式停用；缺省（nil）不产出字段。
func TestPolicyViewEventsDisabledSubscription(t *testing.T) {
	off := false
	paused := connectors.Policy{Events: connectors.EventsPolicy{Enabled: true, Subscriptions: []connectors.EventSubscription{
		{ID: "paused", Enabled: &off, Types: []string{"turn.*"}, Deliver: "notice"},
	}}}
	view := policyViewFrom(paused)
	require.Len(t, view.Events.Subscriptions, 1)
	require.NotNil(t, view.Events.Subscriptions[0].Enabled)
	require.False(t, *view.Events.Subscriptions[0].Enabled)
	back := policyFrom(&view)
	require.NotNil(t, back.Events.Subscriptions[0].Enabled)
	require.False(t, *back.Events.Subscriptions[0].Enabled)

	// 缺省启用：nil 指针（omitempty → 线上不产出该字段）。
	on := connectors.Policy{Events: connectors.EventsPolicy{Enabled: true, Subscriptions: []connectors.EventSubscription{
		{ID: "on", Types: []string{"turn.*"}, Deliver: "notice"},
	}}}
	require.Nil(t, policyViewFrom(on).Events.Subscriptions[0].Enabled)
}

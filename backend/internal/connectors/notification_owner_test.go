package connectors

import "testing"

func TestNotificationsOwnerFromEnv(t *testing.T) {
	cases := map[string]string{
		"":        "auto",
		"node":    "node",
		"gateway": "gateway",
		"GATEWAY": "gateway",
		"auto":    "auto",
		"weird":   "auto",
	}
	for value, want := range cases {
		t.Setenv(EnvNotificationOwner, value)
		if got := NotificationsOwnerFromEnv(); got != want {
			t.Fatalf("env=%q got %q want %q", value, got, want)
		}
	}
}

func TestGatewaySuppressRules(t *testing.T) {
	t.Setenv(EnvNotificationOwner, "auto")
	ResetGatewayNotifySuppressForTest()
	defer ResetGatewayNotifySuppressForTest()

	SetGatewayNotifySuppress([]NotifySuppressRule{
		{Kinds: []string{"approval"}, Sessions: []string{"s1"}},
		{Kinds: []string{"completed"}}, // all sessions; local completion maps to completed
	})

	cases := []struct {
		session string
		kind    string
		want    bool
	}{
		{"s1", "approval", true},
		{"s2", "approval", false},
		{"s1", "completion", true},
		{"s2", "completion", true},
		{"s1", "question", false},
	}
	for _, tc := range cases {
		if got := GatewaySuppresses(tc.session, tc.kind); got != tc.want {
			t.Fatalf("session=%q kind=%q got %v want %v", tc.session, tc.kind, got, tc.want)
		}
	}

	t.Setenv(EnvNotificationOwner, "node")
	if GatewaySuppresses("s1", "approval") {
		t.Fatal("owner=node must ignore gateway suppression rules")
	}
	t.Setenv(EnvNotificationOwner, "gateway")
	if !GatewaySuppresses("any", "question") {
		t.Fatal("owner=gateway must suppress every local notification")
	}
}

package agent

import (
	"encoding/json"
	"testing"
	"time"
)

func TestStatusJSONShape(t *testing.T) {
	st := Status{
		State:                StateConnected,
		NodeID:               "node-aicli-9f3a",
		GatewayURL:           "wss://gw.example.com",
		ConsoleURL:           "https://gw.example.com/n/node-aicli-9f3a/web/",
		ConnectedAt:          time.Unix(0, 0).UTC(),
		LastHeartbeat:        time.Unix(0, 0).UTC(),
		ReconnectCount:       2,
		Sessions:             3,
		Mode:                 ModeAuto,
		OperatorDisconnected: false,
		TokenHint:            "agw_…",
	}
	raw, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{
		"state", "node_id", "gateway_url", "console_url", "connected_at",
		"last_heartbeat", "reconnect_count", "sessions", "mode",
		"operator_disconnected", "token_hint",
	} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("status JSON missing key %q: %s", key, raw)
		}
	}
	// Secret-adjacent fields must never appear.
	for _, forbidden := range []string{"token", "password", "secret"} {
		if _, ok := decoded[forbidden]; ok {
			t.Fatalf("status JSON must not expose %q: %s", forbidden, raw)
		}
	}
}

func TestStatusConnected(t *testing.T) {
	if !(Status{State: StateConnected}).Connected() {
		t.Fatal("connected state should report Connected()=true")
	}
	for _, s := range []State{StateDisabled, StateDisconnected, StateConnecting, StateReconnecting, StateError} {
		if (Status{State: s}).Connected() {
			t.Fatalf("state %s must not report Connected()", s)
		}
	}
}

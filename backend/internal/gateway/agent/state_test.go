package agent

import "testing"

func TestStateValid(t *testing.T) {
	for _, s := range allStates {
		if !s.Valid() {
			t.Fatalf("state %q should be valid", s)
		}
	}
	if State("bogus").Valid() {
		t.Fatal("unknown state must be invalid")
	}
}

func TestCanTransitionAllowed(t *testing.T) {
	allowed := []struct{ from, to State }{
		{StateDisabled, StateDisconnected},
		{StateDisconnected, StateConnecting},
		{StateDisconnected, StateDisabled},
		{StateConnecting, StateConnected},
		{StateConnecting, StateError},
		{StateConnecting, StateDisconnecting},
		{StateConnected, StateReconnecting},
		{StateConnected, StateDisconnecting},
		{StateConnected, StateError},
		{StateConnected, StateDisconnected},
		{StateReconnecting, StateConnecting},
		{StateReconnecting, StateDisconnected},
		{StateReconnecting, StateDisconnecting},
		{StateDisconnecting, StateDisconnected},
		{StateError, StateConnecting},
		{StateError, StateReconnecting},
		{StateError, StateDisconnecting},
		{StateError, StateDisconnected},
		{StateError, StateDisabled},
	}
	for _, tc := range allowed {
		if !canTransition(tc.from, tc.to) {
			t.Fatalf("expected %s -> %s to be allowed", tc.from, tc.to)
		}
	}
}

func TestCanTransitionSelfAlwaysAllowed(t *testing.T) {
	for _, s := range allStates {
		if !canTransition(s, s) {
			t.Fatalf("self transition %s -> %s must be allowed", s, s)
		}
	}
}

func TestCanTransitionDisallowed(t *testing.T) {
	disallowed := []struct{ from, to State }{
		{StateDisabled, StateConnecting},
		{StateDisabled, StateConnected},
		{StateConnected, StateConnecting}, // reconnect goes through disconnecting
		{StateDisconnecting, StateConnecting},
		{StateDisconnecting, StateConnected},
		{StateDisconnected, StateConnected},
	}
	for _, tc := range disallowed {
		if canTransition(tc.from, tc.to) {
			t.Fatalf("expected %s -> %s to be disallowed", tc.from, tc.to)
		}
	}
}

func TestTransitionAppliesState(t *testing.T) {
	c := &Controller{state: StateDisconnected}
	c.status.State = StateDisconnected
	c.transition(StateConnecting)
	if c.state != StateConnecting || c.status.State != StateConnecting {
		t.Fatalf("transition did not apply: state=%s status=%s", c.state, c.status.State)
	}
	// Idempotent.
	c.transition(StateConnecting)
	if c.state != StateConnecting {
		t.Fatalf("self transition changed state to %s", c.state)
	}
}

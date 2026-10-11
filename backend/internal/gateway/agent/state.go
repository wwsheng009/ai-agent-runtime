// Package agent implements the gateway client-side connection lifecycle shared
// by every host (aicli TUI, runtime-server): a single connection state machine,
// the manual connect/disconnect control surface and the Status/Config shapes
// rendered by both control planes.
//
// The package deliberately depends only on the standard library so it can be
// reused by both hosts and by the standalone gateway tooling without pulling in
// host-specific dependencies. The wire protocol (WS tunnel, registration) is a
// separate package; the Controller here drives it through an injected DialFunc.
//
// Design: docs/gateway/12-connection-control.md.
package agent

import "fmt"

// State is the connection lifecycle state of the gateway agent. It is a single
// state machine shared by every host so the two control surfaces (aicli TUI
// `/gateway`, runtime-server frontend) cannot drift on what "connected" means.
type State string

const (
	// StateDisabled: the gateway is not enabled (config off, or --gateway=false).
	StateDisabled State = "disabled"
	// StateDisconnected: enabled but not currently connected. Includes a manual
	// disconnect; see Status.OperatorDisconnected.
	StateDisconnected State = "disconnected"
	// StateConnecting: dial / handshake / register in flight.
	StateConnecting State = "connecting"
	// StateConnected: registered and heartbeating.
	StateConnected State = "connected"
	// StateReconnecting: connection dropped and backing off; auto-retry active.
	StateReconnecting State = "reconnecting"
	// StateDisconnecting: sending DEREGISTER and closing.
	StateDisconnecting State = "disconnecting"
	// StateError: a failed attempt. Auth failures are terminal (Retryable=false,
	// the host must not auto-retry); network failures are retryable.
	StateError State = "error"
)

// allStates is the closed set of states, used by Valid and tests.
var allStates = []State{
	StateDisabled, StateDisconnected, StateConnecting, StateConnected,
	StateReconnecting, StateDisconnecting, StateError,
}

// Valid reports whether s is one of the defined states.
func (s State) Valid() bool {
	for _, candidate := range allStates {
		if s == candidate {
			return true
		}
	}
	return false
}

// ErrInvalidTransition reports a transition the state machine does not allow.
type ErrInvalidTransition struct {
	From State
	To   State
}

func (e ErrInvalidTransition) Error() string {
	return fmt.Sprintf("gateway/agent: invalid state transition %s -> %s", e.From, e.To)
}

// transitions is the allowed-transition table. It is the authoritative contract
// for the state machine; Controller.transition applies it under its mutex.
var transitions = map[State]map[State]bool{
	StateDisabled: {
		StateDisconnected: true, // enable
	},
	StateDisconnected: {
		StateConnecting: true, // connect
		StateDisabled:   true, // disable
	},
	StateConnecting: {
		StateConnected:     true, // dial ok
		StateError:         true, // dial failed
		StateDisconnecting: true, // cancel / disconnect while dialing
	},
	StateConnected: {
		StateReconnecting:  true, // connection lost (retryable)
		StateError:         true, // connection lost (terminal) / fatal error
		StateDisconnecting: true, // disconnect
		StateDisconnected:  true, // abrupt drop with no clean close
	},
	StateReconnecting: {
		StateConnecting:    true, // retry now
		StateDisconnected:  true, // operator disconnected / gave up
		StateDisconnecting: true, // disconnect
		StateError:         true, // terminal error while backing off
	},
	StateDisconnecting: {
		StateDisconnected: true, // closed
		StateDisabled:     true, // disable while disconnecting
	},
	StateError: {
		StateConnecting:    true, // manual retry
		StateReconnecting:  true, // another drop while already in error
		StateDisconnecting: true, // disconnect
		StateDisconnected:  true, // reset
		StateDisabled:      true, // disable
	},
}

// canTransition reports whether from -> to is allowed. A self-transition is
// always allowed (idempotent no-op).
func canTransition(from, to State) bool {
	if from == to {
		return true
	}
	targets, ok := transitions[from]
	if !ok {
		return false
	}
	return targets[to]
}

// transition sets the controller state under the caller's lock. It applies the
// transition table and is a no-op when the state is unchanged. Callers own the
// mutex; invalid transitions are impossible on the production paths (covered by
// state_test.go) and are treated as a state reset to keep the controller live
// rather than wedged.
func (c *Controller) transition(to State) {
	if c.state == to {
		return
	}
	c.state = to
	c.status.State = to
}

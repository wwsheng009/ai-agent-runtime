package hooks

import (
	"testing"
	"time"
)

// Defaults used to be 3s for every handler type, which is too tight for a real
// command hook (cold interpreter startup plus real work) and, because failures
// are fail-open, made under-budget hooks silently do nothing.
func TestDefaultHandlerTimeouts(t *testing.T) {
	if defaultCommandTimeout != 10*time.Second {
		t.Fatalf("command default timeout = %v, want 10s", defaultCommandTimeout)
	}
	if defaultHTTPTimeout != 5*time.Second {
		t.Fatalf("http default timeout = %v, want 5s", defaultHTTPTimeout)
	}
	if defaultHookTimeout != defaultCommandTimeout {
		t.Fatalf("reported default = %v, want %v", defaultHookTimeout, defaultCommandTimeout)
	}
	if defaultHTTPTimeout > defaultCommandTimeout {
		t.Fatalf("http default (%v) must not exceed command default (%v)", defaultHTTPTimeout, defaultCommandTimeout)
	}
}

// Diagnostics and execution must agree on the timeout. Reporting the raw field
// showed "0" for handlers that really ran with 10s/5s, which is exactly the
// kind of contradiction that makes a debugging surface untrustworthy.
func TestEffectiveTimeoutMatchesExecutorDefaults(t *testing.T) {
	cases := []struct {
		name string
		hook HookConfig
		want time.Duration
	}{
		{"command unset", HookConfig{Exec: ExecConfig{Type: "shell"}}, 10 * time.Second},
		{"command legacy empty type", HookConfig{Exec: ExecConfig{}}, 10 * time.Second},
		{"http unset", HookConfig{Exec: ExecConfig{Type: "http"}}, 5 * time.Second},
		{"type is case insensitive", HookConfig{Exec: ExecConfig{Type: "HTTP"}}, 5 * time.Second},
		{"explicit wins", HookConfig{Timeout: 42 * time.Second, Exec: ExecConfig{Type: "http"}}, 42 * time.Second},
		{"mcp has no timeout", HookConfig{Exec: ExecConfig{Type: "mcp"}}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.hook.EffectiveTimeout(); got != tc.want {
				t.Fatalf("EffectiveTimeout() = %v, want %v", got, tc.want)
			}
		})
	}
}

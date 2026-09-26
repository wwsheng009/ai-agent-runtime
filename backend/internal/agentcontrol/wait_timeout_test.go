package agentcontrol

import (
	"strings"
	"testing"
)

func TestResolveWaitTimeoutZeroUsesConfiguredDefault(t *testing.T) {
	policy := WaitTimeoutPolicy{DefaultMs: 30000, MinMs: 10000, MaxMs: 120000}

	resolution, err := ResolveWaitTimeout(0, policy)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolution.EffectiveMs != 30000 {
		t.Fatalf("effective timeout = %d, want 30000", resolution.EffectiveMs)
	}
	if resolution.RequestedMs != 0 || resolution.Clamped {
		t.Fatalf("unexpected resolution: %#v", resolution)
	}
}

func TestResolveWaitTimeoutClampsToBounds(t *testing.T) {
	policy := WaitTimeoutPolicy{DefaultMs: 30000, MinMs: 10000, MaxMs: 120000}
	cases := []struct {
		name        string
		requested   int
		wantMs      int
		wantClamped bool
	}{
		{name: "below min", requested: 1, wantMs: 10000, wantClamped: true},
		{name: "just below min", requested: 9999, wantMs: 10000, wantClamped: true},
		{name: "exact min", requested: 10000, wantMs: 10000},
		{name: "in range", requested: 20000, wantMs: 20000},
		{name: "exact max", requested: 120000, wantMs: 120000},
		{name: "above max", requested: 2400000, wantMs: 120000, wantClamped: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolution, err := ResolveWaitTimeout(tc.requested, policy)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resolution.EffectiveMs != tc.wantMs {
				t.Fatalf("effective timeout = %d, want %d", resolution.EffectiveMs, tc.wantMs)
			}
			if resolution.Clamped != tc.wantClamped {
				t.Fatalf("clamped = %v, want %v", resolution.Clamped, tc.wantClamped)
			}
			if resolution.RequestedMs != tc.requested {
				t.Fatalf("requested echo = %d, want %d", resolution.RequestedMs, tc.requested)
			}
		})
	}
}

func TestResolveWaitTimeoutErrorModeRejectsOutOfRange(t *testing.T) {
	policy := WaitTimeoutPolicy{DefaultMs: 30000, MinMs: 10000, MaxMs: 120000, Mode: WaitTimeoutModeError}

	_, err := ResolveWaitTimeout(1, policy)
	if err == nil || !strings.Contains(err.Error(), "minWaitTimeoutMs") {
		t.Fatalf("expected min-bound error, got %v", err)
	}

	_, err = ResolveWaitTimeout(2400000, policy)
	if err == nil || !strings.Contains(err.Error(), "maxWaitTimeoutMs") {
		t.Fatalf("expected max-bound error, got %v", err)
	}

	resolution, err := ResolveWaitTimeout(20000, policy)
	if err != nil {
		t.Fatalf("in-range request must not fail: %v", err)
	}
	if resolution.EffectiveMs != 20000 {
		t.Fatalf("effective timeout = %d, want 20000", resolution.EffectiveMs)
	}
}

func TestResolveWaitTimeoutNormalizesZeroValuePolicy(t *testing.T) {
	resolution, err := ResolveWaitTimeout(0, WaitTimeoutPolicy{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolution.EffectiveMs != DefaultWaitTimeoutMs {
		t.Fatalf("effective timeout = %d, want %d", resolution.EffectiveMs, DefaultWaitTimeoutMs)
	}

	policy := WaitTimeoutPolicy{}.Normalize()
	if policy.MinMs != MinWaitTimeoutMs || policy.MaxMs != MaxWaitTimeoutMs || policy.DefaultMs != DefaultWaitTimeoutMs {
		t.Fatalf("unexpected normalized bounds: %#v", policy)
	}
	if policy.Mode != WaitTimeoutModeClamp {
		t.Fatalf("mode = %q, want %q", policy.Mode, WaitTimeoutModeClamp)
	}
}

func TestResolveWaitTimeoutClampsMisconfiguredDefault(t *testing.T) {
	policy := WaitTimeoutPolicy{DefaultMs: 1000, MinMs: 10000, MaxMs: 120000}

	resolution, err := ResolveWaitTimeout(0, policy)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolution.EffectiveMs != 10000 || !resolution.Clamped {
		t.Fatalf("unexpected resolution: %#v", resolution)
	}
}
